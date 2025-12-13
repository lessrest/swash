package host

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/mbrock/swash/internal/executor"
	"github.com/mbrock/swash/internal/journal"
	"github.com/mbrock/swash/internal/protocol"
)

// runTestTask sets up fakes, runs a task to completion, and returns the journal for assertions.
func runTestTask(t *testing.T, cmd executor.FakeCommand, proto protocol.Protocol) *journal.FakeJournal {
	t.Helper()

	exec := executor.NewFakeExecutor()
	fj := journal.NewFakeJournal()
	exec.RegisterCommand("test-cmd", cmd)

	host := NewHost(HostConfig{
		SessionID: "TEST01",
		Command:   []string{"test-cmd"},
		Protocol:  proto,
		Events:    fj,
		Executor:  exec,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := host.RunTask(ctx); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	return fj
}

// startTestTask starts a task in a goroutine and returns the Host for interaction.
// The caller must wait on the done channel for task completion.
func startTestTask(t *testing.T, exec *executor.FakeExecutor, fj *journal.FakeJournal, cmd executor.FakeCommand, proto protocol.Protocol) (*Host, <-chan error) {
	t.Helper()

	exec.RegisterCommand("test-cmd", cmd)

	host := NewHost(HostConfig{
		SessionID: "TEST01",
		Command:   []string{"test-cmd"},
		Protocol:  proto,
		Events:    fj,
		Executor:  exec,
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
	fj := runTestTask(t, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		fmt.Fprintln(stdout, "hello")
		return 0
	}, protocol.ProtocolShell)

	entries := fj.Entries()

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
	fj := runTestTask(t, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		fmt.Fprintln(stderr, "error message")
		return 0
	}, protocol.ProtocolShell)

	entries := fj.Entries()

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
	fj := runTestTask(t, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		return 42
	}, protocol.ProtocolShell)

	// Check journal exited event
	entries := fj.Entries()
	var found bool
	for _, e := range entries {
		if e.Fields[journal.FieldEvent] == journal.EventExited && e.Fields[journal.FieldExitCode] == "42" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected exited event with SWASH_EXIT_CODE='42', got entries: %+v", entries)
	}
}

func TestHost_LifecycleEvents(t *testing.T) {
	fj := runTestTask(t, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		return 0
	}, protocol.ProtocolShell)

	entries := fj.Entries()

	var hasStarted, hasExited bool
	for _, e := range entries {
		switch e.Fields[journal.FieldEvent] {
		case journal.EventStarted:
			hasStarted = true
			if e.Fields[journal.FieldSession] != "TEST01" {
				t.Errorf("started event has wrong session ID: %s", e.Fields[journal.FieldSession])
			}
		case journal.EventExited:
			hasExited = true
			if e.Fields[journal.FieldSession] != "TEST01" {
				t.Errorf("exited event has wrong session ID: %s", e.Fields[journal.FieldSession])
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
	exec := executor.NewFakeExecutor()
	fj := journal.NewFakeJournal()

	// Command that reads from stdin and echoes to stdout
	inputReceived := make(chan string, 1)
	host, done := startTestTask(t, exec, fj, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
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
	}, protocol.ProtocolShell)

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
	entries := fj.Entries()
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
	fj := journal.NewFakeJournal()

	host := NewHost(HostConfig{
		SessionID: "TEST01",
		Command:   []string{"test-cmd"},
		Protocol:  protocol.ProtocolShell,
		Events:    fj,
	})

	// Try to send input before task is started
	_, err := host.SendInput("hello")
	if err == nil {
		t.Error("expected error when sending input with no process")
	}
}

func countOpenFDs(t *testing.T) int {
	t.Helper()

	// This test helper relies on procfs.
	if runtime.GOOS != "linux" {
		t.Skip("fd counting via /proc is linux-only")
	}

	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("unable to read /proc/self/fd: %v", err)
	}
	return len(ents)
}

func TestHost_StartTaskProcess_NoFDLeakOnStartError(t *testing.T) {
	fj := journal.NewFakeJournal()
	exec := executor.NewFakeExecutor()
	// Don't register the command; FakeExecutor will fail with "executable not found".

	host := NewHost(HostConfig{
		SessionID: "TEST01",
		Command:   []string{"missing-cmd"},
		Protocol:  protocol.ProtocolShell,
		Events:    fj,
		Executor:  exec,
	})

	// Run once and then use that as baseline. This avoids false positives if the
	// runtime opens any one-time fds during the first call.
	_, err := host.startTaskProcess()
	if err == nil {
		t.Fatalf("expected startTaskProcess to fail, got nil error")
	}

	after1 := countOpenFDs(t)

	_, err = host.startTaskProcess()
	if err == nil {
		t.Fatalf("expected startTaskProcess to fail, got nil error (second call)")
	}

	after2 := countOpenFDs(t)
	if after2 != after1 {
		t.Fatalf("fd leak on Start() error: after1=%d after2=%d", after1, after2)
	}
}

func TestHost_Kill(t *testing.T) {
	exec := executor.NewFakeExecutor()
	fj := journal.NewFakeJournal()

	// Command that blocks until context is cancelled
	host, done := startTestTask(t, exec, fj, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		<-ctx.Done()
		return 0
	}, protocol.ProtocolShell)

	// Wait a bit for task to start
	time.Sleep(10 * time.Millisecond)

	// Kill the task
	err := host.Kill()
	if err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	// Wait for task to complete
	<-done

	// Verify the host reports not running
	status, err := host.Gist()
	if err != nil {
		t.Fatalf("Gist failed: %v", err)
	}
	if status.Running {
		t.Error("expected Running=false after kill")
	}
}

func TestHost_Gist(t *testing.T) {
	exec := executor.NewFakeExecutor()
	fj := journal.NewFakeJournal()

	// Command that blocks until context is cancelled
	blocked := make(chan struct{})
	host, done := startTestTask(t, exec, fj, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		close(blocked)
		<-ctx.Done()
		return 42
	}, protocol.ProtocolShell)

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
