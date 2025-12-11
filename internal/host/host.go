package host

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/godbus/dbus/v5"
	"github.com/mbrock/swash/internal/eventlog"
	journald "github.com/mbrock/swash/internal/platform/systemd/eventlog"
	systemdproc "github.com/mbrock/swash/internal/platform/systemd/process"
	"github.com/mbrock/swash/internal/process"
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

	processes process.ProcessBackend
	events    eventlog.EventLog

	mu       sync.Mutex
	stdin    io.WriteCloser
	running  bool
	exitCode *int
}

// HostConfig holds the configuration for creating a Host.
type HostConfig struct {
	SessionID string
	Command   []string
	Protocol  protocol.Protocol
	Tags      map[string]string
	Processes process.ProcessBackend
	Events    eventlog.EventLog
}

// NewHost creates a new Host with the given configuration.
func NewHost(cfg HostConfig) *Host {
	// Merge session ID into tags so output lines can be filtered
	tags := make(map[string]string)
	for k, v := range cfg.Tags {
		tags[k] = v
	}
	tags[eventlog.FieldSession] = cfg.SessionID

	return &Host{
		sessionID: cfg.SessionID,
		command:   cfg.Command,
		protocol:  cfg.Protocol,
		tags:      tags,
		processes: cfg.Processes,
		events:    cfg.Events,
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
func (h *Host) Kill() error {
	ctx := context.Background()
	return h.processes.Kill(ctx, process.TaskProcess(h.sessionID), syscall.SIGKILL)
}

// Run starts the D-Bus host and runs until the task exits or a signal is received.
func (h *Host) Run() error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to D-Bus: %w", err)
	}
	defer conn.Close()

	busName := fmt.Sprintf("%s.%s", session.DBusNamePrefix, h.sessionID)
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("requesting bus name: %w", err)
	}

	conn.ExportAll(h, dbus.ObjectPath(session.DBusPath), session.DBusNamePrefix)

	// Set up context that cancels on SIGTERM/SIGINT
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case sig := <-sigChan:
			fmt.Fprintf(os.Stderr, "Received %v, killing task\n", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	err = h.RunTask(ctx)

	// Emit exit signal before closing D-Bus connection
	h.mu.Lock()
	exitCode := h.exitCode
	h.mu.Unlock()
	if exitCode != nil {
		conn.Emit(dbus.ObjectPath(session.DBusPath), session.DBusNamePrefix+".Exited", int32(*exitCode))
	}

	return err
}

// RunTask starts the task process and waits for it to complete.
// This is the core logic without D-Bus setup or signal handling,
// suitable for testing.
func (h *Host) RunTask(ctx context.Context) error {
	doneChan, err := h.startTaskProcess()
	if err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	// Emit lifecycle event
	if err := eventlog.EmitStarted(h.events, h.sessionID, h.command); err != nil {
		return fmt.Errorf("emitting started event: %w", err)
	}

	select {
	case <-doneChan:
		return nil
	case <-ctx.Done():
		h.processes.Kill(context.Background(), process.TaskProcess(h.sessionID), syscall.SIGKILL)
		<-doneChan
		return ctx.Err()
	}
}

// startTaskProcess starts the task subprocess via the process backend.
func (srv *Host) startTaskProcess() (chan struct{}, error) {
	ctx := context.Background()

	// Subscribe to exit notifications before starting the task
	taskRef := process.TaskProcess(srv.sessionID)
	exitCh, err := srv.processes.SubscribeExit(ctx, taskRef)
	if err != nil {
		return nil, fmt.Errorf("subscribing to exit notifications: %w", err)
	}

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

	// Build environment (excluding underscore-prefixed vars)
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "_") {
			continue
		}
		if idx := strings.Index(e, "="); idx > 0 {
			env[e[:idx]] = e[idx+1:]
		}
	}

	cwd, _ := os.Getwd()

	// Get file descriptor numbers
	stdinFd := int(stdinRead.Fd())
	stdoutFd := int(stdoutWrite.Fd())
	stderrFd := int(stderrWrite.Fd())

	// Get self executable for ExecStopPost
	selfExe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("getting executable path: %w", err)
	}

	hostRef := process.HostProcess(srv.sessionID)

	spec := process.ProcessSpec{
		Ref:         taskRef,
		WorkingDir:  cwd,
		Description: strings.Join(srv.command, " "),
		Environment: env,
		Command:     srv.command,
		Collect:     false, // Keep workload around long enough to query exit status
		IO: process.IODescriptor{
			Stdin:  &stdinFd,
			Stdout: &stdoutFd,
			Stderr: &stderrFd,
		},
		Dependencies: []process.ProcessRef{hostRef},
		// Notify via EmitUnitExit when task exits (args passed directly, not via env)
		PostStop: [][]string{
			{selfExe, "notify-exit", srv.sessionID, "$EXIT_STATUS", "$SERVICE_RESULT"},
		},
		LaunchKind: process.LaunchKindExec,
	}

	if err := srv.processes.Start(ctx, spec); err != nil {
		return nil, err
	}

	// Close the task-facing ends of the pipes (owned by the backend now)
	stdinRead.Close()
	stdoutWrite.Close()
	stderrWrite.Close()

	// Store stdin for SendInput
	srv.mu.Lock()
	srv.stdin = stdinWrite
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

	// Wait for exit notification from SubscribeUnitExit
	go func() {
		// Wait for exit notification
		notification := <-exitCh

		// Wait for pipes to ensure all output is captured
		wg.Wait()

		srv.mu.Lock()
		srv.exitCode = &notification.ExitCode
		srv.mu.Unlock()

		// Emit lifecycle event
		if err := eventlog.EmitExited(srv.events, srv.sessionID, notification.ExitCode, srv.command); err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to emit exited event: %v\n", err)
		}

		srv.mu.Lock()
		srv.running = false
		if srv.stdin != nil {
			srv.stdin.Close()
		}
		srv.stdin = nil
		srv.mu.Unlock()

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

	ctx := context.Background()

	systemd, err := systemdproc.ConnectUserSystemd(ctx)
	if err != nil {
		return fmt.Errorf("connecting to systemd: %w", err)
	}
	processes := systemdproc.NewSystemdBackend(systemd)
	defer processes.Close()

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
			Processes: processes,
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
		Processes: processes,
		Events:    events,
	})

	return host.Run()
}
