// Package journalfile implements the systemd journal file format.
// Based on https://systemd.io/JOURNAL_FILE_FORMAT/
package journalfile

import "math/bits"

// Jenkins hash functions ported from lookup3.c by Bob Jenkins.
// Public domain code.

func rot(x, k uint32) uint32 {
	return bits.RotateLeft32(x, int(k))
}

func mix(a, b, c *uint32) {
	*a -= *c
	*a ^= rot(*c, 4)
	*c += *b
	*b -= *a
	*b ^= rot(*a, 6)
	*a += *c
	*c -= *b
	*c ^= rot(*b, 8)
	*b += *a
	*a -= *c
	*a ^= rot(*c, 16)
	*c += *b
	*b -= *a
	*b ^= rot(*a, 19)
	*a += *c
	*c -= *b
	*c ^= rot(*b, 4)
	*b += *a
}

func final(a, b, c *uint32) {
	*c ^= *b
	*c -= rot(*b, 14)
	*a ^= *c
	*a -= rot(*c, 11)
	*b ^= *a
	*b -= rot(*a, 25)
	*c ^= *b
	*c -= rot(*b, 16)
	*a ^= *c
	*a -= rot(*c, 4)
	*b ^= *a
	*b -= rot(*a, 14)
	*c ^= *b
	*c -= rot(*b, 24)
}

// JenkinsHashLittle2 computes two 32-bit hash values from data.
// This is the hashlittle2 function from lookup3.c.
func JenkinsHashLittle2(data []byte) (pc, pb uint32) {
	length := len(data)
	a := uint32(0xdeadbeef) + uint32(length)
	b := a
	c := a

	// Process 12-byte chunks
	for len(data) > 12 {
		a += uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
		b += uint32(data[4]) | uint32(data[5])<<8 | uint32(data[6])<<16 | uint32(data[7])<<24
		c += uint32(data[8]) | uint32(data[9])<<8 | uint32(data[10])<<16 | uint32(data[11])<<24
		mix(&a, &b, &c)
		data = data[12:]
	}

	// Handle the last (probably partial) block
	switch len(data) {
	case 12:
		c += uint32(data[8]) | uint32(data[9])<<8 | uint32(data[10])<<16 | uint32(data[11])<<24
		fallthrough
	case 8:
		b += uint32(data[4]) | uint32(data[5])<<8 | uint32(data[6])<<16 | uint32(data[7])<<24
		fallthrough
	case 4:
		a += uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
	case 11:
		c += uint32(data[10]) << 16
		fallthrough
	case 10:
		c += uint32(data[9]) << 8
		fallthrough
	case 9:
		c += uint32(data[8])
		fallthrough
	case 7:
		b += uint32(data[6]) << 16
		fallthrough
	case 6:
		b += uint32(data[5]) << 8
		fallthrough
	case 5:
		b += uint32(data[4])
		a += uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
	case 3:
		a += uint32(data[2]) << 16
		fallthrough
	case 2:
		a += uint32(data[1]) << 8
		fallthrough
	case 1:
		a += uint32(data[0])
	case 0:
		return c, b
	}

	final(&a, &b, &c)
	return c, b
}

// JenkinsHash64 computes a 64-bit hash from data.
func JenkinsHash64(data []byte) uint64 {
	a, b := JenkinsHashLittle2(data)
	return uint64(a)<<32 | uint64(b)
}
