package eventlog

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

// -----------------------------------------------------------------------------
// Lifecycle + output helpers (semantic)
// -----------------------------------------------------------------------------

// Lifecycle event constants.
const (
	EventStarted        = "started"
	EventExited         = "exited"
	EventScreen         = "screen"          // Final screen state for TTY sessions
	EventContextCreated = "context-created" // Context creation
	EventSessionContext = "session-context" // Session-to-context relation
)

// Event field names for swash events.
const (
	FieldEvent    = "SWASH_EVENT"
	FieldSession  = "SWASH_SESSION"
	FieldCommand  = "SWASH_COMMAND"
	FieldExitCode = "SWASH_EXIT_CODE"
	FieldContext  = "SWASH_CONTEXT"
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

// EmitContextCreated writes a context creation event to the log.
func EmitContextCreated(log EventLog, contextID string, dir string) error {
	return log.Write("Context created", map[string]string{
		FieldEvent:   EventContextCreated,
		FieldContext: contextID,
		"DIR":        dir,
	})
}

// EmitSessionContext writes a session-to-context relation event.
func EmitSessionContext(log EventLog, sessionID, contextID string) error {
	return log.Write("Session belongs to context", map[string]string{
		FieldEvent:   EventSessionContext,
		FieldSession: sessionID,
		FieldContext: contextID,
	})
}

// FilterByContext creates a filter for a context's SWASH_CONTEXT field.
func FilterByContext(contextID string) EventFilter {
	return EventFilter{Field: FieldContext, Value: contextID}
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
