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

	"github.com/mbrock/swash/internal/backend"
	_ "github.com/mbrock/swash/internal/backend/all"
	"github.com/mbrock/swash/internal/host"
	systemdproc "github.com/mbrock/swash/internal/platform/systemd/process"
	"github.com/mbrock/swash/internal/process"
	"github.com/mbrock/swash/internal/protocol"
	flag "github.com/spf13/pflag"
)

// Global flags
var (
	backendFlag           string
	protocolFlag          string
	tagFlags              []string
	ttyFlag               bool
	rowsFlag              int
	colsFlag              int
	detachAfterFlag       time.Duration
	detachAfterOutputFlag int
)

// Global backend (initialized for commands that need it)
var bk backend.Backend

func main() {
	// Handle "host" subcommand before flag parsing, since it has its own flags
	// that pflag doesn't know about (--session, --command-json)
	if len(os.Args) >= 2 && os.Args[1] == "host" {
		cmdHost()
		return
	}

	// Handle "notify-exit" subcommand (called by systemd ExecStopPost)
	if len(os.Args) >= 2 && os.Args[1] == "notify-exit" {
		cmdNotifyExit()
		return
	}

	// Register flags
	flag.StringVar(&backendFlag, "backend", os.Getenv("SWASH_BACKEND"), "Backend: systemd, posix (overrides SWASH_BACKEND)")
	flag.StringVarP(&protocolFlag, "protocol", "p", "shell", "Protocol: shell, sse")
	flag.StringArrayVarP(&tagFlags, "tag", "t", nil, "Add journal field KEY=VALUE (can be repeated)")
	flag.BoolVar(&ttyFlag, "tty", false, "Use PTY mode with terminal emulation")
	flag.IntVar(&rowsFlag, "rows", 24, "Terminal rows (for --tty mode)")
	flag.IntVar(&colsFlag, "cols", 80, "Terminal columns (for --tty mode)")
	flag.DurationVarP(&detachAfterFlag, "detach-after", "d", 3*time.Second, "Detach after duration (0 = immediate)")
	flag.IntVar(&detachAfterOutputFlag, "detach-after-output", 80*24, "Detach after this many bytes of output (0 = unlimited)")

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
  swash http                         Run HTTP API server
  swash http install [port]          Install HTTP server as systemd socket service
  swash http uninstall               Uninstall HTTP server
  swash http status                  Show HTTP server status
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
			cmdRunTTY(cmdArgs)
		} else {
			cmdRun(cmdArgs, detachAfterFlag, detachAfterOutputFlag)
		}
	case "start":
		if len(cmdArgs) == 0 {
			fatal("usage: swash start <command>")
		}
		cmdRun(cmdArgs, 0, detachAfterOutputFlag) // immediate detach
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
	case "http":
		cmdHTTP(cmdArgs)
	default:
		fatal("unknown command: %s", cmd)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

// initBackend initializes the global backend. Call this before using bk.
func initBackend() {
	var err error
	kind := backend.Kind(backendFlag)
	if kind == "" {
		kind = backend.DetectKind()
	}
	bk, err = backend.Open(context.Background(), backend.Config{
		Kind:        kind,
		HostCommand: findHostCommand(),
	})
	if err != nil {
		fatal("initializing backend: %v", err)
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
	if err := host.RunHost(); err != nil {
		fatal("%v", err)
	}
}

// cmdNotifyExit is called by systemd's ExecStopPost to notify the host of task exit.
// Usage: swash notify-exit <sessionID> <exitStatus> <serviceResult>
// Arguments are passed directly (systemd expands $EXIT_STATUS and $SERVICE_RESULT).
func cmdNotifyExit() {
	if len(os.Args) < 5 {
		fatal("usage: swash notify-exit <sessionID> <exitStatus> <serviceResult>")
	}
	sessionID := os.Args[2]
	exitStatusStr := os.Args[3]
	serviceResult := os.Args[4]

	exitStatus := 0
	if exitStatusStr != "" {
		fmt.Sscanf(exitStatusStr, "%d", &exitStatus)
	}

	// Connect to systemd and emit unit exit notification
	ctx := context.Background()
	systemd, err := systemdproc.ConnectUserSystemd(ctx)
	if err != nil {
		// Don't fatal - systemd may not be available
		fmt.Fprintf(os.Stderr, "swash notify-exit: connecting to systemd: %v\n", err)
		os.Exit(1)
	}
	defer systemd.Close()

	backend := systemdproc.NewSystemdBackend(systemd)

	if err := backend.EmitExit(ctx, process.TaskProcess(sessionID), exitStatus, serviceResult); err != nil {
		fmt.Fprintf(os.Stderr, "swash notify-exit: emitting exit: %v\n", err)
		os.Exit(1)
	}
}

func cmdStatus() {
	initBackend()
	defer bk.Close()

	sessions, err := bk.ListSessions(context.Background())
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

func cmdRun(command []string, detachAfter time.Duration, outputLimit int) {
	initBackend()
	defer bk.Close()

	// Build session options from flags
	opts := backend.SessionOptions{
		Protocol: protocol.Protocol(protocolFlag),
		Tags:     parseTags(tagFlags),
		TTY:      ttyFlag,
		Rows:     rowsFlag,
		Cols:     colsFlag,
	}

	sessionID, err := bk.StartSession(context.Background(), command, opts)
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
		if err := bk.KillSession(context.Background(), sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "swash: failed to kill session: %v\n", err)
		}
		cancel()
	}()

	// Follow with timeout
	exitCode, result := bk.FollowSession(ctx, sessionID, detachAfter, outputLimit)

	switch result {
	case backend.FollowCompleted:
		os.Exit(exitCode)
	case backend.FollowTimedOut:
		fmt.Fprintf(os.Stderr, "swash: still running after %s, detaching\n", detachAfter)
		fmt.Fprintf(os.Stderr, "swash: session ID: %s\n", sessionID)
		fmt.Fprintf(os.Stderr, "swash: swash follow %s\n", sessionID)
		os.Exit(1)
	case backend.FollowOutputLimit:
		fmt.Fprintf(os.Stderr, "swash: output exceeded %d bytes, detaching\n", outputLimit)
		fmt.Fprintf(os.Stderr, "swash: session ID: %s\n", sessionID)
		fmt.Fprintf(os.Stderr, "swash: swash follow %s\n", sessionID)
		os.Exit(1)
	case backend.FollowCancelled:
		fmt.Fprintf(os.Stderr, "swash: cancelled, killed session %s\n", sessionID)
		os.Exit(130) // Standard exit code for SIGINT
	}
}

func cmdRunTTY(command []string) {
	initBackend()
	defer bk.Close()

	rows, cols := GetContentSize()
	opts := backend.SessionOptions{
		Protocol: protocol.Protocol(protocolFlag),
		Tags:     parseTags(tagFlags),
		TTY:      true,
		Rows:     rows,
		Cols:     cols,
	}

	sessionID, err := bk.StartSession(context.Background(), command, opts)
	if err != nil {
		fatal("starting session: %v", err)
	}

	cmdAttach(sessionID)
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
	initBackend()
	defer bk.Close()

	if err := bk.StopSession(context.Background(), sessionID); err != nil {
		fatal("stopping session: %v", err)
	}
	fmt.Printf("%s stopped\n", sessionID)
}

func cmdPoll(sessionID string) {
	initBackend()
	defer bk.Close()

	events, _, err := bk.PollSessionOutput(context.Background(), sessionID, "")
	if err != nil {
		fatal("polling journal: %v", err)
	}
	for _, e := range events {
		fmt.Println(e.Text)
	}
}

func cmdFollow(sessionID string) {
	initBackend()
	defer bk.Close()

	exitCode, result := bk.FollowSession(context.Background(), sessionID, 0, 0)
	if result == backend.FollowCancelled {
		os.Exit(130)
	}
	os.Exit(exitCode)
}

func cmdSend(sessionID, input string) {
	initBackend()
	defer bk.Close()

	if _, err := bk.SendInput(context.Background(), sessionID, input); err != nil {
		fatal("sending input: %v", err)
	}
}

func cmdKill(sessionID string) {
	initBackend()
	defer bk.Close()

	if err := bk.KillSession(context.Background(), sessionID); err != nil {
		fatal("killing: %v", err)
	}
	fmt.Printf("%s killed\n", sessionID)
}

func cmdScreen(sessionID string) {
	initBackend()
	defer bk.Close()

	screen, err := bk.GetScreen(context.Background(), sessionID)
	if err != nil {
		fatal("%v", err)
	}
	fmt.Print(screen)
}

func cmdHistory() {
	initBackend()
	defer bk.Close()

	sessions, err := bk.ListHistory(context.Background())
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
