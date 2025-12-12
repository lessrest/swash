package journalfile

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Verify Reader implements JournalReader at compile time.
var _ JournalReader = (*Reader)(nil)

// Reader reads entries from a journal file.
type Reader struct {
	f      *os.File
	header Header

	// Current position for iteration
	currentSeqnum uint64

	// Filters (field=value pairs that must match)
	matches []match
}

type match struct {
	field string
	value string
}

// OpenRead opens an existing journal file for reading.
func OpenRead(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open journal file: %w", err)
	}

	r := &Reader{f: f}
	if err := r.readHeader(); err != nil {
		f.Close()
		return nil, err
	}

	return r, nil
}

func (r *Reader) readHeader() error {
	buf := make([]byte, HeaderSize)
	if _, err := r.f.ReadAt(buf, 0); err != nil {
		return fmt.Errorf("read header: %w", err)
	}

	if err := binary.Read(bytes.NewReader(buf), le, &r.header); err != nil {
		return fmt.Errorf("decode header: %w", err)
	}

	if r.header.Signature != HeaderSignature {
		return fmt.Errorf("invalid signature: %x", r.header.Signature)
	}

	return nil
}

// Close closes the reader.
func (r *Reader) Close() error {
	return r.f.Close()
}

// AddMatch adds a filter. Only entries where field=value will be returned.
// Multiple matches are ANDed together.
func (r *Reader) AddMatch(field, value string) {
	r.matches = append(r.matches, match{field: field, value: value})
}

// FlushMatches clears all filters.
func (r *Reader) FlushMatches() {
	r.matches = nil
}

// SeekHead positions the reader at the beginning.
func (r *Reader) SeekHead() {
	r.currentSeqnum = 0
}

// SeekCursor positions the reader after the given cursor.
// Cursor format: "s=<seqnum>" (simplified) or full "s=<seqnum>;i=<index>;b=<bootid>"
func (r *Reader) SeekCursor(cursor string) error {
	seqnum, err := parseCursor(cursor)
	if err != nil {
		return err
	}
	r.currentSeqnum = seqnum
	return nil
}

// formatCursor creates a cursor string from seqnum and bootID.
func formatCursor(seqnum uint64, bootID ID128) string {
	return fmt.Sprintf("s=%d;b=%x", seqnum, bootID)
}

func parseCursor(cursor string) (uint64, error) {
	// Parse "s=<seqnum>" or "s=<seqnum>;..."
	for part := range strings.SplitSeq(cursor, ";") {
		if after, ok := strings.CutPrefix(part, "s="); ok {
			seqStr := after
			seqnum, err := strconv.ParseUint(seqStr, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid cursor seqnum: %w", err)
			}
			return seqnum, nil
		}
	}
	return 0, fmt.Errorf("cursor missing seqnum: %s", cursor)
}

// Next reads the next entry matching filters. Returns io.EOF when done.
func (r *Reader) Next() (*Entry, error) {
	for {
		entry, err := r.nextEntry()
		if err != nil {
			return nil, err
		}

		if r.matchesEntry(entry) {
			return entry, nil
		}
	}
}

func (r *Reader) matchesEntry(e *Entry) bool {
	for _, m := range r.matches {
		if e.Fields[m.field] != m.value {
			return false
		}
	}
	return true
}

// nextEntry reads the next entry regardless of filters.
func (r *Reader) nextEntry() (*Entry, error) {
	// Walk entry arrays to find entries
	arrayOffset := r.header.EntryArrayOffset
	if arrayOffset == 0 {
		return nil, io.EOF
	}

	for arrayOffset != 0 {
		entries, nextArray, err := r.readEntryArray(arrayOffset)
		if err != nil {
			return nil, err
		}

		for _, entryOffset := range entries {
			if entryOffset == 0 {
				continue
			}

			entry, err := r.readEntry(entryOffset)
			if err != nil {
				return nil, err
			}

			// Skip entries we've already seen
			if entry.Seqnum <= r.currentSeqnum {
				continue
			}

			r.currentSeqnum = entry.Seqnum
			return entry, nil
		}

		arrayOffset = nextArray
	}

	return nil, io.EOF
}

func (r *Reader) readEntryArray(offset uint64) ([]uint64, uint64, error) {
	// Read object header
	var hdr EntryArrayObject
	if err := r.readAt(int64(offset), &hdr); err != nil {
		return nil, 0, fmt.Errorf("read entry array header: %w", err)
	}

	if hdr.Type != ObjectEntryArray {
		return nil, 0, fmt.Errorf("expected entry array, got type %d", hdr.Type)
	}

	// Calculate number of items
	itemsSize := hdr.Size - EntryArrayObjectHeaderSize
	numItems := itemsSize / 8

	// Read items
	items := make([]uint64, numItems)
	itemsOffset := int64(offset) + EntryArrayObjectHeaderSize
	buf := make([]byte, itemsSize)
	if _, err := r.f.ReadAt(buf, itemsOffset); err != nil {
		return nil, 0, fmt.Errorf("read entry array items: %w", err)
	}
	if err := binary.Read(bytes.NewReader(buf), le, items); err != nil {
		return nil, 0, fmt.Errorf("decode entry array items: %w", err)
	}

	return items, hdr.NextEntryArrayOffset, nil
}

func (r *Reader) readEntry(offset uint64) (*Entry, error) {
	// Read entry header
	var hdr EntryObject
	if err := r.readAt(int64(offset), &hdr); err != nil {
		return nil, fmt.Errorf("read entry header: %w", err)
	}

	if hdr.Type != ObjectEntry {
		return nil, fmt.Errorf("expected entry, got type %d", hdr.Type)
	}

	// Calculate number of items
	itemsSize := hdr.Size - EntryObjectHeaderSize
	numItems := itemsSize / EntryItemSize

	// Read items
	items := make([]EntryItem, numItems)
	itemsOffset := int64(offset) + EntryObjectHeaderSize
	buf := make([]byte, itemsSize)
	if _, err := r.f.ReadAt(buf, itemsOffset); err != nil {
		return nil, fmt.Errorf("read entry items: %w", err)
	}
	if err := binary.Read(bytes.NewReader(buf), le, items); err != nil {
		return nil, fmt.Errorf("decode entry items: %w", err)
	}

	// Read data for each item
	fields := make(map[string]string, len(items))
	for _, item := range items {
		key, value, err := r.readDataObject(item.ObjectOffset)
		if err != nil {
			return nil, fmt.Errorf("read data object: %w", err)
		}
		fields[key] = value
	}

	return &Entry{
		Seqnum:    hdr.Seqnum,
		Realtime:  time.Unix(int64(hdr.Realtime/1000000), int64((hdr.Realtime%1000000)*1000)),
		Monotonic: hdr.Monotonic,
		BootID:    hdr.BootID,
		Fields:    fields,
	}, nil
}

func (r *Reader) readDataObject(offset uint64) (string, string, error) {
	// Read data header
	var hdr DataObject
	if err := r.readAt(int64(offset), &hdr); err != nil {
		return "", "", fmt.Errorf("read data header: %w", err)
	}

	if hdr.Type != ObjectData {
		return "", "", fmt.Errorf("expected data, got type %d", hdr.Type)
	}

	// Read payload
	payloadSize := hdr.Size - DataObjectHeaderSize
	payload := make([]byte, payloadSize)
	payloadOffset := int64(offset) + DataObjectHeaderSize
	if _, err := r.f.ReadAt(payload, payloadOffset); err != nil {
		return "", "", fmt.Errorf("read data payload: %w", err)
	}

	// Parse field=value
	eqIdx := bytes.IndexByte(payload, '=')
	if eqIdx < 0 {
		return "", "", fmt.Errorf("invalid data: no = found")
	}

	return string(payload[:eqIdx]), string(payload[eqIdx+1:]), nil
}

func (r *Reader) readAt(offset int64, v any) error {
	size := binary.Size(v)
	buf := make([]byte, size)
	if _, err := r.f.ReadAt(buf, offset); err != nil {
		return err
	}
	return binary.Read(bytes.NewReader(buf), le, v)
}

// Header returns the parsed file header.
func (r *Reader) Header() Header {
	return r.header
}

// NEntries returns the number of entries in the file.
func (r *Reader) NEntries() uint64 {
	return r.header.NEntries
}

// Refresh re-reads the header to pick up new entries.
// Call this when waiting for new entries in a follow loop.
func (r *Reader) Refresh() error {
	return r.readHeader()
}

// TailSeqnum returns the sequence number of the last entry.
func (r *Reader) TailSeqnum() uint64 {
	return r.header.TailEntrySeqnum
}
