package journalfile

import (
	"encoding/binary"
)

// File signature "LPKSHHRH"
var HeaderSignature = [8]byte{'L', 'P', 'K', 'S', 'H', 'H', 'R', 'H'}

// Object types
const (
	ObjectUnused         = 0
	ObjectData           = 1
	ObjectField          = 2
	ObjectEntry          = 3
	ObjectDataHashTable  = 4
	ObjectFieldHashTable = 5
	ObjectEntryArray     = 6
	ObjectTag            = 7
)

// Header flags
const (
	HeaderIncompatibleCompressedXZ   = 1 << 0
	HeaderIncompatibleCompressedLZ4  = 1 << 1
	HeaderIncompatibleKeyedHash      = 1 << 2
	HeaderIncompatibleCompressedZSTD = 1 << 3
	HeaderIncompatibleCompact        = 1 << 4
)

// State values
const (
	StateOffline  = 0
	StateOnline   = 1
	StateArchived = 2
)

// ID128 is a 128-bit identifier (like sd_id128_t)
type ID128 [16]byte

// Header is the journal file header (272 bytes)
type Header struct {
	Signature            [8]byte // "LPKSHHRH"
	CompatibleFlags      uint32  // le32
	IncompatibleFlags    uint32  // le32
	State                uint8   // file state
	Reserved             [7]byte // padding
	FileID               ID128   // unique file ID
	MachineID            ID128   // machine ID
	TailEntryBootID      ID128   // boot ID of last entry
	SeqnumID             ID128   // seqnum ID
	HeaderSize           uint64  // le64
	ArenaSize            uint64  // le64 - size of data area
	DataHashTableOffset  uint64  // le64
	DataHashTableSize    uint64  // le64
	FieldHashTableOffset uint64  // le64
	FieldHashTableSize   uint64  // le64
	TailObjectOffset     uint64  // le64 - offset of last object
	NObjects             uint64  // le64 - number of objects
	NEntries             uint64  // le64 - number of entries
	TailEntrySeqnum      uint64  // le64
	HeadEntrySeqnum      uint64  // le64
	EntryArrayOffset     uint64  // le64
	HeadEntryRealtime    uint64  // le64
	TailEntryRealtime    uint64  // le64
	TailEntryMonotonic   uint64  // le64
	// Added in v187
	NData   uint64 // le64
	NFields uint64 // le64
	// Added in v189
	NTags        uint64 // le64
	NEntryArrays uint64 // le64
	// Added in v246
	DataHashChainDepth  uint64 // le64
	FieldHashChainDepth uint64 // le64
	// Added in v252
	TailEntryArrayOffset   uint32 // le32
	TailEntryArrayNEntries uint32 // le32
	// Added in v254
	TailEntryOffset uint64 // le64
}

// HeaderSize matches the on-disk header length used by journald.
const HeaderSize = 272

// ObjectHeader is the common header for all objects
type ObjectHeader struct {
	Type     uint8
	Flags    uint8
	Reserved [6]byte
	Size     uint64 // le64 - total size including header
}

const ObjectHeaderSize = 16

// HashItem is an entry in a hash table
type HashItem struct {
	HeadHashOffset uint64 // le64
	TailHashOffset uint64 // le64
}

const HashItemSize = 16

// DataObject stores field=value data
type DataObject struct {
	ObjectHeader
	Hash             uint64 // le64 - hash of payload
	NextHashOffset   uint64 // le64 - next in hash chain
	NextFieldOffset  uint64 // le64 - next data with same field
	EntryOffset      uint64 // le64 - first entry referencing this
	EntryArrayOffset uint64 // le64 - entry array for more refs
	NEntries         uint64 // le64 - number of entries referencing
	// Payload follows (field=value)
}

const DataObjectHeaderSize = ObjectHeaderSize + 48

// FieldObject stores field names
type FieldObject struct {
	ObjectHeader
	Hash           uint64 // le64
	NextHashOffset uint64 // le64
	HeadDataOffset uint64 // le64 - first data object with this field
	// Payload follows (field name without =)
}

const FieldObjectHeaderSize = ObjectHeaderSize + 24

// EntryItem is a reference to a data object within an entry
type EntryItem struct {
	ObjectOffset uint64 // le64
	Hash         uint64 // le64
}

const EntryItemSize = 16

// EntryObject binds data objects into a log entry
type EntryObject struct {
	ObjectHeader
	Seqnum    uint64 // le64
	Realtime  uint64 // le64 - CLOCK_REALTIME timestamp (usec)
	Monotonic uint64 // le64 - CLOCK_MONOTONIC timestamp (usec)
	BootID    ID128
	XorHash   uint64 // le64 - XOR of all data hashes
	// Items follow (array of EntryItem)
}

const EntryObjectHeaderSize = ObjectHeaderSize + 48

// EntryArrayObject is a linked list of entry offsets
type EntryArrayObject struct {
	ObjectHeader
	NextEntryArrayOffset uint64 // le64
	// Items follow (array of uint64 offsets)
}

const EntryArrayObjectHeaderSize = ObjectHeaderSize + 8

// DataEntryInfo holds the entry-related fields of a DataObject (for partial reads/writes)
type DataEntryInfo struct {
	EntryOffset      uint64
	EntryArrayOffset uint64
	NEntries         uint64
}

// Endianness for binary.Read/Write
var le = binary.LittleEndian
