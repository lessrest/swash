package swash

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
)

// Host is the D-Bus host for a swash session.
type Host struct {
	sessionID string
	command   []string
	protocol  Protocol
	tags      map[string]string

	systemd Systemd
	journal Journal

	mu       sync.Mutex
	stdin    io.WriteCloser
	running  bool
	exitCode *int
}

// HostConfig holds the configuration for creating a Host.
type HostConfig struct {
	SessionID string
	Command   []string
	Protocol  Protocol
	Tags      map[string]string
	Systemd   Systemd
	Journal   Journal
}

// NewHost creates a new Host with the given configuration.
func NewHost(cfg HostConfig) *Host {
	// Merge session ID into tags so output lines can be filtered
	tags := make(map[string]string)
	for k, v := range cfg.Tags {
		tags[k] = v
	}
	tags[FieldSession] = cfg.SessionID

	return &Host{
		sessionID: cfg.SessionID,
		command:   cfg.Command,
		protocol:  cfg.Protocol,
		tags:      tags,
		systemd:   cfg.Systemd,
		journal:   cfg.Journal,
	}
}

// HostStatus represents the current state of a Host session.
type HostStatus struct {
	Running  bool     `json:"running"`
	ExitCode *int     `json:"exit_code"`
	Command  []string `json:"command"`
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

// Kill sends SIGKILL to the task process.
func (h *Host) Kill() error {
	ctx := context.Background()
	return h.systemd.KillUnit(ctx, TaskUnit(h.sessionID), syscall.SIGKILL)
}

// Run starts the D-Bus host and runs until the task exits or a signal is received.
func (h *Host) Run() error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to D-Bus: %w", err)
	}
	defer conn.Close()

	busName := fmt.Sprintf("%s.%s", DBusNamePrefix, h.sessionID)
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("requesting bus name: %w", err)
	}

	conn.ExportAll(h, dbus.ObjectPath(DBusPath), DBusNamePrefix)

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

	return h.RunTask(ctx)
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
	if err := EmitStarted(h.journal, h.sessionID, h.command); err != nil {
		return fmt.Errorf("emitting started event: %w", err)
	}

	select {
	case <-doneChan:
		return nil
	case <-ctx.Done():
		h.systemd.KillUnit(context.Background(), TaskUnit(h.sessionID), syscall.SIGKILL)
		<-doneChan
		return ctx.Err()
	}
}

// startTaskProcess starts the task subprocess via systemd D-Bus API.
func (srv *Host) startTaskProcess() (chan struct{}, error) {
	ctx := context.Background()

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

	spec := TransientSpec{
		Unit:        TaskUnit(srv.sessionID),
		Slice:       SessionSlice(srv.sessionID),
		ServiceType: "exec",
		WorkingDir:  cwd,
		Description: strings.Join(srv.command, " "),
		Environment: env,
		Command:     srv.command,
		Collect:     false, // Keep unit around long enough to query exit status
		Stdin:       &stdinFd,
		Stdout:      &stdoutFd,
		Stderr:      &stderrFd,
	}

	if err := srv.systemd.StartTransient(ctx, spec); err != nil {
		return nil, err
	}

	// Close the unit-facing ends of the pipes (they're now owned by systemd)
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
		WriteOutput(srv.journal, fd, text, fields)
	}

	// Read stdout and write to journal (protocol-aware)
	go func() {
		defer wg.Done()
		reader := NewProtocolReader(srv.protocol, 1, outputHandler, srv.tags)
		reader.Process(stdoutRead)
		stdoutRead.Close()
	}()

	// Read stderr and write to journal (always line-oriented)
	go func() {
		defer wg.Done()
		reader := NewProtocolReader(ProtocolShell, 2, outputHandler, srv.tags)
		reader.Process(stderrRead)
		stderrRead.Close()
	}()

	// Watch for unit exit via D-Bus
	go func() {
		// Wait for systemd unit to exit
		exitCode, err := srv.systemd.WaitUnitExit(ctx, TaskUnit(srv.sessionID))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to wait for unit exit: %v\n", err)
			os.Exit(1)
		}

		// Wait for pipes to ensure all output is captured
		wg.Wait()

		srv.mu.Lock()
		srv.exitCode = &exitCode
		srv.mu.Unlock()

		// Emit lifecycle event
		if err := EmitExited(srv.journal, srv.sessionID, exitCode, srv.command); err != nil {
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

	systemd, err := ConnectUserSystemd(ctx)
	if err != nil {
		return fmt.Errorf("connecting to systemd: %w", err)
	}
	defer systemd.Close()

	journal, err := OpenJournal()
	if err != nil {
		return fmt.Errorf("opening journal: %w", err)
	}
	defer journal.Close()

	// Use TTYHost for --tty mode, otherwise use regular Host
	if *ttyFlag {
		host := NewTTYHost(TTYHostConfig{
			SessionID: *sessionIDFlag,
			Command:   command,
			Rows:      *rowsFlag,
			Cols:      *colsFlag,
			Tags:      tags,
			Systemd:   systemd,
			Journal:   journal,
		})
		defer host.Close()
		return host.Run()
	}

	host := NewHost(HostConfig{
		SessionID: *sessionIDFlag,
		Command:   command,
		Protocol:  Protocol(*protocolFlag),
		Tags:      tags,
		Systemd:   systemd,
		Journal:   journal,
	})

	return host.Run()
}
