// swash - Interactive process sessions over D-Bus
//
// Usage:
//
//	swash                              Show running sessions
//	swash run <command>                Run command in new session
//	swash stop <session_id>            Stop session
//	swash poll <session_id>            Get recent output
//	swash follow <session_id>          Follow output until exit
//	swash send <session_id> <input>    Send input to process
//	swash kill <session_id>            Kill process
//	swash screen <session_id>          Show TTY session screen
//	swash attach <session_id>          Attach to TTY session interactively
//	swash history                      Show session history
//	swash host                         (internal) Run as task host
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mbrock/swash/internal/swash"
	flag "github.com/spf13/pflag"
	"golang.org/x/term"
)

// Global flags
var (
	protocolFlag    string
	tagFlags        []string
	ttyFlag         bool
	rowsFlag        int
	colsFlag        int
	detachAfterFlag time.Duration
)

// Global runtime (initialized for commands that need it)
var rt *swash.Runtime

func main() {
	// Handle "host" subcommand before flag parsing, since it has its own flags
	// that pflag doesn't know about (--session, --command-json)
	if len(os.Args) >= 2 && os.Args[1] == "host" {
		cmdHost()
		return
	}

	// Register flags
	flag.StringVarP(&protocolFlag, "protocol", "p", "shell", "Protocol: shell, sse")
	flag.StringArrayVarP(&tagFlags, "tag", "t", nil, "Add journal field KEY=VALUE (can be repeated)")
	flag.BoolVar(&ttyFlag, "tty", false, "Use PTY mode with terminal emulation")
	flag.IntVar(&rowsFlag, "rows", 24, "Terminal rows (for --tty mode)")
	flag.IntVar(&colsFlag, "cols", 80, "Terminal columns (for --tty mode)")
	flag.DurationVarP(&detachAfterFlag, "detach-after", "d", 3*time.Second, "Detach after duration (0 = immediate)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `swash - Interactive process sessions over D-Bus

Usage:
  swash                              Show running sessions
  swash run [flags] -- <command>     Run command, wait for completion or detach
  swash start [flags] -- <command>   Start command in background (same as run -d0)
  swash stop <session_id>            Stop session
  swash poll <session_id>            Get recent output
  swash follow <session_id>          Follow output until exit
  swash send <session_id> <input>    Send input to process
  swash kill <session_id>            Kill process
  swash screen <session_id>          Show TTY session screen
  swash attach <session_id>          Attach to TTY session interactively
  swash history                      Show session history
  swash host                         (internal) Run as task host

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		cmdStatus()
		return
	}

	cmd := args[0]
	cmdArgs := args[1:]

	switch cmd {
	case "run":
		if len(cmdArgs) == 0 {
			fatal("usage: swash run <command>")
		}
		if ttyFlag {
			fatal("--tty is not supported with 'run'; use 'start --tty' instead")
		}
		cmdRun(cmdArgs, detachAfterFlag)
	case "start":
		if len(cmdArgs) == 0 {
			fatal("usage: swash start <command>")
		}
		cmdRun(cmdArgs, 0) // immediate detach
	case "stop":
		if len(cmdArgs) == 0 {
			fatal("usage: swash stop <session_id>")
		}
		cmdStop(cmdArgs[0])
	case "poll":
		if len(cmdArgs) == 0 {
			fatal("usage: swash poll <session_id>")
		}
		cmdPoll(cmdArgs[0])
	case "follow":
		if len(cmdArgs) == 0 {
			fatal("usage: swash follow <session_id>")
		}
		cmdFollow(cmdArgs[0])
	case "send":
		if len(cmdArgs) < 2 {
			fatal("usage: swash send <session_id> <input>")
		}
		cmdSend(cmdArgs[0], cmdArgs[1])
	case "kill":
		if len(cmdArgs) == 0 {
			fatal("usage: swash kill <session_id>")
		}
		cmdKill(cmdArgs[0])
	case "history":
		cmdHistory()
	case "screen":
		if len(cmdArgs) == 0 {
			fatal("usage: swash screen <session_id>")
		}
		cmdScreen(cmdArgs[0])
	case "attach":
		if len(cmdArgs) == 0 {
			fatal("usage: swash attach <session_id>")
		}
		cmdAttach(cmdArgs[0])
	case "host":
		cmdHost()
	default:
		fatal("unknown command: %s", cmd)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

// initRuntime initializes the global runtime. Call this before using rt.
func initRuntime() {
	var err error
	rt, err = swash.DefaultRuntime(context.Background())
	if err != nil {
		fatal("initializing runtime: %v", err)
	}
}

// formatAge converts a systemd timestamp to a relative age like "2m", "1h", "3d"
func formatAge(timestamp string) string {
	if timestamp == "" {
		return "-"
	}
	// Parse systemd timestamp format: "Fri 2025-12-05 22:22:37 EET"
	t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", timestamp)
	if err != nil {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// truncate shortens a string to maxLen, adding "..." if truncated
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// findHostCommand returns the command to run the task host (this binary with "host" subcommand).
func findHostCommand() []string {
	self, err := os.Executable()
	if err != nil {
		fatal("cannot find own executable: %v", err)
	}
	return []string{self, "host"}
}

// cmdHost runs as the task host (D-Bus server for a session).
// This is launched by systemd, not by users directly.
func cmdHost() {
	if err := swash.RunHost(); err != nil {
		fatal("%v", err)
	}
}

func cmdStatus() {
	initRuntime()
	defer rt.Close()

	sessions, err := rt.ListSessions(context.Background())
	if err != nil {
		fatal("listing sessions: %v", err)
	}

	if len(sessions) == 0 {
		fmt.Println("no sessions")
		fmt.Println("swash run <command>")
		return
	}

	// Print header
	fmt.Printf("%-8s %-8s %-8s %-8s %s\n", "ID", "STATUS", "AGE", "PID", "COMMAND")

	for _, s := range sessions {
		age := formatAge(s.Started)
		cmd := truncate(s.Command, 50)
		fmt.Printf("%-8s %-8s %-8s %-8d %s\n", s.ID, s.Status, age, s.PID, cmd)
	}
}

func cmdRun(command []string, detachAfter time.Duration) {
	initRuntime()
	defer rt.Close()

	hostCommand := findHostCommand()

	// Build session options from flags
	opts := swash.SessionOptions{
		Protocol: swash.Protocol(protocolFlag),
		Tags:     parseTags(tagFlags),
		TTY:      ttyFlag,
		Rows:     rowsFlag,
		Cols:     colsFlag,
	}

	sessionID, err := rt.StartSessionWithOptions(context.Background(), command, hostCommand, opts)
	if err != nil {
		fatal("starting session: %v", err)
	}

	// Immediate detach mode (detachAfter == 0)
	if detachAfter == 0 {
		fmt.Printf("%s started\n", sessionID)
		return
	}

	// Set up signal handling to kill the process on Ctrl+C
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		// User pressed Ctrl+C - kill the background process
		if err := rt.KillSession(sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "swash: failed to kill session: %v\n", err)
		}
		cancel()
	}()

	// Follow with timeout
	exitCode, result := rt.FollowSession(ctx, sessionID, detachAfter)

	switch result {
	case swash.FollowCompleted:
		os.Exit(exitCode)
	case swash.FollowTimedOut:
		fmt.Fprintf(os.Stderr, "swash: still running after %s, detaching\n", detachAfter)
		fmt.Fprintf(os.Stderr, "swash: session ID: %s\n", sessionID)
		fmt.Fprintf(os.Stderr, "swash: swash follow %s\n", sessionID)
		os.Exit(1)
	case swash.FollowCancelled:
		fmt.Fprintf(os.Stderr, "swash: cancelled, killed session %s\n", sessionID)
		os.Exit(130) // Standard exit code for SIGINT
	}
}

// parseTags converts ["KEY=VALUE", ...] to map[string]string
func parseTags(tags []string) map[string]string {
	result := make(map[string]string)
	for _, tag := range tags {
		if idx := strings.Index(tag, "="); idx > 0 {
			result[tag[:idx]] = tag[idx+1:]
		}
	}
	return result
}

func cmdStop(sessionID string) {
	initRuntime()
	defer rt.Close()

	if err := rt.StopSession(context.Background(), sessionID); err != nil {
		fatal("stopping session: %v", err)
	}
	fmt.Printf("%s stopped\n", sessionID)
}

func cmdPoll(sessionID string) {
	initRuntime()
	defer rt.Close()

	events, _, err := rt.PollSessionOutput(sessionID, "")
	if err != nil {
		fatal("polling journal: %v", err)
	}
	for _, e := range events {
		fmt.Println(e.Text)
	}
}

func cmdFollow(sessionID string) {
	initRuntime()
	defer rt.Close()

	exitCode, result := rt.FollowSession(context.Background(), sessionID, 0)
	if result == swash.FollowCancelled {
		os.Exit(130)
	}
	os.Exit(exitCode)
}

func cmdSend(sessionID, input string) {
	initRuntime()
	defer rt.Close()

	if _, err := rt.SendInput(sessionID, input); err != nil {
		fatal("sending input: %v", err)
	}
}

func cmdKill(sessionID string) {
	initRuntime()
	defer rt.Close()

	if err := rt.KillSession(sessionID); err != nil {
		fatal("killing: %v", err)
	}
	fmt.Printf("%s killed\n", sessionID)
}

func cmdScreen(sessionID string) {
	client, err := swash.ConnectTTYSession(sessionID)
	if err != nil {
		fatal("connecting to session: %v", err)
	}
	defer client.Close()

	screen, err := client.GetScreenANSI()
	if err != nil {
		fatal("getting screen: %v", err)
	}

	fmt.Print(screen)
}

func cmdHistory() {
	initRuntime()
	defer rt.Close()

	sessions, err := rt.ListHistory(context.Background())
	if err != nil {
		fatal("listing history: %v", err)
	}

	if len(sessions) == 0 {
		fmt.Println("no history")
		return
	}

	// Print header
	fmt.Printf("%-8s %-8s %-6s %-8s %s\n", "ID", "STATUS", "EXIT", "AGE", "COMMAND")

	for _, s := range sessions {
		age := formatAge(s.Started)
		exitStr := "-"
		if s.ExitCode != nil {
			exitStr = fmt.Sprintf("%d", *s.ExitCode)
		}
		cmd := truncate(s.Command, 50)
		fmt.Printf("%-8s %-8s %-6s %-8s %s\n", s.ID, s.Status, exitStr, age, cmd)
	}
}

func cmdAttach(sessionID string) {
	client, err := swash.ConnectTTYSession(sessionID)
	if err != nil {
		fatal("connecting to session: %v", err)
	}
	defer client.Close()

	// Get terminal size
	stdinFd := int(os.Stdin.Fd())
	var clientRows, clientCols int32 = 24, 80 // defaults
	if term.IsTerminal(stdinFd) {
		width, height, err := term.GetSize(stdinFd)
		if err == nil {
			clientRows = int32(height)
			clientCols = int32(width)
		}
	}

	// Call AttachWithSize to get fds and screen snapshot
	outputFD, inputFD, _, _, screenANSI, err := client.AttachWithSize(clientRows, clientCols)
	if err != nil {
		fatal("attaching to session: %v", err)
	}

	// Convert fds to *os.File
	output := os.NewFile(uintptr(outputFD), "attach-output")
	input := os.NewFile(uintptr(inputFD), "attach-input")
	defer output.Close()
	defer input.Close()

	// Put terminal in raw mode (if we're connected to a terminal)
	var oldState *term.State
	if term.IsTerminal(stdinFd) {
		oldState, err = term.MakeRaw(stdinFd)
		if err != nil {
			fatal("setting raw mode: %v", err)
		}
		defer term.Restore(stdinFd, oldState)
	}

	// Switch to alternate screen buffer (like vim does) and show remote screen
	fmt.Print("\x1b[?1049h")   // Enter alternate screen buffer
	fmt.Print("\x1b[2J\x1b[H") // Clear screen, move to top-left
	fmt.Print(screenANSI)

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Channels to signal exit
	done := make(chan struct{})
	detach := make(chan struct{})

	// Forward PTY output to stdout
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := output.Read(buf)
			if err != nil {
				close(done)
				return
			}
			os.Stdout.Write(buf[:n])
		}
	}()

	// Forward stdin to PTY input (with Ctrl+\ detection for detach)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				if buf[0] == 0x1c { // Ctrl+\ to detach
					close(detach)
					return
				}
				input.Write(buf[:n])
			}
		}
	}()

	select {
	case <-done:
	case <-detach:
	case <-sigCh:
	}

	// Leave alternate screen buffer (restores original screen content)
	fmt.Print("\x1b[?1049l")

	// Restore terminal and print exit message
	if oldState != nil {
		term.Restore(stdinFd, oldState)
	}
	fmt.Println("[detached]")
}
