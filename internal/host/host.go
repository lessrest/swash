package host

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/godbus/dbus/v5"

	"github.com/mbrock/swash/internal/eventlog"
	"github.com/mbrock/swash/internal/eventlog/sink"
	"github.com/mbrock/swash/internal/eventlog/source"
	"github.com/mbrock/swash/internal/executor"
	journald "github.com/mbrock/swash/internal/platform/systemd/eventlog"
	"github.com/mbrock/swash/internal/protocol"
	"github.com/mbrock/swash/internal/session"
	"github.com/mbrock/swash/internal/tty"
)

// Host is the D-Bus host for a swash session.
type Host struct {
	sessionID string
	command   []string
	protocol  protocol.Protocol
	tags      map[string]string

	events   eventlog.EventLog
	executor executor.Executor

	mu        sync.Mutex
	proc      executor.Process // the running task process
	stdin     io.WriteCloser
	running   bool
	exitCode  *int
	restartCh chan struct{} // signals a restart request
	doneCh    chan struct{} // current task's done channel

	// Pipe read ends - kept so we can close them to unblock readers on shutdown
	stdoutRead *os.File
	stderrRead *os.File
}

// HostConfig holds the configuration for creating a Host.
type HostConfig struct {
	SessionID string
	Command   []string
	Protocol  protocol.Protocol
	Tags      map[string]string
	Events    eventlog.EventLog
	Executor  executor.Executor // Optional; defaults to ExecExecutor if nil
}

// NewHost creates a new Host with the given configuration.
func NewHost(cfg HostConfig) *Host {
	// Merge session ID into tags so output lines can be filtered
	tags := make(map[string]string)
	maps.Copy(tags, cfg.Tags)
	tags[eventlog.FieldSession] = cfg.SessionID

	execImpl := cfg.Executor
	if execImpl == nil {
		execImpl = executor.Default()
	}

	return &Host{
		sessionID: cfg.SessionID,
		command:   cfg.Command,
		protocol:  cfg.Protocol,
		tags:      tags,
		events:    cfg.Events,
		executor:  execImpl,
	}
}

type HostStatus = session.HostStatus

// Gist returns the current session status.
func (h *Host) Gist() (HostStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return HostStatus{
		Running:  h.running,
		ExitCode: h.exitCode,
		Command:  h.command,
	}, nil
}

// SessionID returns the session ID.
func (h *Host) SessionID() (string, error) {
	return h.sessionID, nil
}

// SendInput writes data to the process stdin.
// Returns the number of bytes written.
func (h *Host) SendInput(data string) (int, error) {
	h.mu.Lock()
	stdin := h.stdin
	running := h.running
	h.mu.Unlock()

	if !running || stdin == nil {
		return 0, fmt.Errorf("no process running")
	}

	return stdin.Write([]byte(data))
}

// Kill sends SIGKILL to the task process.
// This should only be used for restart; for shutdown use GracefulKill.
func (h *Host) Kill() error {
	slog.Debug("Host.Kill called", "session", h.sessionID)
	h.mu.Lock()
	proc := h.proc
	h.mu.Unlock()
	if proc == nil {
		slog.Debug("Host.Kill no process")
		return fmt.Errorf("no process running")
	}
	slog.Warn("Host.Kill sending SIGKILL", "session", h.sessionID)
	return proc.Kill()
}

// GracefulKill sends SIGTERM to the task process and waits for it to exit.
// The doneChan should signal when the process has exited.
// This allows the child process to flush coverage data before exiting.
func (h *Host) GracefulKill(doneChan <-chan struct{}) {
	slog.Debug("Host.GracefulKill called", "session", h.sessionID)
	h.mu.Lock()
	proc := h.proc
	h.mu.Unlock()
	if proc == nil {
		slog.Debug("Host.GracefulKill no process")
		return
	}
	slog.Debug("Host.GracefulKill sending SIGTERM")
	proc.Signal(syscall.SIGTERM)
	// Wait for process to exit - the caller will handle timeout via doneChan
}

// closePipes closes the pipe read ends to unblock any readers.
func (h *Host) closePipes() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stdoutRead != nil {
		h.stdoutRead.Close()
		h.stdoutRead = nil
	}
	if h.stderrRead != nil {
		h.stderrRead.Close()
		h.stderrRead = nil
	}
}

// Restart kills the current task and spawns a new one with the same command.
func (h *Host) Restart() error {
	h.mu.Lock()
	if !h.running {
		h.mu.Unlock()
		return fmt.Errorf("no task running")
	}
	restartCh := h.restartCh
	h.mu.Unlock()

	if restartCh == nil {
		return fmt.Errorf("restart not supported")
	}

	// Signal the restart request
	select {
	case restartCh <- struct{}{}:
		return nil
	default:
		return fmt.Errorf("restart already in progress")
	}
}

// Run starts the D-Bus host and runs until the task exits or a signal is received.
func (h *Host) Run() error {
	slog.Debug("Host.Run starting", "session", h.sessionID, "command", h.command)

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		slog.Debug("Host.Run D-Bus connect failed", "error", err)
		return fmt.Errorf("connecting to D-Bus: %w", err)
	}
	defer conn.Close()

	busName := fmt.Sprintf("%s.%s", session.DBusNamePrefix, h.sessionID)
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		slog.Debug("Host.Run bus name request failed", "busName", busName, "error", err)
		return fmt.Errorf("requesting bus name: %w", err)
	}
	slog.Debug("Host.Run acquired bus name", "busName", busName)

	conn.ExportAll(h, dbus.ObjectPath(session.DBusPath), session.DBusNamePrefix)

	// Set up context that cancels on SIGTERM/SIGINT
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case sig := <-sigChan:
			slog.Debug("Host.Run received signal, cancelling context", "signal", sig, "session", h.sessionID)
			cancel()
		case <-ctx.Done():
		}
	}()

	slog.Debug("Host.Run starting task", "session", h.sessionID)
	err = h.RunTask(ctx)
	slog.Debug("Host.Run task finished", "session", h.sessionID, "error", err)

	// Emit exit signal before closing D-Bus connection
	h.mu.Lock()
	exitCode := h.exitCode
	h.mu.Unlock()
	if exitCode != nil {
		slog.Debug("Host.Run emitting exit signal", "session", h.sessionID, "exitCode", *exitCode)
		conn.Emit(dbus.ObjectPath(session.DBusPath), session.DBusNamePrefix+".Exited", int32(*exitCode))
	}

	slog.Debug("Host.Run exiting", "session", h.sessionID)
	return err
}

// RunTask starts the task process and waits for it to complete.
// This is the core logic without D-Bus setup or signal handling,
// suitable for testing.
func (h *Host) RunTask(ctx context.Context) error {
	slog.Debug("Host.RunTask starting", "session", h.sessionID)

	// Create restart channel
	h.mu.Lock()
	h.restartCh = make(chan struct{}, 1)
	h.mu.Unlock()

	for {
		slog.Debug("Host.RunTask starting task process", "session", h.sessionID)
		doneChan, err := h.startTaskProcess()
		if err != nil {
			slog.Debug("Host.RunTask failed to start process", "session", h.sessionID, "error", err)
			return fmt.Errorf("starting process: %w", err)
		}

		h.mu.Lock()
		h.doneCh = doneChan
		h.mu.Unlock()

		// Emit lifecycle event
		if err := eventlog.EmitStarted(h.events, h.sessionID, h.command); err != nil {
			return fmt.Errorf("emitting started event: %w", err)
		}

		slog.Debug("Host.RunTask waiting for task", "session", h.sessionID)
		select {
		case <-doneChan:
			// Task exited normally
			slog.Debug("Host.RunTask task exited normally", "session", h.sessionID)
			return nil
		case <-h.restartCh:
			// Restart requested - kill current task and loop
			slog.Debug("Host.RunTask restart requested", "session", h.sessionID)
			h.Kill()
			// Close pipes to unblock readers
			h.closePipes()
			<-doneChan // Wait for task to actually exit
			slog.Debug("Host.RunTask restarting", "session", h.sessionID)
			// Loop continues to start new task
		case <-ctx.Done():
			slog.Debug("Host.RunTask context done, gracefully killing task", "session", h.sessionID)
			h.GracefulKill(doneChan)
			// Close pipes to unblock readers so doneChan can complete
			h.closePipes()
			<-doneChan
			slog.Debug("Host.RunTask task exited after SIGTERM", "session", h.sessionID)
			return ctx.Err()
		}
	}
}

// startTaskProcess starts the task subprocess via the executor.
func (srv *Host) startTaskProcess() (chan struct{}, error) {
	slog.Debug("Host.startTaskProcess", "session", srv.sessionID, "command", srv.command)

	// Create pipes for stdio
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		stdinRead.Close()
		stdinWrite.Close()
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		stdinRead.Close()
		stdinWrite.Close()
		stdoutRead.Close()
		stdoutWrite.Close()
		return nil, fmt.Errorf("creating stderr pipe: %w", err)
	}

	closeAllPipes := func() {
		stdinRead.Close()
		stdinWrite.Close()
		stdoutRead.Close()
		stdoutWrite.Close()
		stderrRead.Close()
		stderrWrite.Close()
	}

	// Start the process using the executor
	proc, err := srv.executor.Start(srv.command, stdinRead, stdoutWrite, stderrWrite)
	if err != nil {
		closeAllPipes()
		return nil, fmt.Errorf("starting process: %w", err)
	}

	// Close the child-facing ends of the pipes (child now owns them)
	stdinRead.Close()
	stdoutWrite.Close()
	stderrWrite.Close()

	// Store proc and stdin for SendInput/Kill
	srv.mu.Lock()
	srv.proc = proc
	srv.stdin = stdinWrite
	srv.stdoutRead = stdoutRead
	srv.stderrRead = stderrRead
	srv.running = true
	srv.mu.Unlock()

	doneChan := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)

	// Output handler that writes to journal with tags
	outputHandler := func(fd int, text string, fields map[string]string) {
		eventlog.WriteOutput(srv.events, fd, text, fields)
	}

	// Read stdout and write to journal (protocol-aware)
	go func() {
		defer wg.Done()
		reader := protocol.NewProtocolReader(srv.protocol, 1, outputHandler, srv.tags)
		reader.Process(stdoutRead)
		stdoutRead.Close()
	}()

	// Read stderr and write to journal (always line-oriented)
	go func() {
		defer wg.Done()
		reader := protocol.NewProtocolReader(protocol.ProtocolShell, 2, outputHandler, srv.tags)
		reader.Process(stderrRead)
		stderrRead.Close()
	}()

	// Wait for process to exit
	go func() {
		// Wait for process to exit and get exit code
		exitCode, _ := proc.Wait()
		if exitCode != 0 {
			slog.Warn("Host.startTaskProcess task exited abnormally", "session", srv.sessionID, "exitCode", exitCode, "command", srv.command)
		} else {
			slog.Debug("Host.startTaskProcess task process exited", "session", srv.sessionID, "exitCode", exitCode)
		}

		// Wait for pipes to finish reading
		wg.Wait()

		srv.mu.Lock()
		srv.exitCode = &exitCode
		srv.running = false
		srv.proc = nil
		if srv.stdin != nil {
			srv.stdin.Close()
		}
		srv.stdin = nil
		srv.mu.Unlock()

		// Emit lifecycle event
		if err := eventlog.EmitExited(srv.events, srv.sessionID, exitCode, srv.command); err != nil {
			slog.Debug("Host.startTaskProcess failed to emit exited event", "error", err)
		}

		close(doneChan)
	}()

	return doneChan, nil
}

// RunHost is the entrypoint for the "swash host" command.
// It parses flags, creates real implementations, and runs the server.
func RunHost() error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	sessionIDFlag := fs.String("session", "", "Session ID")
	commandJSONFlag := fs.String("command-json", "", "Command as JSON array")
	protocolFlag := fs.String("protocol", "shell", "Protocol: shell, sse")
	tagsJSONFlag := fs.String("tags-json", "", "Extra journal fields as JSON object")
	ttyFlag := fs.Bool("tty", false, "Use PTY mode with terminal emulation")
	rowsFlag := fs.Int("rows", 24, "Terminal rows (for --tty mode)")
	colsFlag := fs.Int("cols", 80, "Terminal columns (for --tty mode)")
	unixSocketFlag := fs.String("unix-socket", "", "Serve control plane over a unix socket (posix backend)")
	// Skip "swash" (index 0) and "host" (index 1) to get to the flags
	fs.Parse(os.Args[2:])

	if *sessionIDFlag == "" || *commandJSONFlag == "" {
		return fmt.Errorf("missing required flags")
	}

	var command []string
	if err := json.Unmarshal([]byte(*commandJSONFlag), &command); err != nil {
		return fmt.Errorf("parsing command: %w", err)
	}

	tags := make(map[string]string)
	if *tagsJSONFlag != "" {
		if err := json.Unmarshal([]byte(*tagsJSONFlag), &tags); err != nil {
			return fmt.Errorf("parsing tags: %w", err)
		}
	}

	// POSIX (unix socket) mode: run without systemd/journald/D-Bus.
	if *unixSocketFlag != "" {
		// Use socket-based eventlog via SWASH_JOURNAL_SOCKET.
		socketPath := os.Getenv("SWASH_JOURNAL_SOCKET")
		if socketPath == "" {
			return fmt.Errorf("missing SWASH_JOURNAL_SOCKET for --unix-socket mode")
		}
		journalPath := os.Getenv("SWASH_JOURNAL_PATH")
		if journalPath == "" {
			journalPath = filepath.Join(filepath.Dir(socketPath), "swash.journal")
		}

		// Create combined eventlog: SocketSink for writing, JournalfileSource for reading
		snk := sink.NewSocketSink(socketPath)
		src, err := source.NewJournalfileSource(journalPath)
		if err != nil {
			snk.Close()
			return fmt.Errorf("opening journal source: %w", err)
		}
		events := eventlog.NewCombinedEventLog(snk, src)
		defer events.Close()

		// Set up context that cancels on SIGTERM/SIGINT (like D-Bus mode).
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			select {
			case <-sigChan:
				cancel()
			case <-ctx.Done():
			}
		}()

		if *ttyFlag {
			h := tty.NewTTYHost(tty.TTYHostConfig{
				SessionID: *sessionIDFlag,
				Command:   command,
				Rows:      *rowsFlag,
				Cols:      *colsFlag,
				Tags:      tags,
				Events:    events,
			})
			defer h.Close()

			srv, err := ServeUnix(*unixSocketFlag, h, h)
			if err != nil {
				return err
			}
			defer srv.Close()

			return h.RunTask(ctx)
		}

		h := NewHost(HostConfig{
			SessionID: *sessionIDFlag,
			Command:   command,
			Protocol:  protocol.Protocol(*protocolFlag),
			Tags:      tags,
			Events:    events,
		})

		srv, err := ServeUnix(*unixSocketFlag, h, nil)
		if err != nil {
			return err
		}
		defer srv.Close()

		return h.RunTask(ctx)
	}

	// Systemd mode: use journald for events
	events, err := journald.Open()
	if err != nil {
		return fmt.Errorf("opening event log: %w", err)
	}
	defer events.Close()

	// Use TTYHost for --tty mode, otherwise use regular Host
	if *ttyFlag {
		host := tty.NewTTYHost(tty.TTYHostConfig{
			SessionID: *sessionIDFlag,
			Command:   command,
			Rows:      *rowsFlag,
			Cols:      *colsFlag,
			Tags:      tags,
			Events:    events,
		})
		defer host.Close()
		return host.Run()
	}

	host := NewHost(HostConfig{
		SessionID: *sessionIDFlag,
		Command:   command,
		Protocol:  protocol.Protocol(*protocolFlag),
		Tags:      tags,
		Events:    events,
	})

	return host.Run()
}
