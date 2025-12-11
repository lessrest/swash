package swash

import (
	"context"
	"fmt"
	"iter"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/journal"
	"github.com/coreos/go-systemd/v22/sdjournal"
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

// EventRecord represents a single persisted event.
type EventRecord struct {
	Cursor    string
	Timestamp time.Time
	Message   string
	Fields    map[string]string
}

// EventFilter describes a simple equality match for queries.
type EventFilter struct {
	Field string
	Value string
}

// FilterBySession creates a filter for a session's SWASH_SESSION field.
func FilterBySession(sessionID string) EventFilter {
	return EventFilter{Field: FieldSession, Value: sessionID}
}

// FilterByEvent creates a filter for an event kind (started, exited, screen, ...).
func FilterByEvent(kind string) EventFilter {
	return EventFilter{Field: FieldEvent, Value: kind}
}

// EventLog provides semantic operations for storing and reading swash events.
// The default implementation is backed by systemd-journald, but callers only
// see domain concepts.
type EventLog interface {
	// Write sends a structured entry to the backing store.
	Write(message string, fields map[string]string) error

	// Poll reads entries matching filters since cursor.
	Poll(ctx context.Context, filters []EventFilter, cursor string) ([]EventRecord, string, error)

	// Follow returns an iterator over entries matching filters.
	Follow(ctx context.Context, filters []EventFilter) iter.Seq[EventRecord]

	// Close releases any resources.
	Close() error
}

// journaldEventLog implements EventLog using go-systemd/sdjournal.
type journaldEventLog struct {
	j *sdjournal.Journal
}

// JournalDir can be set via ldflags to read from a specific journal directory.
// If empty, reads from the default system journal.
// Example: -ldflags "-X github.com/mbrock/swash/internal/swash.JournalDir=/path/to/journal"
var JournalDir string

// OpenEventLog opens the backing event log (journald by default).
func OpenEventLog() (EventLog, error) {
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
	filters []EventFilter,
	cursor string,
) ([]EventRecord, string, error) {
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

	var entries []EventRecord
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
func (jl *journaldEventLog) Follow(ctx context.Context, filters []EventFilter) iter.Seq[EventRecord] {
	return func(yield func(EventRecord) bool) {
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
func (jl *journaldEventLog) parseEntry() (EventRecord, error) {
	raw, err := jl.j.GetEntry()
	if err != nil {
		return EventRecord{}, err
	}

	cursor, _ := jl.j.GetCursor()

	return EventRecord{
		Cursor:    cursor,
		Timestamp: time.Unix(int64(raw.RealtimeTimestamp/1000000), int64((raw.RealtimeTimestamp%1000000)*1000)),
		Message:   raw.Fields["MESSAGE"],
		Fields:    raw.Fields,
	}, nil
}

// -----------------------------------------------------------------------------
// Lifecycle + output helpers (semantic, journald-backed)
// -----------------------------------------------------------------------------

// Lifecycle event constants
const (
	EventStarted = "started"
	EventExited  = "exited"
	EventScreen  = "screen" // Final screen state for TTY sessions
)

// Event field names for swash events
const (
	FieldEvent    = "SWASH_EVENT"
	FieldSession  = "SWASH_SESSION"
	FieldCommand  = "SWASH_COMMAND"
	FieldExitCode = "SWASH_EXIT_CODE"
)

// EmitStarted writes a session started event to the log.
func EmitStarted(log EventLog, sessionID string, command []string) error {
	return log.Write("Session started", map[string]string{
		FieldEvent:   EventStarted,
		FieldSession: sessionID,
		FieldCommand: strings.Join(command, " "),
	})
}

// EmitExited writes a session exited event to the log.
func EmitExited(log EventLog, sessionID string, exitCode int, command []string) error {
	return log.Write("Session exited", map[string]string{
		FieldEvent:    EventExited,
		FieldSession:  sessionID,
		FieldExitCode: strconv.Itoa(exitCode),
		FieldCommand:  strings.Join(command, " "),
	})
}

// WriteOutput writes process output to the log with FD and extra fields.
func WriteOutput(log EventLog, fd int, text string, extraFields map[string]string) error {
	fields := map[string]string{
		"FD": fmt.Sprintf("%d", fd),
	}
	for k, v := range extraFields {
		fields[k] = v
	}
	return log.Write(text, fields)
}

// EmitScreen writes the final screen state to the log.
// This preserves the visible screen content when a TTY session exits.
func EmitScreen(log EventLog, sessionID string, screenText string, rows, cols int) error {
	return log.Write(screenText, map[string]string{
		FieldEvent:   EventScreen,
		FieldSession: sessionID,
		"ROWS":       strconv.Itoa(rows),
		"COLS":       strconv.Itoa(cols),
	})
}

// OutputEvent represents a parsed output event from the log.
type OutputEvent struct {
	Cursor    string
	Timestamp int64
	Text      string
	FD        int // 1=stdout, 2=stderr
}

// Event is kept as a compatibility alias for legacy call sites.
type Event = OutputEvent

// HistorySession represents a session from history.
type HistorySession struct {
	ID       string
	Status   string // "running", "exited", "killed"
	ExitCode *int
	Command  string
	Started  string
}
