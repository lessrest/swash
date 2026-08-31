package systemd

import (
	"context"
	"fmt"
	"iter"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"swa.sh/internal/backend"
	"swa.sh/internal/host"
	"swa.sh/internal/journal"
	"swa.sh/internal/protocol"
)

func init() {
	backend.Register(backend.KindSystemd, Open)
}

// SystemdBackend is the production backend backed by user systemd + journald + D-Bus.
type SystemdBackend struct {
	processes   ProcessBackend
	events      journal.EventLog
	hostCommand []string
}

var _ backend.Backend = (*SystemdBackend)(nil)

// Open constructs the systemd backend.
func Open(ctx context.Context, cfg backend.Config) (backend.Backend, error) {
	if err := backend.ValidateHostCommand(cfg.HostCommand); err != nil {
		return nil, err
	}

	sd, err := ConnectUserSystemd(ctx)
	if err != nil {
		return nil, err
	}
	proc := NewProcessManager(sd)

	j, err := journal.OpenSystemd()
	if err != nil {
		proc.Close()
		return nil, err
	}

	return &SystemdBackend{
		processes:   proc,
		events:      j,
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
	statuses, err := b.processes.List(ctx)
	if err != nil {
		return nil, err
	}

	sessions := make([]backend.Session, 0, len(statuses))
	for _, st := range statuses {
		sessions = append(sessions, backend.Session{
			ID:      st.SessionID,
			Backend: string(backend.KindSystemd),
			Handle:  HostUnit(st.SessionID).String(),
			PID:     st.PID,
			CWD:     st.WorkingDir,
			Status:  "running",
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
	client, err := b.ConnectTTYSession(sessionID)
	if err == nil {
		defer client.Close()
		screen, err := client.GetScreenANSI()
		if err == nil {
			return screen, nil
		}
		// D-Bus call failed - session probably ended, try journal
	}

	// Fall back to journal for saved screen
	filters := []journal.EventFilter{
		journal.FilterByEvent(journal.EventScreen),
		journal.FilterBySession(sessionID),
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
	sessionID := host.GenID()
	cwd := opts.WorkingDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	dbusName := fmt.Sprintf("%s.%s", host.DBusNamePrefix, sessionID)
	cmdStr := strings.Join(command, " ")

	// Resolve command[0] to absolute path so systemd can find it
	// (systemd uses its own PATH, not the inherited environment)
	if len(command) > 0 && !strings.HasPrefix(command[0], "/") {
		if absPath, err := exec.LookPath(command[0]); err == nil {
			command = append([]string{absPath}, command[1:]...)
		}
	}

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
	env["SWASH_SESSION"] = sessionID

	// Build the actual command: hostCommand... --session ID --command-json [...] [--protocol ...] [--tags-json ...]
	serverCmd := append([]string{}, b.hostCommand...)
	serverCmd = append(serverCmd,
		"--session", sessionID,
		"--command-json", host.MustJSON(command),
	)

	// Add protocol if not default (only for non-TTY mode)
	if !opts.TTY && opts.Protocol != "" && opts.Protocol != protocol.ProtocolShell {
		serverCmd = append(serverCmd, "--protocol", string(opts.Protocol))
	}

	// Add tags if present
	if len(opts.Tags) > 0 {
		serverCmd = append(serverCmd, "--tags-json", host.MustJSON(opts.Tags))
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
		SessionID:   sessionID,
		WorkingDir:  cwd,
		Description: cmdStr,
		Environment: env,
		Command:     serverCmd,
		Collect:     true,
		BusName:     dbusName,
	}

	if err := b.processes.Start(ctx, spec); err != nil {
		return "", err
	}

	return sessionID, nil
}

// StopSession stops a session by ID.
func (b *SystemdBackend) StopSession(ctx context.Context, sessionID string) error {
	return b.processes.Stop(ctx, sessionID)
}

// KillSession sends SIGKILL to every process in the session's cgroup.
func (b *SystemdBackend) KillSession(ctx context.Context, sessionID string) error {
	return b.processes.Kill(ctx, sessionID, syscall.SIGKILL)
}

// SendInput sends input to the process via the swash control plane.
func (b *SystemdBackend) SendInput(ctx context.Context, sessionID, input string) (int, error) {
	_ = ctx
	client, err := b.ConnectSession(sessionID)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	return client.SendInput(input)
}

// PollSessionOutput reads output events from a session's journal since cursor.
func (b *SystemdBackend) PollSessionOutput(ctx context.Context, sessionID, cursor string) ([]backend.Event, string, error) {
	filters := []journal.EventFilter{journal.FilterBySession(sessionID)}

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
	filters := []journal.EventFilter{journal.FilterBySession(sessionID)}
	outputBytes := 0

	// Create a timeout context if timeout > 0
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	followCtx, stopFollowing := context.WithCancel(ctx)
	defer stopFollowing()
	events := make(chan journal.EventRecord)
	go func() {
		defer close(events)
		for event := range b.events.Follow(followCtx, filters, "") {
			select {
			case events <- event:
			case <-followCtx.Done():
				return
			}
		}
	}()

	const disappearanceGrace = 500 * time.Millisecond
	checkUnit := time.NewTicker(100 * time.Millisecond)
	defer checkUnit.Stop()
	var missingSince time.Time

	for {
		select {
		case e, ok := <-events:
			if !ok {
				if ctx.Err() == context.DeadlineExceeded {
					return 0, backend.FollowTimedOut
				}
				return 0, backend.FollowCancelled
			}
			if e.Fields[journal.FieldEvent] == journal.EventExited {
				exitCode := 0
				if codeStr := e.Fields[journal.FieldExitCode]; codeStr != "" {
					exitCode, _ = strconv.Atoi(codeStr)
				}
				return exitCode, backend.FollowCompleted
			}
			if e.Fields["FD"] != "" && e.Message != "" {
				fmt.Println(e.Message)
				if outputLimit > 0 {
					outputBytes += len(e.Message) + 1
					if outputBytes > outputLimit {
						return 0, backend.FollowOutputLimit
					}
				}
			}
		case <-checkUnit.C:
			live, err := b.sessionIsLive(ctx, sessionID)
			if err != nil || live {
				missingSince = time.Time{}
				continue
			}
			if missingSince.IsZero() {
				missingSince = time.Now()
			} else if time.Since(missingSince) >= disappearanceGrace {
				return 0, backend.FollowKilled
			}
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return 0, backend.FollowTimedOut
			}
			return 0, backend.FollowCancelled
		}
	}
}

func (b *SystemdBackend) sessionIsLive(ctx context.Context, sessionID string) (bool, error) {
	sessions, err := b.ListSessions(ctx)
	if err != nil {
		return false, err
	}
	for _, session := range sessions {
		if session.ID == sessionID {
			return true, nil
		}
	}
	return false, nil
}

// ListHistory returns recently exited sessions by querying lifecycle events.
func (b *SystemdBackend) ListHistory(ctx context.Context) ([]backend.HistorySession, error) {
	started, _, err := b.events.Poll(ctx, []journal.EventFilter{journal.FilterByEvent(journal.EventStarted)}, "")
	if err != nil {
		return nil, err
	}
	live, err := b.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	liveIDs := make(map[string]bool, len(live))
	for _, session := range live {
		liveIDs[session.ID] = true
	}
	exited, _, err := b.events.Poll(ctx, []journal.EventFilter{journal.FilterByEvent(journal.EventExited)}, "")
	if err != nil {
		return nil, err
	}

	type lifecycle struct {
		started    journal.EventRecord
		exited     journal.EventRecord
		hasStarted bool
		hasExited  bool
	}
	bySession := make(map[string]lifecycle)
	for _, event := range started {
		id := event.Fields[journal.FieldSession]
		if id == "" {
			continue
		}
		item := bySession[id]
		item.started = event
		item.hasStarted = true
		bySession[id] = item
	}
	for _, event := range exited {
		id := event.Fields[journal.FieldSession]
		if id == "" {
			continue
		}
		item := bySession[id]
		item.exited = event
		item.hasExited = true
		bySession[id] = item
	}

	type historyItem struct {
		timestamp time.Time
		session   backend.HistorySession
	}
	items := make([]historyItem, 0, len(bySession))
	for id, item := range bySession {
		if liveIDs[id] {
			continue
		}
		status := "killed"
		command := item.started.Fields[journal.FieldCommand]
		startedAt := item.started.Timestamp
		latest := startedAt
		var exitCode *int
		if item.hasExited && (!item.hasStarted || !item.exited.Timestamp.Before(item.started.Timestamp)) {
			status = "exited"
			latest = item.exited.Timestamp
			if !item.hasStarted {
				command = item.exited.Fields[journal.FieldCommand]
				startedAt = item.exited.Timestamp
			}
			codeStr := item.exited.Fields[journal.FieldExitCode]
			if codeStr != "" {
				if code, err := strconv.Atoi(codeStr); err == nil {
					exitCode = &code
				}
			}
		}
		items = append(items, historyItem{
			timestamp: latest,
			session: backend.HistorySession{
				ID:       id,
				Status:   status,
				ExitCode: exitCode,
				Command:  command,
				Started:  startedAt.Format("Mon 2006-01-02 15:04:05 MST"),
			},
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].timestamp.After(items[j].timestamp) })
	sessions := make([]backend.HistorySession, len(items))
	for i, item := range items {
		sessions[i] = item.session
	}
	return sessions, nil
}

func (b *SystemdBackend) ConnectSession(sessionID string) (host.Client, error) {
	return Connect(sessionID)
}

func (b *SystemdBackend) ConnectTTYSession(sessionID string) (host.TTYClient, error) {
	return ConnectTTY(sessionID)
}

func (b *SystemdBackend) EmitSessionEvent(ctx context.Context, sessionID, event, message string, fields map[string]string) error {
	_ = ctx
	return journal.EmitSessionEvent(b.events, sessionID, event, message, fields)
}

func (b *SystemdBackend) PollEvents(ctx context.Context, filters []backend.EventFilter, cursor string) ([]journal.EventRecord, string, error) {
	return b.events.Poll(ctx, filters, cursor)
}

func (b *SystemdBackend) FollowEvents(ctx context.Context, filters []backend.EventFilter, cursor string) iter.Seq[journal.EventRecord] {
	return b.events.Follow(ctx, filters, cursor)
}
