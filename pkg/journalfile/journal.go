package journalfile

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"sync"
	"time"
)

// Default hash table sizes (number of HashItems)
const (
	DefaultDataHashTableSize  = 2047  // ~32KB
	DefaultFieldHashTableSize = 333   // ~5KB
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

	// Entry array tracking (written at Close)
	entryArrayItems []uint64 // entry offsets to include in entry array
}

// Create creates a new journal file at path
func Create(path string, machineID, bootID ID128) (*File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return nil, fmt.Errorf("create journal file: %w", err)
	}

	jf := &File{
		f:         f,
		path:      path,
		machineID: machineID,
		bootID:    bootID,
		dataCache: make(map[uint64]uint64),
	}

	if err := jf.initialize(); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
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
		IncompatibleFlags:    0, // No compression, no keyed hash for simplicity
		State:                StateOnline,
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
	if _, err := jf.f.Write(jf.header.Encode()); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	// Write data hash table object with ObjectHeader
	jf.dataHashTable = make([]HashItem, DefaultDataHashTableSize)
	dataHashObj := make([]byte, dataHashObjSize)
	dataHashObj[0] = ObjectDataHashTable // type
	le.PutUint64(dataHashObj[8:16], dataHashObjSize)
	if _, err := jf.f.Write(dataHashObj); err != nil {
		return fmt.Errorf("write data hash table: %w", err)
	}

	// Write field hash table object with ObjectHeader
	jf.fieldHashTable = make([]HashItem, DefaultFieldHashTableSize)
	fieldHashObj := make([]byte, fieldHashObjSize)
	fieldHashObj[0] = ObjectFieldHashTable // type
	le.PutUint64(fieldHashObj[8:16], fieldHashObjSize)
	if _, err := jf.f.Write(fieldHashObj); err != nil {
		return fmt.Errorf("write field hash table: %w", err)
	}

	// Update tail object offset (last hash table object)
	jf.header.TailObjectOffset = fieldHashObjOffset

	return jf.syncHeader()
}

func (jf *File) syncHeader() error {
	if _, err := jf.f.Seek(0, 0); err != nil {
		return err
	}
	_, err := jf.f.Write(jf.header.Encode())
	return err
}

// AppendEntry appends a new journal entry with the given fields
func (jf *File) AppendEntry(fields map[string]string) error {
	jf.mu.Lock()
	defer jf.mu.Unlock()

	now := time.Now()
	realtime := uint64(now.UnixMicro())
	monotonic := uint64(now.UnixNano() / 1000) // approximate

	// Build data objects for each field
	items := make([]EntryItem, 0, len(fields))
	var xorHash uint64

	for k, v := range fields {
		data := []byte(k + "=" + v)
		hash := JenkinsHash64(data)

		// Check if data already exists
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
		xorHash ^= hash
	}

	// Append entry object
	return jf.appendEntryObject(realtime, monotonic, xorHash, items)
}

// align64 rounds up to the next 8-byte boundary
func align64(n uint64) uint64 {
	return (n + 7) &^ 7
}

func (jf *File) appendData(data []byte, hash uint64) (uint64, error) {
	// Find the field name (everything before =)
	eqIdx := bytes.IndexByte(data, '=')
	if eqIdx < 0 {
		return 0, fmt.Errorf("invalid data: no = found")
	}
	fieldName := data[:eqIdx]

	// First, ensure field object exists
	fieldHash := JenkinsHash64(fieldName)
	_, err := jf.ensureField(fieldName, fieldHash)
	if err != nil {
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

	// Build data object with exact size
	buf := make([]byte, objSize)
	buf[0] = ObjectData        // type
	buf[1] = 0                 // flags (no compression)
	le.PutUint64(buf[8:16], objSize)
	le.PutUint64(buf[16:24], hash)
	// NextHashOffset, NextFieldOffset, EntryOffset, EntryArrayOffset, NEntries all 0 for now
	copy(buf[DataObjectHeaderSize:], data)

	if _, err := jf.f.Write(buf); err != nil {
		return 0, err
	}

	// Link into hash table
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
	// For simplicity, we'll create a new field object each time
	// A proper implementation would check the field hash table first

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

	buf := make([]byte, objSize)
	buf[0] = ObjectField
	le.PutUint64(buf[8:16], objSize)
	le.PutUint64(buf[16:24], hash)
	copy(buf[FieldObjectHeaderSize:], fieldName)

	if _, err := jf.f.Write(buf); err != nil {
		return 0, err
	}

	jf.header.NObjects++
	jf.header.NFields++
	jf.header.TailObjectOffset = uint64(offset)

	return uint64(offset), nil
}

func (jf *File) linkDataHash(offset, hash uint64, idx uint64) error {
	// Update in-memory hash table
	item := &jf.dataHashTable[idx]
	if item.HeadHashOffset == 0 {
		item.HeadHashOffset = offset
	}
	item.TailHashOffset = offset

	// Write hash table entry to disk
	hashTableOffset := jf.header.DataHashTableOffset + idx*HashItemSize
	if _, err := jf.f.Seek(int64(hashTableOffset), 0); err != nil {
		return err
	}

	buf := make([]byte, HashItemSize)
	le.PutUint64(buf[0:8], item.HeadHashOffset)
	le.PutUint64(buf[8:16], item.TailHashOffset)
	_, err := jf.f.Write(buf)
	return err
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

	buf := make([]byte, objSize)
	buf[0] = ObjectEntry
	le.PutUint64(buf[8:16], objSize)
	le.PutUint64(buf[16:24], seqnum)
	le.PutUint64(buf[24:32], realtime)
	le.PutUint64(buf[32:40], monotonic)
	copy(buf[40:56], jf.bootID[:])
	le.PutUint64(buf[56:64], xorHash)

	// Write items
	for i, item := range items {
		itemOffset := EntryObjectHeaderSize + i*EntryItemSize
		le.PutUint64(buf[itemOffset:itemOffset+8], item.ObjectOffset)
		le.PutUint64(buf[itemOffset+8:itemOffset+16], item.Hash)
	}

	if _, err := jf.f.Write(buf); err != nil {
		return err
	}

	// Update header
	jf.header.NObjects++
	jf.header.NEntries++
	jf.header.TailObjectOffset = uint64(offset)
	jf.header.TailEntryOffset = uint64(offset)
	jf.header.TailEntryBootID = jf.bootID
	jf.header.TailEntryRealtime = realtime
	jf.header.TailEntryMonotonic = monotonic

	if jf.header.HeadEntrySeqnum == 0 {
		jf.header.HeadEntrySeqnum = seqnum
		jf.header.HeadEntryRealtime = realtime
	}

	// Track entry for entry array (will be written at Close)
	jf.entryArrayItems = append(jf.entryArrayItems, uint64(offset))

	return jf.syncHeader()
}

// Sync flushes pending writes and sets state to OFFLINE so external readers
// (like journalctl) can access the file. The file remains open for further writes.
// This mimics journald's SyncIntervalSec behavior.
func (jf *File) Sync() error {
	jf.mu.Lock()
	defer jf.mu.Unlock()

	// Write entry array if we have entries
	if len(jf.entryArrayItems) > 0 {
		if err := jf.writeEntryArray(); err != nil {
			return err
		}
	}

	// Update arena size
	endPos, err := jf.f.Seek(0, 2)
	if err == nil {
		jf.header.ArenaSize = uint64(endPos) - HeaderSize
	}

	// Set offline so readers can access
	jf.header.State = StateOffline
	if err := jf.syncHeader(); err != nil {
		return err
	}

	// Fsync to ensure data is on disk
	return jf.f.Sync()
}

// Close closes the journal file
func (jf *File) Close() error {
	jf.mu.Lock()
	defer jf.mu.Unlock()

	// Write entry array if we have entries
	if len(jf.entryArrayItems) > 0 {
		if err := jf.writeEntryArray(); err != nil {
			return err
		}
	}

	// Compute arena size from file size
	endPos, err := jf.f.Seek(0, 2)
	if err == nil {
		jf.header.ArenaSize = uint64(endPos) - HeaderSize
	}

	jf.header.State = StateOffline
	jf.syncHeader()
	return jf.f.Close()
}

func (jf *File) writeEntryArray() error {
	nItems := len(jf.entryArrayItems)
	// Entry array: header (24 bytes) + items (8 bytes each), already 8-byte aligned
	objSize := uint64(EntryArrayObjectHeaderSize + nItems*8)

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

	buf := make([]byte, objSize)
	buf[0] = ObjectEntryArray
	le.PutUint64(buf[8:16], objSize)
	// NextEntryArrayOffset = 0 at offset 16

	for i, entryOff := range jf.entryArrayItems {
		off := EntryArrayObjectHeaderSize + i*8
		le.PutUint64(buf[off:off+8], entryOff)
	}

	if _, err := jf.f.Write(buf); err != nil {
		return err
	}

	jf.header.EntryArrayOffset = uint64(offset)
	jf.header.NObjects++
	jf.header.NEntryArrays++
	jf.header.TailObjectOffset = uint64(offset)
	// For a single entry array, it's both head and tail
	jf.header.TailEntryArrayOffset = uint32(offset)
	jf.header.TailEntryArrayNEntries = uint32(nItems)

	// Clear items so subsequent flushes only include new entries
	jf.entryArrayItems = jf.entryArrayItems[:0]

	return nil
}

// Path returns the file path
func (jf *File) Path() string {
	return jf.path
}
