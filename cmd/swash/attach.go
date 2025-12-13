package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/mbrock/swash/internal/host"
	"github.com/mbrock/swash/pkg/vterm"
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
	rows, _ := vt.GetSize()
	contentTop := r.borderTop + 1
	contentLeft := r.borderLeft + 1

	for row := range rows {
		fmt.Printf("\x1b[%d;%dH", contentTop+row+1, contentLeft+1)
		fmt.Print(vt.RenderRowANSI(row))
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
	var topBorder strings.Builder
	topBorder.WriteString("┌")
	infoStart := max((r.remoteCols-len(info))/2, 0)
	for i := 0; i < r.remoteCols; i++ {
		if i == infoStart {
			topBorder.WriteString(info)
			i += len(info) - 1
		} else {
			topBorder.WriteString("─")
		}
	}
	topBorder.WriteString("┐")
	fmt.Printf("\x1b[%d;%dH%s", r.borderTop+1, r.borderLeft+1, topBorder.String())

	// Side borders
	for row := 0; row < r.remoteRows; row++ {
		fmt.Printf("\x1b[%d;%dH│", r.borderTop+row+2, r.borderLeft+1)
		fmt.Printf("\x1b[%d;%dH│", r.borderTop+row+2, r.borderLeft+r.remoteCols+2)
	}

	// Bottom border with hint
	hint := " Ctrl+\\ to detach "
	var bottomBorder strings.Builder
	bottomBorder.WriteString("└")
	hintStart := max((r.remoteCols-len(hint))/2, 0)
	for i := 0; i < r.remoteCols; i++ {
		if i == hintStart {
			bottomBorder.WriteString(hint)
			i += len(hint) - 1
		} else {
			bottomBorder.WriteString("─")
		}
	}
	bottomBorder.WriteString("┘")
	fmt.Printf("\x1b[%d;%dH%s", r.borderTop+r.remoteRows+2, r.borderLeft+1, bottomBorder.String())
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

// attachSession encapsulates the state for an attached terminal session.
type attachSession struct {
	sessionID string
	client    host.TTYClient
	stdinFd   int
	oldState  *term.State

	// Remote PTY IO
	conn io.ReadWriteCloser

	// Sizes
	localRows, localCols   int
	remoteRows, remoteCols int
	initialScreen          string

	// Coordination
	ctx       context.Context
	cancel    context.CancelFunc
	vtermCh   chan vtermMsg
	vtermDone chan struct{}
	detachCh  chan struct{}
	exitedCh  <-chan int32
	sigCh     chan os.Signal

	// Rendering
	renderer *terminalRenderer

	// Exit state
	exitReason string
	exitCode   *int32
}

func newAttachSession(sessionID string, connectTTY func(string) (host.TTYClient, error)) (*attachSession, error) {
	s := &attachSession{
		sessionID: sessionID,
		stdinFd:   int(os.Stdin.Fd()),
	}

	// Connect to session
	client, err := connectTTY(sessionID)
	if err != nil {
		return nil, fmt.Errorf("connecting to session: %w", err)
	}
	s.client = client

	// Get local terminal size
	if term.IsTerminal(s.stdinFd) {
		s.localCols, s.localRows, err = term.GetSize(s.stdinFd)
		if err != nil {
			s.localCols, s.localRows = 80, 24
		}
	} else {
		s.localCols, s.localRows = 80, 24
	}

	// Attach to session
	requestRows, requestCols := GetContentSize()
	att, err := client.Attach(int32(requestRows), int32(requestCols))
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("attaching to session: %w", err)
	}
	if att == nil || att.Conn == nil {
		client.Close()
		return nil, fmt.Errorf("attaching to session: missing stream")
	}

	s.conn = att.Conn
	s.remoteRows = int(att.Rows)
	s.remoteCols = int(att.Cols)
	s.initialScreen = att.ScreenANSI

	return s, nil
}

func (s *attachSession) readOutput() {
	if s.conn == nil {
		return
	}
	buf := make([]byte, 4096)
	for {
		n, err := s.conn.Read(buf)
		if err != nil {
			return
		}
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			select {
			case s.vtermCh <- vtermWrite{data: data}:
			case <-s.ctx.Done():
				return
			}
		}
	}
}

func (s *attachSession) readInput() {
	if s.conn == nil {
		return
	}
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		if n > 0 {
			if buf[0] == 0x1c { // Ctrl+\
				close(s.detachCh)
				return
			}
			if _, err := s.conn.Write(buf[:n]); err != nil {
				return
			}
		}
	}
}

func (s *attachSession) handleResize() {
	if !term.IsTerminal(s.stdinFd) {
		return
	}

	newCols, newRows, err := term.GetSize(s.stdinFd)
	if err != nil {
		return
	}

	newContentRows := max(newRows-2, 1)
	newContentCols := max(newCols-2, 1)

	// Try to resize remote if we're the only client
	count, _, _, err := s.client.GetAttachedClients()
	if err == nil && count == 1 {
		if s.client.Resize(int32(newContentRows), int32(newContentCols)) == nil {
			s.renderer.updateSize(newRows, newCols, newContentRows, newContentCols)
			select {
			case s.vtermCh <- vtermResize{rows: newContentRows, cols: newContentCols}:
			case <-s.ctx.Done():
			}
		}
	} else {
		// Just update local size for border recalc
		s.renderer.updateSize(newRows, newCols, s.renderer.remoteRows, s.renderer.remoteCols)
		select {
		case s.vtermCh <- vtermRender{clear: true}:
		case <-s.ctx.Done():
		}
	}
}

func (s *attachSession) run() {
	// Set up terminal
	if term.IsTerminal(s.stdinFd) {
		var err error
		s.oldState, err = term.MakeRaw(s.stdinFd)
		if err != nil {
			fatal("setting raw mode: %v", err)
		}
	}
	fmt.Print("\x1b[?1049h") // Enter alternate screen

	// Initialize coordination channels
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.vtermCh = make(chan vtermMsg, 64)
	s.vtermDone = make(chan struct{})
	s.detachCh = make(chan struct{})
	s.sigCh = make(chan os.Signal, 1)

	s.renderer = newRenderer(s.sessionID, s.remoteRows, s.remoteCols, s.localRows, s.localCols)

	// Start vterm owner
	go func() {
		defer close(s.vtermDone)
		runVtermOwner(s.ctx, s.vtermCh, s.renderer, s.remoteRows, s.remoteCols, []byte(s.initialScreen))
	}()

	// Subscribe to exit signal and OS signals
	s.exitedCh = s.client.WaitExited()
	signal.Notify(s.sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH)

	// Start IO goroutines
	go s.readOutput()
	go s.readInput()

	// Event loop
	for {
		select {
		case code := <-s.exitedCh:
			s.exitReason = "exited"
			s.exitCode = &code
			return

		case <-s.detachCh:
			s.exitReason = "detached"
			return

		case sig := <-s.sigCh:
			if sig == syscall.SIGWINCH {
				s.handleResize()
			} else {
				s.exitReason = "interrupted"
				return
			}
		}
	}
}

func (s *attachSession) cleanup() {
	s.cancel()
	<-s.vtermDone

	fmt.Print("\x1b[?1049l") // Exit alternate screen

	if s.oldState != nil {
		term.Restore(s.stdinFd, s.oldState)
	}

	if s.conn != nil {
		s.conn.Close()
	}
	s.client.Close()

	switch s.exitReason {
	case "exited":
		if s.exitCode != nil {
			fmt.Printf("[exited: %d]\n", *s.exitCode)
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

func cmdAttach(sessionID string) {
	initBackend()
	defer bk.Close()

	session, err := newAttachSession(sessionID, bk.ConnectTTYSession)
	if err != nil {
		fatal("%v", err)
	}
	defer session.cleanup()

	session.run()
}
