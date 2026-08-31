// Package integration contains black-box integration tests for swash.
// Run with: go test ./integration/...
package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"swa.sh/internal/journal"
)

const testTimeout = 10 * time.Second

// withTimeout runs f with a timeout, panicking with a goroutine dump if it exceeds the limit.
func withTimeout(name string, timeout time.Duration, f func()) {
	timer := time.AfterFunc(timeout, func() {
		debug.SetTraceback("all")
		panic(fmt.Sprintf("%s timed out after %v", name, timeout))
	})
	defer timer.Stop()
	f()
}

// runCmd runs a command with the given timeout, logging what it runs.
func runCmd(timeout time.Duration, name string, args ...string) ([]byte, error) {
	return runCmdEnv(timeout, nil, name, args...)
}

func runCmdEnv(timeout time.Duration, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fmt.Fprintf(os.Stderr, "$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			fmt.Fprintf(os.Stderr, "%s", out)
		}
		fmt.Fprintf(os.Stderr, " -> error: %v\n", err)
	}
	return out, err
}

// runTest wraps a test function with automatic timeout and goroutine dump on hang.
func runTest(t *testing.T, f func(t *testing.T, e *testEnv)) {
	t.Parallel()
	e := getEnv(t)
	withTimeout(t.Name(), testTimeout, func() { f(t, e) })
}

// testEnv holds the test environment configuration
type testEnv struct {
	swashBin   string
	tmpDir     string
	mode       string // "real" or "posix"
	journalCmd string
	journalDir string
	rootSlice  string // unique slice for test isolation

	// posix-specific
	stateDir   string
	runtimeDir string
}

var (
	env     *testEnv
	envOnce sync.Once
	envErr  error
)

func getEnv(t *testing.T) *testEnv {
	envOnce.Do(func() {
		env, envErr = setupEnv()
	})
	if envErr != nil {
		t.Fatalf("failed to setup test environment: %v", envErr)
	}
	return env
}

func setupEnv() (*testEnv, error) {
	mode := os.Getenv("SWASH_TEST_MODE")
	if mode == "" {
		mode = "posix" // default to posix (isolated, no side effects)
	}
	if mode != "real" && mode != "posix" {
		return nil, fmt.Errorf("unknown SWASH_TEST_MODE: %s (use 'real' or 'posix')", mode)
	}

	tmpDir, err := os.MkdirTemp("", "swash-integration-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}

	// Create unique root slice for test isolation
	rootSlice := fmt.Sprintf("swashtest%d", os.Getpid())

	env := &testEnv{
		tmpDir:    tmpDir,
		mode:      mode,
		rootSlice: rootSlice,
	}

	// Use pre-built binary from bin/swash (built by `make build`)
	swashBin := filepath.Join(getProjectRoot(), "bin", "swash")
	if _, err := os.Stat(swashBin); err != nil {
		return nil, fmt.Errorf("swash binary not found at %s - run 'make build' first", swashBin)
	}
	env.swashBin = swashBin

	switch mode {
	case "real":
		env.journalCmd = "journalctl --user"
	case "posix":
		if err := env.setupPosix(); err != nil {
			return nil, err
		}
	}

	return env, nil
}

func (e *testEnv) setupPosix() error {
	// Set up shared directories for posix mode
	// - stateDir: persistent data (SQLite event database)
	// - runtimeDir: ephemeral data (sockets, session metadata)
	e.stateDir = filepath.Join(e.tmpDir, "state")
	e.runtimeDir = filepath.Join(e.tmpDir, "runtime")

	if err := os.MkdirAll(e.stateDir, 0755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	if err := os.MkdirAll(e.runtimeDir, 0755); err != nil {
		return fmt.Errorf("creating runtime dir: %w", err)
	}
	return nil
}

func (e *testEnv) cleanup() {
	withTimeout("cleanup", 5*time.Second, func() {
		// Stop the test slice and all children (real systemd mode)
		if e.mode == "real" && e.rootSlice != "" {
			runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
			if runtimeDir == "" {
				runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
			}
			cleanupEnv := setEnv(os.Environ(), "XDG_RUNTIME_DIR", runtimeDir)
			cleanupEnv = setEnv(cleanupEnv, "DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(runtimeDir, "bus"))
			runCmdEnv(3*time.Second, cleanupEnv, "systemctl", "--user", "kill", "--signal=SIGKILL", e.rootSlice+".slice")
			runCmdEnv(3*time.Second, cleanupEnv, "systemctl", "--user", "stop", e.rootSlice+".slice")
			runCmdEnv(3*time.Second, cleanupEnv, "systemctl", "--user", "reset-failed")
		}

		if e.tmpDir != "" {
			os.RemoveAll(e.tmpDir)
		}
	})
}

func getProjectRoot() string {
	// Find project root by looking for go.mod
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Fallback
			return "."
		}
		dir = parent
	}
}

// runSwash runs a swash command and returns stdout, stderr, and error
func (e *testEnv) runSwash(args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.swashBin, args...)
	cmd.Env = e.getEnvVars()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// runSwashEnv runs swash with additional environment overrides applied.
// This is used by tests that need per-test SWASH_* directories without mutating
// the global process environment (tests run in parallel).
func (e *testEnv) runSwashEnv(overrides map[string]string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.swashBin, args...)
	env := e.getEnvVars()
	for k, v := range overrides {
		env = setEnv(env, k, v)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// getEnvVars returns the environment variables for running swash
func (e *testEnv) getEnvVars() []string {
	env := os.Environ()
	// Always set root slice for test isolation
	env = append(env, "SWASH_ROOT_SLICE="+e.rootSlice)
	// Pass through GOCOVERDIR for coverage collection
	if coverDir := os.Getenv("GOCOVERDIR"); coverDir != "" {
		env = setEnv(env, "GOCOVERDIR", coverDir)
	}
	switch e.mode {
	case "real":
		// Explicitly request systemd backend in case DBUS_SESSION_BUS_ADDRESS
		// isn't set in the CI environment (which would cause auto-detection
		// to choose posix instead).
		env = append(env,
			"SWASH_BACKEND=systemd",
			"SWASH_RUNTIME_DIR="+e.tmpDir,
		)
	case "posix":
		env = append(env,
			"SWASH_BACKEND=posix",
			"SWASH_STATE_DIR="+e.stateDir,
			"SWASH_RUNTIME_DIR="+e.runtimeDir,
			// Clear DBUS_SESSION_BUS_ADDRESS to ensure posix backend is auto-detected
			"DBUS_SESSION_BUS_ADDRESS=",
		)
	}
	return env
}

// runJournalctl queries the real journal in systemd mode and the SQLite event
// database in POSIX mode. It supports the journalctl arguments used by tests.
func (e *testEnv) runJournalctl(args ...string) (string, error) {
	if e.mode == "real" {
		out, err := runCmd(5*time.Second, "journalctl", append([]string{"--user"}, args...)...)
		return string(out), err
	}

	outputFormat := "short"
	var filters []journal.EventFilter
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-o" && i+1 < len(args) {
			outputFormat = args[i+1]
			i++
		} else if strings.Contains(arg, "=") && !strings.HasPrefix(arg, "-") {
			parts := strings.SplitN(arg, "=", 2)
			filters = append(filters, journal.EventFilter{Field: parts[0], Value: parts[1]})
		}
	}

	log, err := journal.OpenSQLite(filepath.Join(e.stateDir, "events.db"))
	if err != nil {
		return "", err
	}
	defer log.Close()
	records, _, err := log.Poll(context.Background(), filters, "")
	if err != nil {
		return "", err
	}

	var lines []string
	for _, record := range records {
		switch outputFormat {
		case "cat":
			lines = append(lines, record.Message)
		case "short":
			lines = append(lines, fmt.Sprintf("%s %s", record.Timestamp.Format("Jan 02 15:04:05"), record.Message))
		default:
			lines = append(lines, record.Message)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// TestMain handles setup and teardown
func TestMain(m *testing.M) {
	// Print mode information before running tests
	mode := os.Getenv("SWASH_TEST_MODE")
	if mode == "" {
		mode = "posix"
	}
	switch mode {
	case "real":
		fmt.Println("=== Running with real systemd ===")
		fmt.Println("    This will create transient units in your user systemd.")
		fmt.Println("    To test with posix backend: SWASH_TEST_MODE=posix go test ./integration/")
		fmt.Println()
	case "posix":
		fmt.Println("=== Running with posix backend (isolated) ===")
		fmt.Println("    To test with real systemd: SWASH_TEST_MODE=real go test ./integration/")
		fmt.Println()
	}

	code := m.Run()

	if env != nil {
		env.cleanup()
	}

	os.Exit(code)
}

// --- Actual Tests ---

func TestSwashStart(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		stdout, stderr, err := e.runSwash("start", "echo", "hello")
		if err != nil {
			t.Fatalf("swash start failed: %v\nstderr: %s", err, stderr)
		}

		if !strings.Contains(stdout, "started") {
			t.Errorf("expected 'started' in output, got: %s", stdout)
		}

		// Extract session ID and kill it
		parts := strings.Fields(stdout)
		if len(parts) > 0 {
			sessionID := parts[0]
			e.runSwash("kill", sessionID)
		}
	})
}

func TestEmitSemanticEvent(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		sessionID := fmt.Sprintf("EMIT%d", os.Getpid())
		stdout, stderr, err := e.runSwash(
			"emit", sessionID,
			"--event", "slynk-ready",
			"--message", "Slynk is ready",
			"--field", "LUV_ROOT=/work/luv",
			"--field", "LUV_SLYNK_PORT=4172",
		)
		if err != nil {
			t.Fatalf("swash emit failed: %v\nstderr: %s", err, stderr)
		}
		if strings.TrimSpace(stdout) != sessionID+" slynk-ready" {
			t.Fatalf("unexpected emit output: %q", stdout)
		}

		root, err := e.runJournalctl("-o", "cat", "SWASH_SESSION="+sessionID, "SWASH_EVENT=slynk-ready", "LUV_ROOT=/work/luv")
		if err != nil {
			t.Fatalf("querying emitted event: %v", err)
		}
		if strings.TrimSpace(root) != "Slynk is ready" {
			t.Fatalf("emitted event message = %q", root)
		}

		port, err := e.runJournalctl("-o", "cat", "SWASH_SESSION="+sessionID, "LUV_SLYNK_PORT=4172")
		if err != nil {
			t.Fatalf("querying emitted port field: %v", err)
		}
		if strings.TrimSpace(port) != "Slynk is ready" {
			t.Fatalf("emitted port event message = %q", port)
		}

		eventsJSON, eventsErr, err := e.runSwash(
			"events", "--session", sessionID,
			"--event", "slynk-ready",
			"--field", "LUV_ROOT=/work/luv",
			"--json",
		)
		if err != nil {
			t.Fatalf("swash events failed: %v\nstderr: %s", err, eventsErr)
		}
		var readyEvent struct {
			Cursor  string            `json:"cursor"`
			Message string            `json:"message"`
			Fields  map[string]string `json:"fields"`
		}
		if err := json.Unmarshal([]byte(eventsJSON), &readyEvent); err != nil {
			t.Fatalf("decoding events JSON %q: %v", eventsJSON, err)
		}
		if readyEvent.Cursor == "" || readyEvent.Message != "Slynk is ready" || readyEvent.Fields["LUV_SLYNK_PORT"] != "4172" {
			t.Fatalf("unexpected ready event: %#v", readyEvent)
		}

		_, stderr, err = e.runSwash(
			"emit", sessionID,
			"--event", "eval-completed",
			"--field", "LUV_PACKAGE=CL-USER",
		)
		if err != nil {
			t.Fatalf("second swash emit failed: %v\nstderr: %s", err, stderr)
		}
		afterJSON, afterErr, err := e.runSwash(
			"events", "--session", sessionID,
			"--cursor", readyEvent.Cursor,
			"--json",
		)
		if err != nil {
			t.Fatalf("cursor query failed: %v\nstderr: %s", err, afterErr)
		}
		var afterEvent struct {
			Message string            `json:"message"`
			Fields  map[string]string `json:"fields"`
		}
		if err := json.Unmarshal([]byte(afterJSON), &afterEvent); err != nil {
			t.Fatalf("decoding cursor query %q: %v", afterJSON, err)
		}
		if afterEvent.Fields["SWASH_EVENT"] != "eval-completed" || afterEvent.Fields["LUV_PACKAGE"] != "CL-USER" {
			t.Fatalf("unexpected event after cursor: %#v", afterEvent)
		}

		lastJSON, lastErr, err := e.runSwash(
			"events", "--session", sessionID,
			"--last", "1",
			"--json",
		)
		if err != nil {
			t.Fatalf("last-event query failed: %v\nstderr: %s", err, lastErr)
		}
		var lastEvent struct {
			Fields map[string]string `json:"fields"`
		}
		if err := json.Unmarshal([]byte(lastJSON), &lastEvent); err != nil {
			t.Fatalf("decoding last-event query %q: %v", lastJSON, err)
		}
		if lastEvent.Fields["SWASH_EVENT"] != "eval-completed" {
			t.Fatalf("last event = %#v", lastEvent)
		}

		followID := sessionID + "F"
		followCtx, cancelFollow := context.WithCancel(context.Background())
		defer cancelFollow()
		followCmd := exec.CommandContext(
			followCtx, e.swashBin,
			"events", "--session", followID, "--follow", "--json",
		)
		followCmd.Env = e.getEnvVars()
		followOut, err := followCmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var followErr bytes.Buffer
		followCmd.Stderr = &followErr
		if err := followCmd.Start(); err != nil {
			t.Fatalf("starting event follower: %v", err)
		}
		lineCh := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(followOut).ReadString('\n')
			lineCh <- line
		}()

		_, stderr, err = e.runSwash(
			"emit", followID,
			"--event", "health",
			"--field", "LUV_HEALTH=ready",
		)
		if err != nil {
			cancelFollow()
			followCmd.Wait()
			t.Fatalf("emit for follower failed: %v\nstderr: %s", err, stderr)
		}

		select {
		case line := <-lineCh:
			cancelFollow()
			_ = followCmd.Wait()
			var followed struct {
				Fields map[string]string `json:"fields"`
			}
			if err := json.Unmarshal([]byte(line), &followed); err != nil {
				t.Fatalf("decoding followed event %q: %v", line, err)
			}
			if followed.Fields["SWASH_EVENT"] != "health" || followed.Fields["LUV_HEALTH"] != "ready" {
				t.Fatalf("unexpected followed event: %#v", followed)
			}
		case <-time.After(2 * time.Second):
			cancelFollow()
			_ = followCmd.Wait()
			t.Fatalf("timed out following semantic event; stderr: %s", followErr.String())
		}
	})
}

func TestSwashRun(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		stdout, stderr, err := e.runSwash("run", "echo", "hello world")
		if err != nil {
			t.Fatalf("swash run failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}

		if !strings.Contains(stdout, "hello world") {
			t.Errorf("expected 'hello world' in output, got: %s", stdout)
		}
	})
}

func TestTaskReceivesSwashSessionID(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		stdout, stderr, err := e.runSwash("run", "--", "/bin/sh", "-c", "printf '%s\\n' \"$SWASH_SESSION\"")
		if err != nil {
			t.Fatalf("swash run failed: %v\nstderr: %s", err, stderr)
		}
		sessionID := strings.TrimSpace(stdout)
		if len(sessionID) != 6 {
			t.Fatalf("SWASH_SESSION = %q, want six-character session ID", sessionID)
		}
		for i, character := range sessionID {
			if (i < 3 && (character < 'A' || character > 'Z')) ||
				(i >= 3 && (character < '0' || character > '9')) {
				t.Fatalf("SWASH_SESSION = %q, want LLLDDD", sessionID)
			}
		}
	})
}

func TestSwashRunExitCode(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		_, _, err := e.runSwash("run", "--", "sh", "-c", "exit 42")
		if err == nil {
			t.Fatal("expected error for non-zero exit code")
		}

		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() != 42 {
				t.Errorf("expected exit code 42, got %d", exitErr.ExitCode())
			}
		} else {
			t.Errorf("expected ExitError, got %T", err)
		}
	})
}

func TestSwashRunSignalExitCode(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		for _, signal := range []struct {
			name string
			num  int
		}{
			{name: "TERM", num: 15},
			{name: "KILL", num: 9},
		} {
			t.Run(signal.name, func(t *testing.T) {
				_, _, err := e.runSwash("run", "--", "/bin/sh", "-c", fmt.Sprintf("kill -%d 0", signal.num))
				exitErr, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatalf("error = %v (%T), want ExitError", err, err)
				}
				if want := 128 + signal.num; exitErr.ExitCode() != want {
					t.Fatalf("exit code = %d, want %d", exitErr.ExitCode(), want)
				}
			})
		}
	})
}

func TestSystemdFollowReturnsAfterHardKill(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		if e.mode != "real" {
			t.Skip("requires the systemd backend")
		}
		stdout, stderr, err := e.runSwash("start", "sleep", "60")
		if err != nil {
			t.Fatalf("starting session: %v\nstderr: %s", err, stderr)
		}
		sessionID := strings.Fields(stdout)[0]

		follow := exec.Command(e.swashBin, "follow", sessionID)
		follow.Env = e.getEnvVars()
		if err := follow.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		if _, killErr, err := e.runSwash("kill", sessionID); err != nil {
			t.Fatalf("killing session: %v\nstderr: %s", err, killErr)
		}

		waited := make(chan error, 1)
		go func() { waited <- follow.Wait() }()
		select {
		case err := <-waited:
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 137 {
				t.Fatalf("follow error = %v, want exit 137", err)
			}
		case <-time.After(3 * time.Second):
			_ = follow.Process.Kill()
			t.Fatal("follow remained blocked after hard kill")
		}

		history, historyErr, err := e.runSwash("history")
		if err != nil {
			t.Fatalf("reading history: %v\nstderr: %s", err, historyErr)
		}
		if !strings.Contains(history, sessionID) || !strings.Contains(history, "killed") {
			t.Fatalf("history does not report killed session %s:\n%s", sessionID, history)
		}
	})
}

func TestSystemdStopAllowsTaskCleanup(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		if e.mode != "real" {
			t.Skip("requires the systemd backend")
		}
		for _, tty := range []bool{false, true} {
			name := "pipe"
			if tty {
				name = "tty"
			}
			t.Run(name, func(t *testing.T) {
				marker := filepath.Join(e.tmpDir, fmt.Sprintf("stop-%s-%d", name, time.Now().UnixNano()))
				script := marker + ".sh"
				content := "#!/bin/sh\n" +
					"trap 'echo cleaned > \"$1.cleaned\"; exit 0' TERM\n" +
					"echo ready > \"$1.ready\"\n" +
					"while :; do sleep 1; done\n"
				if err := os.WriteFile(script, []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
				args := []string{"start"}
				if tty {
					args = append(args, "--tty")
				}
				args = append(args, "--", script, marker)
				stdout, stderr, err := e.runSwash(args...)
				if err != nil {
					t.Fatalf("starting session: %v\nstderr: %s", err, stderr)
				}
				sessionID := strings.Fields(stdout)[0]
				defer e.runSwash("kill", sessionID)

				deadline := time.Now().Add(2 * time.Second)
				for {
					if _, err := os.Stat(marker + ".ready"); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("task did not become ready")
					}
					time.Sleep(20 * time.Millisecond)
				}

				if _, stopErr, err := e.runSwash("stop", sessionID); err != nil {
					t.Fatalf("stopping session: %v\nstderr: %s", err, stopErr)
				}
				if _, err := os.Stat(marker + ".cleaned"); err != nil {
					t.Fatalf("task did not handle SIGTERM: %v", err)
				}
			})
		}
	})
}

func TestTTYModeOutput(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		// Start a TTY session
		stdout, _, err := e.runSwash("start", "--tty", "echo", "TTY_TEST_OUTPUT")
		if err != nil {
			t.Fatalf("swash start --tty failed: %v", err)
		}

		parts := strings.Fields(stdout)
		if len(parts) == 0 {
			t.Fatal("no session ID in output")
		}
		sessionID := parts[0]
		defer e.runSwash("kill", sessionID)

		// Wait for it to complete
		e.runSwash("follow", sessionID)

		// Check screen output - should work even after session ended
		screenOut, screenErr, err := e.runSwash("screen", sessionID)
		if err != nil {
			t.Fatalf("screen command failed: %v\nstderr: %s", err, screenErr)
		}

		if !strings.Contains(screenOut, "TTY_TEST_OUTPUT") {
			t.Errorf("expected 'TTY_TEST_OUTPUT' in screen, got: %s", screenOut)
		}
	})
}

func TestTTYAttach(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		// Check if tmux is available
		if _, err := exec.LookPath("tmux"); err != nil {
			t.Skip("tmux not available")
		}

		// Start a TTY session with cat (waits forever)
		stdout, _, err := e.runSwash("start", "--tty", "--rows", "10", "--cols", "50", "--", "cat")
		if err != nil {
			t.Fatalf("swash start --tty failed: %v", err)
		}

		parts := strings.Fields(stdout)
		if len(parts) == 0 {
			t.Fatal("no session ID in output")
		}
		sessionID := parts[0]
		defer e.runSwash("kill", sessionID)

		time.Sleep(200 * time.Millisecond)

		// Start tmux session and attach
		tmuxSession := fmt.Sprintf("swash-test-%d", os.Getpid())
		exec.Command("tmux", "new-session", "-d", "-s", tmuxSession, "-x", "60", "-y", "15").Run()
		defer exec.Command("tmux", "kill-session", "-t", tmuxSession).Run()

		// Build command with env vars for the test mode
		attachCmd := e.swashBin + " attach " + sessionID
		// Add GOCOVERDIR for coverage collection if set
		if coverDir := os.Getenv("GOCOVERDIR"); coverDir != "" {
			attachCmd = fmt.Sprintf("GOCOVERDIR=%s %s", coverDir, attachCmd)
		}
		switch e.mode {
		case "real":
			// Explicitly set backend in case DBUS_SESSION_BUS_ADDRESS isn't in tmux env
			attachCmd = fmt.Sprintf("SWASH_BACKEND=systemd %s", attachCmd)
		case "posix":
			attachCmd = fmt.Sprintf("env SWASH_BACKEND=posix SWASH_STATE_DIR=%s SWASH_RUNTIME_DIR=%s %s",
				filepath.Join(e.tmpDir, "state"), filepath.Join(e.tmpDir, "runtime"), attachCmd)
		}
		exec.Command("tmux", "send-keys", "-t", tmuxSession, attachCmd, "Enter").Run()
		time.Sleep(300 * time.Millisecond)

		// Send input
		exec.Command("tmux", "send-keys", "-t", tmuxSession, "HELLO_ATTACH_TEST", "Enter").Run()
		time.Sleep(300 * time.Millisecond)

		// Capture pane
		captureOut, _ := exec.Command("tmux", "capture-pane", "-t", tmuxSession, "-p").Output()

		if !strings.Contains(string(captureOut), "HELLO_ATTACH_TEST") {
			t.Errorf("expected 'HELLO_ATTACH_TEST' in tmux pane, got: %s", string(captureOut))
		}

		// Detach cleanly with Ctrl+C so coverage data is flushed
		exec.Command("tmux", "send-keys", "-t", tmuxSession, "C-c").Run()
		time.Sleep(100 * time.Millisecond)
	})
}

func TestRunTimeoutDetach(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		stdout, stderr, err := e.runSwash("run", "-d", "1s", "sleep", "10")
		combined := stdout + stderr

		if err == nil {
			t.Log("command succeeded (may have finished quickly)")
		}

		if !strings.Contains(combined, "still running") && !strings.Contains(combined, "started") {
			// It either timed out and detached, or finished - both are ok
			t.Logf("output: %s", combined)
		}

		// Clean up any lingering session
		if strings.Contains(combined, "session ID:") {
			// Extract and kill
			for line := range strings.SplitSeq(combined, "\n") {
				if strings.Contains(line, "session ID:") {
					parts := strings.Fields(line)
					if len(parts) >= 3 {
						e.runSwash("kill", parts[2])
					}
				}
			}
		}
	})
}

func TestStartImmediate(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		start := time.Now()
		stdout, _, err := e.runSwash("start", "sleep", "10")
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("swash start failed: %v", err)
		}

		if elapsed > time.Second {
			t.Errorf("start took %v, expected < 1s", elapsed)
		}

		if !strings.Contains(stdout, "started") {
			t.Errorf("expected 'started' in output, got: %s", stdout)
		}

		// Clean up
		parts := strings.Fields(stdout)
		if len(parts) > 0 {
			e.runSwash("kill", parts[0])
		}
	})
}

func TestTaskOutputCapture(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		uniqueMarker := fmt.Sprintf("UNIQUE_OUTPUT_%d", time.Now().UnixNano())

		// Start session and wait for completion
		stdout, _, err := e.runSwash("start", "echo", uniqueMarker)
		if err != nil {
			t.Fatalf("swash start failed: %v", err)
		}

		parts := strings.Fields(stdout)
		if len(parts) == 0 {
			t.Fatal("no session ID in output")
		}
		sessionID := parts[0]

		// Wait for completion
		e.runSwash("follow", sessionID)

		// Check journal for output
		journalOut, err := e.runJournalctl("-o", "cat")
		if err != nil {
			t.Logf("journalctl error (may be expected): %v", err)
		}

		if !strings.Contains(journalOut, uniqueMarker) {
			t.Errorf("expected %q in journal, got: %s", uniqueMarker, journalOut)
		}
	})
}

func TestNewlineSplitting(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		// Create a script that outputs multiple lines
		script := filepath.Join(e.tmpDir, fmt.Sprintf("multiline_%d.sh", time.Now().UnixNano()))
		content := `#!/bin/sh
echo LINE_ONE_TEST
echo LINE_TWO_TEST
echo LINE_THREE_TEST
`
		if err := os.WriteFile(script, []byte(content), 0755); err != nil {
			t.Fatalf("failed to write script: %v", err)
		}

		stdout, _, err := e.runSwash("start", script)
		if err != nil {
			t.Fatalf("swash start failed: %v", err)
		}

		parts := strings.Fields(stdout)
		if len(parts) == 0 {
			t.Fatal("no session ID in output")
		}
		sessionID := parts[0]

		// Wait for completion
		e.runSwash("follow", sessionID)

		// Check journal for all lines
		journalOut, _ := e.runJournalctl("-o", "cat")

		found := 0
		for _, line := range []string{"LINE_ONE_TEST", "LINE_TWO_TEST", "LINE_THREE_TEST"} {
			if strings.Contains(journalOut, line) {
				found++
			}
		}

		if found != 3 {
			t.Errorf("expected 3 lines in journal, found %d. Output: %s", found, journalOut)
		}
	})
}

func TestTTYColorsPreserved(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		// Create a script that outputs colored text
		script := filepath.Join(e.tmpDir, fmt.Sprintf("colors_%d.sh", time.Now().UnixNano()))
		content := "#!/bin/sh\nprintf '\\033[31mRED_TEXT_TEST\\033[0m\\n'\n"
		if err := os.WriteFile(script, []byte(content), 0755); err != nil {
			t.Fatalf("failed to write script: %v", err)
		}

		stdout, _, err := e.runSwash("start", "--tty", script)
		if err != nil {
			t.Fatalf("swash start --tty failed: %v", err)
		}

		parts := strings.Fields(stdout)
		if len(parts) == 0 {
			t.Fatal("no session ID in output")
		}
		sessionID := parts[0]
		defer e.runSwash("kill", sessionID)

		// Wait for completion
		e.runSwash("follow", sessionID)

		// Check journal for colored text
		journalOut, _ := e.runJournalctl("-o", "cat")

		if !strings.Contains(journalOut, "RED_TEXT_TEST") {
			t.Errorf("expected 'RED_TEXT_TEST' in journal, got: %s", journalOut)
		}
	})
}

func TestTTYScreenEvent(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		stdout, _, err := e.runSwash("start", "--tty", "--rows", "5", "--cols", "40", "echo", "SCREEN_CAPTURE_TEST")
		if err != nil {
			t.Fatalf("swash start --tty failed: %v", err)
		}

		parts := strings.Fields(stdout)
		if len(parts) == 0 {
			t.Fatal("no session ID in output")
		}
		sessionID := parts[0]
		defer e.runSwash("kill", sessionID)

		// Wait for completion
		e.runSwash("follow", sessionID)

		// Check journal for screen event
		journalOut, _ := e.runJournalctl("SWASH_EVENT=screen", "-o", "cat")

		if !strings.Contains(journalOut, "SCREEN_CAPTURE_TEST") {
			t.Errorf("expected 'SCREEN_CAPTURE_TEST' in screen event, got: %s", journalOut)
		}
	})
}
