// Package integration provides a test harness for running swash with mini-systemd.
package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// TestEnv represents a test environment with dbus-daemon and mini-systemd.
type TestEnv struct {
	// Paths
	TempDir       string
	SocketPath    string
	JournalDir    string
	JournalSocket string // Path to journal socket for journal.Send()

	// Processes
	dbusDaemon  *exec.Cmd
	miniSystemd *exec.Cmd

	// Environment variable for connecting to this bus
	BusAddress string
}

// NewTestEnv creates a new test environment.
func NewTestEnv() (*TestEnv, error) {
	tmpDir, err := os.MkdirTemp("", "swash-test-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	return &TestEnv{
		TempDir:    tmpDir,
		SocketPath: filepath.Join(tmpDir, "bus.sock"),
		JournalDir: filepath.Join(tmpDir, "journal"),
	}, nil
}

// Start launches dbus-daemon and mini-systemd.
func (e *TestEnv) Start(ctx context.Context) error {
	// Create journal directory
	if err := os.MkdirAll(e.JournalDir, 0750); err != nil {
		return fmt.Errorf("create journal dir: %w", err)
	}

	// Try to use /run/systemd/journal/socket so journal.Send() works
	// (go-systemd hardcodes this path)
	e.JournalSocket = "/run/systemd/journal/socket"
	if err := os.MkdirAll("/run/systemd/journal", 0755); err != nil {
		// Fall back to temp dir (journal.Send() won't work)
		e.JournalSocket = filepath.Join(e.TempDir, "journal.socket")
	}

	// Start dbus-daemon
	e.dbusDaemon = exec.CommandContext(ctx,
		"dbus-daemon",
		"--session",
		"--nofork",
		"--address=unix:path="+e.SocketPath,
		"--print-address",
	)
	e.dbusDaemon.Stderr = os.Stderr

	if err := e.dbusDaemon.Start(); err != nil {
		return fmt.Errorf("start dbus-daemon: %w", err)
	}

	e.BusAddress = "unix:path=" + e.SocketPath

	// Wait for socket to appear
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(e.SocketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Start mini-systemd with journal socket
	e.miniSystemd = exec.CommandContext(ctx,
		"mini-systemd",
		"--journal-dir="+e.JournalDir,
		"--journal-socket="+e.JournalSocket,
	)
	e.miniSystemd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+e.BusAddress)
	e.miniSystemd.Stderr = os.Stderr

	if err := e.miniSystemd.Start(); err != nil {
		e.dbusDaemon.Process.Kill()
		return fmt.Errorf("start mini-systemd: %w", err)
	}

	// Wait for mini-systemd to register on the bus and create socket
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(e.JournalSocket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	return nil
}

// Stop shuts down mini-systemd and dbus-daemon.
func (e *TestEnv) Stop() error {
	var errs []error

	if e.miniSystemd != nil && e.miniSystemd.Process != nil {
		e.miniSystemd.Process.Signal(os.Interrupt)
		e.miniSystemd.Wait()
	}

	if e.dbusDaemon != nil && e.dbusDaemon.Process != nil {
		e.dbusDaemon.Process.Kill()
		e.dbusDaemon.Wait()
	}

	if len(errs) > 0 {
		return fmt.Errorf("stop errors: %v", errs)
	}
	return nil
}

// Cleanup removes the temp directory.
func (e *TestEnv) Cleanup() error {
	return os.RemoveAll(e.TempDir)
}

// Env returns the environment variables needed to connect to this test environment.
func (e *TestEnv) Env() []string {
	return append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+e.BusAddress)
}

// RunSwash runs the swash binary with the given arguments.
func (e *TestEnv) RunSwash(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "swash", args...)
	cmd.Env = e.Env()
	output, err := cmd.CombinedOutput()
	return string(output), err
}
