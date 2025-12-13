package posix

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mbrock/swash/internal/backend"
	"github.com/mbrock/swash/internal/graph"
	"github.com/mbrock/swash/internal/job"
	"github.com/mbrock/swash/internal/journal"
	"github.com/mbrock/swash/pkg/oxigraph"
)

func init() {
	backend.Register(backend.KindPosix, Open)
}

type PosixBackend struct {
	cfg backend.Config

	// journaldConfig holds the socket and journal paths for "swash minijournald".
	journaldConfig journal.Config

	// sharedLog is the combined eventlog for the shared journal.
	// Uses SocketSink for writing and JournalfileSource for reading.
	// Lazily initialized on first use.
	sharedLog journal.EventLog
}

var _ backend.Backend = (*PosixBackend)(nil)

// Open constructs the posix backend.
func Open(ctx context.Context, cfg backend.Config) (backend.Backend, error) {
	_ = ctx

	// Configure journald paths:
	// - Socket in RuntimeDir (ephemeral, cleared on reboot)
	// - Journal file in StateDir (persistent)
	jcfg := journal.Config{
		SocketPath:  filepath.Join(cfg.RuntimeDir, "journal.socket"),
		JournalPath: filepath.Join(cfg.StateDir, "swash.journal"),
	}

	return &PosixBackend{
		cfg:            cfg,
		journaldConfig: jcfg,
	}, nil
}

func (b *PosixBackend) Close() error {
	if b.sharedLog != nil {
		return b.sharedLog.Close()
	}
	return nil
}

// ensureJournald starts "swash minijournald" if it's not already running.
// It returns when the daemon is ready to accept connections.
func (b *PosixBackend) ensureJournald(ctx context.Context) error {
	socketPath := b.journaldConfig.SocketPath

	// Check if socket already exists and is accepting connections
	if _, err := os.Stat(socketPath); err == nil {
		// Socket exists, assume daemon is running
		return nil
	}

	// Use the same swash binary with "minijournald" subcommand
	swashBin := b.cfg.HostCommand[0]

	cmd := osexec.CommandContext(ctx, swashBin, "minijournald",
		"--socket", socketPath,
		"--journal", b.journaldConfig.JournalPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	// Redirect output to /dev/null - daemon logs to stderr
	devNull, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if devNull != nil {
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = os.Stderr // Let daemon errors show
	}

	if err := cmd.Start(); err != nil {
		if devNull != nil {
			devNull.Close()
		}
		return fmt.Errorf("starting swash minijournald: %w", err)
	}
	if devNull != nil {
		devNull.Close()
	}

	// Wait for socket to appear
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for swash minijournald socket")
		}
		if _, err := os.Stat(socketPath); err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// ensureSharedLog lazily initializes the eventlog for reading and writing
// to the shared journal. Uses SocketSink for writing and JournalfileSource for reading.
func (b *PosixBackend) ensureSharedLog(ctx context.Context) (journal.EventLog, error) {
	if b.sharedLog != nil {
		return b.sharedLog, nil
	}

	// Make sure "swash minijournald" is running
	if err := b.ensureJournald(ctx); err != nil {
		return nil, fmt.Errorf("ensuring journald: %w", err)
	}

	// Create sink (write to socket)
	snk := journal.NewSocketSink(b.journaldConfig.SocketPath)

	// Create source (read from journal file)
	src, err := journal.NewJournalfileSource(b.journaldConfig.JournalPath)
	if err != nil {
		snk.Close()
		return nil, fmt.Errorf("opening journal source: %w", err)
	}

	// Combine into full EventLog
	b.sharedLog = journal.NewCombinedEventLog(snk, src)
	return b.sharedLog, nil
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

	SocketPath string `json:"socket_path"`
}

// sessionDir returns the runtime directory for a session (ephemeral, sockets + metadata).
func (b *PosixBackend) sessionDir(sessionID string) string {
	return filepath.Join(b.cfg.RuntimeDir, "sessions", sessionID)
}

func (b *PosixBackend) metaPath(sessionID string) string {
	return filepath.Join(b.sessionDir(sessionID), "meta.json")
}

func (b *PosixBackend) socketPath(sessionID string) string {
	return filepath.Join(b.sessionDir(sessionID), "control.sock")
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
	el, err := b.ensureSharedLog(ctx)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	filters := []journal.EventFilter{
		journal.FilterByEvent(journal.EventExited),
	}
	recs, _, err := el.Poll(ctx, filters, "")
	if err != nil {
		return nil, err
	}

	// Group by session ID, keeping only the most recent exit per session
	bySession := make(map[string]journal.EventRecord)
	for _, rec := range recs {
		sid := rec.Fields[journal.FieldSession]
		if sid == "" {
			continue
		}
		// Later records overwrite earlier ones
		bySession[sid] = rec
	}

	type histItem struct {
		ts time.Time
		hs backend.HistorySession
	}
	var items []histItem

	for sessionID, rec := range bySession {
		var exitCode *int
		if s := rec.Fields[journal.FieldExitCode]; s != "" {
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
				Command:  rec.Fields[journal.FieldCommand],
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
	// Ensure "swash minijournald" is running
	if err := b.ensureJournald(ctx); err != nil {
		return "", fmt.Errorf("ensuring journald: %w", err)
	}

	sessionID := job.GenID()

	sessionDir := b.sessionDir(sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(b.cfg.RuntimeDir, "sessions"), 0o755); err != nil {
		return "", err
	}

	socketPath := b.socketPath(sessionID)

	// Build host command line (no longer passing --eventlog, using socket instead).
	args := append([]string{}, b.cfg.HostCommand...)
	args = append(args,
		"--session", sessionID,
		"--command-json", job.MustJSON(command),
		"--unix-socket", socketPath,
	)

	// Protocol only matters for non-TTY; keep behavior consistent with systemd backend.
	if !opts.TTY && opts.Protocol != "" {
		args = append(args, "--protocol", string(opts.Protocol))
	}
	if len(opts.Tags) > 0 {
		args = append(args, "--tags-json", job.MustJSON(opts.Tags))
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

	// Pass journal socket path to host via environment
	cmd.Env = append(os.Environ(),
		"SWASH_JOURNAL_SOCKET="+b.journaldConfig.SocketPath,
		"SWASH_JOURNAL_PATH="+b.journaldConfig.JournalPath,
	)

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
		ID:         sessionID,
		Command:    command,
		Started:    time.Now().Format("Mon 2006-01-02 15:04:05 MST"),
		PID:        cmd.Process.Pid,
		TTY:        opts.TTY,
		Rows:       opts.Rows,
		Cols:       opts.Cols,
		SocketPath: socketPath,
		// EventLogPath removed - now using shared journal
	}
	_ = b.writeMeta(m)

	// Wait for the control socket to appear and respond.
	if err := b.waitReady(ctx, sessionID, cmd.Process.Pid, socketPath); err != nil {
		return "", err
	}

	// Emit session-context relation if context is set
	if opts.ContextID != "" {
		if err := b.emitSessionContext(ctx, sessionID, opts.ContextID); err != nil {
			// Log but don't fail - session already started
			fmt.Fprintf(os.Stderr, "warning: failed to emit session-context: %v\n", err)
		}
	}

	// Emit service type if set
	if opts.ServiceType != "" {
		if err := b.emitServiceType(ctx, sessionID, opts.ServiceType); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to emit service-type: %v\n", err)
		}
	}

	return sessionID, nil
}

func (b *PosixBackend) waitReady(ctx context.Context, sessionID string, pid int, socketPath string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for host socket")
		}

		if _, err := os.Stat(socketPath); err == nil {
			c, err := job.ConnectUnix(sessionID, socketPath)
			if err == nil {
				_, err = c.Gist()
				_ = c.Close()
				if err == nil {
					return nil
				}
			}
		}

		// Check if session has started event in shared journal.
		// The host writes the "started" event after successfully binding the socket,
		// so finding it means startup succeeded. This handles fast commands that
		// complete before we can connect to the socket.
		if b.hasSessionStarted(sessionID) {
			return nil
		}

		// If process died and no started event, startup failed.
		if !pidAlive(pid) {
			return fmt.Errorf("host process exited before writing any events (pid %d) - check for startup errors", pid)
		}

		time.Sleep(25 * time.Millisecond)
	}
}

// hasSessionStarted checks if a session has a "started" event in the shared journal.
func (b *PosixBackend) hasSessionStarted(sessionID string) bool {
	el, err := b.ensureSharedLog(context.Background())
	if err != nil {
		return false
	}

	filters := []journal.EventFilter{
		journal.FilterBySession(sessionID),
		journal.FilterByEvent(journal.EventStarted),
	}
	entries, _, err := el.Poll(context.Background(), filters, "")
	return err == nil && len(entries) > 0
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
	el, err := b.ensureSharedLog(ctx)
	if err != nil {
		return nil, "", err
	}

	filters := []journal.EventFilter{journal.FilterBySession(sessionID)}
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
	el, err := b.ensureSharedLog(ctx)
	if err != nil {
		return 0, backend.FollowCancelled
	}

	filters := []journal.EventFilter{journal.FilterBySession(sessionID)}
	outputBytes := 0

	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	for e := range el.Follow(ctx, filters) {
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
	}

	if ctx.Err() == context.DeadlineExceeded {
		return 0, backend.FollowTimedOut
	}
	return 0, backend.FollowCancelled
}

func (b *PosixBackend) GetScreen(ctx context.Context, sessionID string) (string, error) {
	// Try control plane for a live session first.
	client, err := b.ConnectTTYSession(sessionID)
	if err == nil {
		defer client.Close()
		if screen, err := client.GetScreenANSI(); err == nil {
			return screen, nil
		}
	}

	// Fall back to saved screen event in shared journal.
	el, err := b.ensureSharedLog(ctx)
	if err != nil {
		return "", err
	}

	filters := []journal.EventFilter{
		journal.FilterByEvent(journal.EventScreen),
		journal.FilterBySession(sessionID),
	}
	entries, _, err := el.Poll(ctx, filters, "")
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no screen found for session %s", sessionID)
	}
	return entries[len(entries)-1].Message, nil
}

func (b *PosixBackend) ConnectSession(sessionID string) (job.Client, error) {
	sock := b.socketPath(sessionID)
	if _, err := os.Stat(sock); err != nil {
		return nil, err
	}
	return job.ConnectUnix(sessionID, sock)
}

func (b *PosixBackend) ConnectTTYSession(sessionID string) (job.TTYClient, error) {
	sock := b.socketPath(sessionID)
	if _, err := os.Stat(sock); err != nil {
		return nil, err
	}
	return job.ConnectUnixTTY(sessionID, sock)
}

// -----------------------------------------------------------------------------
// Context management
// -----------------------------------------------------------------------------

func (b *PosixBackend) contextDir(contextID string) string {
	return filepath.Join(b.cfg.StateDir, "contexts", contextID)
}

func (b *PosixBackend) CreateContext(ctx context.Context) (string, string, error) {
	contextID := job.GenID()
	dir := b.contextDir(contextID)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("creating context directory: %w", err)
	}

	el, err := b.ensureSharedLog(ctx)
	if err != nil {
		return "", "", fmt.Errorf("opening shared event log: %w", err)
	}
	// Don't close - kept open for future writes

	if err := journal.EmitContextCreated(el, contextID, dir); err != nil {
		return "", "", fmt.Errorf("emitting context-created event: %w", err)
	}

	return contextID, dir, nil
}

func (b *PosixBackend) ListContexts(ctx context.Context) ([]backend.Context, error) {
	el, err := b.ensureSharedLog(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening shared event log: %w", err)
	}
	// Don't close - kept open

	filters := []journal.EventFilter{journal.FilterByEvent(journal.EventContextCreated)}
	entries, _, err := el.Poll(ctx, filters, "")
	if err != nil {
		return nil, err
	}

	var contexts []backend.Context
	for _, e := range entries {
		contexts = append(contexts, backend.Context{
			ID:      e.Fields[journal.FieldContext],
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
	el, err := b.ensureSharedLog(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening shared event log: %w", err)
	}
	// Don't close - kept open

	filters := []journal.EventFilter{
		journal.FilterByEvent(journal.EventSessionContext),
		journal.FilterByContext(contextID),
	}
	entries, _, err := el.Poll(ctx, filters, "")
	if err != nil {
		return nil, err
	}

	var sessionIDs []string
	for _, e := range entries {
		if sid := e.Fields[journal.FieldSession]; sid != "" {
			sessionIDs = append(sessionIDs, sid)
		}
	}
	return sessionIDs, nil
}

func (b *PosixBackend) emitSessionContext(ctx context.Context, sessionID, contextID string) error {
	el, err := b.ensureSharedLog(ctx)
	if err != nil {
		return fmt.Errorf("opening shared event log: %w", err)
	}
	// Don't close - kept open for future writes

	return journal.EmitSessionContext(el, sessionID, contextID)
}

func (b *PosixBackend) emitServiceType(ctx context.Context, sessionID, serviceType string) error {
	el, err := b.ensureSharedLog(ctx)
	if err != nil {
		return fmt.Errorf("opening shared event log: %w", err)
	}
	return journal.EmitServiceType(el, sessionID, serviceType)
}

// -----------------------------------------------------------------------------
// Graph (RDF knowledge graph)
// -----------------------------------------------------------------------------

func (b *PosixBackend) graphSocketPath() string {
	return filepath.Join(b.cfg.RuntimeDir, "graph.sock")
}

func (b *PosixBackend) graphClient() *graph.Client {
	return graph.NewClient(b.graphSocketPath())
}

// ensureGraph starts "swash graph serve" if it's not already running.
func (b *PosixBackend) ensureGraph(ctx context.Context) error {
	socketPath := b.graphSocketPath()

	// Check if service is already running and healthy
	client := b.graphClient()
	if err := client.Health(ctx); err == nil {
		return nil // Already running
	}

	// Use the same swash binary with "graph serve" subcommand
	swashBin := b.cfg.HostCommand[0]

	cmd := osexec.CommandContext(ctx, swashBin, "graph", "serve",
		"--socket", socketPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	// Redirect output
	devNull, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if devNull != nil {
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = os.Stderr // Let errors show
	}

	if err := cmd.Start(); err != nil {
		if devNull != nil {
			devNull.Close()
		}
		return fmt.Errorf("starting swash graph serve: %w", err)
	}
	if devNull != nil {
		devNull.Close()
	}

	// Wait for health check to pass
	deadline := time.Now().Add(10 * time.Second) // Graph service takes longer to start (WASM)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for graph service to start")
		}
		if err := client.Health(ctx); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (b *PosixBackend) GraphQuery(ctx context.Context, sparql string) ([]oxigraph.Solution, error) {
	if err := b.ensureGraph(ctx); err != nil {
		return nil, fmt.Errorf("ensuring graph service: %w", err)
	}

	client := b.graphClient()
	return client.Query(ctx, sparql)
}

func (b *PosixBackend) GraphSerialize(ctx context.Context, pattern oxigraph.Pattern, format oxigraph.Format) ([]byte, error) {
	if err := b.ensureGraph(ctx); err != nil {
		return nil, fmt.Errorf("ensuring graph service: %w", err)
	}

	client := b.graphClient()

	// Map format to string for HTTP API
	formatStr := ""
	if format == oxigraph.NQuads {
		formatStr = "nquads"
	}

	return client.Quads(ctx, pattern, formatStr)
}

func (b *PosixBackend) GraphLoad(ctx context.Context, data []byte, format oxigraph.Format) error {
	if err := b.ensureGraph(ctx); err != nil {
		return fmt.Errorf("ensuring graph service: %w", err)
	}

	client := b.graphClient()
	return client.Load(ctx, data, format)
}

// -----------------------------------------------------------------------------
// Lifecycle events (for graph population)
// -----------------------------------------------------------------------------

func (b *PosixBackend) PollLifecycleEvents(ctx context.Context, cursor string) ([]journal.EventRecord, string, error) {
	log, err := b.ensureSharedLog(ctx)
	if err != nil {
		return nil, "", err
	}
	filters := journal.LifecycleEventFilters()
	return log.Poll(ctx, filters, cursor)
}

func (b *PosixBackend) FollowLifecycleEvents(ctx context.Context) iter.Seq[journal.EventRecord] {
	log, err := b.ensureSharedLog(ctx)
	if err != nil {
		return func(yield func(journal.EventRecord) bool) {}
	}
	filters := journal.LifecycleEventFilters()
	return log.Follow(ctx, filters)
}
