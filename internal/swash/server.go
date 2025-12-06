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
	"github.com/godbus/dbus/v5/introspect"
)

// Server state
var (
	serverSessionID string
	serverCommand   []string
	serverProtocol  Protocol
	serverTags      map[string]string
	serverStdin     io.WriteCloser
	serverMutex     sync.Mutex
	serverRunning   bool
	serverExitCode  *int
)

// SwashService implements the D-Bus interface.
type SwashService struct {
	sessionID string
}

// Gist returns session status as JSON.
func (s *SwashService) Gist() (string, *dbus.Error) {
	serverMutex.Lock()
	defer serverMutex.Unlock()

	gist := map[string]any{
		"running":   serverRunning,
		"exit_code": serverExitCode,
		"command":   serverCommand,
	}
	b, _ := json.Marshal(gist)
	return string(b), nil
}

// SessionID returns the session ID.
func (s *SwashService) SessionID() (string, *dbus.Error) {
	return s.sessionID, nil
}

// SendInput sends input to the process stdin.
func (s *SwashService) SendInput(data string) (string, *dbus.Error) {
	serverMutex.Lock()
	stdin := serverStdin
	running := serverRunning
	serverMutex.Unlock()

	if !running || stdin == nil {
		return `{"error":"no process"}`, nil
	}

	n, err := stdin.Write([]byte(data))
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error()), nil
	}
	return fmt.Sprintf(`{"sent":%d}`, n), nil
}

// Kill kills the process.
func (s *SwashService) Kill() (string, *dbus.Error) {
	ctx := context.Background()
	sd, err := ConnectUserSystemd(ctx)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error()), nil
	}
	defer sd.Close()

	err = sd.KillUnit(ctx, TaskUnit(serverSessionID), syscall.SIGKILL)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error()), nil
	}
	return `{"killed":true}`, nil
}

// RunServer runs the D-Bus server for a session.
// Called as: swash host --session ID --command-json [...] [--protocol shell|sse] [--tags-json {...}]
func RunServer() error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	sessionIDFlag := fs.String("session", "", "Session ID")
	commandJSONFlag := fs.String("command-json", "", "Command as JSON array")
	protocolFlag := fs.String("protocol", "shell", "Protocol: shell, sse")
	tagsJSONFlag := fs.String("tags-json", "", "Extra journal fields as JSON object")
	// Skip "swash" (index 0) and "host" (index 1) to get to the flags
	fs.Parse(os.Args[2:])

	if *sessionIDFlag == "" || *commandJSONFlag == "" {
		return fmt.Errorf("missing required flags")
	}

	serverSessionID = *sessionIDFlag
	serverProtocol = Protocol(*protocolFlag)

	// Parse tags
	serverTags = make(map[string]string)
	if *tagsJSONFlag != "" {
		if err := json.Unmarshal([]byte(*tagsJSONFlag), &serverTags); err != nil {
			return fmt.Errorf("parsing tags: %w", err)
		}
	}

	if err := json.Unmarshal([]byte(*commandJSONFlag), &serverCommand); err != nil {
		return fmt.Errorf("parsing command: %w", err)
	}

	if isatty(os.Stdin.Fd()) {
		return fmt.Errorf("must be launched via systemd (stdin is a tty)")
	}

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to D-Bus: %w", err)
	}
	defer conn.Close()

	busName := fmt.Sprintf("%s.%s", DBusNamePrefix, serverSessionID)
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("requesting bus name: %w", err)
	}

	service := &SwashService{sessionID: serverSessionID}
	conn.Export(service, dbus.ObjectPath(DBusPath), DBusNamePrefix)

	node := &introspect.Node{
		Name: DBusPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			{
				Name: DBusNamePrefix,
				Methods: []introspect.Method{
					{Name: "Gist", Args: []introspect.Arg{{Direction: "out", Type: "s"}}},
					{Name: "SessionID", Args: []introspect.Arg{{Direction: "out", Type: "s"}}},
					{Name: "SendInput", Args: []introspect.Arg{{Direction: "in", Type: "s"}, {Direction: "out", Type: "s"}}},
					{Name: "Kill", Args: []introspect.Arg{{Direction: "out", Type: "s"}}},
				},
			},
		},
	}
	conn.Export(introspect.NewIntrospectable(node), dbus.ObjectPath(DBusPath), "org.freedesktop.DBus.Introspectable")

	doneChan, err := startTaskProcess()
	if err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	// Emit lifecycle event
	if err := EmitStarted(serverSessionID, serverCommand); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to emit started event: %v\n", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	select {
	case <-doneChan:
		// Process exited
	case sig := <-sigChan:
		fmt.Fprintf(os.Stderr, "Received %v, killing task\n", sig)
		ctx := context.Background()
		if sd, err := ConnectUserSystemd(ctx); err == nil {
			sd.KillUnit(ctx, TaskUnit(serverSessionID), syscall.SIGKILL)
			sd.Close()
		}
		<-doneChan
	}

	return nil
}

// startTaskProcess starts the task subprocess via systemd D-Bus API.
func startTaskProcess() (chan struct{}, error) {
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
		Unit:        TaskUnit(serverSessionID),
		Slice:       SessionSlice(serverSessionID), // same slice as host unit
		ServiceType: "exec",
		WorkingDir:  cwd,
		Description: strings.Join(serverCommand, " "),
		Environment: env,
		Command:     serverCommand,
		Collect:     true,
		Stdin:       &stdinFd,
		Stdout:      &stdoutFd,
		Stderr:      &stderrFd,
	}

	sd, err := ConnectUserSystemd(ctx)
	if err != nil {
		return nil, err
	}

	if err := sd.StartTransient(ctx, spec); err != nil {
		sd.Close()
		return nil, err
	}
	sd.Close()

	// Close the unit-facing ends of the pipes (they're now owned by systemd)
	stdinRead.Close()
	stdoutWrite.Close()
	stderrWrite.Close()

	// Store stdin for SendInput
	serverMutex.Lock()
	serverStdin = stdinWrite
	serverRunning = true
	serverMutex.Unlock()

	doneChan := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)

	// Output handler that writes to journal with tags
	outputHandler := func(fd int, text string, fields map[string]string) {
		WriteOutputWithFields(fd, text, fields)
	}

	// Read stdout and write to journal (protocol-aware)
	go func() {
		defer wg.Done()
		reader := NewProtocolReader(serverProtocol, 1, outputHandler, serverTags)
		reader.Process(stdoutRead)
		stdoutRead.Close()
	}()

	// Read stderr and write to journal (always line-oriented)
	go func() {
		defer wg.Done()
		reader := NewProtocolReader(ProtocolShell, 2, outputHandler, serverTags)
		reader.Process(stderrRead)
		stderrRead.Close()
	}()

	// Wait for pipes to close (unit exited) and get exit status
	go func() {
		wg.Wait()

		// Get exit status from unit
		var exitCode int
		ctx := context.Background()
		sd, err := ConnectUserSystemd(ctx)
		if err == nil {
			unit, err := sd.GetUnit(ctx, TaskUnit(serverSessionID))
			if err == nil {
				exitCode = int(unit.ExitStatus)
				serverMutex.Lock()
				serverExitCode = &exitCode
				serverMutex.Unlock()
			}
			sd.Close()
		}

		// Emit lifecycle event
		if err := EmitExited(serverSessionID, exitCode, serverCommand); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to emit exited event: %v\n", err)
		}

		serverMutex.Lock()
		serverRunning = false
		if serverStdin != nil {
			serverStdin.Close()
		}
		serverStdin = nil
		serverMutex.Unlock()

		close(doneChan)
	}()

	return doneChan, nil
}

func isatty(fd uintptr) bool {
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, 0)
	return err == 0
}
