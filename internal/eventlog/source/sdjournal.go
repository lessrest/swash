// Package source provides EventSource implementations for reading from journals.
package source

import (
	"context"
	"fmt"
	"iter"
	"os"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"

	"github.com/mbrock/swash/internal/eventlog"
)

// SDJournalSource implements EventSource using go-systemd/sdjournal (CGO).
// This provides full libsystemd features including compression, sealing, etc.
type SDJournalSource struct {
	journal    *sdjournal.Journal
	journalDir string
}

var _ eventlog.EventSource = (*SDJournalSource)(nil)

// NewSDJournalSource creates a source that reads from the system journal.
// If journalDir is non-empty, reads from that directory instead of the default.
func NewSDJournalSource(journalDir string) (*SDJournalSource, error) {
	// Check environment override
	if dir := os.Getenv("SWASH_JOURNAL_DIR"); dir != "" {
		journalDir = dir
	}

	var j *sdjournal.Journal
	var err error

	if journalDir != "" {
		j, err = sdjournal.NewJournalFromDir(journalDir)
	} else {
		j, err = sdjournal.NewJournal()
	}
	if err != nil {
		return nil, fmt.Errorf("opening journal: %w", err)
	}

	return &SDJournalSource{
		journal:    j,
		journalDir: journalDir,
	}, nil
}

// Poll reads entries matching filters since cursor.
func (s *SDJournalSource) Poll(ctx context.Context, filters []eventlog.EventFilter, cursor string) ([]eventlog.EventRecord, string, error) {
	// Process any pending journal updates (Wait(0) is equivalent to sd_journal_process())
	// This ensures we see entries that were written since the last call.
	s.journal.Wait(0)

	// Apply matches
	s.journal.FlushMatches()
	for _, f := range filters {
		if err := s.journal.AddMatch(f.Field + "=" + f.Value); err != nil {
			return nil, "", fmt.Errorf("adding match %s=%s: %w", f.Field, f.Value, err)
		}
	}

	// Seek to position
	if cursor != "" {
		if err := s.journal.SeekCursor(cursor); err == nil {
			s.journal.Next() // Skip the cursor entry itself
		} else {
			s.journal.SeekHead()
		}
	} else {
		s.journal.SeekHead()
	}

	var entries []eventlog.EventRecord
	var lastCursor string

	for {
		n, err := s.journal.Next()
		if err != nil {
			return nil, "", fmt.Errorf("reading journal: %w", err)
		}
		if n == 0 {
			break
		}

		record, err := s.parseEntry()
		if err != nil {
			continue
		}
		entries = append(entries, record)
		lastCursor = record.Cursor
	}

	return entries, lastCursor, nil
}

// Follow returns an iterator over entries matching filters.
func (s *SDJournalSource) Follow(ctx context.Context, filters []eventlog.EventFilter) iter.Seq[eventlog.EventRecord] {
	return func(yield func(eventlog.EventRecord) bool) {
		// Apply matches
		s.journal.FlushMatches()
		for _, f := range filters {
			if err := s.journal.AddMatch(f.Field + "=" + f.Value); err != nil {
				return
			}
		}

		s.journal.SeekHead()

		for {
			n, err := s.journal.Next()
			if err != nil {
				return
			}

			if n == 0 {
				// No entries available, wait for new ones
				waitCh := make(chan struct{})
				go func() {
					s.journal.Wait(5 * time.Second)
					close(waitCh)
				}()

				select {
				case <-ctx.Done():
					return
				case <-waitCh:
					continue
				}
			}

			record, err := s.parseEntry()
			if err != nil {
				continue
			}

			if !yield(record) {
				return
			}
		}
	}
}

// parseEntry parses the current journal position into an EventRecord.
func (s *SDJournalSource) parseEntry() (eventlog.EventRecord, error) {
	raw, err := s.journal.GetEntry()
	if err != nil {
		return eventlog.EventRecord{}, err
	}

	cursor, _ := s.journal.GetCursor()

	return eventlog.EventRecord{
		Cursor:    cursor,
		Timestamp: time.Unix(int64(raw.RealtimeTimestamp/1000000), int64((raw.RealtimeTimestamp%1000000)*1000)),
		Message:   raw.Fields["MESSAGE"],
		Fields:    raw.Fields,
	}, nil
}

// Close releases resources.
func (s *SDJournalSource) Close() error {
	return s.journal.Close()
}

// JournalDir returns the configured journal directory (empty for system default).
func (s *SDJournalSource) JournalDir() string {
	return s.journalDir
}
