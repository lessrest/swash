// Package source provides EventSource implementations for reading from journals.
package source

import (
	"context"
	"fmt"
	"iter"
	"os"
	"time"

	"golang.org/x/sys/unix"

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
	// Process any pending journal updates (non-blocking)
	// This ensures we see entries that were written since the last call.
	s.journal.Process()

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
// Uses the file descriptor-based API for efficient, non-blocking waits
// that integrate properly with Go's runtime scheduler.
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

		// Get the journal file descriptor for polling
		fd, err := s.journal.GetFD()
		if err != nil {
			// Fall back to the old goroutine-based approach if GetFD fails
			s.followWithGoroutine(ctx, yield)
			return
		}

		// Get the events to poll for (typically POLLIN)
		events, err := s.journal.GetEvents()
		if err != nil {
			s.followWithGoroutine(ctx, yield)
			return
		}

		for {
			n, err := s.journal.Next()
			if err != nil {
				return
			}

			if n == 0 {
				// No entries available, wait for new ones using poll
				if err := s.waitForJournal(ctx, fd, events); err != nil {
					return // context cancelled or error
				}
				// Process the events
				s.journal.Process()
				continue
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

// waitForJournal waits for the journal FD to become readable or context to cancel.
// Uses poll(2) with a timeout so we can check the context periodically.
func (s *SDJournalSource) waitForJournal(ctx context.Context, fd int, events int) error {
	pollFds := []unix.PollFd{{
		Fd:     int32(fd),
		Events: int16(events),
	}}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Poll with 100ms timeout so we can check context regularly
		n, err := unix.Poll(pollFds, 100)
		if err != nil {
			if err == unix.EINTR {
				continue // Interrupted, retry
			}
			return err
		}
		if n > 0 {
			return nil // FD is ready
		}
		// Timeout, loop to check context and poll again
	}
}

// followWithGoroutine is the fallback implementation using the blocking Wait().
// Used when GetFD() is not available.
func (s *SDJournalSource) followWithGoroutine(ctx context.Context, yield func(eventlog.EventRecord) bool) {
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
