package journal

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"strconv"
	"strings"
	"time"
)

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

// EventSink is a write-only interface for sending events.
type EventSink interface {
	// Write sends a structured entry (fire-and-forget).
	// Use for high-volume streaming data like process output.
	Write(message string, fields map[string]string) error

	// Close releases any resources.
	Close() error
}

// EventSource is a read-only interface for querying events.
// Implementations:
//   - SDJournalSource: uses sdjournal (CGO, full libsystemd features)
//   - SQLiteLog: uses SQLite in WAL mode for the portable backend
type EventSource interface {
	// Poll reads entries matching filters since cursor.
	Poll(ctx context.Context, filters []EventFilter, cursor string) ([]EventRecord, string, error)

	// Follow returns entries matching filters after cursor, then waits for new
	// ones. An empty cursor starts at the journal head.
	Follow(ctx context.Context, filters []EventFilter, cursor string) iter.Seq[EventRecord]

	// Close releases any resources.
	Close() error
}

// EventLog combines EventSink and EventSource, adding WriteSync for
// read-after-write consistency. This is the main interface used by most code.
type EventLog interface {
	EventSink
	EventSource

	// WriteSync sends a structured entry and waits until it is readable.
	// Use for lifecycle events that need read-after-write consistency.
	// This requires both write (sink) and read (source) capabilities.
	WriteSync(message string, fields map[string]string) error
}

// -----------------------------------------------------------------------------
// Lifecycle + output helpers (semantic)
// -----------------------------------------------------------------------------

// Lifecycle event constants.
const (
	EventStarted = "started"
	EventExited  = "exited"
	EventScreen  = "screen" // Final screen state for TTY sessions
)

// Event field names for swash events.
const (
	FieldEvent    = "SWASH_EVENT"
	FieldSession  = "SWASH_SESSION"
	FieldCommand  = "SWASH_COMMAND"
	FieldExitCode = "SWASH_EXIT_CODE"
)

// EmitStarted writes a session started event to the log.
func EmitStarted(log EventLog, sessionID string, command []string, tags map[string]string) error {
	fields := make(map[string]string, len(tags)+3)
	maps.Copy(fields, tags)
	fields[FieldEvent] = EventStarted
	fields[FieldSession] = sessionID
	fields[FieldCommand] = strings.Join(command, " ")
	return log.Write("Session started", fields)
}

// EmitExited writes a session exited event to the log.
func EmitExited(log EventLog, sessionID string, exitCode int, command []string, tags map[string]string) error {
	fields := make(map[string]string, len(tags)+4)
	maps.Copy(fields, tags)
	fields[FieldEvent] = EventExited
	fields[FieldSession] = sessionID
	fields[FieldExitCode] = strconv.Itoa(exitCode)
	fields[FieldCommand] = strings.Join(command, " ")
	return log.Write("Session exited", fields)
}

// WriteOutput writes process output to the log with FD and extra fields.
func WriteOutput(log EventLog, fd int, text string, extraFields map[string]string) error {
	fields := map[string]string{
		"FD": fmt.Sprintf("%d", fd),
	}
	maps.Copy(fields, extraFields)
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

// EmitSessionEvent appends an application-defined semantic event. Arbitrary
// fields describe the event, while Swash retains ownership of its identity.
func EmitSessionEvent(log EventLog, sessionID, event, message string, extraFields map[string]string) error {
	fields := make(map[string]string, len(extraFields)+2)
	maps.Copy(fields, extraFields)
	fields[FieldEvent] = event
	fields[FieldSession] = sessionID
	return log.WriteSync(message, fields)
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
