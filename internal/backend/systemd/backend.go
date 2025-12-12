package systemd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mbrock/swash/internal/backend"
	"github.com/mbrock/swash/internal/control"
	controldbus "github.com/mbrock/swash/internal/control/dbus"
	"github.com/mbrock/swash/internal/eventlog"
	journald "github.com/mbrock/swash/internal/platform/systemd/eventlog"
	systemdproc "github.com/mbrock/swash/internal/platform/systemd/process"
	"github.com/mbrock/swash/internal/process"
	"github.com/mbrock/swash/internal/protocol"
	"github.com/mbrock/swash/internal/session"
)

func init() {
	backend.Register(backend.KindSystemd, Open)
}

// SystemdBackend is the production backend backed by user systemd + journald + D-Bus.
type SystemdBackend struct {
	processes   process.ProcessBackend
	events      eventlog.EventLog
	control     control.ControlPlane
	hostCommand []string
}

var _ backend.Backend = (*SystemdBackend)(nil)

// Open constructs the systemd backend.
func Open(ctx context.Context, cfg backend.Config) (backend.Backend, error) {
	if err := backend.ValidateHostCommand(cfg.HostCommand); err != nil {
		return nil, err
	}

	sd, err := systemdproc.ConnectUserSystemd(ctx)
	if err != nil {
		return nil, err
	}
	proc := systemdproc.NewSystemdBackend(sd)

	j, err := journald.Open()
	if err != nil {
		proc.Close()
		return nil, err
	}

	return &SystemdBackend{
		processes:   proc,
		events:      j,
		control:     controldbus.NewDBusControlPlane(),
		hostCommand: cfg.HostCommand,
	}, nil
}

// Close releases resources held by the backend.
func (b *SystemdBackend) Close() error {
	var firstErr error
	if b.events != nil {
		if err := b.events.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if b.processes != nil {
		if err := b.processes.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ListSessions returns all running swash sessions.
func (b *SystemdBackend) ListSessions(ctx context.Context) ([]backend.Session, error) {
	statuses, err := b.processes.List(ctx, process.ProcessFilter{
		Roles:  []process.ProcessRole{process.ProcessRoleHost},
		States: []process.ProcessState{process.ProcessStateRunning, process.ProcessStateStarting},
	})
	if err != nil {
		return nil, err
	}

	sessions := make([]backend.Session, 0, len(statuses))
	for _, st := range statuses {
		status := "running"
		if st.ExitStatus != 0 {
			status = "exited"
		}

		sessions = append(sessions, backend.Session{
			ID:      st.Ref.SessionID,
			Backend: string(backend.KindSystemd),
			Handle:  unitNameStringForRef(st.Ref),
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
func (b *SystemdBackend) GetScreen(ctx context.Context, sessionID string) (string, error) {
	// Try D-Bus for live session
	if b.control != nil {
		client, err := b.control.ConnectTTYSession(sessionID)
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
	filters := []eventlog.EventFilter{
		eventlog.FilterByEvent(eventlog.EventScreen),
		eventlog.FilterBySession(sessionID),
	}

	entries, _, err := b.events.Poll(ctx, filters, "")
	if err != nil {
		return "", fmt.Errorf("querying journal: %w", err)
	}

	if len(entries) == 0 {
		return "", fmt.Errorf("no screen found for session %s", sessionID)
	}

	// Return the most recent screen (last entry)
	return entries[len(entries)-1].Message, nil
}

// StartSession starts a new swash session with the given command and options.
func (b *SystemdBackend) StartSession(ctx context.Context, command []string, opts backend.SessionOptions) (string, error) {
	sessionID := session.GenSessionID()
	cwd, _ := os.Getwd()
	dbusName := fmt.Sprintf("%s.%s", session.DBusNamePrefix, sessionID)
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
	serverCmd := append([]string{}, b.hostCommand...)
	serverCmd = append(serverCmd,
		"--session", sessionID,
		"--command-json", session.MustJSON(command),
	)

	// Add protocol if not default (only for non-TTY mode)
	if !opts.TTY && opts.Protocol != "" && opts.Protocol != protocol.ProtocolShell {
		serverCmd = append(serverCmd, "--protocol", string(opts.Protocol))
	}

	// Add tags if present
	if len(opts.Tags) > 0 {
		serverCmd = append(serverCmd, "--tags-json", session.MustJSON(opts.Tags))
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

	spec := process.ProcessSpec{
		Ref:         process.HostProcess(sessionID),
		WorkingDir:  cwd,
		Description: cmdStr,
		Environment: env,
		Command:     serverCmd,
		Collect:     true,
		BusName:     dbusName,
		LaunchKind:  process.LaunchKindService,
	}

	if err := b.processes.Start(ctx, spec); err != nil {
		return "", err
	}

	return sessionID, nil
}

// StopSession stops a session by ID.
func (b *SystemdBackend) StopSession(ctx context.Context, sessionID string) error {
	return b.processes.Stop(ctx, process.HostProcess(sessionID))
}

// KillSession sends SIGKILL to the process in a session.
func (b *SystemdBackend) KillSession(ctx context.Context, sessionID string) error {
	client, err := b.control.ConnectSession(sessionID)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Kill()
}

// SendInput sends input to the process via the swash control plane.
func (b *SystemdBackend) SendInput(ctx context.Context, sessionID, input string) (int, error) {
	_ = ctx
	client, err := b.control.ConnectSession(sessionID)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	return client.SendInput(input)
}

// PollSessionOutput reads output events from a session's journal since cursor.
func (b *SystemdBackend) PollSessionOutput(ctx context.Context, sessionID, cursor string) ([]backend.Event, string, error) {
	filters := []eventlog.EventFilter{eventlog.FilterBySession(sessionID)}

	entries, newCursor, err := b.events.Poll(ctx, filters, cursor)
	if err != nil {
		return nil, "", err
	}

	var events []backend.Event
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

		events = append(events, backend.Event{
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

// FollowSession follows a session's output until it exits, times out, or exceeds the output limit.
// If timeout is 0, waits indefinitely. If outputLimit is 0, output is unlimited.
// Returns (exitCode, result). exitCode is only valid when result is FollowCompleted.
func (b *SystemdBackend) FollowSession(ctx context.Context, sessionID string, timeout time.Duration, outputLimit int) (int, backend.FollowResult) {
	filters := []eventlog.EventFilter{eventlog.FilterBySession(sessionID)}
	outputBytes := 0

	// Create a timeout context if timeout > 0
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	for e := range b.events.Follow(ctx, filters) {
		// Check for exit event
		if e.Fields[eventlog.FieldEvent] == eventlog.EventExited {
			exitCode := 0
			if codeStr := e.Fields[eventlog.FieldExitCode]; codeStr != "" {
				exitCode, _ = strconv.Atoi(codeStr)
			}
			return exitCode, backend.FollowCompleted
		}

		// Print output (entries with FD field)
		if e.Fields["FD"] != "" && e.Message != "" {
			fmt.Println(e.Message)

			if outputLimit > 0 {
				outputBytes += len(e.Message) + 1 // +1 for the newline added by Println
				if outputBytes > outputLimit {
					return 0, backend.FollowOutputLimit
				}
			}
		}
	}

	// Context was cancelled - distinguish timeout from explicit cancel
	if ctx.Err() == context.DeadlineExceeded {
		return 0, backend.FollowTimedOut
	}
	return 0, backend.FollowCancelled
}

// ListHistory returns recently exited sessions by querying lifecycle events.
func (b *SystemdBackend) ListHistory(ctx context.Context) ([]backend.HistorySession, error) {
	// Query for exited events
	filters := []eventlog.EventFilter{eventlog.FilterByEvent(eventlog.EventExited)}

	entries, _, err := b.events.Poll(ctx, filters, "")
	if err != nil {
		return nil, err
	}

	// Build sessions from events (most recent last in entries, we want most recent first)
	seen := make(map[string]bool)
	var sessions []backend.HistorySession

	// Iterate backwards to get most recent first and dedupe
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		sessionID := e.Fields[eventlog.FieldSession]
		if sessionID == "" || seen[sessionID] {
			continue
		}
		seen[sessionID] = true

		var exitCode *int
		if codeStr := e.Fields[eventlog.FieldExitCode]; codeStr != "" {
			if code, err := strconv.Atoi(codeStr); err == nil {
				exitCode = &code
			}
		}

		sessions = append(sessions, backend.HistorySession{
			ID:       sessionID,
			Status:   "exited",
			ExitCode: exitCode,
			Command:  e.Fields[eventlog.FieldCommand],
			Started:  e.Timestamp.Format("Mon 2006-01-02 15:04:05 MST"),
		})
	}

	return sessions, nil
}

func (b *SystemdBackend) ConnectSession(sessionID string) (session.SessionClient, error) {
	return b.control.ConnectSession(sessionID)
}

func (b *SystemdBackend) ConnectTTYSession(sessionID string) (session.TTYClient, error) {
	return b.control.ConnectTTYSession(sessionID)
}

func unitNameStringForRef(ref process.ProcessRef) string {
	switch ref.Role {
	case process.ProcessRoleHost:
		return fmt.Sprintf("swash-host-%s.service", ref.SessionID)
	default:
		return fmt.Sprintf("swash-task-%s.service", ref.SessionID)
	}
}
