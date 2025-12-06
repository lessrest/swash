package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ensureBinaries builds swash and mini-systemd if needed and adds them to PATH
func ensureBinaries(t *testing.T) {
	t.Helper()

	binDir := "/tmp/bin"

	// Add bin directory to PATH so harness can find binaries
	currentPath := os.Getenv("PATH")
	if currentPath == "" {
		os.Setenv("PATH", binDir)
	} else {
		os.Setenv("PATH", binDir+":"+currentPath)
	}

	// Create bin directory
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}

	// Get project root for CGO_CFLAGS (vendored systemd headers)
	projectRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("failed to get project root: %v", err)
	}
	cgoFlags := "-I" + filepath.Join(projectRoot, "cvendor")

	// Build swash if not present
	swashPath := filepath.Join(binDir, "swash")
	if _, err := os.Stat(swashPath); os.IsNotExist(err) {
		t.Log("Building swash...")
		cmd := exec.Command("go", "build", "-o", swashPath, "./cmd/swash/")
		cmd.Dir = "../.."
		cmd.Env = append(os.Environ(), "CGO_CFLAGS="+cgoFlags)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to build swash: %v\n%s", err, out)
		}
	}

	// Build mini-systemd if not present
	miniSystemdPath := filepath.Join(binDir, "mini-systemd")
	if _, err := os.Stat(miniSystemdPath); os.IsNotExist(err) {
		t.Log("Building mini-systemd...")
		cmd := exec.Command("go", "build", "-o", miniSystemdPath, "./cmd/mini-systemd/")
		cmd.Dir = "../.."
		cmd.Env = append(os.Environ(), "CGO_CFLAGS="+cgoFlags)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to build mini-systemd: %v\n%s", err, out)
		}
	}
}

func TestSwashJournalWrite(t *testing.T) {
	ensureBinaries(t)

	env, err := NewTestEnv()
	if err != nil {
		t.Fatalf("NewTestEnv: %v", err)
	}
	defer env.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := env.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer env.Stop()

	// Test: write to journal via D-Bus
	cmd := exec.CommandContext(ctx, "dbus-send",
		"--print-reply",
		"--dest=org.freedesktop.systemd1",
		"/org/freedesktop/systemd1",
		"sh.swa.MiniSystemd.Journal.Send",
		"string:Test message from Go test",
		"dict:string:string:TEST_KEY,test_value",
	)
	cmd.Env = env.Env()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dbus-send failed: %v\n%s", err, out)
	}

	// Verify: read with journalctl
	cmd = exec.CommandContext(ctx, "journalctl",
		"--file="+env.JournalDir+"/*.journal",
		"-o", "short",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("journalctl failed: %v\n%s", err, out)
	}

	if !strings.Contains(string(out), "Test message from Go test") {
		t.Errorf("expected message not found in journal output:\n%s", out)
	}
}

func TestSwashRunEcho(t *testing.T) {
	ensureBinaries(t)

	env, err := NewTestEnv()
	if err != nil {
		t.Fatalf("NewTestEnv: %v", err)
	}
	defer env.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := env.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer env.Stop()

	// Run swash
	output, err := env.RunSwash(ctx, "run", "echo", "hello from test")
	if err != nil {
		t.Fatalf("swash run failed: %v\n%s", err, output)
	}

	// Should print session ID
	if !strings.Contains(output, "started") {
		t.Errorf("expected 'started' in output, got: %s", output)
	}

	// Extract session ID (format: "XXXXXX started")
	parts := strings.Fields(output)
	if len(parts) < 1 {
		t.Fatalf("unexpected output format: %s", output)
	}
	sessionID := parts[0]
	t.Logf("Session ID: %s", sessionID)

	// Wait a moment for the process to complete
	time.Sleep(500 * time.Millisecond)

	// Check journal for output (via D-Bus interface since file might still be locked)
	cmd := exec.CommandContext(ctx, "dbus-send",
		"--print-reply",
		"--dest=org.freedesktop.systemd1",
		"/org/freedesktop/systemd1",
		"sh.swa.MiniSystemd.Journal.GetEntries",
	)
	cmd.Env = env.Env()
	journalOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Warning: GetEntries failed: %v", err)
	} else {
		t.Logf("Journal entries:\n%s", journalOut)
	}
}

// TestTaskOutputCapture tests that task stdout/stderr is captured in the journal.
// This test is expected to FAIL until we implement proper output capture.
//
// The issue: swash host reads task output via pipes and calls journal.Send(),
// which writes to /run/systemd/journal/socket (real journald). In our test
// environment with mini-systemd, there's no real journald, so output is lost.
//
// To fix: swash host should write output to mini-systemd's journal via D-Bus
// when running under mini-systemd (detect via checking if systemd1 bus name
// is mini-systemd, or via environment variable).
func TestTaskOutputCapture(t *testing.T) {
	ensureBinaries(t)

	env, err := NewTestEnv()
	if err != nil {
		t.Fatalf("NewTestEnv: %v", err)
	}
	defer env.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := env.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer env.Stop()

	// Run a command that produces distinctive output
	const testMessage = "UNIQUE_TEST_OUTPUT_12345"
	output, err := env.RunSwash(ctx, "run", "echo", testMessage)
	if err != nil {
		t.Fatalf("swash run failed: %v\n%s", err, output)
	}

	// Extract session ID
	parts := strings.Fields(output)
	if len(parts) < 1 {
		t.Fatalf("unexpected output format: %s", output)
	}
	sessionID := parts[0]
	t.Logf("Session ID: %s", sessionID)

	// Wait for task to complete and output to be captured
	time.Sleep(1 * time.Second)

	// Query mini-systemd's journal for the output
	cmd := exec.CommandContext(ctx, "dbus-send",
		"--print-reply",
		"--dest=org.freedesktop.systemd1",
		"/org/freedesktop/systemd1",
		"sh.swa.MiniSystemd.Journal.GetEntries",
	)
	cmd.Env = env.Env()
	journalOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("GetEntries failed: %v\n%s", err, journalOut)
	}
	t.Logf("Journal entries:\n%s", journalOut)

	// The test: task output should appear in mini-systemd's journal
	if !strings.Contains(string(journalOut), testMessage) {
		t.Errorf("Task output not captured in journal!\n"+
			"Expected to find %q in journal entries.\n"+
			"This fails because swash host uses journal.Send() which writes to\n"+
			"/run/systemd/journal/socket (real journald), not mini-systemd's D-Bus journal.\n"+
			"Journal contents:\n%s", testMessage, journalOut)
	}
}

// TestNewlineSplitting verifies that multi-line output is split into separate
// journal entries (one per line), matching systemd/journald semantics.
//
// From systemd docs: "When stdout/stderr is connected to the journal, output is
// logged line by line. Each newline terminates a journal entry."
//
// Note: This test also requires fixing the output capture issue first.
func TestNewlineSplitting(t *testing.T) {
	ensureBinaries(t)

	env, err := NewTestEnv()
	if err != nil {
		t.Fatalf("NewTestEnv: %v", err)
	}
	defer env.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := env.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer env.Stop()

	// Create a test script that produces multiple lines
	// This avoids the pflag issue with dash-prefixed arguments
	testScript := filepath.Join(env.TempDir, "multiline.sh")
	if err := os.WriteFile(testScript, []byte("#!/bin/sh\necho LINE_ONE\necho LINE_TWO\necho LINE_THREE\n"), 0755); err != nil {
		t.Fatalf("creating test script: %v", err)
	}

	output, err := env.RunSwash(ctx, "run", testScript)
	if err != nil {
		t.Fatalf("swash run failed: %v\n%s", err, output)
	}

	// Wait for output to be captured
	time.Sleep(1 * time.Second)

	// Query journal
	cmd := exec.CommandContext(ctx, "dbus-send",
		"--print-reply",
		"--dest=org.freedesktop.systemd1",
		"/org/freedesktop/systemd1",
		"sh.swa.MiniSystemd.Journal.GetEntries",
	)
	cmd.Env = env.Env()
	journalOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("GetEntries failed: %v\n%s", err, journalOut)
	}
	t.Logf("Journal entries:\n%s", journalOut)

	journalStr := string(journalOut)

	// Each line should appear as a separate MESSAGE in the journal
	// (not concatenated as "LINE_ONE\nLINE_TWO\nLINE_THREE")
	lines := []string{"LINE_ONE", "LINE_TWO", "LINE_THREE"}
	for _, line := range lines {
		if !strings.Contains(journalStr, line) {
			t.Errorf("Expected line %q not found in journal.\n"+
				"This test requires output capture to work first.\n"+
				"Journal contents:\n%s", line, journalStr)
		}
	}

	// Verify lines are NOT concatenated (they should be separate entries)
	if strings.Contains(journalStr, "LINE_ONE\\nLINE_TWO") ||
		strings.Contains(journalStr, "LINE_ONELINE_TWO") {
		t.Errorf("Lines appear concatenated instead of split into separate entries.\n"+
			"systemd/journald splits stdout on newlines, each line becomes one entry.\n"+
			"Journal contents:\n%s", journalStr)
	}
}

func TestEnvStartStop(t *testing.T) {
	ensureBinaries(t)

	env, err := NewTestEnv()
	if err != nil {
		t.Fatalf("NewTestEnv: %v", err)
	}
	defer env.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := env.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Verify dbus-daemon is accessible
	cmd := exec.CommandContext(ctx, "dbus-send",
		"--print-reply",
		"--dest=org.freedesktop.DBus",
		"/org/freedesktop/DBus",
		"org.freedesktop.DBus.ListNames",
	)
	cmd.Env = env.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dbus-send failed: %v\n%s", err, out)
	}

	// Should have systemd1 registered
	if !strings.Contains(string(out), "org.freedesktop.systemd1") {
		t.Errorf("expected org.freedesktop.systemd1 in bus names:\n%s", out)
	}

	if err := env.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}
