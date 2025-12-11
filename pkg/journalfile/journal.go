// Package journalfile implements a writer for systemd journal (*.journal) files.
//
// This package is intended to be usable independently of swash; swash uses it
// to let `cmd/mini-systemd` write real journal files that can be read by
// `journalctl`.
package journalfile

import (
	"bytes"
	"crypto/rand"
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
	// Write the entire header in a single syscall. At 272 bytes this fits well
	// within a filesystem block, giving readers a coherent view without the
	// risk of observing partially-updated fields.
	_, err := jf.f.WriteAt(jf.header.Encode(), 0)
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
		hash := JenkinsHash64(data)

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
		xorHash ^= hash
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

	// First, ensure field object exists
	fieldHash := JenkinsHash64(fieldName)
	fieldOffset, err := jf.ensureField(fieldName, fieldHash)
	if err != nil {
		return 0, err
	}

	// Read the field's current head_data_offset (at offset 32 in the field object)
	var prevHeadData uint64
	if _, err := jf.f.Seek(int64(fieldOffset)+32, 0); err != nil {
		return 0, err
	}
	headBuf := make([]byte, 8)
	if _, err := jf.f.Read(headBuf); err != nil {
		return 0, err
	}
	prevHeadData = le.Uint64(headBuf)

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
	buf[0] = ObjectData // type
	buf[1] = 0          // flags (no compression)
	le.PutUint64(buf[8:16], objSize)
	le.PutUint64(buf[16:24], hash)
	// NextHashOffset at 24: 0 (we don't chain data objects in hash table)
	le.PutUint64(buf[32:40], prevHeadData) // NextFieldOffset: link to previous head
	// EntryOffset, EntryArrayOffset, NEntries: 0 for now
	copy(buf[DataObjectHeaderSize:], data)

	if _, err := jf.f.Write(buf); err != nil {
		return 0, err
	}

	// Update field's head_data_offset to point to this new data object
	if _, err := jf.f.Seek(int64(fieldOffset)+32, 0); err != nil {
		return 0, err
	}
	le.PutUint64(headBuf, uint64(offset))
	if _, err := jf.f.Write(headBuf); err != nil {
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

	buf := make([]byte, objSize)
	buf[0] = ObjectField
	le.PutUint64(buf[8:16], objSize)
	le.PutUint64(buf[16:24], hash)
	// NextHashOffset at offset 24: will be set by linkFieldHash
	// HeadDataOffset at offset 32: starts as 0, updated when data is linked
	copy(buf[FieldObjectHeaderSize:], fieldName)

	if _, err := jf.f.Write(buf); err != nil {
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
		// Data object's next_hash_offset is at byte offset 24
		if _, err := jf.f.Seek(int64(prevTail)+24, 0); err != nil {
			return err
		}
		buf := make([]byte, 8)
		le.PutUint64(buf, offset)
		if _, err := jf.f.Write(buf); err != nil {
			return err
		}
	}

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

func (jf *File) linkFieldHash(offset, hash uint64, idx uint64) error {
	// Update in-memory hash table
	item := &jf.fieldHashTable[idx]
	if item.HeadHashOffset == 0 {
		item.HeadHashOffset = offset
	}
	item.TailHashOffset = offset

	// Write hash table entry to disk
	hashTableOffset := jf.header.FieldHashTableOffset + idx*HashItemSize
	if _, err := jf.f.Seek(int64(hashTableOffset), 0); err != nil {
		return err
	}

	buf := make([]byte, HashItemSize)
	le.PutUint64(buf[0:8], item.HeadHashOffset)
	le.PutUint64(buf[8:16], item.TailHashOffset)
	_, err := jf.f.Write(buf)
	return err
}

// linkDataToEntry updates a data object to reference an entry.
// For the first entry, sets entry_offset. For additional entries, uses entry arrays.
func (jf *File) linkDataToEntry(dataOffset, entryOffset uint64) error {
	// Data object layout:
	// 40-48: entry_offset
	// 48-56: entry_array_offset
	// 56-64: n_entries

	// Read current state
	if _, err := jf.f.Seek(int64(dataOffset)+40, 0); err != nil {
		return err
	}
	buf := make([]byte, 24)
	if _, err := jf.f.Read(buf); err != nil {
		return err
	}

	currentEntryOffset := le.Uint64(buf[0:8])
	entryArrayOffset := le.Uint64(buf[8:16])
	nEntries := le.Uint64(buf[16:24])

	if currentEntryOffset == 0 {
		// First entry - just set entry_offset
		le.PutUint64(buf[0:8], entryOffset)
	} else {
		// Additional entry - need to use entry array
		if entryArrayOffset == 0 {
			// Create first entry array
			arrayOffset, err := jf.createEntryArray(entryOffset)
			if err != nil {
				return err
			}
			le.PutUint64(buf[8:16], arrayOffset)
		} else {
			// Append to existing entry array chain
			if err := jf.appendToEntryArray(entryArrayOffset, entryOffset); err != nil {
				return err
			}
		}
	}

	// Increment n_entries
	le.PutUint64(buf[16:24], nEntries+1)

	// Write back
	if _, err := jf.f.Seek(int64(dataOffset)+40, 0); err != nil {
		return err
	}
	if _, err := jf.f.Write(buf); err != nil {
		return err
	}

	return nil
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

	buf := make([]byte, objSize)
	buf[0] = ObjectEntryArray
	le.PutUint64(buf[8:16], objSize)
	// next_entry_array_offset at 16: 0
	// First item at 24
	le.PutUint64(buf[24:32], entryOffset)

	if _, err := jf.f.Write(buf); err != nil {
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
		if _, err := jf.f.Seek(int64(arrayOffset), 0); err != nil {
			return err
		}
		header := make([]byte, EntryArrayObjectHeaderSize)
		if _, err := jf.f.Read(header); err != nil {
			return err
		}

		objSize := le.Uint64(header[8:16])
		nextArray := le.Uint64(header[16:24])

		// If there's a next array, follow the chain
		if nextArray != 0 {
			arrayOffset = nextArray
			continue
		}

		// Read items to find a free slot (0 means empty)
		itemCount := (objSize - EntryArrayObjectHeaderSize) / 8
		items := make([]byte, itemCount*8)
		if _, err := jf.f.Read(items); err != nil {
			return err
		}

		for i := uint64(0); i < itemCount; i++ {
			if le.Uint64(items[i*8:(i+1)*8]) == 0 {
				// Found free slot, write entry offset
				if _, err := jf.f.Seek(int64(arrayOffset)+int64(EntryArrayObjectHeaderSize)+int64(i*8), 0); err != nil {
					return err
				}
				buf := make([]byte, 8)
				le.PutUint64(buf, entryOffset)
				if _, err := jf.f.Write(buf); err != nil {
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
		if _, err := jf.f.Seek(int64(arrayOffset)+16, 0); err != nil {
			return err
		}
		buf := make([]byte, 8)
		le.PutUint64(buf, newArrayOffset)
		if _, err := jf.f.Write(buf); err != nil {
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

	buf := make([]byte, objSize)
	buf[0] = ObjectEntryArray
	le.PutUint64(buf[8:16], objSize)
	// NextEntryArrayOffset remains zero until we link a subsequent array.
	le.PutUint64(buf[EntryArrayObjectHeaderSize:], entryOffset)

	if _, err := jf.f.Write(buf); err != nil {
		return 0, err
	}

	// Link previous tail to this new array.
	if prevTail != 0 {
		if _, err := jf.f.Seek(int64(prevTail)+16, 0); err != nil {
			return 0, err
		}
		link := make([]byte, 8)
		le.PutUint64(link, uint64(offset))
		if _, err := jf.f.Write(link); err != nil {
			return 0, err
		}
	}

	return uint64(offset), nil
}

// Path returns the file path
func (jf *File) Path() string {
	return jf.path
}
