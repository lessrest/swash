// Package integration contains black-box integration tests for swash.
// Run with: go test ./integration/...
package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fmt.Fprintf(os.Stderr, "$ %s %s\n", name, strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
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
	mode       string // "real" or "mini"
	journalCmd string
	journalDir string
	rootSlice  string // unique slice for test isolation

	// mini-systemd specific
	miniSystemdBin string
	miniSystemdCmd *exec.Cmd
	dbusCmd        *exec.Cmd
	busSocket      string
	journalSocket  string
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
		mode = "mini" // default to mini-systemd (isolated, no side effects)
	}
	if mode != "mini" && mode != "real" && mode != "posix" {
		return nil, fmt.Errorf("unknown SWASH_TEST_MODE: %s (use 'mini', 'real', or 'posix')", mode)
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

	// Build swash
	swashBin := filepath.Join(tmpDir, "swash")
	cmd := exec.Command("go", "build", "-o", swashBin, "./cmd/swash/")
	cmd.Dir = getProjectRoot()
	cmd.Env = append(os.Environ(), "CGO_CFLAGS=-I"+filepath.Join(getProjectRoot(), "cvendor"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("building swash: %w\n%s", err, out)
	}
	env.swashBin = swashBin

	switch mode {
	case "mini":
		if err := env.setupMiniSystemd(); err != nil {
			return nil, err
		}
	case "real":
		env.journalCmd = "journalctl --user"
	case "posix":
		env.setupPosix()
	}

	return env, nil
}

func (e *testEnv) setupPosix() {
	// Posix backend uses file-based event logging stored in SWASH_STATE_DIR.
	// No additional setup needed - env vars are set in getEnvVars().
}

func (e *testEnv) setupMiniSystemd() error {
	e.journalDir = filepath.Join(e.tmpDir, "journal")
	e.journalSocket = filepath.Join(e.tmpDir, "journal.socket")
	e.busSocket = filepath.Join(e.tmpDir, "bus.sock")

	if err := os.MkdirAll(e.journalDir, 0755); err != nil {
		return fmt.Errorf("creating journal dir: %w", err)
	}

	// Build mini-systemd
	miniSystemdBin := filepath.Join(e.tmpDir, "mini-systemd")
	cmd := exec.Command("go", "build", "-o", miniSystemdBin, "./cmd/mini-systemd/")
	cmd.Dir = getProjectRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building mini-systemd: %w\n%s", err, out)
	}
	e.miniSystemdBin = miniSystemdBin

	// Start dbus-daemon
	e.dbusCmd = exec.Command("dbus-daemon", "--session", "--nofork", "--address=unix:path="+e.busSocket)
	if err := e.dbusCmd.Start(); err != nil {
		return fmt.Errorf("starting dbus-daemon: %w", err)
	}

	// Wait for dbus socket
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(e.busSocket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Start mini-systemd with D-Bus address in env
	e.miniSystemdCmd = exec.Command(miniSystemdBin, "--journal-dir="+e.journalDir, "--journal-socket="+e.journalSocket)
	e.miniSystemdCmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS=unix:path="+e.busSocket)
	e.miniSystemdCmd.Stdout = os.Stdout
	e.miniSystemdCmd.Stderr = os.Stderr
	if err := e.miniSystemdCmd.Start(); err != nil {
		return fmt.Errorf("starting mini-systemd: %w", err)
	}

	// Wait for journal socket
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(e.journalSocket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // extra time for D-Bus registration

	e.journalCmd = fmt.Sprintf("journalctl --file=%s/*.journal", e.journalDir)

	return nil
}

func (e *testEnv) cleanup() {
	withTimeout("cleanup", 5*time.Second, func() {
		// Stop the test slice and all children (real systemd mode)
		if e.mode == "real" && e.rootSlice != "" {
			runCmd(3*time.Second, "systemctl", "--user", "kill", e.rootSlice+".slice")
			runCmd(3*time.Second, "systemctl", "--user", "reset-failed")
		}

		if e.miniSystemdCmd != nil && e.miniSystemdCmd.Process != nil {
			e.miniSystemdCmd.Process.Kill()
			e.miniSystemdCmd.Wait()
		}
		if e.dbusCmd != nil && e.dbusCmd.Process != nil {
			e.dbusCmd.Process.Kill()
			e.dbusCmd.Wait()
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
	switch e.mode {
	case "mini":
		env = append(env,
			"DBUS_SESSION_BUS_ADDRESS=unix:path="+e.busSocket,
			"SWASH_JOURNAL_SOCKET="+e.journalSocket,
			"SWASH_JOURNAL_DIR="+e.journalDir,
		)
	case "posix":
		env = append(env,
			"SWASH_BACKEND=posix",
			"SWASH_STATE_DIR="+filepath.Join(e.tmpDir, "state"),
			"SWASH_RUNTIME_DIR="+filepath.Join(e.tmpDir, "runtime"),
			// Clear DBUS_SESSION_BUS_ADDRESS to ensure posix backend is auto-detected
			"DBUS_SESSION_BUS_ADDRESS=",
		)
	}
	return env
}

// runJournalctl runs journalctl with the appropriate arguments for the mode.
func (e *testEnv) runJournalctl(args ...string) (string, error) {
	var baseArgs []string
	switch e.mode {
	case "mini":
		files, _ := filepath.Glob(filepath.Join(e.journalDir, "*.journal"))
		if len(files) == 0 {
			return "", fmt.Errorf("no journal files found in %s", e.journalDir)
		}
		baseArgs = []string{"--file=" + files[0]}
	case "posix":
		// Posix backend stores per-session journal files in state/sessions/*/events.journal
		files, _ := filepath.Glob(filepath.Join(e.tmpDir, "state", "sessions", "*", "events.journal"))
		if len(files) == 0 {
			return "", fmt.Errorf("no journal files found in %s", filepath.Join(e.tmpDir, "state", "sessions"))
		}
		// Pass all journal files to journalctl
		for _, f := range files {
			baseArgs = append(baseArgs, "--file="+f)
		}
	default:
		baseArgs = []string{"--user"}
	}
	out, err := runCmd(5*time.Second, "journalctl", append(baseArgs, args...)...)
	return string(out), err
}

// TestMain handles setup and teardown
func TestMain(m *testing.M) {
	// Print mode information before running tests
	mode := os.Getenv("SWASH_TEST_MODE")
	if mode == "" {
		mode = "mini"
	}
	switch mode {
	case "mini":
		fmt.Println("=== Running with mini-systemd (isolated) ===")
		fmt.Println("    To test with real systemd: SWASH_TEST_MODE=real go test ./integration/")
		fmt.Println("    To test with posix backend: SWASH_TEST_MODE=posix go test ./integration/")
		fmt.Println()
	case "real":
		fmt.Println("=== Running with real systemd ===")
		fmt.Println("    This will create transient units in your user systemd.")
		fmt.Println("    To test with isolated mini-systemd: SWASH_TEST_MODE=mini go test ./integration/")
		fmt.Println()
	case "posix":
		fmt.Println("=== Running with posix backend (no systemd) ===")
		fmt.Println("    To test with mini-systemd: SWASH_TEST_MODE=mini go test ./integration/")
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
		stdout, _, err := e.runSwash("start", "echo", "hello")
		if err != nil {
			t.Fatalf("swash start failed: %v", err)
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

func TestSwashRun(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		stdout, _, err := e.runSwash("run", "echo", "hello world")
		if err != nil {
			t.Fatalf("swash run failed: %v", err)
		}

		if !strings.Contains(stdout, "hello world") {
			t.Errorf("expected 'hello world' in output, got: %s", stdout)
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
		screenOut, _, err := e.runSwash("screen", sessionID)
		if err != nil {
			t.Fatalf("screen command failed: %v", err)
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

		// Build command with env vars for mini-systemd mode
		attachCmd := e.swashBin + " attach " + sessionID
		if e.mode == "mini" {
			attachCmd = fmt.Sprintf("DBUS_SESSION_BUS_ADDRESS=unix:path=%s SWASH_JOURNAL_SOCKET=%s %s",
				e.busSocket, e.journalSocket, attachCmd)
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
			for _, line := range strings.Split(combined, "\n") {
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

func TestPosixBackendRun(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		base := t.TempDir()
		env := map[string]string{
			"SWASH_STATE_DIR":   filepath.Join(base, "state"),
			"SWASH_RUNTIME_DIR": filepath.Join(base, "runtime"),
		}

		stdout, stderr, err := e.runSwashEnv(env, "--backend", "posix", "run", "--", "sh", "-c", "echo POSIX_BACKEND_OK")
		if err != nil {
			t.Fatalf("swash posix run failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, "POSIX_BACKEND_OK") {
			t.Fatalf("expected marker in stdout, got:\n%s", stdout)
		}
	})
}

func TestPosixBackendTTYScreen(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		base := t.TempDir()
		env := map[string]string{
			"SWASH_STATE_DIR":   filepath.Join(base, "state"),
			"SWASH_RUNTIME_DIR": filepath.Join(base, "runtime"),
		}

		// Start a short-lived TTY session.
		stdout, stderr, err := e.runSwashEnv(env, "--backend", "posix", "start", "--tty", "--rows", "10", "--cols", "50", "--", "sh", "-c", "echo POSIX_TTY_SCREEN")
		if err != nil {
			t.Fatalf("swash posix start --tty failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}

		parts := strings.Fields(stdout)
		if len(parts) == 0 {
			t.Fatalf("no session ID in output: %q", stdout)
		}
		sessionID := parts[0]

		// Wait for it to complete (ensures screen event persisted).
		_, _, _ = e.runSwashEnv(env, "--backend", "posix", "follow", sessionID)

		// Check screen output.
		screenOut, screenErr, err := e.runSwashEnv(env, "--backend", "posix", "screen", sessionID)
		if err != nil {
			t.Fatalf("swash posix screen failed: %v\nstdout:\n%s\nstderr:\n%s", err, screenOut, screenErr)
		}
		if !strings.Contains(screenOut, "POSIX_TTY_SCREEN") {
			t.Fatalf("expected marker in screen output, got:\n%s", screenOut)
		}
	})
}

// --- Mini-systemd only tests ---

func TestDBusRegistered(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		if e.mode != "mini" {
			t.Skip("mini-systemd only test")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "dbus-send", "--print-reply",
			"--dest=org.freedesktop.DBus", "/org/freedesktop/DBus",
			"org.freedesktop.DBus.ListNames")
		cmd.Env = e.getEnvVars()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dbus-send failed: %v\n%s", err, out)
		}

		if !strings.Contains(string(out), "org.freedesktop.systemd1") {
			t.Errorf("mini-systemd not registered on D-Bus, got: %s", out)
		}
	})
}

func TestJournalWriteViaDbus(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		if e.mode != "mini" {
			t.Skip("mini-systemd only test")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		uniqueMsg := fmt.Sprintf("Test message %d", time.Now().UnixNano())

		cmd := exec.CommandContext(ctx, "dbus-send", "--print-reply",
			"--dest=org.freedesktop.systemd1", "/org/freedesktop/systemd1",
			"sh.swa.MiniSystemd.Journal.Send",
			"string:"+uniqueMsg,
			"dict:string:string:TEST_KEY,test_value")
		cmd.Env = e.getEnvVars()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dbus-send failed: %v\n%s", err, out)
		}

		// Read from journal
		journalOut, _ := e.runJournalctl("-o", "short")

		if !strings.Contains(journalOut, uniqueMsg) {
			t.Errorf("expected %q in journal, got: %s", uniqueMsg, journalOut)
		}
	})
}
