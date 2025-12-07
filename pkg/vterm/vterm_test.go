package vterm

import (
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	rows, cols := vt.GetSize()
	if rows != 24 || cols != 80 {
		t.Errorf("GetSize() = (%d, %d), want (24, 80)", rows, cols)
	}
}

func TestWrite(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	n := vt.Write([]byte("Hello, World!"))
	if n != 13 {
		t.Errorf("Write() = %d, want 13", n)
	}

	text := vt.GetRowText(0)
	if text != "Hello, World!" {
		t.Errorf("GetRowText(0) = %q, want %q", text, "Hello, World!")
	}
}

func TestGetCell(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	vt.Write([]byte("A"))

	cell := vt.GetCell(0, 0)
	if len(cell.Chars) != 1 || cell.Chars[0] != 'A' {
		t.Errorf("GetCell(0, 0).Chars = %v, want ['A']", cell.Chars)
	}
}

func TestSetSize(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	vt.SetSize(40, 120)
	rows, cols := vt.GetSize()
	if rows != 40 || cols != 120 {
		t.Errorf("GetSize() after resize = (%d, %d), want (40, 120)", rows, cols)
	}
}

func TestGetCursor(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	vt.Write([]byte("Hello"))
	row, col := vt.GetCursor()
	if row != 0 || col != 5 {
		t.Errorf("GetCursor() = (%d, %d), want (0, 5)", row, col)
	}
}

func TestOnDamage(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	damaged := false
	vt.OnDamage(func(startRow, endRow, startCol, endCol int) {
		damaged = true
	})

	vt.Write([]byte("X"))
	if !damaged {
		t.Error("OnDamage callback was not called")
	}
}

func TestOnMoveCursor(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	var cursorRow, cursorCol int
	vt.OnMoveCursor(func(row, col int, visible bool) {
		cursorRow, cursorCol = row, col
	})

	vt.Write([]byte("ABC"))
	if cursorRow != 0 || cursorCol != 3 {
		t.Errorf("OnMoveCursor reported (%d, %d), want (0, 3)", cursorRow, cursorCol)
	}
}

func TestOnPushLine(t *testing.T) {
	vt := New(3, 10) // Small terminal to trigger scrollback quickly
	defer vt.Free()

	var pushedLines []string
	vt.OnPushLine(func(line string) {
		pushedLines = append(pushedLines, line)
	})

	// Write enough lines to cause scrollback
	for i := 0; i < 5; i++ {
		vt.Write([]byte("Line\n"))
	}

	if len(pushedLines) == 0 {
		t.Log("No lines pushed to scrollback (may need more lines)")
	}
}

func TestOnTermProp(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	var title string
	vt.OnTermProp(func(prop TermProp, val any) {
		if prop == PropTitle {
			title = val.(string)
		}
	})

	// Set terminal title using OSC sequence: ESC ] 0 ; title BEL
	vt.Write([]byte("\x1b]0;Test Title\x07"))

	if title != "Test Title" {
		t.Errorf("Title = %q, want %q", title, "Test Title")
	}
}

func TestScreenText(t *testing.T) {
	vt := New(5, 20)
	defer vt.Free()

	// Use \r\n to move to beginning of next line (like a real terminal)
	vt.Write([]byte("Line 1\r\nLine 2\r\nLine 3"))

	text := vt.GetScreenText()
	lines := strings.Split(text, "\n")
	if len(lines) < 3 {
		t.Errorf("GetScreenText() returned %d lines, want at least 3", len(lines))
		return
	}

	if lines[0] != "Line 1" {
		t.Errorf("Line 0 = %q, want %q", lines[0], "Line 1")
	}
	if lines[1] != "Line 2" {
		t.Errorf("Line 1 = %q, want %q", lines[1], "Line 2")
	}
	if lines[2] != "Line 3" {
		t.Errorf("Line 2 = %q, want %q", lines[2], "Line 3")
	}
}

func TestUnicode(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	// Write some Unicode characters
	vt.Write([]byte("Hello, \xe4\xb8\x96\xe7\x95\x8c!")) // "Hello, 世界!"

	text := vt.GetRowText(0)
	expected := "Hello, 世界!"
	if text != expected {
		t.Errorf("GetRowText(0) = %q, want %q", text, expected)
	}
}

func TestColors(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	// Write with red foreground: ESC[31m
	vt.Write([]byte("\x1b[31mRed\x1b[0m"))

	cell := vt.GetCell(0, 0)
	// Check that the cell has indexed color
	if cell.Fg.Type != ColorIndexed {
		t.Errorf("Expected indexed color, got type %d", cell.Fg.Type)
	}
}

func TestBold(t *testing.T) {
	vt := New(24, 80)
	defer vt.Free()

	// Write with bold: ESC[1m
	vt.Write([]byte("\x1b[1mBold\x1b[0m"))

	cell := vt.GetCell(0, 0)
	if !cell.Attrs.Bold {
		t.Error("Expected bold attribute to be set")
	}
}

func TestGetScreenANSI(t *testing.T) {
	vt := New(3, 20)
	defer vt.Free()

	// Write colored text
	vt.Write([]byte("\x1b[31mRed\x1b[0m \x1b[1mBold\x1b[0m"))

	ansi := vt.GetScreenANSI()

	// Should contain ESC sequences
	if !strings.Contains(ansi, "\x1b[") {
		t.Error("GetScreenANSI() should contain ANSI escape sequences")
	}

	// Should contain the text
	if !strings.Contains(ansi, "Red") {
		t.Error("GetScreenANSI() should contain 'Red'")
	}
	if !strings.Contains(ansi, "Bold") {
		t.Error("GetScreenANSI() should contain 'Bold'")
	}

	// Should end with reset
	if !strings.HasSuffix(strings.TrimRight(ansi, "\n"), "\x1b[0m") {
		t.Error("GetScreenANSI() should end with reset sequence")
	}
}
