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
//	swash history                      Show session history
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/mbrock/swash/internal/swash"
	flag "github.com/spf13/pflag"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `swash - Interactive process sessions over D-Bus

Usage:
  swash                              Show running sessions
  swash run <command>                Run command in new session
  swash stop <session_id>            Stop session
  swash poll <session_id>            Get recent output
  swash follow <session_id>          Follow output until exit
  swash send <session_id> <input>    Send input to process
  swash kill <session_id>            Kill process
  swash history                      Show session history

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
		cmdRun(cmdArgs)
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
	default:
		fatal("unknown command: %s", cmd)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
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

// findServerBinary locates the swash-server binary.
func findServerBinary() string {
	// First, check if swash-server is next to the current executable
	self, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(self)
		serverPath := filepath.Join(dir, "swash-server")
		if _, err := os.Stat(serverPath); err == nil {
			return serverPath
		}
	}

	// Fall back to PATH
	path, err := exec.LookPath("swash-server")
	if err == nil {
		return path
	}

	fatal("swash-server not found (should be next to swash binary or in PATH)")
	return ""
}

func cmdStatus() {
	ctx := context.Background()
	sessions, err := swash.ListSessions(ctx)
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

func cmdRun(command []string) {
	ctx := context.Background()
	serverBinary := findServerBinary()
	sessionID, err := swash.StartSession(ctx, command, serverBinary)
	if err != nil {
		fatal("starting session: %v", err)
	}
	fmt.Printf("%s started\n", sessionID)
}

func cmdStop(sessionID string) {
	ctx := context.Background()
	if err := swash.StopSession(ctx, sessionID); err != nil {
		fatal("stopping session: %v", err)
	}
	fmt.Printf("%s stopped\n", sessionID)
}

func cmdPoll(sessionID string) {
	events, _, err := swash.PollSessionOutput(sessionID, "")
	if err != nil {
		fatal("polling journal: %v", err)
	}
	for _, e := range events {
		fmt.Println(e.Text)
	}
}

func cmdFollow(sessionID string) {
	if err := swash.FollowSession(sessionID); err != nil {
		fatal("following: %v", err)
	}
}

func cmdSend(sessionID, input string) {
	if err := swash.SendInput(sessionID, input); err != nil {
		fatal("sending input: %v", err)
	}
}

func cmdKill(sessionID string) {
	if err := swash.KillSession(sessionID); err != nil {
		fatal("killing: %v", err)
	}
	fmt.Printf("%s killed\n", sessionID)
}

func cmdHistory() {
	sessions, err := swash.ListHistory()
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
