package journal

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"strconv"
	"strings"
	"time"

	"swa.sh/go/swash/pkg/oxigraph"
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
// Implementations use the native journald socket protocol.
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
//   - JournalfileSource: uses journalfile (pure Go, portable)
type EventSource interface {
	// Poll reads entries matching filters since cursor.
	Poll(ctx context.Context, filters []EventFilter, cursor string) ([]EventRecord, string, error)

	// Follow returns an iterator over entries matching filters.
	Follow(ctx context.Context, filters []EventFilter) iter.Seq[EventRecord]

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
	EventStarted        = "started"
	EventExited         = "exited"
	EventScreen         = "screen"          // Final screen state for TTY sessions
	EventContextCreated = "context-created" // Context creation
	EventSessionContext = "session-context" // Session-to-context relation
	EventServiceType    = "service-type"    // Session declares its service type
)

// Event field names for swash events.
const (
	FieldEvent    = "SWASH_EVENT"
	FieldSession  = "SWASH_SESSION"
	FieldCommand  = "SWASH_COMMAND"
	FieldExitCode = "SWASH_EXIT_CODE"
	FieldContext  = "SWASH_CONTEXT"
	FieldService  = "SWASH_SERVICE"
)

// EmitStarted writes a session started event to the log.
func EmitStarted(log EventLog, sessionID string, command []string) error {
	return log.WriteSync("Session started", map[string]string{
		FieldEvent:   EventStarted,
		FieldSession: sessionID,
		FieldCommand: strings.Join(command, " "),
	})
}

// EmitExited writes a session exited event to the log.
func EmitExited(log EventLog, sessionID string, exitCode int, command []string) error {
	return log.WriteSync("Session exited", map[string]string{
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
// Uses WriteSync to ensure the screen is persisted before the host exits.
func EmitScreen(log EventLog, sessionID string, screenText string, rows, cols int) error {
	return log.WriteSync(screenText, map[string]string{
		FieldEvent:   EventScreen,
		FieldSession: sessionID,
		"ROWS":       strconv.Itoa(rows),
		"COLS":       strconv.Itoa(cols),
	})
}

// EmitContextCreated writes a context creation event to the log.
func EmitContextCreated(log EventLog, contextID string, dir string) error {
	return log.WriteSync("Context created", map[string]string{
		FieldEvent:   EventContextCreated,
		FieldContext: contextID,
		"DIR":        dir,
	})
}

// EmitSessionContext writes a session-to-context relation event.
func EmitSessionContext(log EventLog, sessionID, contextID string) error {
	return log.WriteSync("Session belongs to context", map[string]string{
		FieldEvent:   EventSessionContext,
		FieldSession: sessionID,
		FieldContext: contextID,
	})
}

// EmitServiceType writes an event declaring the session's service type.
// This allows finding sessions by their role (e.g., "graph", "journald").
func EmitServiceType(log EventLog, sessionID, serviceType string) error {
	return log.WriteSync("Service type", map[string]string{
		FieldEvent:   EventServiceType,
		FieldSession: sessionID,
		FieldService: serviceType,
	})
}

// FilterByService creates a filter for a service's SWASH_SERVICE field.
func FilterByService(serviceType string) EventFilter {
	return EventFilter{Field: FieldService, Value: serviceType}
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

// -----------------------------------------------------------------------------
// RDF representation of lifecycle events
// -----------------------------------------------------------------------------

// RDF namespace constants.
const (
	NSSwash = "https://swa.sh/ns#"
	NSXSD   = "http://www.w3.org/2001/XMLSchema#"
	NSRdf   = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
)

// Predicate IRIs for swash RDF vocabulary.
var (
	RdfType        = oxigraph.IRI(NSRdf + "type")
	SwashSession   = oxigraph.IRI(NSSwash + "Session")
	SwashContext   = oxigraph.IRI(NSSwash + "Context")
	SwashCommand   = oxigraph.IRI(NSSwash + "command")
	SwashStartedAt = oxigraph.IRI(NSSwash + "startedAt")
	SwashExitedAt  = oxigraph.IRI(NSSwash + "exitedAt")
	SwashExitCode  = oxigraph.IRI(NSSwash + "exitCode")
	SwashDirectory = oxigraph.IRI(NSSwash + "directory")
	SwashCreatedAt = oxigraph.IRI(NSSwash + "createdAt")
	SwashInContext = oxigraph.IRI(NSSwash + "context")

	// Service types
	SwashGraphService = oxigraph.IRI(NSSwash + "GraphService")
)

// EventToQuads converts an EventRecord to RDF quads.
// Returns nil if the event type is not a lifecycle event.
func EventToQuads(e EventRecord) []oxigraph.Quad {
	eventType := e.Fields[FieldEvent]
	timestamp := e.Timestamp.Format("2006-01-02T15:04:05Z")

	switch eventType {
	case EventStarted:
		return sessionStartedQuads(e, timestamp)
	case EventExited:
		return sessionExitedQuads(e, timestamp)
	case EventContextCreated:
		return contextCreatedQuads(e, timestamp)
	case EventSessionContext:
		return sessionContextQuads(e)
	case EventServiceType:
		return serviceTypeQuads(e)
	default:
		return nil
	}
}

// sessionStartedQuads generates quads for a session started event.
func sessionStartedQuads(e EventRecord, timestamp string) []oxigraph.Quad {
	sessionID := e.Fields[FieldSession]
	if sessionID == "" {
		return nil
	}

	subject := SessionIRI(sessionID)
	quads := []oxigraph.Quad{
		{Subject: subject, Predicate: RdfType, Object: SwashSession},
		{Subject: subject, Predicate: SwashStartedAt, Object: dateTimeLiteral(timestamp)},
	}

	if cmd := e.Fields[FieldCommand]; cmd != "" {
		quads = append(quads, oxigraph.Quad{
			Subject: subject, Predicate: SwashCommand, Object: oxigraph.StringLiteral(cmd),
		})
	}

	return quads
}

// sessionExitedQuads generates quads for a session exited event.
func sessionExitedQuads(e EventRecord, timestamp string) []oxigraph.Quad {
	sessionID := e.Fields[FieldSession]
	if sessionID == "" {
		return nil
	}

	subject := SessionIRI(sessionID)
	quads := []oxigraph.Quad{
		{Subject: subject, Predicate: SwashExitedAt, Object: dateTimeLiteral(timestamp)},
	}

	if codeStr := e.Fields[FieldExitCode]; codeStr != "" {
		if code, err := strconv.Atoi(codeStr); err == nil {
			quads = append(quads, oxigraph.Quad{
				Subject: subject, Predicate: SwashExitCode, Object: intLiteral(code),
			})
		}
	}

	return quads
}

// contextCreatedQuads generates quads for a context created event.
func contextCreatedQuads(e EventRecord, timestamp string) []oxigraph.Quad {
	contextID := e.Fields[FieldContext]
	if contextID == "" {
		return nil
	}

	subject := ContextIRI(contextID)
	quads := []oxigraph.Quad{
		{Subject: subject, Predicate: RdfType, Object: SwashContext},
		{Subject: subject, Predicate: SwashCreatedAt, Object: dateTimeLiteral(timestamp)},
	}

	if dir := e.Fields["DIR"]; dir != "" {
		quads = append(quads, oxigraph.Quad{
			Subject: subject, Predicate: SwashDirectory, Object: oxigraph.StringLiteral(dir),
		})
	}

	return quads
}

// sessionContextQuads generates quads linking a session to its context.
func sessionContextQuads(e EventRecord) []oxigraph.Quad {
	sessionID := e.Fields[FieldSession]
	contextID := e.Fields[FieldContext]
	if sessionID == "" || contextID == "" {
		return nil
	}

	return []oxigraph.Quad{
		{
			Subject:   SessionIRI(sessionID),
			Predicate: SwashInContext,
			Object:    ContextIRI(contextID),
		},
	}
}

// serviceTypeQuads generates quads for a service type declaration.
func serviceTypeQuads(e EventRecord) []oxigraph.Quad {
	sessionID := e.Fields[FieldSession]
	serviceType := e.Fields[FieldService]
	if sessionID == "" || serviceType == "" {
		return nil
	}

	// Map service type string to RDF class
	var typeIRI oxigraph.NamedNode
	switch serviceType {
	case "graph":
		typeIRI = SwashGraphService
	default:
		// Unknown service type - use a generic IRI
		typeIRI = oxigraph.IRI(NSSwash + "Service/" + serviceType)
	}

	return []oxigraph.Quad{
		{
			Subject:   SessionIRI(sessionID),
			Predicate: RdfType,
			Object:    typeIRI,
		},
	}
}

// SessionIRI returns the URN for a session.
func SessionIRI(id string) oxigraph.NamedNode {
	return oxigraph.IRI("urn:swash:session:" + id)
}

// ContextIRI returns the URN for a context.
func ContextIRI(id string) oxigraph.NamedNode {
	return oxigraph.IRI("urn:swash:context:" + id)
}

func dateTimeLiteral(ts string) oxigraph.Literal {
	return oxigraph.TypedLiteral(ts, NSXSD+"dateTime")
}

func intLiteral(n int) oxigraph.Literal {
	return oxigraph.TypedLiteral(fmt.Sprintf("%d", n), NSXSD+"integer")
}

// IsLifecycleEvent returns true if the event has a SWASH_EVENT field.
func IsLifecycleEvent(e EventRecord) bool {
	return e.Fields[FieldEvent] != ""
}

// LifecycleEventFilters returns filters that match all lifecycle event types.
// Multiple filters for the same field create an OR condition.
func LifecycleEventFilters() []EventFilter {
	return []EventFilter{
		{Field: FieldEvent, Value: EventStarted},
		{Field: FieldEvent, Value: EventExited},
		{Field: FieldEvent, Value: EventScreen},
		{Field: FieldEvent, Value: EventContextCreated},
		{Field: FieldEvent, Value: EventSessionContext},
		{Field: FieldEvent, Value: EventServiceType},
	}
}
