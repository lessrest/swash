package journalfile

import "github.com/dchest/siphash"

// SipHash24 computes a 64-bit keyed hash using SipHash-2-4.
// The key is the file_id from the journal header (16 bytes).
// This is the preferred hash function for newer journal files
// (when HEADER_INCOMPATIBLE_KEYED_HASH is set).
func SipHash24(data []byte, key ID128) uint64 {
	// siphash.Hash expects k0 and k1 as two uint64 values.
	// The ID128 is 16 bytes, which we split into two 64-bit LE integers.
	k0 := le.Uint64(key[0:8])
	k1 := le.Uint64(key[8:16])
	return siphash.Hash(k0, k1, data)
}
