package backend

import (
	"swa.sh/internal/journal"
	"swa.sh/internal/protocol"
)

// Session is the platform-neutral presentation model for a swash session.
type Session struct {
	ID string `json:"id"`

	// Backend identifies which implementation is managing the session
	// (e.g. "systemd", "posix").
	Backend string `json:"backend"`

	// Handle is an optional backend-specific identifier (e.g. a systemd unit name).
	Handle string `json:"handle,omitempty"`

	PID uint32 `json:"pid"`
	CWD string `json:"cwd"`

	Status  string `json:"status"`
	Command string `json:"command"`
	Started string `json:"started"`
}

// SessionOptions configures a new session.
type SessionOptions struct {
	Protocol   protocol.Protocol // shell (default), sse
	Tags       map[string]string // Extra journal fields
	TTY        bool              // Use PTY mode with terminal emulation
	Rows       int               // Terminal rows (for TTY mode)
	Cols       int               // Terminal columns (for TTY mode)
	WorkingDir string            // Working directory for the session (optional, defaults to cwd)
}

// EventFilter is a semantic event query filter.
type EventFilter = journal.EventFilter

// Event is a parsed output event.
type Event = journal.Event

// HistorySession is a presentation model for a historical session entry.
type HistorySession = journal.HistorySession

// FollowResult indicates how FollowSession completed.
type FollowResult int

const (
	// FollowCompleted means the session exited within the timeout.
	FollowCompleted FollowResult = iota
	// FollowTimedOut means the timeout expired while session was still running.
	FollowTimedOut
	// FollowCancelled means the context was cancelled (e.g., Ctrl+C).
	FollowCancelled
	// FollowOutputLimit means output limit was reached while session was still running.
	FollowOutputLimit
)
