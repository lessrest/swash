package posix

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mbrock/swash/internal/backend"
	"github.com/mbrock/swash/internal/eventlog"
	eventlogfile "github.com/mbrock/swash/internal/eventlog/file"
	"github.com/mbrock/swash/internal/session"
)

func init() {
	backend.Register(backend.KindPosix, Open)
}

type PosixBackend struct {
	cfg backend.Config

	// contextLog is a persistent writer for the context event journal.
	// Lazily initialized on first context operation.
	contextLog eventlog.EventLog
}

var _ backend.Backend = (*PosixBackend)(nil)

// Open constructs the posix backend.
func Open(ctx context.Context, cfg backend.Config) (backend.Backend, error) {
	_ = ctx
	return &PosixBackend{cfg: cfg}, nil
}

func (b *PosixBackend) Close() error {
	if b.contextLog != nil {
		return b.contextLog.Close()
	}
	return nil
}

// ensureContextLog lazily initializes the context event log writer.
func (b *PosixBackend) ensureContextLog() (eventlog.EventLog, error) {
	if b.contextLog != nil {
		return b.contextLog, nil
	}

	path := b.contextEventLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating context log dir: %w", err)
	}

	el, err := eventlogfile.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating context event log: %w", err)
	}
	b.contextLog = el
	return el, nil
}

// -----------------------------------------------------------------------------
// Paths / metadata
// -----------------------------------------------------------------------------

type meta struct {
	ID      string   `json:"id"`
	Command []string `json:"command"`

	Started string `json:"started"`
	PID     int    `json:"pid"`

	TTY  bool `json:"tty"`
	Rows int  `json:"rows,omitempty"`
	Cols int  `json:"cols,omitempty"`

	SocketPath   string `json:"socket_path"`
	EventLogPath string `json:"eventlog_path"`
}

func (b *PosixBackend) sessionDir(sessionID string) string {
	return filepath.Join(b.cfg.StateDir, "sessions", sessionID)
}

func (b *PosixBackend) metaPath(sessionID string) string {
	return filepath.Join(b.sessionDir(sessionID), "meta.json")
}

func (b *PosixBackend) eventLogPath(sessionID string) string {
	return filepath.Join(b.sessionDir(sessionID), "events.journal")
}

func (b *PosixBackend) socketPath(sessionID string) string {
	// Unix socket paths have length limits: 104 bytes on macOS/BSD, 108 on Linux.
	// Use /tmp for sockets to avoid path length issues with deep temp directories.
	// The session ID is unique enough to avoid conflicts.
	return filepath.Join(os.TempDir(), "swash-"+sessionID+".sock")
}

func (b *PosixBackend) readMeta(sessionID string) (*meta, error) {
	p := b.metaPath(sessionID)
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (b *PosixBackend) writeMeta(m meta) error {
	dir := b.sessionDir(m.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), raw, 0o644)
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal 0 doesn't actually send a signal, but performs error checking.
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	return true
}

// hasJournalEntries checks if a journal file has any entries (not just header).
// The host writes a "started" event after successfully binding the socket,
// so NEntries > 0 indicates the host got past initialization.
func hasJournalEntries(path string) bool {
	r, err := eventlogfile.Open(path)
	if err != nil {
		return false
	}
	defer r.Close()
	// Poll with no filters to check entry count
	entries, _, err := r.Poll(context.Background(), nil, "")
	return err == nil && len(entries) > 0
}

// -----------------------------------------------------------------------------
// Backend methods
// -----------------------------------------------------------------------------

func (b *PosixBackend) ListSessions(ctx context.Context) ([]backend.Session, error) {
	_ = ctx
	root := filepath.Join(b.cfg.StateDir, "sessions")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []backend.Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		m, err := b.readMeta(sessionID)
		if err != nil {
			continue
		}

		if !pidAlive(m.PID) {
			continue
		}

		sessions = append(sessions, backend.Session{
			ID:      m.ID,
			Backend: string(backend.KindPosix),
			Handle:  m.SocketPath,
			PID:     uint32(m.PID),
			CWD:     "",
			Status:  "running",
			Command: strings.Join(m.Command, " "),
			Started: m.Started,
		})
	}

	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions, nil
}

func (b *PosixBackend) ListHistory(ctx context.Context) ([]backend.HistorySession, error) {
	_ = ctx
	root := filepath.Join(b.cfg.StateDir, "sessions")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	type histItem struct {
		ts time.Time
		hs backend.HistorySession
	}
	var items []histItem

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()

		evPath := b.eventLogPath(sessionID)
		el, err := eventlogfile.Open(evPath)
		if err != nil {
			continue
		}

		filters := []eventlog.EventFilter{
			eventlog.FilterByEvent(eventlog.EventExited),
			eventlog.FilterBySession(sessionID),
		}
		recs, _, err := el.Poll(context.Background(), filters, "")
		_ = el.Close()
		if err != nil || len(recs) == 0 {
			continue
		}

		// Take the most recent exit event (last).
		rec := recs[len(recs)-1]

		var exitCode *int
		if s := rec.Fields[eventlog.FieldExitCode]; s != "" {
			if code, err := strconv.Atoi(s); err == nil {
				exitCode = &code
			}
		}

		items = append(items, histItem{
			ts: rec.Timestamp,
			hs: backend.HistorySession{
				ID:       sessionID,
				Status:   "exited",
				ExitCode: exitCode,
				Command:  rec.Fields[eventlog.FieldCommand],
				Started:  rec.Timestamp.Format("Mon 2006-01-02 15:04:05 MST"),
			},
		})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].ts.After(items[j].ts) })
	out := make([]backend.HistorySession, 0, len(items))
	for _, it := range items {
		out = append(out, it.hs)
	}
	return out, nil
}

func (b *PosixBackend) StartSession(ctx context.Context, command []string, opts backend.SessionOptions) (string, error) {
	sessionID := session.GenSessionID()

	sessionDir := b.sessionDir(sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(b.cfg.RuntimeDir, "sessions"), 0o755); err != nil {
		return "", err
	}

	socketPath := b.socketPath(sessionID)
	eventLogPath := b.eventLogPath(sessionID)

	// Build host command line.
	args := append([]string{}, b.cfg.HostCommand...)
	args = append(args,
		"--session", sessionID,
		"--command-json", session.MustJSON(command),
		"--unix-socket", socketPath,
		"--eventlog", eventLogPath,
	)

	// Protocol only matters for non-TTY; keep behavior consistent with systemd backend.
	if !opts.TTY && opts.Protocol != "" {
		args = append(args, "--protocol", string(opts.Protocol))
	}
	if len(opts.Tags) > 0 {
		args = append(args, "--tags-json", session.MustJSON(opts.Tags))
	}
	if opts.TTY {
		args = append(args, "--tty")
		if opts.Rows > 0 {
			args = append(args, "--rows", fmt.Sprintf("%d", opts.Rows))
		}
		if opts.Cols > 0 {
			args = append(args, "--cols", fmt.Sprintf("%d", opts.Cols))
		}
	}

	cmd := osexec.CommandContext(ctx, args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if opts.WorkingDir != "" {
		cmd.Dir = opts.WorkingDir
	}

	devNull, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if devNull != nil {
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}

	if err := cmd.Start(); err != nil {
		if devNull != nil {
			devNull.Close()
		}
		return "", err
	}
	if devNull != nil {
		devNull.Close()
	}

	m := meta{
		ID:           sessionID,
		Command:      command,
		Started:      time.Now().Format("Mon 2006-01-02 15:04:05 MST"),
		PID:          cmd.Process.Pid,
		TTY:          opts.TTY,
		Rows:         opts.Rows,
		Cols:         opts.Cols,
		SocketPath:   socketPath,
		EventLogPath: eventLogPath,
	}
	_ = b.writeMeta(m)

	// Wait for the control socket to appear and respond.
	if err := b.waitReady(ctx, sessionID, cmd.Process.Pid, socketPath, eventLogPath); err != nil {
		return "", err
	}

	// Emit session-context relation if context is set
	if opts.ContextID != "" {
		if err := b.emitSessionContext(sessionID, opts.ContextID); err != nil {
			// Log but don't fail - session already started
			fmt.Fprintf(os.Stderr, "warning: failed to emit session-context: %v\n", err)
		}
	}

	return sessionID, nil
}

func (b *PosixBackend) waitReady(ctx context.Context, sessionID string, pid int, socketPath, eventLogPath string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for host socket")
		}

		if _, err := os.Stat(socketPath); err == nil {
			c, err := session.ConnectUnixSession(sessionID, socketPath)
			if err == nil {
				_, err = c.Gist()
				_ = c.Close()
				if err == nil {
					return nil
				}
			}
		}

		// Check if the journal has actual entries (not just the header).
		// The host writes the "started" event after successfully binding the socket,
		// so entries > 0 means startup succeeded. This handles both:
		// - Fast commands that complete before we can connect to the socket
		// - Startup failures where the host crashes before writing any events
		if hasJournalEntries(eventLogPath) {
			return nil
		}

		// If process died and journal has no entries, startup failed.
		if !pidAlive(pid) {
			return fmt.Errorf("host process exited before writing any events (pid %d) - check for startup errors", pid)
		}

		time.Sleep(25 * time.Millisecond)
	}
}

func (b *PosixBackend) StopSession(ctx context.Context, sessionID string) error {
	_ = ctx
	m, err := b.readMeta(sessionID)
	if err != nil {
		return err
	}
	if m.PID <= 0 {
		return fmt.Errorf("no pid for session %s", sessionID)
	}
	return syscall.Kill(m.PID, syscall.SIGTERM)
}

func (b *PosixBackend) KillSession(ctx context.Context, sessionID string) error {
	// Prefer control plane, fall back to SIGKILL of the host.
	client, err := b.ConnectSession(sessionID)
	if err == nil {
		defer client.Close()
		if err := client.Kill(); err == nil {
			return nil
		}
	}

	m, err2 := b.readMeta(sessionID)
	if err2 != nil {
		return err2
	}
	return syscall.Kill(m.PID, syscall.SIGKILL)
}

func (b *PosixBackend) SendInput(ctx context.Context, sessionID, input string) (int, error) {
	_ = ctx
	client, err := b.ConnectSession(sessionID)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	return client.SendInput(input)
}

func (b *PosixBackend) PollSessionOutput(ctx context.Context, sessionID, cursor string) ([]backend.Event, string, error) {
	evPath := b.eventLogPath(sessionID)
	el, err := eventlogfile.Open(evPath)
	if err != nil {
		return nil, "", err
	}
	defer el.Close()

	filters := []eventlog.EventFilter{eventlog.FilterBySession(sessionID)}
	entries, newCursor, err := el.Poll(ctx, filters, cursor)
	if err != nil {
		return nil, "", err
	}

	var events []backend.Event
	for _, e := range entries {
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

func (b *PosixBackend) FollowSession(ctx context.Context, sessionID string, timeout time.Duration, outputLimit int) (int, backend.FollowResult) {
	evPath := b.eventLogPath(sessionID)
	el, err := eventlogfile.Open(evPath)
	if err != nil {
		return 0, backend.FollowCancelled
	}
	defer el.Close()

	filters := []eventlog.EventFilter{eventlog.FilterBySession(sessionID)}
	outputBytes := 0

	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	for e := range el.Follow(ctx, filters) {
		if e.Fields[eventlog.FieldEvent] == eventlog.EventExited {
			exitCode := 0
			if codeStr := e.Fields[eventlog.FieldExitCode]; codeStr != "" {
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
	}

	if ctx.Err() == context.DeadlineExceeded {
		return 0, backend.FollowTimedOut
	}
	return 0, backend.FollowCancelled
}

func (b *PosixBackend) GetScreen(ctx context.Context, sessionID string) (string, error) {
	_ = ctx
	// Try control plane for a live session first.
	client, err := b.ConnectTTYSession(sessionID)
	if err == nil {
		defer client.Close()
		if screen, err := client.GetScreenANSI(); err == nil {
			return screen, nil
		}
	}

	// Fall back to saved screen event.
	evPath := b.eventLogPath(sessionID)
	el, err := eventlogfile.Open(evPath)
	if err != nil {
		return "", err
	}
	defer el.Close()

	filters := []eventlog.EventFilter{
		eventlog.FilterByEvent(eventlog.EventScreen),
		eventlog.FilterBySession(sessionID),
	}
	entries, _, err := el.Poll(context.Background(), filters, "")
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no screen found for session %s", sessionID)
	}
	return entries[len(entries)-1].Message, nil
}

func (b *PosixBackend) ConnectSession(sessionID string) (session.SessionClient, error) {
	sock := b.socketPath(sessionID)
	if _, err := os.Stat(sock); err != nil {
		return nil, err
	}
	return session.ConnectUnixSession(sessionID, sock)
}

func (b *PosixBackend) ConnectTTYSession(sessionID string) (session.TTYClient, error) {
	sock := b.socketPath(sessionID)
	if _, err := os.Stat(sock); err != nil {
		return nil, err
	}
	return session.ConnectUnixTTYSession(sessionID, sock)
}

// -----------------------------------------------------------------------------
// Context management
// -----------------------------------------------------------------------------

func (b *PosixBackend) contextDir(contextID string) string {
	return filepath.Join(b.cfg.StateDir, "contexts", contextID)
}

func (b *PosixBackend) contextEventLogPath() string {
	return filepath.Join(b.cfg.StateDir, "contexts.journal")
}

func (b *PosixBackend) CreateContext(ctx context.Context) (string, string, error) {
	_ = ctx
	contextID := session.GenSessionID()
	dir := b.contextDir(contextID)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("creating context directory: %w", err)
	}

	el, err := b.ensureContextLog()
	if err != nil {
		return "", "", fmt.Errorf("opening context event log: %w", err)
	}
	// Don't close - kept open for future writes

	if err := eventlog.EmitContextCreated(el, contextID, dir); err != nil {
		return "", "", fmt.Errorf("emitting context-created event: %w", err)
	}

	return contextID, dir, nil
}

func (b *PosixBackend) ListContexts(ctx context.Context) ([]backend.Context, error) {
	_ = ctx
	el, err := eventlogfile.Open(b.contextEventLogPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening context event log: %w", err)
	}
	defer el.Close()

	filters := []eventlog.EventFilter{eventlog.FilterByEvent(eventlog.EventContextCreated)}
	entries, _, err := el.Poll(context.Background(), filters, "")
	if err != nil {
		return nil, err
	}

	var contexts []backend.Context
	for _, e := range entries {
		contexts = append(contexts, backend.Context{
			ID:      e.Fields[eventlog.FieldContext],
			Dir:     e.Fields["DIR"],
			Created: e.Timestamp,
		})
	}
	return contexts, nil
}

func (b *PosixBackend) GetContextDir(ctx context.Context, contextID string) (string, error) {
	_ = ctx
	dir := b.contextDir(contextID)
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("context %s not found", contextID)
	}
	return dir, nil
}

func (b *PosixBackend) ListContextSessions(ctx context.Context, contextID string) ([]string, error) {
	_ = ctx
	el, err := eventlogfile.Open(b.contextEventLogPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening context event log: %w", err)
	}
	defer el.Close()

	filters := []eventlog.EventFilter{
		eventlog.FilterByEvent(eventlog.EventSessionContext),
		eventlog.FilterByContext(contextID),
	}
	entries, _, err := el.Poll(context.Background(), filters, "")
	if err != nil {
		return nil, err
	}

	var sessionIDs []string
	for _, e := range entries {
		if sid := e.Fields[eventlog.FieldSession]; sid != "" {
			sessionIDs = append(sessionIDs, sid)
		}
	}
	return sessionIDs, nil
}

func (b *PosixBackend) emitSessionContext(sessionID, contextID string) error {
	el, err := b.ensureContextLog()
	if err != nil {
		return fmt.Errorf("opening context event log: %w", err)
	}
	// Don't close - kept open for future writes

	return eventlog.EmitSessionContext(el, sessionID, contextID)
}
