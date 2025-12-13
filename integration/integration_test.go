// Package integration contains black-box integration tests for swash.
// Run with: go test ./integration/...
package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mbrock/swash/pkg/journalfile"
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
	mode       string // "real" or "posix"
	journalCmd string
	journalDir string
	rootSlice  string // unique slice for test isolation

	// posix-specific
	journaldCmd   *exec.Cmd
	journalSocket string
	stateDir      string
	runtimeDir    string
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
	// - stateDir: persistent data (journal files)
	// - runtimeDir: ephemeral data (sockets, session metadata)
	e.stateDir = filepath.Join(e.tmpDir, "state")
	e.runtimeDir = filepath.Join(e.tmpDir, "runtime")
	e.journalSocket = filepath.Join(e.runtimeDir, "journal.socket")
	e.journalDir = e.stateDir // journal file goes here

	if err := os.MkdirAll(e.stateDir, 0755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	if err := os.MkdirAll(e.runtimeDir, 0755); err != nil {
		return fmt.Errorf("creating runtime dir: %w", err)
	}

	// Start "swash minijournald" daemon for the test suite (uses already-built swash binary)
	journalPath := filepath.Join(e.stateDir, "swash.journal")
	e.journaldCmd = exec.Command(e.swashBin, "minijournald",
		"--socket", e.journalSocket,
		"--journal", journalPath,
	)
	e.journaldCmd.Env = os.Environ()
	e.journaldCmd.Stdout = os.Stdout
	e.journaldCmd.Stderr = os.Stderr
	e.journaldCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := e.journaldCmd.Start(); err != nil {
		return fmt.Errorf("starting swash minijournald: %w", err)
	}

	// Wait for socket to appear
	for range 50 {
		if _, err := os.Stat(e.journalSocket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(e.journalSocket); err != nil {
		return fmt.Errorf("swash minijournald socket did not appear: %w", err)
	}

	return nil
}

func (e *testEnv) cleanup() {
	withTimeout("cleanup", 5*time.Second, func() {
		// Stop the test slice and all children (real systemd mode)
		// Use SIGKILL because SIGTERM doesn't reliably terminate host processes
		if e.mode == "real" && e.rootSlice != "" {
			runCmd(3*time.Second, "systemctl", "--user", "kill", "--signal=SIGKILL", e.rootSlice+".slice")
			runCmd(3*time.Second, "systemctl", "--user", "reset-failed")
		}

		// Gracefully stop "swash minijournald" for posix mode
		if e.journaldCmd != nil && e.journaldCmd.Process != nil {
			syscall.Kill(e.journaldCmd.Process.Pid, syscall.SIGTERM)
			time.Sleep(100 * time.Millisecond)
			syscall.Kill(-e.journaldCmd.Process.Pid, syscall.SIGKILL)
			e.journaldCmd.Wait()
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
		// Also isolate runtime dir so graph tests don't use system's graph service.
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

// journalReaderMode determines how to read journal files in tests.
// Set SWASH_TEST_JOURNAL_READER=journalctl to force journalctl,
// SWASH_TEST_JOURNAL_READER=native to force pure Go reader,
// or leave empty for auto-detect (journalctl if available, else native).
func journalReaderMode() string {
	mode := os.Getenv("SWASH_TEST_JOURNAL_READER")
	if mode != "" {
		return mode
	}
	// Auto-detect: use journalctl if available
	if _, err := exec.LookPath("journalctl"); err == nil {
		return "journalctl"
	}
	return "native"
}

// runJournalctl runs journalctl with the appropriate arguments for the mode,
// or falls back to pure Go reader if journalctl is not available.
func (e *testEnv) runJournalctl(args ...string) (string, error) {
	readerMode := journalReaderMode()

	// For "real" mode with system journal, we must use journalctl
	if e.mode == "real" && readerMode == "native" {
		return "", fmt.Errorf("native reader cannot read system journal; install journalctl or use SWASH_TEST_MODE=posix")
	}

	// Collect journal files for posix mode
	var journalFiles []string
	if e.mode == "posix" {
		// Posix mode uses a single shared journal file
		sharedJournal := filepath.Join(e.tmpDir, "state", "swash.journal")
		if _, err := os.Stat(sharedJournal); err != nil {
			return "", fmt.Errorf("shared journal file not found: %s", sharedJournal)
		}
		journalFiles = []string{sharedJournal}
	}

	if readerMode == "native" && len(journalFiles) > 0 {
		return e.readJournalNative(journalFiles, args...)
	}

	// Use journalctl
	var baseArgs []string
	switch e.mode {
	case "posix":
		for _, f := range journalFiles {
			baseArgs = append(baseArgs, "--file="+f)
		}
	default:
		baseArgs = []string{"--user"}
	}
	out, err := runCmd(5*time.Second, "journalctl", append(baseArgs, args...)...)
	return string(out), err
}

// readJournalNative reads journal files using our pure Go reader.
// Supports a subset of journalctl args: -o cat, FIELD=VALUE filters.
func (e *testEnv) readJournalNative(files []string, args ...string) (string, error) {
	// Parse args for output format and filters
	outputFormat := "short"
	var filters []struct{ field, value string }

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-o" && i+1 < len(args) {
			outputFormat = args[i+1]
			i++
		} else if strings.Contains(arg, "=") && !strings.HasPrefix(arg, "-") {
			parts := strings.SplitN(arg, "=", 2)
			filters = append(filters, struct{ field, value string }{parts[0], parts[1]})
		}
	}

	var lines []string
	for _, path := range files {
		r, err := journalfile.OpenRead(path)
		if err != nil {
			continue // skip unreadable files
		}

		// Apply filters
		for _, f := range filters {
			r.AddMatch(f.field, f.value)
		}

		for {
			entry, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}

			switch outputFormat {
			case "cat":
				lines = append(lines, entry.Fields["MESSAGE"])
			case "short":
				lines = append(lines, fmt.Sprintf("%s %s",
					entry.Realtime.Format("Jan 02 15:04:05"),
					entry.Fields["MESSAGE"]))
			default:
				lines = append(lines, entry.Fields["MESSAGE"])
			}
		}
		r.Close()
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
		stdout, stderr, err := e.runSwash("run", "echo", "hello world")
		if err != nil {
			t.Fatalf("swash run failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
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

// --- Context tests ---

func TestContextNew(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		stdout, stderr, err := e.runSwash("context", "new")
		if err != nil {
			t.Fatalf("swash context new failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}

		if !strings.Contains(stdout, "created") {
			t.Errorf("expected 'created' in output, got: %s", stdout)
		}

		// Extract context ID (first word)
		parts := strings.Fields(stdout)
		if len(parts) == 0 {
			t.Fatal("no context ID in output")
		}
		contextID := parts[0]

		// Verify directory was created
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(lines) < 2 {
			t.Fatalf("expected directory path in output, got: %s", stdout)
		}
		contextDir := lines[1]

		if _, err := os.Stat(contextDir); err != nil {
			t.Errorf("context directory not created: %v", err)
		}

		// Verify context appears in list
		listOut, _, err := e.runSwash("context", "list")
		if err != nil {
			t.Fatalf("swash context list failed: %v", err)
		}
		if !strings.Contains(listOut, contextID) {
			t.Errorf("expected context %s in list, got: %s", contextID, listOut)
		}
	})
}

func TestContextWorkingDirectory(t *testing.T) {
	runTest(t, func(t *testing.T, e *testEnv) {
		// Create context
		stdout, _, err := e.runSwash("context", "new")
		if err != nil {
			t.Fatalf("swash context new failed: %v", err)
		}
		parts := strings.Fields(stdout)
		contextID := parts[0]

		// Get context directory
		dirOut, _, _ := e.runSwash("context", "dir", contextID)
		contextDir := strings.TrimSpace(dirOut)

		// Run pwd in context - should be context directory
		env := map[string]string{"SWASH_CONTEXT": contextID}
		stdout, _, err = e.runSwashEnv(env, "run", "pwd")
		if err != nil {
			t.Fatalf("swash run pwd failed: %v", err)
		}
		if strings.TrimSpace(stdout) != contextDir {
			t.Errorf("expected working dir %s, got: %s", contextDir, stdout)
		}
	})
}
