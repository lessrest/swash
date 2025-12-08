package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/mbrock/swash/internal/swash"
	"github.com/mbrock/swash/pkg/vterm"
	"golang.org/x/term"
)

// attachState holds the state for an attached session
type attachState struct {
	sessionID  string
	clientID   string
	remoteRows int
	remoteCols int
	localRows  int
	localCols  int

	// File handles for PTY communication
	output *os.File // read end - receives PTY output from host
	input  *os.File // write end - sends input to host PTY

	client swash.TTYClient

	// Border/centering
	needsBorder bool
	borderTop   int // row offset for remote content (0-based)
	borderLeft  int // col offset for remote content (0-based)

	// Local vterm for rendering
	vt *vterm.VTerm

	// Rendering synchronization
	mu       sync.Mutex
	dirty    bool          // true if screen needs redraw
	damageCh chan struct{} // signals damage events
}

// newAttachState creates a new attach state with local vterm
func newAttachState(sessionID, clientID string, remoteRows, remoteCols, localRows, localCols int) *attachState {
	s := &attachState{
		sessionID:   sessionID,
		clientID:    clientID,
		remoteRows:  remoteRows,
		remoteCols:  remoteCols,
		localRows:   localRows,
		localCols:   localCols,
		needsBorder: true, // Always draw border
		damageCh:    make(chan struct{}, 1),
	}

	s.calculateBorderOffsets()

	// Create local vterm with remote dimensions
	s.vt = vterm.New(remoteRows, remoteCols)

	// Set up damage callback to signal redraws
	s.vt.OnDamage(func(startRow, endRow, startCol, endCol int) {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
		// Non-blocking send to signal damage
		select {
		case s.damageCh <- struct{}{}:
		default:
		}
	})

	return s
}

// calculateBorderOffsets computes the centering offsets
func (s *attachState) calculateBorderOffsets() {
	if s.needsBorder {
		// Center the remote terminal within local terminal
		// Account for border characters (1 char on each side)
		s.borderTop = (s.localRows - s.remoteRows - 2) / 2
		s.borderLeft = (s.localCols - s.remoteCols - 2) / 2
		if s.borderTop < 0 {
			s.borderTop = 0
		}
		if s.borderLeft < 0 {
			s.borderLeft = 0
		}
	} else {
		s.borderTop = 0
		s.borderLeft = 0
	}
}

// drawBorder draws a box-drawing border (standalone, with sync)
func (s *attachState) drawBorder() {
	fmt.Print("\x1b[?2026h")
	fmt.Print("\x1b[?25l")
	s.drawBorderInner()
	fmt.Print("\x1b[?25h")
	fmt.Print("\x1b[?2026l")
}

// drawBorderInner draws the border without sync wrapper (for use inside render)
func (s *attachState) drawBorderInner() {
	// Build top border with session info
	info := fmt.Sprintf(" %s [%dx%d] ", s.sessionID, s.remoteCols, s.remoteRows)
	topBorder := "┌"
	infoStart := (s.remoteCols - len(info)) / 2
	if infoStart < 0 {
		infoStart = 0
	}
	for i := 0; i < s.remoteCols; i++ {
		if i == infoStart {
			topBorder += info
			i += len(info) - 1
		} else {
			topBorder += "─"
		}
	}
	topBorder += "┐"

	// Position and draw top border (ANSI uses 1-based coordinates)
	fmt.Printf("\x1b[%d;%dH%s", s.borderTop+1, s.borderLeft+1, topBorder)

	// Draw side borders
	for row := 0; row < s.remoteRows; row++ {
		// Left border
		fmt.Printf("\x1b[%d;%dH│", s.borderTop+row+2, s.borderLeft+1)
		// Right border
		fmt.Printf("\x1b[%d;%dH│", s.borderTop+row+2, s.borderLeft+s.remoteCols+2)
	}

	// Build bottom border with detach hint
	hint := " Ctrl+\\ to detach "
	bottomBorder := "└"
	hintStart := (s.remoteCols - len(hint)) / 2
	if hintStart < 0 {
		hintStart = 0
	}
	for i := 0; i < s.remoteCols; i++ {
		if i == hintStart {
			bottomBorder += hint
			i += len(hint) - 1
		} else {
			bottomBorder += "─"
		}
	}
	bottomBorder += "┘"
	fmt.Printf("\x1b[%d;%dH%s", s.borderTop+s.remoteRows+2, s.borderLeft+1, bottomBorder)
}

// render outputs the vterm screen content to stdout at the correct offset
func (s *attachState) render() {
	s.renderFull(false)
}

// renderFull renders the screen, optionally clearing first (for resize)
func (s *attachState) renderFull(clear bool) {
	s.mu.Lock()
	s.dirty = false
	s.mu.Unlock()

	// Begin synchronized update (terminals that don't support it will ignore)
	fmt.Print("\x1b[?2026h")
	// Hide cursor during rendering to avoid flicker
	fmt.Print("\x1b[?25l")

	if clear {
		fmt.Print("\x1b[2J")
	}

	// Always redraw border (it's cheap and ensures consistency)
	s.drawBorderInner()

	rows, cols := s.vt.GetSize()

	// Calculate content offset (inside border)
	contentTop := s.borderTop + 1   // skip border row
	contentLeft := s.borderLeft + 1 // skip border column

	// Render each row at the correct position
	for row := 0; row < rows; row++ {
		// Position cursor at start of this row (ANSI is 1-based)
		fmt.Printf("\x1b[%d;%dH", contentTop+row+1, contentLeft+1)

		// Get the row content with ANSI formatting
		// We need to render cell by cell to handle attributes properly
		s.renderRow(row, cols)
	}

	// Position cursor correctly
	curRow, curCol := s.vt.GetCursor()
	fmt.Printf("\x1b[%d;%dH", contentTop+curRow+1, contentLeft+curCol+1)

	// Show cursor again
	fmt.Print("\x1b[?25h")
	// End synchronized update
	fmt.Print("\x1b[?2026l")
}

// renderRow renders a single row with ANSI attributes
func (s *attachState) renderRow(row, cols int) {
	var lastAttrs vterm.CellAttrs
	var lastFg, lastBg vterm.Color
	firstCell := true

	for col := 0; col < cols; col++ {
		cell := s.vt.GetCell(row, col)

		// Skip continuation cells for wide characters
		if cell.Width == 0 {
			continue
		}

		// Emit ANSI codes if attributes changed
		if firstCell || cell.Attrs != lastAttrs || cell.Fg != lastFg || cell.Bg != lastBg {
			ansi := buildANSI(cell, lastAttrs, lastFg, lastBg, firstCell)
			if len(ansi) > 0 {
				fmt.Print(ansi)
			}
			lastAttrs = cell.Attrs
			lastFg = cell.Fg
			lastBg = cell.Bg
			firstCell = false
		}

		// Output character
		if len(cell.Chars) > 0 && cell.Chars[0] != 0 {
			for _, r := range cell.Chars {
				fmt.Print(string(r))
			}
		} else {
			fmt.Print(" ")
		}
	}

	// Reset attributes at end of row
	if !firstCell {
		fmt.Print("\x1b[0m")
	}
}

// buildANSI generates ANSI escape sequences for cell attribute changes
func buildANSI(cell vterm.Cell, lastAttrs vterm.CellAttrs, lastFg, lastBg vterm.Color, first bool) string {
	var codes []int

	// Check if we need a reset first
	needReset := false
	if !first {
		if lastAttrs.Bold && !cell.Attrs.Bold {
			needReset = true
		}
		if lastAttrs.Italic && !cell.Attrs.Italic {
			needReset = true
		}
		if lastAttrs.Underline > 0 && cell.Attrs.Underline == 0 {
			needReset = true
		}
		if lastAttrs.Reverse && !cell.Attrs.Reverse {
			needReset = true
		}
	}

	hasAttrs := cell.Attrs.Bold || cell.Attrs.Italic || cell.Attrs.Underline > 0 ||
		cell.Attrs.Reverse || cell.Attrs.Strike || !cell.Fg.DefaultFg || !cell.Bg.DefaultBg

	if needReset || (first && hasAttrs) {
		codes = append(codes, 0)
	}

	if cell.Attrs.Bold {
		codes = append(codes, 1)
	}
	if cell.Attrs.Italic {
		codes = append(codes, 3)
	}
	if cell.Attrs.Underline == 1 {
		codes = append(codes, 4)
	}
	if cell.Attrs.Reverse {
		codes = append(codes, 7)
	}
	if cell.Attrs.Strike {
		codes = append(codes, 9)
	}

	// Foreground color
	if !cell.Fg.DefaultFg {
		if cell.Fg.Type == vterm.ColorIndexed {
			if cell.Fg.Index < 8 {
				codes = append(codes, 30+int(cell.Fg.Index))
			} else if cell.Fg.Index < 16 {
				codes = append(codes, 90+int(cell.Fg.Index)-8)
			} else {
				codes = append(codes, 38, 5, int(cell.Fg.Index))
			}
		} else {
			codes = append(codes, 38, 2, int(cell.Fg.R), int(cell.Fg.G), int(cell.Fg.B))
		}
	}

	// Background color
	if !cell.Bg.DefaultBg {
		if cell.Bg.Type == vterm.ColorIndexed {
			if cell.Bg.Index < 8 {
				codes = append(codes, 40+int(cell.Bg.Index))
			} else if cell.Bg.Index < 16 {
				codes = append(codes, 100+int(cell.Bg.Index)-8)
			} else {
				codes = append(codes, 48, 5, int(cell.Bg.Index))
			}
		} else {
			codes = append(codes, 48, 2, int(cell.Bg.R), int(cell.Bg.G), int(cell.Bg.B))
		}
	}

	if len(codes) == 0 {
		return ""
	}

	result := "\x1b["
	for i, code := range codes {
		if i > 0 {
			result += ";"
		}
		result += fmt.Sprintf("%d", code)
	}
	result += "m"
	return result
}

// close releases resources
func (s *attachState) close() {
	if s.vt != nil {
		s.vt.Free()
		s.vt = nil
	}
	if s.output != nil {
		s.output.Close()
	}
	if s.input != nil {
		s.input.Close()
	}
}

// updateLocalSize updates the local terminal size and recalculates offsets
func (s *attachState) updateLocalSize(rows, cols int) {
	s.localRows = rows
	s.localCols = cols
	// needsBorder stays true - we always draw the border
	s.calculateBorderOffsets()
}

func cmdAttach(sessionID string) {
	client, err := swash.ConnectTTYSession(sessionID)
	if err != nil {
		fatal("connecting to session: %v", err)
	}
	defer client.Close()

	// Get local terminal size
	stdinFd := int(os.Stdin.Fd())
	var localCols, localRows int
	if term.IsTerminal(stdinFd) {
		localCols, localRows, err = term.GetSize(stdinFd)
		if err != nil {
			localCols, localRows = 80, 24
		}
	} else {
		localCols, localRows = 80, 24
	}

	// Request a terminal size that leaves room for our border (2 chars each direction)
	requestRows := localRows - 2
	requestCols := localCols - 2
	if requestRows < 1 {
		requestRows = 1
	}
	if requestCols < 1 {
		requestCols = 1
	}

	// Call Attach with the content size (local minus border)
	outputFD, inputFD, remoteRows, remoteCols, screenANSI, clientID, err := client.Attach(int32(requestRows), int32(requestCols))
	if err != nil {
		fatal("attaching to session: %v", err)
	}

	// Convert fds to *os.File
	output := os.NewFile(uintptr(outputFD), "attach-output")
	input := os.NewFile(uintptr(inputFD), "attach-input")

	// Initialize attach state with local vterm
	state := newAttachState(sessionID, clientID, int(remoteRows), int(remoteCols), localRows, localCols)
	state.output = output
	state.input = input
	state.client = client
	defer state.close()

	// Feed initial screen snapshot to local vterm
	state.vt.Write([]byte(screenANSI))

	// Put terminal in raw mode
	var oldState *term.State
	if term.IsTerminal(stdinFd) {
		oldState, err = term.MakeRaw(stdinFd)
		if err != nil {
			fatal("setting raw mode: %v", err)
		}
		defer term.Restore(stdinFd, oldState)
	}

	// Switch to alternate screen buffer
	fmt.Print("\x1b[?1049h") // Enter alternate screen buffer

	// Draw border and render initial screen (with clear)
	state.renderFull(true)

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH)

	// Channels to signal exit
	done := make(chan struct{})
	detach := make(chan struct{})

	// Read PTY output and feed to local vterm
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := output.Read(buf)
			if err != nil {
				close(done)
				return
			}
			if n > 0 {
				// Feed to local vterm (this triggers damage callbacks)
				state.vt.Write(buf[:n])
			}
		}
	}()

	// Render loop - renders when damage occurs
	go func() {
		for {
			select {
			case <-state.damageCh:
				state.render()
			case <-done:
				return
			case <-detach:
				return
			}
		}
	}()

	// Forward stdin to PTY input (with Ctrl+\ detection for detach)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				if buf[0] == 0x1c { // Ctrl+\ to detach
					close(detach)
					return
				}
				input.Write(buf[:n])
			}
		}
	}()

	// Main event loop
	for {
		select {
		case <-done:
			goto cleanup
		case <-detach:
			goto cleanup
		case sig := <-sigCh:
			if sig == syscall.SIGWINCH {
				// Terminal resized - update local size
				if term.IsTerminal(stdinFd) {
					newCols, newRows, err := term.GetSize(stdinFd)
					if err == nil {
						// Calculate new content size (local minus border)
						newContentRows := newRows - 2
						newContentCols := newCols - 2
						if newContentRows < 1 {
							newContentRows = 1
						}
						if newContentCols < 1 {
							newContentCols = 1
						}

						// Check if we can resize the remote terminal
						count, _, _, err := client.GetAttachedClients()
						if err == nil && count == 1 {
							// We're the only client - resize the remote
							if err := client.Resize(int32(newContentRows), int32(newContentCols)); err == nil {
								// Update our state to match new size
								state.remoteRows = newContentRows
								state.remoteCols = newContentCols
								// Resize local vterm to match
								state.vt.SetSize(newContentRows, newContentCols)
							}
						}

						state.updateLocalSize(newRows, newCols)

						// Clear and redraw everything atomically
						state.renderFull(true)
					}
				}
			} else {
				// SIGINT or SIGTERM
				goto cleanup
			}
		}
	}

cleanup:
	// Leave alternate screen buffer
	fmt.Print("\x1b[?1049l")

	// Restore terminal
	if oldState != nil {
		term.Restore(stdinFd, oldState)
	}
	fmt.Println("[detached]")
}
