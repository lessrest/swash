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
	Processes ProcessBackend
	Events    EventLog
	Control   ControlPlane
}

// DefaultRuntime creates a Runtime backed by the real systemd+journald stack.
func DefaultRuntime(ctx context.Context) (*Runtime, error) {
	sd, err := ConnectUserSystemd(ctx)
	if err != nil {
		return nil, err
	}

	proc := NewSystemdBackend(sd)

	j, err := OpenEventLog()
	if err != nil {
		proc.Close()
		return nil, err
	}

	return &Runtime{
		Processes: proc,
		Events:    j,
		Control:   NewDBusControlPlane(),
	}, nil
}

// Close releases resources held by the runtime.
func (r *Runtime) Close() error {
	var firstErr error
	if r.Events != nil {
		if err := r.Events.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if r.Processes != nil {
		if err := r.Processes.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ListSessions returns all running swash sessions.
func (r *Runtime) ListSessions(ctx context.Context) ([]Session, error) {
	statuses, err := r.Processes.List(ctx, ProcessFilter{
		Roles:  []ProcessRole{ProcessRoleHost},
		States: []ProcessState{ProcessStateRunning, ProcessStateStarting},
	})
	if err != nil {
		return nil, err
	}

	sessions := make([]Session, 0, len(statuses))
	for _, st := range statuses {
		status := "running"
		if st.ExitStatus != 0 {
			status = "exited"
		}

		sessions = append(sessions, Session{
			ID:      st.Ref.SessionID,
			Unit:    unitNameForRef(st.Ref).String(),
			PID:     st.PID,
			CWD:     st.WorkingDir,
			Status:  status,
			Command: st.Description,
			Started: st.Started.Format("Mon 2006-01-02 15:04:05 MST"),
		})
	}
	return sessions, nil
}

// GetScreen returns the screen content for a session.
// Tries D-Bus first (for running sessions), then falls back to journal (for finished sessions).
func (r *Runtime) GetScreen(sessionID string) (string, error) {
	// Try D-Bus for live session
	if r.Control != nil {
		client, err := r.Control.ConnectTTYSession(sessionID)
		if err == nil {
			defer client.Close()
			screen, err := client.GetScreenANSI()
			if err == nil {
				return screen, nil
			}
			// D-Bus call failed - session probably ended, try journal
		}
	}

	// Fall back to journal for saved screen
	filters := []EventFilter{
		FilterByEvent(EventScreen),
		FilterBySession(sessionID),
	}

	entries, _, err := r.Events.Poll(context.Background(), filters, "")
	if err != nil {
		return "", fmt.Errorf("querying journal: %w", err)
	}

	if len(entries) == 0 {
		return "", fmt.Errorf("no screen found for session %s", sessionID)
	}

	// Return the most recent screen (last entry)
	return entries[len(entries)-1].Message, nil
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

	spec := ProcessSpec{
		Ref:         HostProcess(sessionID),
		WorkingDir:  cwd,
		Description: cmdStr,
		Environment: env,
		Command:     serverCmd,
		Collect:     true,
		BusName:     dbusName,
		LaunchKind:  LaunchKindService,
	}

	if err := r.Processes.Start(ctx, spec); err != nil {
		return "", err
	}

	return sessionID, nil
}

// StopSession stops a session by ID.
func (r *Runtime) StopSession(ctx context.Context, sessionID string) error {
	return r.Processes.Stop(ctx, HostProcess(sessionID))
}

// KillSession sends SIGKILL to the process in a session.
func (r *Runtime) KillSession(sessionID string) error {
	client, err := r.Control.ConnectSession(sessionID)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Kill()
}

// SendInput sends input to the process via the swash D-Bus service.
func (r *Runtime) SendInput(sessionID string, input string) (int, error) {
	client, err := r.Control.ConnectSession(sessionID)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	return client.SendInput(input)
}

// PollSessionOutput reads output events from a session's journal since cursor.
func (r *Runtime) PollSessionOutput(sessionID, cursor string) ([]Event, string, error) {
	filters := []EventFilter{FilterBySession(sessionID)}

	entries, newCursor, err := r.Events.Poll(context.Background(), filters, cursor)
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
	filters := []EventFilter{FilterBySession(sessionID)}
	outputBytes := 0

	// Create a timeout context if timeout > 0
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	for e := range r.Events.Follow(ctx, filters) {
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
	filters := []EventFilter{FilterByEvent(EventExited)}

	entries, _, err := r.Events.Poll(ctx, filters, "")
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
