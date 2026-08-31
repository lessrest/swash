package host

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"swa.sh/internal/journal"
	"swa.sh/internal/protocol"
	"swa.sh/systemd/daemon"
)

const taskTerminationGrace = 2 * time.Second

// Host is the D-Bus host for a swash session.
type Host struct {
	sessionID string
	command   []string
	protocol  protocol.Protocol
	tags      map[string]string

	events   journal.EventLog
	executor Executor

	mu        sync.Mutex
	proc      Process // the running task process
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
	Events    journal.EventLog
	Executor  Executor // Optional; defaults to ExecExecutor if nil
}

// NewHost creates a new Host with the given configuration.
func NewHost(cfg HostConfig) *Host {
	// Merge session ID into tags so output lines can be filtered
	tags := make(map[string]string)
	maps.Copy(tags, cfg.Tags)
	tags[journal.FieldSession] = cfg.SessionID

	execImpl := cfg.Executor
	if execImpl == nil {
		execImpl = Default()
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

// Kill sends SIGKILL to the task process session.
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

// GracefulKill sends SIGTERM to the task process session. The caller decides
// how long to wait before escalating, allowing cooperative tasks to flush.
func (h *Host) GracefulKill() {
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
}

func (h *Host) terminateTask(doneChan <-chan struct{}) {
	terminateTask(h.sessionID, doneChan, h.GracefulKill, h.Kill, h.closePipes)
}

func terminateTask(sessionID string, doneChan <-chan struct{}, graceful func(), force func() error, closeIO func()) {
	graceful()
	select {
	case <-doneChan:
		return
	case <-time.After(taskTerminationGrace):
		slog.Warn("task ignored SIGTERM; sending SIGKILL to process session",
			"session", sessionID)
		_ = force()
		// A deliberately escaped descendant may still hold inherited I/O.
		closeIO()
		<-doneChan
	}
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

	busName := fmt.Sprintf("%s.%s", DBusNamePrefix, h.sessionID)
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		slog.Debug("Host.Run bus name request failed", "busName", busName, "error", err)
		return fmt.Errorf("requesting bus name: %w", err)
	}
	slog.Debug("Host.Run acquired bus name", "busName", busName)

	conn.ExportAll(h, dbus.ObjectPath(DBusPath), DBusNamePrefix)

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
		conn.Emit(dbus.ObjectPath(DBusPath), DBusNamePrefix+".Exited", int32(*exitCode))
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
		doneChan, eventErrors, err := h.startTaskProcess()
		if err != nil {
			slog.Debug("Host.RunTask failed to start process", "session", h.sessionID, "error", err)
			return fmt.Errorf("starting process: %w", err)
		}

		h.mu.Lock()
		h.doneCh = doneChan
		h.mu.Unlock()

		// Emit lifecycle event
		if err := journal.EmitStarted(h.events, h.sessionID, h.command, h.tags); err != nil {
			h.terminateTask(doneChan)
			return fmt.Errorf("emitting started event: %w", err)
		}
		if _, err := daemon.SdNotify(true, daemon.SdNotifyReady); err != nil {
			h.terminateTask(doneChan)
			return fmt.Errorf("notifying systemd of readiness: %w", err)
		}

		slog.Debug("Host.RunTask waiting for task", "session", h.sessionID)
		select {
		case <-doneChan:
			// Task exited normally
			slog.Debug("Host.RunTask task exited normally", "session", h.sessionID)
			select {
			case err := <-eventErrors:
				return err
			default:
			}
			return nil
		case err := <-eventErrors:
			h.terminateTask(doneChan)
			return err
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
			h.terminateTask(doneChan)
			slog.Debug("Host.RunTask task tree terminated", "session", h.sessionID)
			return ctx.Err()
		}
	}
}

// startTaskProcess starts the task subprocess via the
func (srv *Host) startTaskProcess() (chan struct{}, <-chan error, error) {
	slog.Debug("Host.startTaskProcess", "session", srv.sessionID, "command", srv.command)

	// Create pipes for stdio
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		stdinRead.Close()
		stdinWrite.Close()
		return nil, nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		stdinRead.Close()
		stdinWrite.Close()
		stdoutRead.Close()
		stdoutWrite.Close()
		return nil, nil, fmt.Errorf("creating stderr pipe: %w", err)
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
		return nil, nil, fmt.Errorf("starting process: %w", err)
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
	eventErrors := make(chan error, 1)
	reportEventError := func(err error) {
		select {
		case eventErrors <- err:
		default:
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Output handler that writes to journal with tags
	outputHandler := func(fd int, text string, fields map[string]string) {
		if err := journal.WriteOutput(srv.events, fd, text, fields); err != nil {
			reportEventError(fmt.Errorf("persisting process output: %w", err))
		}
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
		if err := journal.EmitExited(srv.events, srv.sessionID, exitCode, srv.command, srv.tags); err != nil {
			reportEventError(fmt.Errorf("emitting exited event: %w", err))
		}

		close(doneChan)
	}()

	return doneChan, eventErrors, nil
}

// RunHost is the entrypoint for the "swash host" command.
// It parses flags, creates real implementations, and runs the server.
func RunHost() (int, error) {
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
		return 0, fmt.Errorf("missing required flags")
	}

	var command []string
	if err := json.Unmarshal([]byte(*commandJSONFlag), &command); err != nil {
		return 0, fmt.Errorf("parsing command: %w", err)
	}

	tags := make(map[string]string)
	if *tagsJSONFlag != "" {
		if err := json.Unmarshal([]byte(*tagsJSONFlag), &tags); err != nil {
			return 0, fmt.Errorf("parsing tags: %w", err)
		}
	}

	// POSIX (unix socket) mode: run without systemd/journald/D-Bus.
	if *unixSocketFlag != "" {
		databasePath := os.Getenv("SWASH_EVENT_DB")
		if databasePath == "" {
			return 0, fmt.Errorf("missing SWASH_EVENT_DB for --unix-socket mode")
		}
		events, err := journal.OpenSQLite(databasePath)
		if err != nil {
			return 0, fmt.Errorf("opening event database: %w", err)
		}
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
			h, err := NewTTYHost(TTYHostConfig{
				SessionID: *sessionIDFlag,
				Command:   command,
				Rows:      *rowsFlag,
				Cols:      *colsFlag,
				Tags:      tags,
				Events:    events,
			})
			if err != nil {
				return 0, fmt.Errorf("NewTTYHost: %w", err)
			}
			defer h.Close()

			srv, err := ServeUnix(*unixSocketFlag, h, h)
			if err != nil {
				return 0, err
			}
			defer srv.Close()

			return completedTaskResult(h, h.RunTask(ctx))
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
			return 0, err
		}
		defer srv.Close()

		return completedTaskResult(h, h.RunTask(ctx))
	}

	// Systemd mode: use journald for events
	events, err := journal.OpenSystemd()
	if err != nil {
		return 0, fmt.Errorf("opening event log: %w", err)
	}
	defer events.Close()

	// Use TTYHost for --tty mode, otherwise use regular Host
	if *ttyFlag {
		host, err := NewTTYHost(TTYHostConfig{
			SessionID: *sessionIDFlag,
			Command:   command,
			Rows:      *rowsFlag,
			Cols:      *colsFlag,
			Tags:      tags,
			Events:    events,
		})
		if err != nil {
			return 0, fmt.Errorf("NewTTYHost: %w", err)
		}
		defer host.Close()
		return completedTaskResult(host, host.Run())
	}

	host := NewHost(HostConfig{
		SessionID: *sessionIDFlag,
		Command:   command,
		Protocol:  protocol.Protocol(*protocolFlag),
		Tags:      tags,
		Events:    events,
	})

	return completedTaskResult(host, host.Run())
}

type taskStatus interface {
	Gist() (HostStatus, error)
}

func completedTaskResult(task taskStatus, runErr error) (int, error) {
	if errors.Is(runErr, context.Canceled) {
		// The service manager asked the host to stop. The task's normalized
		// result is already in the lifecycle event; stopping the host succeeded.
		return 0, nil
	}
	if runErr != nil {
		return 0, runErr
	}
	status, err := task.Gist()
	if err != nil {
		return 0, err
	}
	if status.ExitCode == nil {
		return 0, fmt.Errorf("task exited without a status")
	}
	return *status.ExitCode, nil
}
