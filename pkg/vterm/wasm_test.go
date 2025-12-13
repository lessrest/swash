package vterm

import (
	"testing"
)

func TestVTerm(t *testing.T) {
	vt, err := New(24, 80)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer vt.Free()

	// Test basic write
	input := []byte("Hello, WASM vterm!\r\n")
	n := vt.Write(input)
	if n != len(input) {
		t.Errorf("Write returned %d, want %d", n, len(input))
	}

	// Test GetRowText
	text := vt.GetRowText(0)
	if text != "Hello, WASM vterm!" {
		t.Errorf("GetRowText(0) = %q, want %q", text, "Hello, WASM vterm!")
	}

	// Test GetScreenText
	screen := vt.GetScreenText()
	if len(screen) == 0 {
		t.Error("GetScreenText returned empty string")
	}

	// Test GetCursor
	row, col := vt.GetCursor()
	t.Logf("Cursor at row=%d, col=%d", row, col)
	if row != 1 { // After \r\n, should be on row 1
		t.Errorf("Cursor row = %d, want 1", row)
	}

	// Test GetSize
	rows, cols := vt.GetSize()
	if rows != 24 || cols != 80 {
		t.Errorf("GetSize() = (%d, %d), want (24, 80)", rows, cols)
	}

	// Test SetSize
	vt.SetSize(30, 100)
	rows, cols = vt.GetSize()
	if rows != 30 || cols != 100 {
		t.Errorf("After SetSize, GetSize() = (%d, %d), want (30, 100)", rows, cols)
	}
}

func TestVTermColors(t *testing.T) {
	vt, err := New(24, 80)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer vt.Free()

	// Write some colored text
	vt.Write([]byte("\x1b[31mRed\x1b[0m Normal"))

	// Get the screen with ANSI codes
	ansi := vt.GetScreenANSI()
	t.Logf("ANSI output: %q", ansi[:min(100, len(ansi))])

	// Get a cell and check attributes
	cell := vt.GetCell(0, 0)
	t.Logf("Cell(0,0): chars=%v, attrs=%+v, fg=%+v, bg=%+v", cell.Chars, cell.Attrs, cell.Fg, cell.Bg)

	// Verify the red cell has indexed color 1 (red)
	if cell.Fg.Type != ColorIndexed || cell.Fg.Index != 1 {
		t.Errorf("Expected red fg (indexed 1), got type=%d index=%d", cell.Fg.Type, cell.Fg.Index)
	}
}

func TestVTermCallbacks(t *testing.T) {
	vt, err := New(24, 80)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer vt.Free()

	// Test OnDamage
	damageCount := 0
	vt.OnDamage(func(startRow, endRow, startCol, endCol int) {
		damageCount++
		t.Logf("Damage: rows %d-%d, cols %d-%d", startRow, endRow, startCol, endCol)
	})

	// Test OnTermProp
	var gotTitle string
	vt.OnTermProp(func(prop TermProp, val any) {
		t.Logf("TermProp: %d = %v", prop, val)
		if prop == PropTitle {
			if s, ok := val.(string); ok {
				gotTitle = s
			}
		}
	})

	// Test OnPushLine
	var pushedLines []string
	vt.OnPushLine(func(line string) {
		t.Logf("PushLine: %q", line)
		pushedLines = append(pushedLines, line)
	})

	// Write content that triggers callbacks
	vt.Write([]byte("Hello\r\n"))
	if damageCount == 0 {
		t.Error("Expected OnDamage to be called")
	}

	// Set title via escape sequence
	vt.Write([]byte("\x1b]0;Test Title\x07"))
	if gotTitle != "Test Title" {
		t.Errorf("Expected title 'Test Title', got %q", gotTitle)
	}

	// Fill screen to trigger scrollback
	for range 30 {
		vt.Write([]byte("Line\r\n"))
	}
	if len(pushedLines) == 0 {
		t.Error("Expected OnPushLine to be called")
	} else {
		t.Logf("Pushed %d lines", len(pushedLines))
	}
}
