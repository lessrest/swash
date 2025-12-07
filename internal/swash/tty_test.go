package swash

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestTTYHost_NewTTYHost(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	// Check initial state
	status, err := host.Gist()
	if err != nil {
		t.Fatalf("Gist failed: %v", err)
	}
	if status.Running {
		t.Error("expected Running=false before task starts")
	}
	if status.ExitCode != nil {
		t.Error("expected ExitCode=nil before task starts")
	}
	if len(status.Command) != 1 || status.Command[0] != "bash" {
		t.Errorf("expected Command=[bash], got %v", status.Command)
	}
}

func TestTTYHost_SessionID(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewTTYHost(TTYHostConfig{
		SessionID: "ABC123",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	id, err := host.SessionID()
	if err != nil {
		t.Fatalf("SessionID failed: %v", err)
	}
	if id != "ABC123" {
		t.Errorf("expected SessionID=ABC123, got %s", id)
	}
}

func TestTTYHost_DefaultSize(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	// Test with zero values - should default to 24x80
	host := NewTTYHost(TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      0,
		Cols:      0,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	// The vterm should be created with default size
	text, err := host.GetScreenText()
	if err != nil {
		t.Fatalf("GetScreenText failed: %v", err)
	}

	// Empty screen should have some content (empty lines)
	lines := strings.Split(text, "\n")
	if len(lines) != 24 {
		t.Errorf("expected 24 lines (default rows), got %d", len(lines))
	}
}

func TestTTYHost_GetScreenText(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      5,
		Cols:      40,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	// Write directly to vterm (simulating process output)
	host.vt.Write([]byte("Hello, World!"))

	text, err := host.GetScreenText()
	if err != nil {
		t.Fatalf("GetScreenText failed: %v", err)
	}

	if !strings.Contains(text, "Hello, World!") {
		t.Errorf("expected screen to contain 'Hello, World!', got %q", text)
	}
}

func TestTTYHost_GetScreenANSI(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      5,
		Cols:      40,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	// Write colored text directly to vterm
	host.vt.Write([]byte("\x1b[31mRed\x1b[0m"))

	ansi, err := host.GetScreenANSI()
	if err != nil {
		t.Fatalf("GetScreenANSI failed: %v", err)
	}

	// Should contain ANSI escape sequences
	if !strings.Contains(ansi, "\x1b[") {
		t.Error("expected ANSI escape sequences in output")
	}
	if !strings.Contains(ansi, "Red") {
		t.Error("expected 'Red' in output")
	}
}

func TestTTYHost_GetCursor(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	// Initial cursor should be at 0,0
	row, col, err := host.GetCursor()
	if err != nil {
		t.Fatalf("GetCursor failed: %v", err)
	}
	if row != 0 || col != 0 {
		t.Errorf("expected cursor at (0,0), got (%d,%d)", row, col)
	}

	// Write some text
	host.vt.Write([]byte("Hello"))

	row, col, err = host.GetCursor()
	if err != nil {
		t.Fatalf("GetCursor failed: %v", err)
	}
	if row != 0 || col != 5 {
		t.Errorf("expected cursor at (0,5), got (%d,%d)", row, col)
	}
}

func TestTTYHost_Resize(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	// Resize to 10x40
	err := host.Resize(10, 40)
	if err != nil {
		t.Fatalf("Resize failed: %v", err)
	}

	// Check internal state
	host.mu.Lock()
	rows, cols := host.rows, host.cols
	host.mu.Unlock()

	if rows != 10 || cols != 40 {
		t.Errorf("expected size (10,40), got (%d,%d)", rows, cols)
	}

	// Screen should now have 10 lines
	text, _ := host.GetScreenText()
	lines := strings.Split(text, "\n")
	if len(lines) != 10 {
		t.Errorf("expected 10 lines after resize, got %d", len(lines))
	}
}

func TestTTYHost_GetTitle(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	// Initial title should be empty
	title, err := host.GetTitle()
	if err != nil {
		t.Fatalf("GetTitle failed: %v", err)
	}
	if title != "" {
		t.Errorf("expected empty title, got %q", title)
	}

	// Set title via OSC sequence
	host.vt.Write([]byte("\x1b]0;My Title\x07"))

	title, err = host.GetTitle()
	if err != nil {
		t.Fatalf("GetTitle failed: %v", err)
	}
	if title != "My Title" {
		t.Errorf("expected title 'My Title', got %q", title)
	}
}

func TestTTYHost_GetMode(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	// Initial mode should be normal (not alternate screen)
	alt, err := host.GetMode()
	if err != nil {
		t.Fatalf("GetMode failed: %v", err)
	}
	if alt {
		t.Error("expected alternate screen to be false initially")
	}

	// Switch to alternate screen
	host.vt.Write([]byte("\x1b[?1049h"))

	alt, err = host.GetMode()
	if err != nil {
		t.Fatalf("GetMode failed: %v", err)
	}
	if !alt {
		t.Error("expected alternate screen to be true after switch")
	}

	// Switch back
	host.vt.Write([]byte("\x1b[?1049l"))

	alt, err = host.GetMode()
	if err != nil {
		t.Fatalf("GetMode failed: %v", err)
	}
	if alt {
		t.Error("expected alternate screen to be false after switch back")
	}
}

func TestTTYHost_GetScrollback(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      3, // Small screen to trigger scrollback
		Cols:      40,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	// Write enough lines to trigger scrollback
	for i := 1; i <= 5; i++ {
		host.vt.Write([]byte("Line\r\n"))
	}

	// Should have some lines in scrollback (lines that scrolled off)
	lines, err := host.GetScrollback(10)
	if err != nil {
		t.Fatalf("GetScrollback failed: %v", err)
	}

	// With 3 rows and 5 lines written, ~2 should have scrolled off
	if len(lines) < 1 {
		t.Errorf("expected some scrollback lines, got %d", len(lines))
	}
}

func TestTTYHost_SendInput_NoProcess(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Systemd:   systemd,
		Journal:   journal,
	})
	defer host.Close()

	// Try to send input before task is started
	_, err := host.SendInput("hello")
	if err == nil {
		t.Error("expected error when sending input with no process")
	}
}

func TestTTYHost_RunTask_WithFakePTY(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	// Register a simple echo command
	systemd.RegisterCommand("echo", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		// Write output - this goes to the slave side which feeds the master
		if len(args) > 1 {
			fmt.Fprintln(stdout, args[1])
		}
		return 0
	})

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TTYTEST1",
		Command:   []string{"echo", "Hello from TTY"},
		Rows:      10,
		Cols:      40,
		Systemd:   systemd,
		Journal:   journal,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	ctx := context.Background()
	err := host.RunTask(ctx)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	// Check status
	status, err := host.Gist()
	if err != nil {
		t.Fatalf("Gist failed: %v", err)
	}
	if status.Running {
		t.Error("expected Running=false after task completes")
	}
	if status.ExitCode == nil || *status.ExitCode != 0 {
		t.Errorf("expected ExitCode=0, got %v", status.ExitCode)
	}

	// Check that screen captured the output
	screen, _ := host.GetScreenText()
	if !strings.Contains(screen, "Hello from TTY") {
		t.Errorf("expected screen to contain output, got:\n%s", screen)
	}

	// Check journal for lifecycle events
	entries := journal.Entries()
	var hasStarted, hasExited, hasScreen bool
	for _, e := range entries {
		switch e.Fields["SWASH_EVENT"] {
		case "started":
			hasStarted = true
		case "exited":
			hasExited = true
		case "screen":
			hasScreen = true
		}
	}
	if !hasStarted {
		t.Error("expected 'started' event in journal")
	}
	if !hasExited {
		t.Error("expected 'exited' event in journal")
	}
	if !hasScreen {
		t.Error("expected 'screen' event in journal")
	}
}

func TestTTYHost_RunTask_ColoredOutput(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	// Register a command that outputs ANSI colors
	systemd.RegisterCommand("colortest", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		fmt.Fprint(stdout, "\x1b[31mRed\x1b[0m \x1b[32mGreen\x1b[0m")
		return 0
	})

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TTYTEST2",
		Command:   []string{"colortest"},
		Rows:      5,
		Cols:      40,
		Systemd:   systemd,
		Journal:   journal,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	ctx := context.Background()
	err := host.RunTask(ctx)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	// Plain text should have the text
	screen, _ := host.GetScreenText()
	if !strings.Contains(screen, "Red") || !strings.Contains(screen, "Green") {
		t.Errorf("expected screen to contain 'Red' and 'Green', got:\n%s", screen)
	}

	// ANSI output should have escape codes
	ansi, _ := host.GetScreenANSI()
	if !strings.Contains(ansi, "\x1b[") {
		t.Errorf("expected ANSI escape sequences in output:\n%s", ansi)
	}
}

func TestTTYHost_RunTask_Scrollback(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	// Register a command that outputs many lines
	systemd.RegisterCommand("manylines", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		for i := 1; i <= 20; i++ {
			fmt.Fprintf(stdout, "Line %d\r\n", i)
		}
		return 0
	})

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TTYTEST3",
		Command:   []string{"manylines"},
		Rows:      5, // Small screen to trigger scrollback
		Cols:      40,
		Systemd:   systemd,
		Journal:   journal,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	ctx := context.Background()
	err := host.RunTask(ctx)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	// Should have scrollback
	scrollback, _ := host.GetScrollback(100)
	if len(scrollback) < 10 {
		t.Errorf("expected at least 10 scrollback lines, got %d", len(scrollback))
	}

	// Journal should have the scrollback lines logged (with ANSI codes now)
	entries := journal.Entries()
	var lineCount int
	for _, e := range entries {
		if strings.Contains(e.Message, "Line ") {
			lineCount++
		}
	}
	if lineCount < 10 {
		t.Errorf("expected at least 10 journal entries with line output, got %d", lineCount)
	}
}

func TestTTYHost_RunTask_Cancel(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	started := make(chan struct{})

	// Register a long-running command
	systemd.RegisterCommand("sleep", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		close(started)
		<-ctx.Done() // Wait for cancellation
		return 137   // Typical killed exit code
	})

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TTYTEST4",
		Command:   []string{"sleep"},
		Rows:      5,
		Cols:      40,
		Systemd:   systemd,
		Journal:   journal,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Run in goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- host.RunTask(ctx)
	}()

	// Wait for process to start
	<-started

	// Verify it's running
	status, _ := host.Gist()
	if !status.Running {
		t.Error("expected task to be running")
	}

	// Cancel
	cancel()

	// Wait for RunTask to return
	err := <-errChan
	if err != context.Canceled {
		t.Errorf("expected context.Canceled error, got: %v", err)
	}

	// Should no longer be running
	status, _ = host.Gist()
	if status.Running {
		t.Error("expected task to be stopped after cancel")
	}
}

func TestTTYHost_RunTask_SendInput(t *testing.T) {
	systemd := NewFakeSystemd()
	journal := NewFakeJournal()

	ready := make(chan struct{})
	gotInput := make(chan string, 1)

	// Register a command that reads from stdin and echoes it
	systemd.RegisterCommand("cat", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		close(ready)
		buf := make([]byte, 1024)
		n, err := stdin.Read(buf)
		if err != nil && err != io.EOF {
			return 1
		}
		if n > 0 {
			gotInput <- string(buf[:n])
			fmt.Fprint(stdout, string(buf[:n]))
		}
		return 0
	})

	host := NewTTYHost(TTYHostConfig{
		SessionID: "TTYTEST5",
		Command:   []string{"cat"},
		Rows:      5,
		Cols:      40,
		Systemd:   systemd,
		Journal:   journal,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run in goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- host.RunTask(ctx)
	}()

	// Wait for process to be ready
	<-ready

	// Send input via the PTY master
	_, err := host.SendInput("Hello from stdin!")
	if err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}

	// Wait for process to receive and echo it
	select {
	case input := <-gotInput:
		if input != "Hello from stdin!" {
			t.Errorf("expected 'Hello from stdin!', got %q", input)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for process to receive input")
	}

	// Wait for process to exit
	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("RunTask failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for process to exit")
	}

	// Verify the echoed output made it to the screen
	screen, _ := host.GetScreenText()
	if !strings.Contains(screen, "Hello from stdin!") {
		t.Errorf("expected screen to contain echoed input, got:\n%s", screen)
	}
}
