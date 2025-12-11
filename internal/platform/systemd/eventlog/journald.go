package journald

import (
	"context"
	"fmt"
	"iter"
	"os"
	"time"

	"github.com/coreos/go-systemd/v22/journal"
	"github.com/coreos/go-systemd/v22/sdjournal"
	"github.com/mbrock/swash/internal/eventlog"
)

func init() {
	// Allow runtime configuration of journal socket path via environment variable.
	// This is used by integration tests with mini-systemd.
	if socket := os.Getenv("SWASH_JOURNAL_SOCKET"); socket != "" {
		journal.SetSocketPath(socket)
	}
	// Also check for journal directory for reading.
	if dir := os.Getenv("SWASH_JOURNAL_DIR"); dir != "" {
		JournalDir = dir
	}
}

// journaldEventLog implements eventlog.EventLog using go-systemd/sdjournal.
type journaldEventLog struct {
	j *sdjournal.Journal
}

// JournalDir can be set via ldflags to read from a specific journal directory.
// If empty, reads from the default system journal.
// Example: -ldflags "-X github.com/mbrock/swash/internal/platform/systemd/eventlog.JournalDir=/path/to/journal"
var JournalDir string

// Open opens the backing event log (journald by default).
func Open() (eventlog.EventLog, error) {
	var j *sdjournal.Journal
	var err error

	if JournalDir != "" {
		j, err = sdjournal.NewJournalFromDir(JournalDir)
	} else {
		j, err = sdjournal.NewJournal()
	}
	if err != nil {
		return nil, fmt.Errorf("opening journal: %w", err)
	}
	return &journaldEventLog{j: j}, nil
}

// Close releases the journal resources.
func (jl *journaldEventLog) Close() error {
	return jl.j.Close()
}

// Write sends a structured entry to the journal.
func (jl *journaldEventLog) Write(message string, fields map[string]string) error {
	return journal.Send(message, journal.PriInfo, fields)
}

// Poll reads entries matching filters since cursor.
func (jl *journaldEventLog) Poll(
	ctx context.Context,
	filters []eventlog.EventFilter,
	cursor string,
) ([]eventlog.EventRecord, string, error) {
	// Apply matches
	jl.j.FlushMatches()
	for _, f := range filters {
		if err := jl.j.AddMatch(f.Field + "=" + f.Value); err != nil {
			return nil, "", fmt.Errorf("adding match %s=%s: %w", f.Field, f.Value, err)
		}
	}

	// Seek to position
	if cursor != "" {
		if err := jl.j.SeekCursor(cursor); err == nil {
			jl.j.Next() // Skip the cursor entry itself
		} else {
			jl.j.SeekHead()
		}
	} else {
		jl.j.SeekHead()
	}

	var entries []eventlog.EventRecord
	var lastCursor string

	for {
		n, err := jl.j.Next()
		if err != nil {
			return nil, "", fmt.Errorf("reading journal: %w", err)
		}
		if n == 0 {
			break
		}

		entry, err := jl.parseEntry()
		if err != nil {
			continue
		}
		entries = append(entries, entry)
		lastCursor = entry.Cursor
	}

	return entries, lastCursor, nil
}

// Follow returns an iterator over entries matching filters.
func (jl *journaldEventLog) Follow(ctx context.Context, filters []eventlog.EventFilter) iter.Seq[eventlog.EventRecord] {
	return func(yield func(eventlog.EventRecord) bool) {
		// Apply matches
		jl.j.FlushMatches()
		for _, f := range filters {
			if err := jl.j.AddMatch(f.Field + "=" + f.Value); err != nil {
				return // Can't report error from iterator, just stop
			}
		}

		jl.j.SeekHead()

		for {
			n, err := jl.j.Next()
			if err != nil {
				return
			}

			if n == 0 {
				// No entries available, wait for new ones (cancellable)
				waitCh := make(chan struct{})
				go func() {
					jl.j.Wait(5 * time.Second)
					close(waitCh)
				}()

				select {
				case <-ctx.Done():
					return
				case <-waitCh:
					continue
				}
			}

			entry, err := jl.parseEntry()
			if err != nil {
				continue
			}

			if !yield(entry) {
				return // Consumer broke out of loop
			}
		}
	}
}

// parseEntry parses the current journal position into an EventRecord.
func (jl *journaldEventLog) parseEntry() (eventlog.EventRecord, error) {
	raw, err := jl.j.GetEntry()
	if err != nil {
		return eventlog.EventRecord{}, err
	}

	cursor, _ := jl.j.GetCursor()

	return eventlog.EventRecord{
		Cursor:    cursor,
		Timestamp: time.Unix(int64(raw.RealtimeTimestamp/1000000), int64((raw.RealtimeTimestamp%1000000)*1000)),
		Message:   raw.Fields["MESSAGE"],
		Fields:    raw.Fields,
	}, nil
}
