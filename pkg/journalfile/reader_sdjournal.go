//go:build linux

package journalfile

import (
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"
)

// SDJournalReader wraps sdjournal.Journal to implement JournalReader.
// This provides access to the reference libsystemd implementation on Linux.
type SDJournalReader struct {
	j    *sdjournal.Journal
	path string
}

// Verify SDJournalReader implements JournalReader at compile time.
var _ JournalReader = (*SDJournalReader)(nil)

// OpenSDJournal opens a journal file using libsystemd's sdjournal.
// This is only available on Linux with libsystemd installed.
func OpenSDJournal(path string) (*SDJournalReader, error) {
	j, err := sdjournal.NewJournalFromFiles(path)
	if err != nil {
		return nil, fmt.Errorf("sdjournal open: %w", err)
	}
	return &SDJournalReader{j: j, path: path}, nil
}

func (r *SDJournalReader) Close() error {
	return r.j.Close()
}

func (r *SDJournalReader) AddMatch(field, value string) {
	// sdjournal uses "FIELD=value" format
	_ = r.j.AddMatch(field + "=" + value)
}

func (r *SDJournalReader) FlushMatches() {
	r.j.FlushMatches()
}

func (r *SDJournalReader) SeekHead() {
	_ = r.j.SeekHead()
}

func (r *SDJournalReader) SeekCursor(cursor string) error {
	if err := r.j.SeekCursor(cursor); err != nil {
		return err
	}
	// SeekCursor positions at the cursor; we need to move past it
	_, _ = r.j.Next()
	return nil
}

func (r *SDJournalReader) Next() (*Entry, error) {
	n, err := r.j.Next()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, io.EOF
	}

	entry, err := r.j.GetEntry()
	if err != nil {
		return nil, fmt.Errorf("get entry: %w", err)
	}

	// Convert sdjournal entry to our Entry type
	var bootID ID128
	if bootStr, ok := entry.Fields["_BOOT_ID"]; ok {
		if decoded, err := hex.DecodeString(bootStr); err == nil && len(decoded) == 16 {
			copy(bootID[:], decoded)
		}
	}

	// sdjournal doesn't expose sequence numbers, use monotonic timestamp instead
	return &Entry{
		Seqnum:    entry.MonotonicTimestamp, // Approximation - monotonic is ordered
		Realtime:  time.Unix(0, int64(entry.RealtimeTimestamp)*1000),
		Monotonic: entry.MonotonicTimestamp,
		BootID:    bootID,
		Fields:    entry.Fields,
	}, nil
}

func (r *SDJournalReader) Refresh() error {
	// sdjournal auto-refreshes on Next(), but we can trigger a wait
	// with zero timeout to check for new entries
	r.j.Wait(0)
	return nil
}

func (r *SDJournalReader) NEntries() uint64 {
	// sdjournal doesn't expose entry count directly
	// Return 0 to indicate unknown
	return 0
}
