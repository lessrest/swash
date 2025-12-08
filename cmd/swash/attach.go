package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mbrock/swash/internal/swash"
	"github.com/mbrock/swash/pkg/vterm"
	"golang.org/x/term"
)

// GetContentSize returns the terminal size minus border (2 rows, 2 cols).
// Returns default 80x24 content area if not a terminal.
func GetContentSize() (rows, cols int) {
	stdinFd := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFd) {
		c, r, err := term.GetSize(stdinFd)
		if err == nil {
			rows, cols = r-2, c-2
			if rows < 1 {
				rows = 1
			}
			if cols < 1 {
				cols = 1
			}
			return rows, cols
		}
	}
	return 22, 78 // default 24x80 minus border
}

// vtermMsg represents a message to the vterm owner goroutine
type vtermMsg interface {
	isVtermMsg()
}

type vtermWrite struct{ data []byte }
type vtermResize struct{ rows, cols int }
type vtermRender struct{ clear bool }

func (vtermWrite) isVtermMsg()  {}
func (vtermResize) isVtermMsg() {}
func (vtermRender) isVtermMsg() {}

// terminalRenderer handles all rendering to the real terminal
type terminalRenderer struct {
	sessionID  string
	remoteRows int
	remoteCols int
	localRows  int
	localCols  int
	borderTop  int
	borderLeft int
}

func newRenderer(sessionID string, remoteRows, remoteCols, localRows, localCols int) *terminalRenderer {
	r := &terminalRenderer{
		sessionID:  sessionID,
		remoteRows: remoteRows,
		remoteCols: remoteCols,
		localRows:  localRows,
		localCols:  localCols,
	}
	r.calculateOffsets()
	return r
}

func (r *terminalRenderer) calculateOffsets() {
	r.borderTop = (r.localRows - r.remoteRows - 2) / 2
	r.borderLeft = (r.localCols - r.remoteCols - 2) / 2
	if r.borderTop < 0 {
		r.borderTop = 0
	}
	if r.borderLeft < 0 {
		r.borderLeft = 0
	}
}

func (r *terminalRenderer) updateSize(localRows, localCols, remoteRows, remoteCols int) {
	r.localRows = localRows
	r.localCols = localCols
	r.remoteRows = remoteRows
	r.remoteCols = remoteCols
	r.calculateOffsets()
}

func (r *terminalRenderer) render(vt *vterm.VTerm, clear bool, cursorVisible bool) {
	// Begin synchronized update
	fmt.Print("\x1b[?2026h")
	fmt.Print("\x1b[?25l")

	if clear {
		fmt.Print("\x1b[2J")
	}

	// Draw border
	r.drawBorder()

	// Render vterm content
	rows, cols := vt.GetSize()
	contentTop := r.borderTop + 1
	contentLeft := r.borderLeft + 1

	for row := 0; row < rows; row++ {
		fmt.Printf("\x1b[%d;%dH", contentTop+row+1, contentLeft+1)
		r.renderRow(vt, row, cols)
	}

	// Position cursor
	curRow, curCol := vt.GetCursor()
	fmt.Printf("\x1b[%d;%dH", contentTop+curRow+1, contentLeft+curCol+1)

	// Show cursor only if it should be visible
	if cursorVisible {
		fmt.Print("\x1b[?25h")
	}

	// End synchronized update
	fmt.Print("\x1b[?2026l")
}

func (r *terminalRenderer) drawBorder() {
	// Top border with session info
	info := fmt.Sprintf(" %s [%dx%d] ", r.sessionID, r.remoteCols, r.remoteRows)
	topBorder := "┌"
	infoStart := (r.remoteCols - len(info)) / 2
	if infoStart < 0 {
		infoStart = 0
	}
	for i := 0; i < r.remoteCols; i++ {
		if i == infoStart {
			topBorder += info
			i += len(info) - 1
		} else {
			topBorder += "─"
		}
	}
	topBorder += "┐"
	fmt.Printf("\x1b[%d;%dH%s", r.borderTop+1, r.borderLeft+1, topBorder)

	// Side borders
	for row := 0; row < r.remoteRows; row++ {
		fmt.Printf("\x1b[%d;%dH│", r.borderTop+row+2, r.borderLeft+1)
		fmt.Printf("\x1b[%d;%dH│", r.borderTop+row+2, r.borderLeft+r.remoteCols+2)
	}

	// Bottom border with hint
	hint := " Ctrl+\\ to detach "
	bottomBorder := "└"
	hintStart := (r.remoteCols - len(hint)) / 2
	if hintStart < 0 {
		hintStart = 0
	}
	for i := 0; i < r.remoteCols; i++ {
		if i == hintStart {
			bottomBorder += hint
			i += len(hint) - 1
		} else {
			bottomBorder += "─"
		}
	}
	bottomBorder += "┘"
	fmt.Printf("\x1b[%d;%dH%s", r.borderTop+r.remoteRows+2, r.borderLeft+1, bottomBorder)
}

func (r *terminalRenderer) renderRow(vt *vterm.VTerm, row, cols int) {
	var lastAttrs vterm.CellAttrs
	var lastFg, lastBg vterm.Color
	firstCell := true

	for col := 0; col < cols; col++ {
		cell := vt.GetCell(row, col)
		if cell.Width == 0 {
			continue
		}

		if firstCell || cell.Attrs != lastAttrs || cell.Fg != lastFg || cell.Bg != lastBg {
			ansi := buildANSI(cell, lastAttrs, firstCell)
			if len(ansi) > 0 {
				fmt.Print(ansi)
			}
			lastAttrs = cell.Attrs
			lastFg = cell.Fg
			lastBg = cell.Bg
			firstCell = false
		}

		if len(cell.Chars) > 0 && cell.Chars[0] != 0 {
			for _, c := range cell.Chars {
				fmt.Print(string(c))
			}
		} else {
			fmt.Print(" ")
		}
	}

	if !firstCell {
		fmt.Print("\x1b[0m")
	}
}

func buildANSI(cell vterm.Cell, lastAttrs vterm.CellAttrs, first bool) string {
	var codes []int

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

// runVtermOwner runs the goroutine that owns the vterm
// It processes messages until context is cancelled, then cleans up
func runVtermOwner(ctx context.Context, msgCh <-chan vtermMsg, renderer *terminalRenderer, rows, cols int, initialScreen []byte) {
	vt := vterm.New(rows, cols)
	defer vt.Free()

	// Track if we have pending damage
	dirty := false
	vt.OnDamage(func(startRow, endRow, startCol, endCol int) {
		dirty = true
	})

	// Track cursor visibility
	cursorVisible := true
	vt.OnTermProp(func(prop vterm.TermProp, val any) {
		if prop == vterm.PropCursorVisible {
			if v, ok := val.(bool); ok {
				cursorVisible = v
			}
		}
	})

	// Process initial screen
	if len(initialScreen) > 0 {
		vt.Write(initialScreen)
	}

	// Initial render
	renderer.render(vt, true, cursorVisible)

	for {
		select {
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case vtermWrite:
				vt.Write(m.data)
				// Drain any pending writes before rendering (batching)
				for drained := true; drained; {
					select {
					case nextMsg := <-msgCh:
						if w, ok := nextMsg.(vtermWrite); ok {
							vt.Write(w.data)
						} else {
							// Non-write message, process after render
							if dirty {
								renderer.render(vt, false, cursorVisible)
								dirty = false
							}
							// Process the non-write message
							switch m2 := nextMsg.(type) {
							case vtermResize:
								vt.SetSize(m2.rows, m2.cols)
								renderer.updateSize(renderer.localRows, renderer.localCols, m2.rows, m2.cols)
								renderer.render(vt, true, cursorVisible)
							case vtermRender:
								renderer.render(vt, m2.clear, cursorVisible)
							}
							drained = false
						}
					default:
						drained = false
					}
				}
				if dirty {
					renderer.render(vt, false, cursorVisible)
					dirty = false
				}
			case vtermResize:
				vt.SetSize(m.rows, m.cols)
				renderer.updateSize(renderer.localRows, renderer.localCols, m.rows, m.cols)
				renderer.render(vt, true, cursorVisible)
			case vtermRender:
				renderer.render(vt, m.clear, cursorVisible)
			}
		case <-ctx.Done():
			return
		}
	}
}

func cmdAttach(sessionID string) {
	client, err := swash.ConnectTTYSession(sessionID)
	if err != nil {
		fatal("connecting to session: %v", err)
	}
	defer client.Close()

	// Get local terminal size and content size (minus border)
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
	requestRows, requestCols := GetContentSize()

	// Attach to session
	outputFD, inputFD, remoteRows, remoteCols, screenANSI, _, err := client.Attach(int32(requestRows), int32(requestCols))
	if err != nil {
		fatal("attaching to session: %v", err)
	}

	output := os.NewFile(uintptr(outputFD), "attach-output")
	input := os.NewFile(uintptr(inputFD), "attach-input")
	defer output.Close()
	defer input.Close()

	// Put terminal in raw mode
	var oldState *term.State
	if term.IsTerminal(stdinFd) {
		oldState, err = term.MakeRaw(stdinFd)
		if err != nil {
			fatal("setting raw mode: %v", err)
		}
		defer term.Restore(stdinFd, oldState)
	}

	// Enter alternate screen (we exit it explicitly at cleanup, not via defer,
	// so the exit message prints to the normal screen)
	fmt.Print("\x1b[?1049h")

	// Set up context for cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create renderer and vterm message channel
	renderer := newRenderer(sessionID, int(remoteRows), int(remoteCols), localRows, localCols)
	vtermCh := make(chan vtermMsg, 64)

	// Start vterm owner goroutine
	vtermDone := make(chan struct{})
	go func() {
		defer close(vtermDone)
		runVtermOwner(ctx, vtermCh, renderer, int(remoteRows), int(remoteCols), []byte(screenANSI))
	}()

	// Subscribe to exit signal
	exitedCh := client.WaitExited()

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH)

	// Read PTY output
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 4096)
		for {
			n, err := output.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				// Copy data since we're sending async
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case vtermCh <- vtermWrite{data: data}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// Read stdin and forward to PTY
	detachCh := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				if buf[0] == 0x1c { // Ctrl+\
					close(detachCh)
					return
				}
				input.Write(buf[:n])
			}
		}
	}()

	// Track exit reason
	var exitReason string
	var exitCode *int32

	// Main event loop
	for {
		select {
		case code := <-exitedCh:
			exitReason = "exited"
			exitCode = &code
			goto cleanup

		case <-detachCh:
			exitReason = "detached"
			goto cleanup

		case sig := <-sigCh:
			if sig == syscall.SIGWINCH {
				if term.IsTerminal(stdinFd) {
					newCols, newRows, err := term.GetSize(stdinFd)
					if err == nil {
						newContentRows := newRows - 2
						newContentCols := newCols - 2
						if newContentRows < 1 {
							newContentRows = 1
						}
						if newContentCols < 1 {
							newContentCols = 1
						}

						// Try to resize remote if we're the only client
						count, _, _, err := client.GetAttachedClients()
						if err == nil && count == 1 {
							if client.Resize(int32(newContentRows), int32(newContentCols)) == nil {
								renderer.updateSize(newRows, newCols, newContentRows, newContentCols)
								select {
								case vtermCh <- vtermResize{rows: newContentRows, cols: newContentCols}:
								case <-ctx.Done():
								}
							}
						} else {
							// Just update local size for border recalc
							renderer.updateSize(newRows, newCols, renderer.remoteRows, renderer.remoteCols)
							select {
							case vtermCh <- vtermRender{clear: true}:
							case <-ctx.Done():
							}
						}
					}
				}
			} else {
				exitReason = "interrupted"
				goto cleanup
			}
		}
	}

cleanup:
	// Cancel context to stop vterm owner
	cancel()
	// Wait for vterm owner to finish and free vterm
	<-vtermDone

	// Exit alternate screen first, so exit message appears on normal screen
	fmt.Print("\x1b[?1049l")

	// Restore terminal
	if oldState != nil {
		term.Restore(stdinFd, oldState)
	}

	// Print exit message
	switch exitReason {
	case "exited":
		if exitCode != nil {
			fmt.Printf("[exited: %d]\n", *exitCode)
		} else {
			fmt.Println("[exited]")
		}
	case "detached":
		fmt.Println("[detached]")
	case "interrupted":
		fmt.Println("[interrupted]")
	default:
		fmt.Println("[disconnected]")
	}
}
