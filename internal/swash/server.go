package swash

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	serverProcess   *exec.Cmd
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
	serverMutex.Lock()
	proc := serverProcess
	serverMutex.Unlock()

	if proc != nil && proc.Process != nil {
		proc.Process.Kill()
		return `{"killed":true}`, nil
	}
	return `{"error":"no process"}`, nil
}

// RunServer runs the D-Bus server for a session.
func RunServer() error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	sessionIDFlag := fs.String("session", "", "Session ID")
	commandJSONFlag := fs.String("command-json", "", "Command as JSON array")
	fs.Parse(os.Args[1:])

	if *sessionIDFlag == "" || *commandJSONFlag == "" {
		return fmt.Errorf("missing required flags")
	}

	serverSessionID = *sessionIDFlag

	if err := json.Unmarshal([]byte(*commandJSONFlag), &serverCommand); err != nil {
		return fmt.Errorf("parsing command: %w", err)
	}

	if isatty(os.Stdin.Fd()) {
		return fmt.Errorf("must be launched via systemd-run")
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

	doneChan, err := startProcess()
	if err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	select {
	case <-doneChan:
		// Process exited (logging already done in startProcess)
	case sig := <-sigChan:
		fmt.Fprintf(os.Stderr, "Received %v, killing process\n", sig)
		if serverProcess != nil && serverProcess.Process != nil {
			serverProcess.Process.Kill()
		}
		<-doneChan // Wait for process to fully exit
	}

	return nil
}

func startProcess() (chan struct{}, error) {
	cmdUnit := fmt.Sprintf("swash-task-%s.service", serverSessionID)
	cwd, _ := os.Getwd()

	args := []string{
		"systemd-run", "--user",
		"--pipe",
		"--collect",
		"--unit=" + cmdUnit,
		"--slice-inherit",
		"--working-directory=" + cwd,
	}

	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "_") {
			args = append(args, "--setenv="+env)
		}
	}

	args = append(args, "--")
	args = append(args, serverCommand...)

	cmd := exec.Command(args[0], args[1:]...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	serverMutex.Lock()
	serverProcess = cmd
	serverStdin = stdin
	serverRunning = true
	serverMutex.Unlock()

	doneChan := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			JournalOutput(1, scanner.Text())
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			JournalOutput(2, scanner.Text())
		}
	}()

	go func() {
		wg.Wait()
		cmd.Wait()

		var exitCode int
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}

		serverMutex.Lock()
		serverRunning = false
		serverExitCode = &exitCode
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
