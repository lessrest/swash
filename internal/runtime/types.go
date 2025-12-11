package runtime

import (
	"fmt"

	"github.com/mbrock/swash/internal/eventlog"
	"github.com/mbrock/swash/internal/process"
	"github.com/mbrock/swash/internal/session"
)

// Session is the presentation model for a running swash session.
// For now this is shared with the D-Bus client layer.
type Session = session.Session

// SessionOptions configures a new session.
type SessionOptions = session.SessionOptions

// EventFilter is a semantic event query filter.
type EventFilter = eventlog.EventFilter

// Event is a parsed output event (compatibility alias).
type Event = eventlog.Event

// HistorySession is a presentation model for a historical session entry.
type HistorySession = eventlog.HistorySession

func unitNameStringForRef(ref process.ProcessRef) string {
	switch ref.Role {
	case process.ProcessRoleHost:
		return fmt.Sprintf("swash-host-%s.service", ref.SessionID)
	default:
		return fmt.Sprintf("swash-task-%s.service", ref.SessionID)
	}
}
