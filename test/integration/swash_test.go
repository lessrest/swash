package integration

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ensureBinaries builds swash and mini-systemd if needed
func ensureBinaries(t *testing.T) {
	t.Helper()

	// Check if binaries exist in PATH or build them
	if _, err := exec.LookPath("swash"); err != nil {
		t.Log("Building swash...")
		cmd := exec.Command("go", "build", "-o", "/tmp/bin/swash", "./cmd/swash/")
		cmd.Dir = "../.."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to build swash: %v\n%s", err, out)
		}
	}

	if _, err := exec.LookPath("mini-systemd"); err != nil {
		t.Log("Building mini-systemd...")
		cmd := exec.Command("go", "build", "-o", "/tmp/bin/mini-systemd", "./cmd/mini-systemd/")
		cmd.Dir = "../.."
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
