// Package attach provides terminal attach functionality for TTY sessions.
package attach

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

// ConnectFunc is the function type for connecting to a TTY session.
type ConnectFunc func(sessionID string) (host.TTYClient, error)

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

// renderer handles all rendering to the real terminal
type renderer struct {
	sessionID  string
	remoteRows int
	remoteCols int
	localRows  int
	localCols  int
	borderTop  int
	borderLeft int
}

func newRenderer(sessionID string, remoteRows, remoteCols, localRows, localCols int) *renderer {
	r := &renderer{
		sessionID:  sessionID,
		remoteRows: remoteRows,
		remoteCols: remoteCols,
		localRows:  localRows,
		localCols:  localCols,
	}
	r.calculateOffsets()
	return r
}

func (r *renderer) calculateOffsets() {
	r.borderTop = (r.localRows - r.remoteRows - 2) / 2
	r.borderLeft = (r.localCols - r.remoteCols - 2) / 2
	if r.borderTop < 0 {
		r.borderTop = 0
	}
	if r.borderLeft < 0 {
		r.borderLeft = 0
	}
}

func (r *renderer) updateSize(localRows, localCols, remoteRows, remoteCols int) {
	r.localRows = localRows
	r.localCols = localCols
	r.remoteRows = remoteRows
	r.remoteCols = remoteCols
	r.calculateOffsets()
}

func (r *renderer) render(vt *vterm.VTerm, clear bool, cursorVisible bool) {
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
		//		fmt.Printf("\x1b[2K")
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

func (r *renderer) drawBorder() {
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
func runVtermOwner(ctx context.Context, msgCh <-chan vtermMsg, rend *renderer, rows, cols int, initialScreen []byte) {
	vt := vterm.New(rows, cols)
	defer vt.Free()

	dirty := false
	vt.OnDamage(func(startRow, endRow, startCol, endCol int) {
		dirty = true
	})

	cursorVisible := true
	vt.OnTermProp(func(prop vterm.TermProp, val any) {
		if prop == vterm.PropCursorVisible {
			if v, ok := val.(bool); ok {
				cursorVisible = v
			}
		}
	})

	if len(initialScreen) > 0 {
		vt.Write(initialScreen)
	}

	rend.render(vt, true, cursorVisible)

	for {
		select {
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case vtermWrite:
				vt.Write(m.data)
				// Drain pending writes before rendering (batching)
				for drained := true; drained; {
					select {
					case nextMsg := <-msgCh:
						if w, ok := nextMsg.(vtermWrite); ok {
							vt.Write(w.data)
						} else {
							if dirty {
								rend.render(vt, false, cursorVisible)
								dirty = false
							}
							switch m2 := nextMsg.(type) {
							case vtermResize:
								vt.SetSize(m2.rows, m2.cols)
								rend.updateSize(rend.localRows, rend.localCols, m2.rows, m2.cols)
								rend.render(vt, true, cursorVisible)
							case vtermRender:
								rend.render(vt, m2.clear, cursorVisible)
							}
							drained = false
						}
					default:
						drained = false
					}
				}
				if dirty {
					rend.render(vt, false, cursorVisible)
					dirty = false
				}
			case vtermResize:
				vt.SetSize(m.rows, m.cols)
				rend.updateSize(rend.localRows, rend.localCols, m.rows, m.cols)
				rend.render(vt, true, cursorVisible)
			case vtermRender:
				rend.render(vt, m.clear, cursorVisible)
			}
		case <-ctx.Done():
			return
		}
	}
}

// ExitReason describes why the attach session ended.
type ExitReason string

const (
	ExitReasonExited      ExitReason = "exited"
	ExitReasonDetached    ExitReason = "detached"
	ExitReasonInterrupted ExitReason = "interrupted"
	ExitReasonDisconnect  ExitReason = "disconnected"
)

// Result contains the outcome of an attach session.
type Result struct {
	Reason   ExitReason
	ExitCode *int32
}

// Session encapsulates the state for an attached terminal session.
type Session struct {
	sessionID string
	client    host.TTYClient
	stdinFd   int
	oldState  *term.State

	conn io.ReadWriteCloser

	localRows, localCols   int
	remoteRows, remoteCols int
	initialScreen          string

	ctx       context.Context
	cancel    context.CancelFunc
	vtermCh   chan vtermMsg
	vtermDone chan struct{}
	detachCh  chan struct{}
	exitedCh  <-chan int32
	sigCh     chan os.Signal

	rend *renderer

	result Result
}

// New creates a new attach session.
func New(sessionID string, connect ConnectFunc) (*Session, error) {
	s := &Session{
		sessionID: sessionID,
		stdinFd:   int(os.Stdin.Fd()),
	}

	client, err := connect(sessionID)
	if err != nil {
		return nil, fmt.Errorf("connecting to session: %w", err)
	}
	s.client = client

	if term.IsTerminal(s.stdinFd) {
		s.localCols, s.localRows, err = term.GetSize(s.stdinFd)
		if err != nil {
			s.localCols, s.localRows = 80, 24
		}
	} else {
		s.localCols, s.localRows = 80, 24
	}

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

func (s *Session) readOutput() {
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

func (s *Session) readInput() {
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

func (s *Session) handleResize() {
	if !term.IsTerminal(s.stdinFd) {
		return
	}

	newCols, newRows, err := term.GetSize(s.stdinFd)
	if err != nil {
		return
	}

	newContentRows := max(newRows-2, 1)
	newContentCols := max(newCols-2, 1)

	count, _, _, err := s.client.GetAttachedClients()
	if err == nil && count == 1 {
		if s.client.Resize(int32(newContentRows), int32(newContentCols)) == nil {
			s.rend.updateSize(newRows, newCols, newContentRows, newContentCols)
			select {
			case s.vtermCh <- vtermResize{rows: newContentRows, cols: newContentCols}:
			case <-s.ctx.Done():
			}
		}
	} else {
		s.rend.updateSize(newRows, newCols, s.rend.remoteRows, s.rend.remoteCols)
		select {
		case s.vtermCh <- vtermRender{clear: true}:
		case <-s.ctx.Done():
		}
	}
}

// Run runs the attach session until it ends.
// Returns the result describing how and why it ended.
func (s *Session) Run() Result {
	// Set up terminal
	if term.IsTerminal(s.stdinFd) {
		var err error
		s.oldState, err = term.MakeRaw(s.stdinFd)
		if err != nil {
			s.result = Result{Reason: ExitReasonDisconnect}
			return s.result
		}
	}
	fmt.Print("\x1b[?1049h") // Enter alternate screen

	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.vtermCh = make(chan vtermMsg, 64)
	s.vtermDone = make(chan struct{})
	s.detachCh = make(chan struct{})
	s.sigCh = make(chan os.Signal, 1)

	s.rend = newRenderer(s.sessionID, s.remoteRows, s.remoteCols, s.localRows, s.localCols)

	go func() {
		defer close(s.vtermDone)
		runVtermOwner(s.ctx, s.vtermCh, s.rend, s.remoteRows, s.remoteCols, []byte(s.initialScreen))
	}()

	s.exitedCh = s.client.WaitExited()
	signal.Notify(s.sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH)

	go s.readOutput()
	go s.readInput()

	// Event loop
	for {
		select {
		case code := <-s.exitedCh:
			s.result = Result{Reason: ExitReasonExited, ExitCode: &code}
			return s.result

		case <-s.detachCh:
			s.result = Result{Reason: ExitReasonDetached}
			return s.result

		case sig := <-s.sigCh:
			if sig == syscall.SIGWINCH {
				s.handleResize()
			} else {
				s.result = Result{Reason: ExitReasonInterrupted}
				return s.result
			}
		}
	}
}

// Close cleans up the session resources.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
		<-s.vtermDone
	}

	fmt.Print("\x1b[?1049l") // Exit alternate screen

	if s.oldState != nil {
		term.Restore(s.stdinFd, s.oldState)
	}

	if s.conn != nil {
		s.conn.Close()
	}
	if s.client != nil {
		s.client.Close()
	}
}

// PrintResult prints a human-readable exit message.
func (r Result) PrintResult() {
	switch r.Reason {
	case ExitReasonExited:
		if r.ExitCode != nil {
			fmt.Printf("[exited: %d]\n", *r.ExitCode)
		} else {
			fmt.Println("[exited]")
		}
	case ExitReasonDetached:
		fmt.Println("[detached]")
	case ExitReasonInterrupted:
		fmt.Println("[interrupted]")
	default:
		fmt.Println("[disconnected]")
	}
}
