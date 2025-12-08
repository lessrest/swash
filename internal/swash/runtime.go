package swash

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Runtime holds the dependencies for swash operations.
// Use DefaultRuntime for production, or construct with custom
// implementations for testing.
type Runtime struct {
	Systemd        Systemd
	Journal        Journal
	ConnectSession func(sessionID string) (SessionClient, error)
}

// DefaultRuntime creates a Runtime connected to the real systemd and journal.
func DefaultRuntime(ctx context.Context) (*Runtime, error) {
	sd, err := ConnectUserSystemd(ctx)
	if err != nil {
		return nil, err
	}

	j, err := OpenJournal()
	if err != nil {
		sd.Close()
		return nil, err
	}

	return &Runtime{
		Systemd:        sd,
		Journal:        j,
		ConnectSession: ConnectSession,
	}, nil
}

// Close releases resources held by the runtime.
func (r *Runtime) Close() error {
	var firstErr error
	if r.Journal != nil {
		if err := r.Journal.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if r.Systemd != nil {
		if err := r.Systemd.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ListSessions returns all running swash sessions.
func (r *Runtime) ListSessions(ctx context.Context) ([]Session, error) {
	units, err := r.Systemd.ListUnits(
		ctx,
		[]UnitName{"swash-host-*.service"},
		[]UnitState{UnitStateActive, UnitStateActivating},
	)
	if err != nil {
		return nil, err
	}

	sessions := make([]Session, 0, len(units))
	for _, u := range units {
		status := "running"
		if u.ExitStatus != 0 {
			status = "exited"
		}

		sessions = append(sessions, Session{
			ID:      u.Name.SessionID(),
			Unit:    u.Name.String(),
			PID:     u.MainPID,
			CWD:     u.WorkingDir,
			Status:  status,
			Command: u.Description,
			Started: u.Started.Format("Mon 2006-01-02 15:04:05 MST"),
		})
	}
	return sessions, nil
}

// StartSession starts a new swash session with the given command.
func (r *Runtime) StartSession(ctx context.Context, command []string, hostCommand []string) (string, error) {
	return r.StartSessionWithOptions(ctx, command, hostCommand, SessionOptions{})
}

// StartSessionWithOptions starts a new swash session with the given command and options.
func (r *Runtime) StartSessionWithOptions(ctx context.Context, command []string, hostCommand []string, opts SessionOptions) (string, error) {
	sessionID := GenSessionID()
	cwd, _ := os.Getwd()
	dbusName := fmt.Sprintf("%s.%s", DBusNamePrefix, sessionID)
	cmdStr := strings.Join(command, " ")

	// Build environment map (excluding underscore-prefixed vars)
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "_") {
			continue
		}
		if idx := strings.Index(e, "="); idx > 0 {
			env[e[:idx]] = e[idx+1:]
		}
	}

	// Build the actual command: hostCommand... --session ID --command-json [...] [--protocol ...] [--tags-json ...]
	serverCmd := append([]string{}, hostCommand...)
	serverCmd = append(serverCmd,
		"--session", sessionID,
		"--command-json", MustJSON(command),
	)

	// Add protocol if not default (only for non-TTY mode)
	if !opts.TTY && opts.Protocol != "" && opts.Protocol != ProtocolShell {
		serverCmd = append(serverCmd, "--protocol", string(opts.Protocol))
	}

	// Add tags if present
	if len(opts.Tags) > 0 {
		serverCmd = append(serverCmd, "--tags-json", MustJSON(opts.Tags))
	}

	// Add TTY mode options
	if opts.TTY {
		serverCmd = append(serverCmd, "--tty")
		if opts.Rows > 0 {
			serverCmd = append(serverCmd, "--rows", fmt.Sprintf("%d", opts.Rows))
		}
		if opts.Cols > 0 {
			serverCmd = append(serverCmd, "--cols", fmt.Sprintf("%d", opts.Cols))
		}
	}

	spec := TransientSpec{
		Unit:        HostUnit(sessionID),
		Slice:       SessionSlice(sessionID),
		ServiceType: "dbus",
		BusName:     dbusName,
		WorkingDir:  cwd,
		Description: cmdStr,
		Environment: env,
		Command:     serverCmd,
		Collect:     true,
	}

	if err := r.Systemd.StartTransient(ctx, spec); err != nil {
		return "", err
	}

	return sessionID, nil
}

// StopSession stops a session by ID.
func (r *Runtime) StopSession(ctx context.Context, sessionID string) error {
	return r.Systemd.StopUnit(ctx, HostUnit(sessionID))
}

// KillSession sends SIGKILL to the process in a session.
func (r *Runtime) KillSession(sessionID string) error {
	client, err := r.ConnectSession(sessionID)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Kill()
}

// SendInput sends input to the process via the swash D-Bus service.
func (r *Runtime) SendInput(sessionID string, input string) (int, error) {
	client, err := r.ConnectSession(sessionID)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	return client.SendInput(input)
}

// PollSessionOutput reads output events from a session's journal since cursor.
func (r *Runtime) PollSessionOutput(sessionID, cursor string) ([]Event, string, error) {
	matches := []JournalMatch{MatchSlice(SessionSlice(sessionID))}

	entries, newCursor, err := r.Journal.Poll(context.Background(), matches, cursor)
	if err != nil {
		return nil, "", err
	}

	var events []Event
	for _, e := range entries {
		// Only care about messages with FD field (process output)
		fdStr := e.Fields["FD"]
		if e.Message == "" || fdStr == "" {
			continue
		}

		fd := 1
		if fdStr == "2" {
			fd = 2
		}

		events = append(events, Event{
			Cursor:    e.Cursor,
			Timestamp: e.Timestamp.Unix(),
			Text:      e.Message,
			FD:        fd,
		})
	}

	if newCursor == "" {
		newCursor = cursor
	}
	return events, newCursor, nil
}

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

// FollowSession follows a session's output until it exits, times out, or exceeds the output limit.
// If timeout is 0, waits indefinitely. If outputLimit is 0, output is unlimited.
// Returns (exitCode, result). exitCode is only valid when result is FollowCompleted.
func (r *Runtime) FollowSession(ctx context.Context, sessionID string, timeout time.Duration, outputLimit int) (int, FollowResult) {
	matches := []JournalMatch{MatchSession(sessionID)}
	outputBytes := 0

	// Create a timeout context if timeout > 0
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	for e := range r.Journal.Follow(ctx, matches) {
		// Check for exit event
		if e.Fields[FieldEvent] == EventExited {
			exitCode := 0
			if codeStr := e.Fields[FieldExitCode]; codeStr != "" {
				exitCode, _ = strconv.Atoi(codeStr)
			}
			return exitCode, FollowCompleted
		}

		// Print output (entries with FD field)
		if e.Fields["FD"] != "" && e.Message != "" {
			fmt.Println(e.Message)

			if outputLimit > 0 {
				outputBytes += len(e.Message) + 1 // +1 for the newline added by Println
				if outputBytes > outputLimit {
					return 0, FollowOutputLimit
				}
			}
		}
	}

	// Context was cancelled - distinguish timeout from explicit cancel
	if ctx.Err() == context.DeadlineExceeded {
		return 0, FollowTimedOut
	}
	return 0, FollowCancelled
}

// ListHistory returns recently exited sessions by querying lifecycle events.
func (r *Runtime) ListHistory(ctx context.Context) ([]HistorySession, error) {
	// Query for exited events
	matches := []JournalMatch{{Field: FieldEvent, Value: EventExited}}

	entries, _, err := r.Journal.Poll(ctx, matches, "")
	if err != nil {
		return nil, err
	}

	// Build sessions from events (most recent last in entries, we want most recent first)
	seen := make(map[string]bool)
	var sessions []HistorySession

	// Iterate backwards to get most recent first and dedupe
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		sessionID := e.Fields[FieldSession]
		if sessionID == "" || seen[sessionID] {
			continue
		}
		seen[sessionID] = true

		var exitCode *int
		if codeStr := e.Fields[FieldExitCode]; codeStr != "" {
			if code, err := strconv.Atoi(codeStr); err == nil {
				exitCode = &code
			}
		}

		sessions = append(sessions, HistorySession{
			ID:       sessionID,
			Status:   "exited",
			ExitCode: exitCode,
			Command:  e.Fields[FieldCommand],
			Started:  e.Timestamp.Format("Mon 2006-01-02 15:04:05 MST"),
		})
	}

	return sessions, nil
}
