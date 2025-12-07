package swash

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"
)

// runTestTask sets up fakes, runs a task to completion, and returns the fakes for assertions.
func runTestTask(t *testing.T, cmd FakeCommand, protocol Protocol) (*FakeJournal, *FakeSystemd) {
	t.Helper()

	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	systemd.RegisterCommand("test-cmd", cmd)

	host := NewHost(HostConfig{
		SessionID: "TEST01",
		Command:   []string{"test-cmd"},
		Protocol:  protocol,
		Systemd:   systemd,
		Journal:   journal,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := host.RunTask(ctx); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	return journal, systemd
}

// startTestTask starts a task in a goroutine and returns the Host for interaction.
// The caller must wait on the done channel for task completion.
func startTestTask(t *testing.T, systemd *FakeSystemd, journal *FakeJournal, cmd FakeCommand, protocol Protocol) (*Host, <-chan error) {
	t.Helper()

	systemd.RegisterCommand("test-cmd", cmd)

	host := NewHost(HostConfig{
		SessionID: "TEST01",
		Command:   []string{"test-cmd"},
		Protocol:  protocol,
		Systemd:   systemd,
		Journal:   journal,
	})

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- host.RunTask(ctx)
	}()

	return host, done
}

func TestHost_OutputCapture(t *testing.T) {
	journal, _ := runTestTask(t, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		fmt.Fprintln(stdout, "hello")
		return 0
	}, ProtocolShell)

	entries := journal.Entries()

	// Find the output entry (not lifecycle events)
	var found bool
	for _, e := range entries {
		if e.Message == "hello" && e.Fields["FD"] == "1" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("expected journal entry with Message='hello' and FD='1', got entries: %+v", entries)
	}
}

func TestHost_StderrCapture(t *testing.T) {
	journal, _ := runTestTask(t, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		fmt.Fprintln(stderr, "error message")
		return 0
	}, ProtocolShell)

	entries := journal.Entries()

	var found bool
	for _, e := range entries {
		if e.Message == "error message" && e.Fields["FD"] == "2" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("expected journal entry with Message='error message' and FD='2', got entries: %+v", entries)
	}
}

func TestHost_ExitCode(t *testing.T) {
	journal, systemd := runTestTask(t, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		return 42
	}, ProtocolShell)

	// Check unit exit status
	ctx := context.Background()
	unit, err := systemd.GetUnit(ctx, TaskUnit("TEST01"))
	if err != nil {
		t.Fatalf("GetUnit failed: %v", err)
	}
	if unit.ExitStatus != 42 {
		t.Errorf("expected unit.ExitStatus=42, got %d", unit.ExitStatus)
	}

	// Check journal exited event
	entries := journal.Entries()
	var found bool
	for _, e := range entries {
		if e.Fields[FieldEvent] == EventExited && e.Fields[FieldExitCode] == "42" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected exited event with SWASH_EXIT_CODE='42', got entries: %+v", entries)
	}
}

func TestHost_LifecycleEvents(t *testing.T) {
	journal, _ := runTestTask(t, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		return 0
	}, ProtocolShell)

	entries := journal.Entries()

	var hasStarted, hasExited bool
	for _, e := range entries {
		switch e.Fields[FieldEvent] {
		case EventStarted:
			hasStarted = true
			if e.Fields[FieldSession] != "TEST01" {
				t.Errorf("started event has wrong session ID: %s", e.Fields[FieldSession])
			}
		case EventExited:
			hasExited = true
			if e.Fields[FieldSession] != "TEST01" {
				t.Errorf("exited event has wrong session ID: %s", e.Fields[FieldSession])
			}
		}
	}

	if !hasStarted {
		t.Error("missing 'started' lifecycle event")
	}
	if !hasExited {
		t.Error("missing 'exited' lifecycle event")
	}
}

func TestHost_SendInput(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	// Command that reads from stdin and echoes to stdout
	inputReceived := make(chan string, 1)
	host, done := startTestTask(t, systemd, journal, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		buf := make([]byte, 100)
		n, err := stdin.Read(buf)
		if err != nil && err != io.EOF {
			fmt.Fprintf(stderr, "read error: %v", err)
			return 1
		}
		if n > 0 {
			inputReceived <- string(buf[:n])
			fmt.Fprintf(stdout, "got: %s", buf[:n])
		}
		return 0
	}, ProtocolShell)

	// Wait a bit for task to start and be ready to receive input
	time.Sleep(10 * time.Millisecond)

	// Send input
	n, err := host.SendInput("hello")
	if err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}
	if n != 5 {
		t.Errorf("expected 5 bytes written, got %d", n)
	}

	// Close stdin to let the command finish
	host.mu.Lock()
	if host.stdin != nil {
		host.stdin.Close()
	}
	host.mu.Unlock()

	// Wait for task to complete
	if err := <-done; err != nil {
		t.Fatalf("task failed: %v", err)
	}

	// Verify input was received
	select {
	case received := <-inputReceived:
		if received != "hello" {
			t.Errorf("expected input 'hello', got %q", received)
		}
	default:
		t.Error("command did not receive input")
	}

	// Verify echoed output in journal
	entries := journal.Entries()
	var found bool
	for _, e := range entries {
		if e.Message == "got: hello" && e.Fields["FD"] == "1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected echoed output in journal, got entries: %+v", entries)
	}
}

func TestHost_SendInput_NoProcess(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewHost(HostConfig{
		SessionID: "TEST01",
		Command:   []string{"test-cmd"},
		Protocol:  ProtocolShell,
		Systemd:   systemd,
		Journal:   journal,
	})

	// Try to send input before task is started
	_, err := host.SendInput("hello")
	if err == nil {
		t.Error("expected error when sending input with no process")
	}
}

func TestHost_Kill(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	// Command that blocks until context is cancelled
	host, done := startTestTask(t, systemd, journal, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		<-ctx.Done()
		return 0
	}, ProtocolShell)

	// Wait a bit for task to start
	time.Sleep(10 * time.Millisecond)

	// Kill the task
	err := host.Kill()
	if err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	// Wait for task to complete
	<-done

	// Verify unit state
	ctx := context.Background()
	unit, err := systemd.GetUnit(ctx, TaskUnit("TEST01"))
	if err != nil {
		t.Fatalf("GetUnit failed: %v", err)
	}
	if unit.State != UnitStateFailed && unit.State != UnitStateInactive {
		t.Errorf("expected unit state failed or inactive, got %s", unit.State)
	}
}

func TestHost_Gist(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	// Command that blocks until context is cancelled
	blocked := make(chan struct{})
	host, done := startTestTask(t, systemd, journal, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		close(blocked)
		<-ctx.Done()
		return 42
	}, ProtocolShell)

	// Wait for task to start
	<-blocked

	// Check Gist while running
	status, err := host.Gist()
	if err != nil {
		t.Fatalf("Gist failed: %v", err)
	}
	if !status.Running {
		t.Error("expected Running=true while task is running")
	}
	if status.ExitCode != nil {
		t.Errorf("expected ExitCode=nil while running, got %d", *status.ExitCode)
	}

	// Kill the task to let it finish
	host.Kill()
	<-done

	// Check Gist after exit
	status, err = host.Gist()
	if err != nil {
		t.Fatalf("Gist failed: %v", err)
	}
	if status.Running {
		t.Error("expected Running=false after task exits")
	}
	// Note: exit code might be 0 since we killed it, not 42
}
