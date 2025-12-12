package journalfile

import (
	"io"
	"time"
)

// JournalReader is the interface for reading journal files.
// Implementations include the native pure-Go reader (works everywhere)
// and an sdjournal-based reader (Linux with libsystemd only).
type JournalReader interface {
	io.Closer

	// AddMatch adds a filter. Only entries where field=value will be returned.
	// Multiple matches are ANDed together.
	AddMatch(field, value string)

	// FlushMatches clears all filters.
	FlushMatches()

	// SeekHead positions the reader at the beginning.
	SeekHead()

	// SeekCursor positions the reader after the given cursor.
	SeekCursor(cursor string) error

	// Next reads the next entry matching filters. Returns io.EOF when done.
	Next() (*Entry, error)

	// Refresh re-reads to pick up new entries (for following).
	Refresh() error

	// NEntries returns the number of entries in the file.
	NEntries() uint64
}

// Entry represents a journal entry with its fields.
// This is the common entry type returned by all reader implementations.
type Entry struct {
	Seqnum    uint64
	Realtime  time.Time
	Monotonic uint64
	BootID    ID128
	Fields    map[string]string
}

// GetCursor returns a cursor string for the given entry.
// The cursor format is "s=<seqnum>;b=<bootid>" which allows seeking
// back to this position.
func GetCursor(e *Entry) string {
	return formatCursor(e.Seqnum, e.BootID)
}
