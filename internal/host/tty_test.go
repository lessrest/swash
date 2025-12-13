package host

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	journal "swa.sh/go/swash/internal/journal"
)

type FakeJournal = journal.FakeJournal

func NewTestFakes() (*FakeExecutor, *FakeJournal) {
	return NewFakeExecutor(), journal.NewFakeJournal()
}

// mustNewTTYHost creates a TTYHost or fails the test
func mustNewTTYHost(t *testing.T, cfg TTYHostConfig) *TTYHost {
	t.Helper()
	h, err := NewTTYHost(cfg)
	if err != nil {
		t.Fatalf("NewTTYHost failed: %v", err)
	}
	return h
}

func TestTTYHost_NewTTYHost(t *testing.T) {
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Events:    journal,
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
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "ABC123",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Events:    journal,
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
	_, journal := NewTestFakes()

	// Test with zero values - should default to 24x80
	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      0,
		Cols:      0,
		Events:    journal,
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
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      5,
		Cols:      40,
		Events:    journal,
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
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      5,
		Cols:      40,
		Events:    journal,
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
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Events:    journal,
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
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Events:    journal,
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
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Events:    journal,
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
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Events:    journal,
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
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      3, // Small screen to trigger scrollback
		Cols:      40,
		Events:    journal,
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
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TEST01",
		Command:   []string{"bash"},
		Rows:      24,
		Cols:      80,
		Events:    journal,
	})
	defer host.Close()

	// Try to send input before task is started
	_, err := host.SendInput("hello")
	if err == nil {
		t.Error("expected error when sending input with no process")
	}
}

func TestTTYHost_RunTask_WithFakePTY(t *testing.T) {
	exec, journal := NewTestFakes()

	// Register a simple echo command
	exec.RegisterCommand("echo", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		// Write output - this goes to the slave side which feeds the master
		if len(args) > 1 {
			fmt.Fprintln(stdout, args[1])
		}
		return 0
	})

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TTYTEST1",
		Command:   []string{"echo", "Hello from TTY"},
		Rows:      10,
		Cols:      40,
		Events:    journal,
		Executor:  exec,
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
	exec, journal := NewTestFakes()

	// Register a command that outputs ANSI colors
	exec.RegisterCommand("colortest", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		fmt.Fprint(stdout, "\x1b[31mRed\x1b[0m \x1b[32mGreen\x1b[0m")
		return 0
	})

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TTYTEST2",
		Command:   []string{"colortest"},
		Rows:      5,
		Cols:      40,
		Events:    journal,
		Executor:  exec,
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
	exec, journal := NewTestFakes()

	// Register a command that outputs many lines
	exec.RegisterCommand("manylines", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		for i := 1; i <= 20; i++ {
			fmt.Fprintf(stdout, "Line %d\r\n", i)
		}
		return 0
	})

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TTYTEST3",
		Command:   []string{"manylines"},
		Rows:      5, // Small screen to trigger scrollback
		Cols:      40,
		Events:    journal,
		Executor:  exec,
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

	// Event log should have the scrollback lines logged (with ANSI codes now)
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
	exec, journal := NewTestFakes()

	started := make(chan struct{})

	// Register a long-running command
	exec.RegisterCommand("sleep", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		close(started)
		<-ctx.Done() // Wait for cancellation
		return 137   // Typical killed exit code
	})

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TTYTEST4",
		Command:   []string{"sleep"},
		Rows:      5,
		Cols:      40,
		Events:    journal,
		Executor:  exec,
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
	exec, journal := NewTestFakes()

	ready := make(chan struct{})
	gotInput := make(chan string, 1)

	// Register a command that reads from stdin and echoes it
	exec.RegisterCommand("cat", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
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

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "TTYTEST5",
		Command:   []string{"cat"},
		Rows:      5,
		Cols:      40,
		Events:    journal,
		Executor:  exec,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	ctx := t.Context()

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

func TestTTYHost_Attach_Basic(t *testing.T) {
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "ATTACH1",
		Command:   []string{"bash"},
		Rows:      5,
		Cols:      40,
		Events:    journal,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	// Write some content to establish screen state
	host.vt.Write([]byte("Hello World"))

	// Attach with client size matching the host
	outputFD, inputFD, rows, cols, screenANSI, _, err := host.Attach(5, 40)
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	output := os.NewFile(uintptr(outputFD), "attach-output")
	input := os.NewFile(uintptr(inputFD), "attach-input")
	defer output.Close()
	defer input.Close()

	// Verify size
	if rows != 5 || cols != 40 {
		t.Errorf("expected size (5,40), got (%d,%d)", rows, cols)
	}

	// Verify screen snapshot contains content
	if !strings.Contains(screenANSI, "Hello World") {
		t.Errorf("expected screen snapshot to contain 'Hello World', got %q", screenANSI)
	}
}

func TestTTYHost_Attach_MultiClient(t *testing.T) {
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "ATTACH2",
		Command:   []string{"bash"},
		Rows:      5,
		Cols:      40,
		Events:    journal,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	// First attach should succeed and set terminal size
	outputFD1, inputFD1, rows1, cols1, _, clientID1, err := host.Attach(10, 80)
	if err != nil {
		t.Fatalf("First Attach failed: %v", err)
	}
	output1 := os.NewFile(uintptr(outputFD1), "output1")
	input1 := os.NewFile(uintptr(inputFD1), "input1")
	defer output1.Close()
	defer input1.Close()

	// First client sets the terminal size
	if rows1 != 10 || cols1 != 80 {
		t.Errorf("expected size (10,80), got (%d,%d)", rows1, cols1)
	}
	if clientID1 == "" {
		t.Error("expected non-empty client ID")
	}

	// Second attach with sufficient size should succeed
	outputFD2, inputFD2, rows2, cols2, _, clientID2, err := host.Attach(12, 100)
	if err != nil {
		t.Fatalf("Second Attach (large enough) failed: %v", err)
	}
	output2 := os.NewFile(uintptr(outputFD2), "output2")
	input2 := os.NewFile(uintptr(inputFD2), "input2")
	defer output2.Close()
	defer input2.Close()

	// Second client should get the same (master) terminal size
	if rows2 != 10 || cols2 != 80 {
		t.Errorf("expected size (10,80) for second client, got (%d,%d)", rows2, cols2)
	}
	if clientID2 == "" || clientID2 == clientID1 {
		t.Error("expected unique client ID for second client")
	}

	// Third attach with too-small terminal should fail
	_, _, _, _, _, _, err = host.Attach(5, 40)
	if err == nil {
		t.Error("expected third Attach with small terminal to fail")
	}

	// Verify client count
	count, masterRows, masterCols, err := host.GetAttachedClients()
	if err != nil {
		t.Fatalf("GetAttachedClients failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 attached clients, got %d", count)
	}
	if masterRows != 10 || masterCols != 80 {
		t.Errorf("expected master size (10,80), got (%d,%d)", masterRows, masterCols)
	}
}

func TestTTYHost_Attach_StreamOutput(t *testing.T) {
	exec, journal := NewTestFakes()

	ready := make(chan struct{})
	done := make(chan struct{})

	// Register a command that outputs some text after a signal
	exec.RegisterCommand("outputter", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		close(ready)
		<-done
		fmt.Fprint(stdout, "STREAMED_OUTPUT")
		return 0
	})

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "ATTACH3",
		Command:   []string{"outputter"},
		Rows:      5,
		Cols:      40,
		Events:    journal,
		Executor:  exec,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	ctx := t.Context()

	// Start the task
	errChan := make(chan error, 1)
	go func() {
		errChan <- host.RunTask(ctx)
	}()

	// Wait for process to be ready
	<-ready

	// Attach while process is running
	outputFD, inputFD, _, _, _, _, err := host.Attach(5, 40)
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	output := os.NewFile(uintptr(outputFD), "output")
	input := os.NewFile(uintptr(inputFD), "input")
	defer output.Close()
	defer input.Close()

	// Tell the process to output
	close(done)

	// Read from the output pipe - should receive the streamed bytes
	buf := make([]byte, 1024)
	output.SetReadDeadline(time.Now().Add(time.Second))
	n, err := output.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read from output pipe: %v", err)
	}

	if !strings.Contains(string(buf[:n]), "STREAMED_OUTPUT") {
		t.Errorf("expected output to contain 'STREAMED_OUTPUT', got %q", string(buf[:n]))
	}

	// Wait for process to exit
	select {
	case <-errChan:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for process to exit")
	}
}

func TestTTYHost_Attach_ReattachAfterDisconnect(t *testing.T) {
	_, journal := NewTestFakes()

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "ATTACH5",
		Command:   []string{"bash"},
		Rows:      5,
		Cols:      40,
		Events:    journal,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	// First attach
	outputFD1, inputFD1, _, _, _, _, err := host.Attach(10, 80)
	if err != nil {
		t.Fatalf("First Attach failed: %v", err)
	}
	output1 := os.NewFile(uintptr(outputFD1), "output1")
	input1 := os.NewFile(uintptr(inputFD1), "input1")

	// Close the pipes (simulating client disconnect)
	output1.Close()
	input1.Close()

	// Give the cleanup goroutine a moment to run
	time.Sleep(50 * time.Millisecond)

	// Second attach should succeed and can use a different size (no other clients)
	outputFD2, inputFD2, rows, cols, _, _, err := host.Attach(20, 100)
	if err != nil {
		t.Fatalf("Second Attach after disconnect failed: %v", err)
	}
	output2 := os.NewFile(uintptr(outputFD2), "output2")
	input2 := os.NewFile(uintptr(inputFD2), "input2")
	defer output2.Close()
	defer input2.Close()

	// After all clients disconnect, next client can resize
	if rows != 20 || cols != 100 {
		t.Errorf("expected size (20,100) after reconnect, got (%d,%d)", rows, cols)
	}
}

func TestTTYHost_Attach_SendInput(t *testing.T) {
	exec, journal := NewTestFakes()

	ready := make(chan struct{})
	gotInput := make(chan string, 1)

	// Register a command that reads from stdin
	exec.RegisterCommand("reader", func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		close(ready)
		buf := make([]byte, 1024)
		n, _ := stdin.Read(buf)
		if n > 0 {
			gotInput <- string(buf[:n])
		}
		return 0
	})

	host := mustNewTTYHost(t, TTYHostConfig{
		SessionID: "ATTACH4",
		Command:   []string{"reader"},
		Rows:      5,
		Cols:      40,
		Events:    journal,
		Executor:  exec,
		OpenPTY:   OpenFakePTY,
	})
	defer host.Close()

	ctx := t.Context()

	// Start the task
	errChan := make(chan error, 1)
	go func() {
		errChan <- host.RunTask(ctx)
	}()

	// Wait for process to be ready
	<-ready

	// Attach
	outputFD, inputFD, _, _, _, _, err := host.Attach(5, 40)
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	output := os.NewFile(uintptr(outputFD), "output")
	input := os.NewFile(uintptr(inputFD), "input")
	defer output.Close()
	defer input.Close()

	// Send input through the attached input pipe
	_, err = input.Write([]byte("ATTACHED_INPUT"))
	if err != nil {
		t.Fatalf("Failed to write to input pipe: %v", err)
	}

	// Verify the process received it
	select {
	case received := <-gotInput:
		if received != "ATTACHED_INPUT" {
			t.Errorf("expected 'ATTACHED_INPUT', got %q", received)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for process to receive input")
	}

	// Wait for process to exit
	select {
	case <-errChan:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for process to exit")
	}
}
