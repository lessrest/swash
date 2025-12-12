// Package journalfile implements a writer for systemd journal (*.journal) files.
//
// This package is intended to be usable independently of swash; swash uses it
// to let `cmd/mini-systemd` write real journal files that can be read by
// `journalctl`.
package journalfile

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// Default hash table sizes (number of HashItems)
const (
	DefaultDataHashTableSize  = 2047 // ~32KB
	DefaultFieldHashTableSize = 333  // ~5KB
)

// File represents an open journal file
type File struct {
	mu   sync.Mutex
	f    *os.File
	path string

	header    Header
	machineID ID128
	bootID    ID128

	// In-memory hash tables for dedup
	dataHashTable  []HashItem
	fieldHashTable []HashItem

	// Cache of data hashes to offsets for fast lookup
	dataCache map[uint64]uint64 // hash -> offset

	// Cache of field name hashes to field offsets
	fieldCache map[uint64]uint64 // hash -> offset

	// Tracks whether new entries were appended since the last durable sync.
	dirty bool
}

// Create creates a new journal file at path
func Create(path string, machineID, bootID ID128) (*File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return nil, fmt.Errorf("create journal file: %w", err)
	}

	jf := &File{
		f:          f,
		path:       path,
		machineID:  machineID,
		bootID:     bootID,
		dataCache:  make(map[uint64]uint64),
		fieldCache: make(map[uint64]uint64),
		dirty:      false,
	}

	if err := jf.initialize(); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}

	return jf, nil
}

// OpenAppend opens an existing journal file for appending new entries.
func OpenAppend(path string) (*File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0640)
	if err != nil {
		return nil, fmt.Errorf("open journal file: %w", err)
	}

	jf := &File{
		f:          f,
		path:       path,
		dataCache:  make(map[uint64]uint64),
		fieldCache: make(map[uint64]uint64),
		dirty:      false,
	}

	// Read existing header
	buf := make([]byte, HeaderSize)
	if _, err := f.ReadAt(buf, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("read header: %w", err)
	}
	if err := binary.Read(bytes.NewReader(buf), le, &jf.header); err != nil {
		f.Close()
		return nil, fmt.Errorf("decode header: %w", err)
	}
	if jf.header.Signature != HeaderSignature {
		f.Close()
		return nil, fmt.Errorf("invalid journal signature")
	}

	// Copy machine ID from header, generate new boot ID for this session
	jf.machineID = jf.header.MachineID
	if _, err := rand.Read(jf.bootID[:]); err != nil {
		f.Close()
		return nil, fmt.Errorf("generate boot ID: %w", err)
	}

	// Read hash tables
	jf.dataHashTable = make([]HashItem, jf.header.DataHashTableSize/HashItemSize)
	dataBuf := make([]byte, jf.header.DataHashTableSize)
	if _, err := f.ReadAt(dataBuf, int64(jf.header.DataHashTableOffset)); err != nil {
		f.Close()
		return nil, fmt.Errorf("read data hash table: %w", err)
	}
	if err := binary.Read(bytes.NewReader(dataBuf), le, jf.dataHashTable); err != nil {
		f.Close()
		return nil, fmt.Errorf("decode data hash table: %w", err)
	}

	jf.fieldHashTable = make([]HashItem, jf.header.FieldHashTableSize/HashItemSize)
	fieldBuf := make([]byte, jf.header.FieldHashTableSize)
	if _, err := f.ReadAt(fieldBuf, int64(jf.header.FieldHashTableOffset)); err != nil {
		f.Close()
		return nil, fmt.Errorf("read field hash table: %w", err)
	}
	if err := binary.Read(bytes.NewReader(fieldBuf), le, jf.fieldHashTable); err != nil {
		f.Close()
		return nil, fmt.Errorf("decode field hash table: %w", err)
	}

	// Position at end of file for appending
	if _, err := f.Seek(0, 2); err != nil {
		f.Close()
		return nil, fmt.Errorf("seek to end: %w", err)
	}

	return jf, nil
}

func (jf *File) initialize() error {
	// Generate unique file ID
	var fileID ID128
	if _, err := rand.Read(fileID[:]); err != nil {
		return fmt.Errorf("generate file ID: %w", err)
	}

	// Generate seqnum ID
	var seqnumID ID128
	if _, err := rand.Read(seqnumID[:]); err != nil {
		return fmt.Errorf("generate seqnum ID: %w", err)
	}

	// Calculate sizes - hash tables are Objects with ObjectHeader
	dataHashItemsSize := DefaultDataHashTableSize * HashItemSize
	fieldHashItemsSize := DefaultFieldHashTableSize * HashItemSize
	dataHashObjSize := uint64(ObjectHeaderSize + dataHashItemsSize)
	fieldHashObjSize := uint64(ObjectHeaderSize + fieldHashItemsSize)

	// Align to 8 bytes
	if dataHashObjSize%8 != 0 {
		dataHashObjSize += 8 - (dataHashObjSize % 8)
	}
	if fieldHashObjSize%8 != 0 {
		fieldHashObjSize += 8 - (fieldHashObjSize % 8)
	}

	// Layout: Header | DataHashTableObject | FieldHashTableObject | arena...
	dataHashObjOffset := uint64(HeaderSize)
	fieldHashObjOffset := dataHashObjOffset + dataHashObjSize

	// The header stores offset to the ITEMS, not the object header
	dataHashTableItemsOffset := dataHashObjOffset + ObjectHeaderSize
	fieldHashTableItemsOffset := fieldHashObjOffset + ObjectHeaderSize

	// Arena size includes the hash table objects
	initialArenaSize := dataHashObjSize + fieldHashObjSize

	// Initialize header
	jf.header = Header{
		Signature:            HeaderSignature,
		CompatibleFlags:      0,
		IncompatibleFlags:    HeaderIncompatibleKeyedHash, // Use siphash24 keyed by file_id
		State:                StateOffline,
		FileID:               fileID,
		MachineID:            jf.machineID,
		SeqnumID:             seqnumID,
		HeaderSize:           HeaderSize,
		ArenaSize:            initialArenaSize, // includes hash tables
		DataHashTableOffset:  dataHashTableItemsOffset,
		DataHashTableSize:    uint64(dataHashItemsSize),
		FieldHashTableOffset: fieldHashTableItemsOffset,
		FieldHashTableSize:   uint64(fieldHashItemsSize),
		NObjects:             2, // The two hash table objects
	}

	// Write header
	var buf bytes.Buffer
	if err := binary.Write(&buf, le, &jf.header); err != nil {
		return fmt.Errorf("encode header: %w", err)
	}
	if _, err := jf.f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	// Write data hash table object with ObjectHeader
	jf.dataHashTable = make([]HashItem, DefaultDataHashTableSize)
	dataHashHdr := ObjectHeader{Type: ObjectDataHashTable, Size: dataHashObjSize}
	if err := binary.Write(jf.f, le, &dataHashHdr); err != nil {
		return fmt.Errorf("write data hash table header: %w", err)
	}
	if err := binary.Write(jf.f, le, jf.dataHashTable); err != nil {
		return fmt.Errorf("write data hash table: %w", err)
	}

	// Write field hash table object with ObjectHeader
	jf.fieldHashTable = make([]HashItem, DefaultFieldHashTableSize)
	fieldHashHdr := ObjectHeader{Type: ObjectFieldHashTable, Size: fieldHashObjSize}
	if err := binary.Write(jf.f, le, &fieldHashHdr); err != nil {
		return fmt.Errorf("write field hash table header: %w", err)
	}
	if err := binary.Write(jf.f, le, jf.fieldHashTable); err != nil {
		return fmt.Errorf("write field hash table: %w", err)
	}

	// Update tail object offset (last hash table object)
	jf.header.TailObjectOffset = fieldHashObjOffset

	return jf.syncHeader()
}

func (jf *File) syncHeader() error {
	// Write the entire header in a single syscall. At 272 bytes this fits well
	// within a filesystem block, giving readers a coherent view without the
	// risk of observing partially-updated fields.
	var buf bytes.Buffer
	if err := binary.Write(&buf, le, &jf.header); err != nil {
		return err
	}
	_, err := jf.f.WriteAt(buf.Bytes(), 0)
	return err
}

// AppendEntry appends a new journal entry with the given fields
func (jf *File) AppendEntry(fields map[string]string) error {
	jf.mu.Lock()
	defer jf.mu.Unlock()

	// File lock for cross-process synchronization
	if err := syscall.Flock(int(jf.f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	defer syscall.Flock(int(jf.f.Fd()), syscall.LOCK_UN)

	// Mark file as being written so readers know indexes may be changing.
	if err := jf.beginWrite(); err != nil {
		return err
	}

	now := time.Now()
	realtime := uint64(now.UnixMicro())
	monotonic := uint64(now.UnixNano() / 1000) // approximate

	jf.dirty = true

	// Build data objects for each field
	items := make([]EntryItem, 0, len(fields))
	var xorHash uint64

	for k, v := range fields {
		data := []byte(k + "=" + v)
		// Use siphash24 keyed by file_id for hash tables (newer journal format)
		hash := SipHash24(data, jf.header.FileID)
		// XOR hash always uses Jenkins, even with keyed hash (per format spec)
		xorHash ^= JenkinsHash64(data)

		// Deduplicate data objects - reuse existing if same content
		offset, exists := jf.dataCache[hash]
		if !exists {
			var err error
			offset, err = jf.appendData(data, hash)
			if err != nil {
				return fmt.Errorf("append data %q: %w", k, err)
			}
			jf.dataCache[hash] = offset
		}

		items = append(items, EntryItem{
			ObjectOffset: offset,
			Hash:         hash,
		})
	}

	// Append entry object
	if err := jf.appendEntryObject(realtime, monotonic, xorHash, items); err != nil {
		return err
	}

	return nil
}

// align64 rounds up to the next 8-byte boundary
func align64(n uint64) uint64 {
	return (n + 7) &^ 7
}

// writeAt writes a binary-encodable value at the given file offset.
func (jf *File) writeAt(offset int64, v any) error {
	var buf bytes.Buffer
	if err := binary.Write(&buf, le, v); err != nil {
		return err
	}
	_, err := jf.f.WriteAt(buf.Bytes(), offset)
	return err
}

// readAt reads a binary-decodable value from the given file offset.
func (jf *File) readAt(offset int64, v any) error {
	size := binary.Size(v)
	buf := make([]byte, size)
	if _, err := jf.f.ReadAt(buf, offset); err != nil {
		return err
	}
	return binary.Read(bytes.NewReader(buf), le, v)
}

// beginWrite mirrors journald: mark the header ONLINE so readers know the file
// may be mid-update. We immediately persist the state flip so new readers see it.
func (jf *File) beginWrite() error {
	if jf.header.State == StateOnline {
		return nil
	}
	jf.header.State = StateOnline
	// Persist just the state byte to avoid rewriting the full header mid-append.
	if _, err := jf.f.WriteAt([]byte{byte(StateOnline)}, 16); err != nil {
		return err
	}
	return nil
}

func (jf *File) appendData(data []byte, hash uint64) (uint64, error) {
	// Find the field name (everything before =)
	eqIdx := bytes.IndexByte(data, '=')
	if eqIdx < 0 {
		return 0, fmt.Errorf("invalid data: no = found")
	}
	fieldName := data[:eqIdx]

	// First, ensure field object exists (use siphash24 keyed by file_id)
	fieldHash := SipHash24(fieldName, jf.header.FileID)
	fieldOffset, err := jf.ensureField(fieldName, fieldHash)
	if err != nil {
		return 0, err
	}

	// Read the field's current head_data_offset
	var prevHeadData uint64
	if err := jf.readAt(int64(fieldOffset)+FieldObjectHeaderSize-8, &prevHeadData); err != nil {
		return 0, err
	}

	// Object size is exact (header + payload), no padding in size
	objSize := uint64(DataObjectHeaderSize + len(data))

	// Get current position (end of file), ensure aligned
	offset, err := jf.f.Seek(0, 2)
	if err != nil {
		return 0, err
	}
	// Align offset to 8 bytes
	alignedOffset := align64(uint64(offset))
	if alignedOffset != uint64(offset) {
		// Write padding
		padding := make([]byte, alignedOffset-uint64(offset))
		if _, err := jf.f.Write(padding); err != nil {
			return 0, err
		}
		offset = int64(alignedOffset)
	}

	// Build and write data object header
	dataHdr := DataObject{
		ObjectHeader:    ObjectHeader{Type: ObjectData, Size: objSize},
		Hash:            hash,
		NextFieldOffset: prevHeadData, // link to previous head
	}
	if err := binary.Write(jf.f, le, &dataHdr); err != nil {
		return 0, err
	}
	// Write payload
	if _, err := jf.f.Write(data); err != nil {
		return 0, err
	}

	// Update field's head_data_offset to point to this new data object
	newHeadData := uint64(offset)
	if err := jf.writeAt(int64(fieldOffset)+FieldObjectHeaderSize-8, &newHeadData); err != nil {
		return 0, err
	}

	// Link into data hash table
	hashIdx := hash % uint64(len(jf.dataHashTable))
	if err := jf.linkDataHash(uint64(offset), hash, hashIdx); err != nil {
		return 0, err
	}

	// Update header
	jf.header.NObjects++
	jf.header.NData++
	jf.header.TailObjectOffset = uint64(offset)

	return uint64(offset), nil
}

func (jf *File) ensureField(fieldName []byte, hash uint64) (uint64, error) {
	// Check cache first
	if offset, ok := jf.fieldCache[hash]; ok {
		return offset, nil
	}

	// Object size is exact (header + payload)
	objSize := uint64(FieldObjectHeaderSize + len(fieldName))

	offset, err := jf.f.Seek(0, 2)
	if err != nil {
		return 0, err
	}
	// Align offset to 8 bytes
	alignedOffset := align64(uint64(offset))
	if alignedOffset != uint64(offset) {
		padding := make([]byte, alignedOffset-uint64(offset))
		if _, err := jf.f.Write(padding); err != nil {
			return 0, err
		}
		offset = int64(alignedOffset)
	}

	// Build and write field object header
	fieldHdr := FieldObject{
		ObjectHeader: ObjectHeader{Type: ObjectField, Size: objSize},
		Hash:         hash,
	}
	if err := binary.Write(jf.f, le, &fieldHdr); err != nil {
		return 0, err
	}
	// Write payload (field name)
	if _, err := jf.f.Write(fieldName); err != nil {
		return 0, err
	}

	// Link into field hash table
	hashIdx := hash % uint64(len(jf.fieldHashTable))
	if err := jf.linkFieldHash(uint64(offset), hash, hashIdx); err != nil {
		return 0, err
	}

	// Cache it
	jf.fieldCache[hash] = uint64(offset)

	jf.header.NObjects++
	jf.header.NFields++
	jf.header.TailObjectOffset = uint64(offset)

	return uint64(offset), nil
}

func (jf *File) linkDataHash(offset, hash uint64, idx uint64) error {
	// Update in-memory hash table
	item := &jf.dataHashTable[idx]
	prevTail := item.TailHashOffset

	if item.HeadHashOffset == 0 {
		item.HeadHashOffset = offset
	}
	item.TailHashOffset = offset

	// If there was a previous tail, update its next_hash_offset to point to this new one
	if prevTail != 0 {
		// Data object's next_hash_offset is at byte 24 (after ObjectHeader + Hash)
		if err := jf.writeAt(int64(prevTail)+ObjectHeaderSize+8, &offset); err != nil {
			return err
		}
	}

	// Write hash table entry to disk
	hashTableOffset := int64(jf.header.DataHashTableOffset + idx*HashItemSize)
	return jf.writeAt(hashTableOffset, item)
}

func (jf *File) linkFieldHash(offset, hash uint64, idx uint64) error {
	// Update in-memory hash table
	item := &jf.fieldHashTable[idx]
	if item.HeadHashOffset == 0 {
		item.HeadHashOffset = offset
	}
	item.TailHashOffset = offset

	// Write hash table entry to disk
	hashTableOffset := int64(jf.header.FieldHashTableOffset + idx*HashItemSize)
	return jf.writeAt(hashTableOffset, item)
}

// linkDataToEntry updates a data object to reference an entry.
// For the first entry, sets entry_offset. For additional entries, uses entry arrays.
func (jf *File) linkDataToEntry(dataOffset, entryOffset uint64) error {
	// Read current entry info from data object (at offset 40 = DataObjectHeaderSize - 24)
	entryInfoOffset := int64(dataOffset) + DataObjectHeaderSize - 24
	var info DataEntryInfo
	if err := jf.readAt(entryInfoOffset, &info); err != nil {
		return err
	}

	if info.EntryOffset == 0 {
		// First entry - just set entry_offset
		info.EntryOffset = entryOffset
	} else {
		// Additional entry - need to use entry array
		if info.EntryArrayOffset == 0 {
			// Create first entry array
			arrayOffset, err := jf.createEntryArray(entryOffset)
			if err != nil {
				return err
			}
			info.EntryArrayOffset = arrayOffset
		} else {
			// Append to existing entry array chain
			if err := jf.appendToEntryArray(info.EntryArrayOffset, entryOffset); err != nil {
				return err
			}
		}
	}

	// Increment n_entries and write back
	info.NEntries++
	return jf.writeAt(entryInfoOffset, &info)
}

// createEntryArray creates a new entry array object with one entry.
func (jf *File) createEntryArray(entryOffset uint64) (uint64, error) {
	// Entry array: ObjectHeader (16) + next_offset (8) + items...
	// Start with space for 16 entries
	const initialCapacity = 16
	objSize := uint64(EntryArrayObjectHeaderSize + 8*initialCapacity)

	offset, err := jf.f.Seek(0, 2)
	if err != nil {
		return 0, err
	}
	alignedOffset := align64(uint64(offset))
	if alignedOffset != uint64(offset) {
		padding := make([]byte, alignedOffset-uint64(offset))
		if _, err := jf.f.Write(padding); err != nil {
			return 0, err
		}
		offset = int64(alignedOffset)
	}

	// Write entry array header
	arrayHdr := EntryArrayObject{
		ObjectHeader: ObjectHeader{Type: ObjectEntryArray, Size: objSize},
	}
	if err := binary.Write(jf.f, le, &arrayHdr); err != nil {
		return 0, err
	}
	// Write first item and padding (zeros for remaining slots)
	items := make([]uint64, initialCapacity)
	items[0] = entryOffset
	if err := binary.Write(jf.f, le, items); err != nil {
		return 0, err
	}

	jf.header.NObjects++
	jf.header.NEntryArrays++
	jf.header.TailObjectOffset = uint64(offset)

	return uint64(offset), nil
}

// appendToEntryArray appends an entry to the entry array chain.
func (jf *File) appendToEntryArray(arrayOffset, entryOffset uint64) error {
	// Find the last array in the chain and find a free slot
	for {
		// Read array header
		var hdr EntryArrayObject
		if err := jf.readAt(int64(arrayOffset), &hdr); err != nil {
			return err
		}

		// If there's a next array, follow the chain
		if hdr.NextEntryArrayOffset != 0 {
			arrayOffset = hdr.NextEntryArrayOffset
			continue
		}

		// Read items to find a free slot (0 means empty)
		itemCount := (hdr.Size - EntryArrayObjectHeaderSize) / 8
		items := make([]uint64, itemCount)
		itemsOffset := int64(arrayOffset) + EntryArrayObjectHeaderSize
		buf := make([]byte, itemCount*8)
		if _, err := jf.f.ReadAt(buf, itemsOffset); err != nil {
			return err
		}
		if err := binary.Read(bytes.NewReader(buf), le, items); err != nil {
			return err
		}

		for i := uint64(0); i < itemCount; i++ {
			if items[i] == 0 {
				// Found free slot, write entry offset
				slotOffset := itemsOffset + int64(i*8)
				if err := jf.writeAt(slotOffset, &entryOffset); err != nil {
					return err
				}
				return nil
			}
		}

		// No free slot, create a new array and link it
		newArrayOffset, err := jf.createEntryArray(entryOffset)
		if err != nil {
			return err
		}

		// Update next_entry_array_offset in current array
		if err := jf.writeAt(int64(arrayOffset)+ObjectHeaderSize, &newArrayOffset); err != nil {
			return err
		}

		return nil
	}
}

func (jf *File) appendEntryObject(realtime, monotonic, xorHash uint64, items []EntryItem) error {
	// Entry objects are already 8-byte aligned (header is 64 bytes, items are 16 bytes each)
	objSize := uint64(EntryObjectHeaderSize + len(items)*EntryItemSize)

	offset, err := jf.f.Seek(0, 2)
	if err != nil {
		return err
	}
	// Align offset to 8 bytes
	alignedOffset := align64(uint64(offset))
	if alignedOffset != uint64(offset) {
		padding := make([]byte, alignedOffset-uint64(offset))
		if _, err := jf.f.Write(padding); err != nil {
			return err
		}
		offset = int64(alignedOffset)
	}

	// Increment seqnum
	jf.header.TailEntrySeqnum++
	seqnum := jf.header.TailEntrySeqnum

	// Write entry header
	entryHdr := EntryObject{
		ObjectHeader: ObjectHeader{Type: ObjectEntry, Size: objSize},
		Seqnum:       seqnum,
		Realtime:     realtime,
		Monotonic:    monotonic,
		BootID:       jf.bootID,
		XorHash:      xorHash,
	}
	if err := binary.Write(jf.f, le, &entryHdr); err != nil {
		return err
	}
	// Write items
	if err := binary.Write(jf.f, le, items); err != nil {
		return err
	}

	entryOffset := uint64(offset)

	// Link each data object to this entry (set entry_offset if not already set, increment n_entries)
	for _, item := range items {
		if err := jf.linkDataToEntry(item.ObjectOffset, entryOffset); err != nil {
			return fmt.Errorf("linking data to entry: %w", err)
		}
	}

	prevTailArray := jf.header.TailEntryArrayOffset
	newArrayOffset, err := jf.appendEntryArray(prevTailArray, entryOffset)
	if err != nil {
		return err
	}

	// Update header counts after the entry array is in place.
	jf.header.NObjects += 2 // entry + entry array
	jf.header.NEntryArrays++
	if jf.header.EntryArrayOffset == 0 {
		jf.header.EntryArrayOffset = newArrayOffset
	}
	jf.header.TailEntryArrayOffset = uint32(newArrayOffset)
	jf.header.TailEntryArrayNEntries = 1
	jf.header.TailObjectOffset = newArrayOffset
	jf.header.NEntries++
	jf.header.TailEntryOffset = entryOffset
	jf.header.TailEntryBootID = jf.bootID
	jf.header.TailEntryRealtime = realtime
	jf.header.TailEntryMonotonic = monotonic

	if jf.header.HeadEntrySeqnum == 0 {
		jf.header.HeadEntrySeqnum = seqnum
		jf.header.HeadEntryRealtime = realtime
	}
	if endPos, err := jf.f.Seek(0, 2); err == nil {
		jf.header.ArenaSize = uint64(endPos) - HeaderSize
	}

	// Mark dirty; Sync/Close will flip offline and fsync.
	jf.dirty = true
	return nil
}

// Sync flushes pending writes and sets state to OFFLINE so external readers
// (like journalctl) can access the file. The file remains open for further writes.
// This mimics journald's SyncIntervalSec behavior.
func (jf *File) Sync() error {
	jf.mu.Lock()
	defer jf.mu.Unlock()

	return jf.syncLocked()
}

// Close closes the journal file
func (jf *File) Close() error {
	jf.mu.Lock()
	defer jf.mu.Unlock()

	if err := jf.syncLocked(); err != nil {
		return err
	}
	return jf.f.Close()
}

// syncLocked assumes jf.mu is held.
func (jf *File) syncLocked() error {
	if !jf.dirty {
		return nil
	}

	// While we append the entry array we should advertise ONLINE like real journald.
	if err := jf.beginWrite(); err != nil {
		return err
	}

	// Update arena size
	endPos, err := jf.f.Seek(0, 2)
	if err == nil {
		jf.header.ArenaSize = uint64(endPos) - HeaderSize
	}

	// Set offline so readers can access a fully consistent header
	jf.header.State = StateOffline
	if err := jf.syncHeader(); err != nil {
		return err
	}

	// Fsync to ensure data is on disk
	if err := jf.f.Sync(); err != nil {
		return err
	}

	jf.dirty = false
	return nil
}

// appendEntryArray writes a new entry array object containing a single entry offset
// and links it from the previous tail. Returns the offset of the new array.
func (jf *File) appendEntryArray(prevTail uint32, entryOffset uint64) (uint64, error) {
	objSize := uint64(EntryArrayObjectHeaderSize + 8) // one offset

	offset, err := jf.f.Seek(0, 2)
	if err != nil {
		return 0, err
	}
	// Align offset to 8 bytes
	alignedOffset := align64(uint64(offset))
	if alignedOffset != uint64(offset) {
		padding := make([]byte, alignedOffset-uint64(offset))
		if _, err := jf.f.Write(padding); err != nil {
			return 0, err
		}
		offset = int64(alignedOffset)
	}

	// Write entry array header + single item
	arrayHdr := EntryArrayObject{
		ObjectHeader: ObjectHeader{Type: ObjectEntryArray, Size: objSize},
	}
	if err := binary.Write(jf.f, le, &arrayHdr); err != nil {
		return 0, err
	}
	if err := binary.Write(jf.f, le, &entryOffset); err != nil {
		return 0, err
	}

	// Link previous tail to this new array
	if prevTail != 0 {
		linkOffset := uint64(offset)
		if err := jf.writeAt(int64(prevTail)+ObjectHeaderSize, &linkOffset); err != nil {
			return 0, err
		}
	}

	return uint64(offset), nil
}

// Path returns the file path
func (jf *File) Path() string {
	return jf.path
}
