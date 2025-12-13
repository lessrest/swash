package host

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/godbus/dbus/v5"

	"swa.sh/go/vterm"

	"swa.sh/go/swash/internal/journal"
)

// PTYPair represents a bidirectional connection for terminal I/O.
// This abstraction allows testing with fake PTYs.
type PTYPair interface {
	// Master returns the master side (what we read/write)
	Master() io.ReadWriteCloser
	// SlaveFd returns the slave file descriptor for passing to systemd
	SlaveFd() int
	// SlavePath returns the path like /dev/pts/5 (can be empty for fakes)
	SlavePath() string
	// SetSize sets the terminal size
	SetSize(rows, cols uint16) error
	// Close closes both sides
	Close() error
	// CloseSlave closes just the slave side (after systemd takes ownership)
	CloseSlave() error
}

// RealPTY implements PTYPair using actual Unix PTYs.
type RealPTY struct {
	master *os.File
	slave  *os.File
}

// FakePTY implements PTYPair using a Unix socket pair for testing.
// Unlike pipes, socket pairs are bidirectional - each end can read and write,
// matching the semantics of a real PTY.
type FakePTY struct {
	master     *os.File // our side (bidirectional)
	slave      *os.File // process side (bidirectional)
	rows, cols uint16
}

// OpenFakePTY creates a fake PTY pair using a Unix socket pair.
func OpenFakePTY() (PTYPair, error) {
	// Create a bidirectional socket pair
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("creating socket pair: %w", err)
	}

	master := os.NewFile(uintptr(fds[0]), "fakePTY-master")
	slave := os.NewFile(uintptr(fds[1]), "fakePTY-slave")

	return &FakePTY{
		master: master,
		slave:  slave,
		rows:   24,
		cols:   80,
	}, nil
}

func (p *FakePTY) Master() io.ReadWriteCloser {
	return p.master
}

func (p *FakePTY) SlaveFd() int {
	return int(p.slave.Fd())
}

func (p *FakePTY) SlavePath() string {
	return "" // Fake PTYs don't have a path
}

func (p *FakePTY) SetSize(rows, cols uint16) error {
	p.rows = rows
	p.cols = cols
	return nil // No actual ioctl needed for fake PTY
}

func (p *FakePTY) Close() error {
	p.master.Close()
	if p.slave != nil {
		p.slave.Close()
	}
	return nil
}

func (p *FakePTY) CloseSlave() error {
	if p.slave != nil {
		p.slave.Close()
		p.slave = nil
	}
	return nil
}

// OpenRealPTY creates a real PTY pair.
func OpenRealPTY() (PTYPair, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return nil, err
	}
	return &RealPTY{master: master, slave: slave}, nil
}

func (p *RealPTY) Master() io.ReadWriteCloser { return p.master }
func (p *RealPTY) SlaveFd() int               { return int(p.slave.Fd()) }
func (p *RealPTY) SlavePath() string          { return p.slave.Name() }

func (p *RealPTY) SetSize(rows, cols uint16) error {
	return pty.Setsize(p.master, &pty.Winsize{Rows: rows, Cols: cols})
}

func (p *RealPTY) Close() error {
	p.master.Close()
	if p.slave != nil {
		p.slave.Close()
	}
	return nil
}

func (p *RealPTY) CloseSlave() error {
	if p.slave != nil {
		err := p.slave.Close()
		p.slave = nil
		return err
	}
	return nil
}

// attachedClient represents a single attached client with its own output pipe and terminal size.
type attachedClient struct {
	id         string
	output     *os.File   // write end of pipe to send PTY output to client
	inputPipe  *os.File   // read end of input pipe (for cleanup)
	rows, cols int        // client's terminal size
	clientFDs  []*os.File // client-side fds to close after D-Bus sends them
}

// TTYHost extends Host with terminal emulation capabilities.
// It uses a PTY instead of pipes and processes output through libvterm.
type TTYHost struct {
	sessionID  string
	command    []string
	rows, cols int
	tags       map[string]string

	events   journal.EventLog
	executor Executor

	mu              sync.Mutex
	proc            Process // the running task process
	vt              *vterm.VTerm
	ptyPair         PTYPair // PTY pair (for Resize access)
	running         bool
	exitCode        *int
	title           string
	alternateScreen bool
	scrollback      []string
	maxScrollback   int

	// For testing: custom PTY opener
	openPTY func() (PTYPair, error)

	// Multi-client attach support
	attachedClients map[string]*attachedClient // clientID -> client
	nextClientID    int                        // counter for generating unique client IDs

	// Restart support
	restartCh chan struct{}
	doneCh    chan struct{}
}

// TTYHostConfig holds the configuration for creating a TTYHost.
type TTYHostConfig struct {
	SessionID  string
	Command    []string
	Rows, Cols int
	Tags       map[string]string
	Events     journal.EventLog
	Executor   Executor // Optional; defaults to ExecExecutor if nil

	// OpenPTY is optional; defaults to OpenRealPTY if nil.
	// Provide a custom implementation for testing.
	OpenPTY func() (PTYPair, error)
}

// NewTTYHost creates a new TTYHost with the given configuration.
func NewTTYHost(cfg TTYHostConfig) (*TTYHost, error) {
	rows, cols := cfg.Rows, cfg.Cols
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}

	openPTY := cfg.OpenPTY
	if openPTY == nil {
		openPTY = OpenRealPTY
	}

	execImpl := cfg.Executor
	if execImpl == nil {
		execImpl = Default()
	}

	// Merge session ID into tags so output lines can be filtered
	tags := make(map[string]string)
	maps.Copy(tags, cfg.Tags)
	tags[journal.FieldSession] = cfg.SessionID

	h := &TTYHost{
		sessionID:       cfg.SessionID,
		command:         cfg.Command,
		rows:            rows,
		cols:            cols,
		tags:            tags,
		events:          cfg.Events,
		executor:        execImpl,
		maxScrollback:   10000,
		openPTY:         openPTY,
		attachedClients: make(map[string]*attachedClient),
	}

	// Create vterm instance
	vt, err := vterm.New(rows, cols)
	if err != nil {
		return nil, fmt.Errorf("vterm.New: %w", err)
	}
	h.vt = vt

	// Set up vterm callbacks
	h.vt.OnPushLine(func(line string) {
		h.mu.Lock()
		// Only log to the event log when not in alternate screen mode
		if !h.alternateScreen {
			h.scrollback = append(h.scrollback, line)
			if len(h.scrollback) > h.maxScrollback {
				h.scrollback = h.scrollback[1:]
			}
			// Write scrollback line to journal
			if h.events != nil {
				journal.WriteOutput(h.events, 1, line, h.tags)
			}
		}
		h.mu.Unlock()
	})

	h.vt.OnTermProp(func(prop vterm.TermProp, val any) {
		h.mu.Lock()
		defer h.mu.Unlock()
		switch prop {
		case vterm.PropTitle:
			if title, ok := val.(string); ok {
				h.title = title
			}
		case vterm.PropAltScreen:
			if alt, ok := val.(bool); ok {
				h.alternateScreen = alt
			}
		}
	})

	return h, nil
}

// HostStatus returns the current session status (same as Host).
func (h *TTYHost) Gist() (HostStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return HostStatus{
		Running:  h.running,
		ExitCode: h.exitCode,
		Command:  h.command,
	}, nil
}

// SessionID returns the session ID.
func (h *TTYHost) SessionID() (string, error) {
	return h.sessionID, nil
}

// SendInput writes data to the PTY master.
func (h *TTYHost) SendInput(data string) (int, error) {
	h.mu.Lock()
	ptyPair := h.ptyPair
	running := h.running
	h.mu.Unlock()

	if !running || ptyPair == nil {
		return 0, fmt.Errorf("no process running")
	}

	return ptyPair.Master().Write([]byte(data))
}

// Kill sends SIGKILL to the task process.
func (h *TTYHost) Kill() error {
	h.mu.Lock()
	proc := h.proc
	h.mu.Unlock()
	if proc == nil {
		return fmt.Errorf("no process running")
	}
	return proc.Kill()
}

// closePTYMaster closes the PTY master to unblock any readers.
func (h *TTYHost) closePTYMaster() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ptyPair != nil {
		h.ptyPair.Close()
		h.ptyPair = nil
	}
}

// Restart kills the current task and spawns a new one with the same command.
func (h *TTYHost) Restart() error {
	h.mu.Lock()
	if !h.running {
		h.mu.Unlock()
		return fmt.Errorf("no task running")
	}
	restartCh := h.restartCh
	h.mu.Unlock()

	if restartCh == nil {
		return fmt.Errorf("restart not supported")
	}

	// Signal the restart request
	select {
	case restartCh <- struct{}{}:
		return nil
	default:
		return fmt.Errorf("restart already in progress")
	}
}

// Terminal-specific methods

// GetScreenText returns the full screen content.
func (h *TTYHost) GetScreenText() (string, error) {
	return h.vt.GetScreenText(), nil
}

// GetScreenANSI returns the screen content with ANSI escape codes for colors/attributes.
func (h *TTYHost) GetScreenANSI() (string, error) {
	return h.vt.GetScreenANSI(), nil
}

// GetRowText returns a single row's text content.
func (h *TTYHost) GetRowText(row int32) (string, error) {
	return h.vt.GetRowText(int(row)), nil
}

// GetCursor returns the current cursor position.
func (h *TTYHost) GetCursor() (int32, int32, error) {
	row, col := h.vt.GetCursor()
	return int32(row), int32(col), nil
}

// Resize changes the terminal size.
func (h *TTYHost) Resize(rows, cols int32) error {
	h.mu.Lock()
	h.rows = int(rows)
	h.cols = int(cols)
	ptyPair := h.ptyPair
	h.mu.Unlock()

	h.vt.SetSize(int(rows), int(cols))

	if ptyPair != nil {
		return ptyPair.SetSize(uint16(rows), uint16(cols))
	}
	return nil
}

// GetScrollback returns the last n lines from scrollback.
func (h *TTYHost) GetScrollback(n int32) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if n <= 0 || len(h.scrollback) == 0 {
		return []string{}, nil
	}

	count := min(int(n), len(h.scrollback))

	return h.scrollback[len(h.scrollback)-count:], nil
}

// GetTitle returns the terminal title.
func (h *TTYHost) GetTitle() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.title, nil
}

// GetMode returns whether alternate screen is active.
func (h *TTYHost) GetMode() (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.alternateScreen, nil
}

// Attach connects a client to the TTY session.
// Parameters:
//   - clientRows, clientCols: the client's terminal size
//
// Returns:
//   - outputFD: read end of pipe receiving PTY output bytes (as dbus.UnixFD for D-Bus compatibility)
//   - inputFD: write end of pipe for sending input to PTY
//   - rows, cols: current terminal size (may be smaller than client's if other clients attached)
//   - screenANSI: current screen content with ANSI codes
//   - clientID: unique ID for this client (used for Detach)
//
// Multi-client behavior:
//   - First client: terminal resizes to match their size
//   - Subsequent clients: must have terminal >= current size, otherwise rejected
//   - When all clients disconnect, next attach can resize again
//
// The screen snapshot and stream are synchronized - no bytes are lost between them.
func (h *TTYHost) Attach(clientRows, clientCols int32) (outputFD, inputFD dbus.UnixFD, rows, cols int32, screenANSI string, clientID string, err error) {
	h.mu.Lock()
	outputRead, inputWrite, rows, cols, screenANSI, clientID, err := h.attachFilesLocked(clientRows, clientCols)
	h.mu.Unlock()
	if err != nil {
		return 0, 0, 0, 0, "", "", err
	}

	// Close our copies of client fds after D-Bus sends them.
	// (The receiver gets its own dup via D-Bus.)
	go func() {
		time.Sleep(100 * time.Millisecond)
		h.mu.Lock()
		if c, ok := h.attachedClients[clientID]; ok && c.clientFDs != nil {
			for _, f := range c.clientFDs {
				f.Close()
			}
			c.clientFDs = nil
		}
		h.mu.Unlock()
	}()

	return dbus.UnixFD(outputRead.Fd()), dbus.UnixFD(inputWrite.Fd()), rows, cols, screenANSI, clientID, nil
}

// AttachIO attaches a client and returns the read/write ends directly.
//
// This is intended for non-D-Bus transports (e.g. unix socket servers) that want
// to bridge the PTY stream without FD passing. The returned files must be closed
// by the caller when done.
// AttachIO attaches a client and returns the read/write ends directly.
//
// This is intended for non-D-Bus transports (e.g. unix socket servers) that want
// to bridge the PTY stream without FD passing. The returned files must be closed
// by the caller when done.
//
// NOTE: this is a function (not a method) so it isn't exported over D-Bus when
// TTYHost is exported via conn.ExportAll.
func AttachIO(h *TTYHost, clientRows, clientCols int32) (output *os.File, input *os.File, rows, cols int32, screenANSI string, clientID string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.attachFilesLocked(clientRows, clientCols)
}

func (h *TTYHost) attachFilesLocked(clientRows, clientCols int32) (outputRead, inputWrite *os.File, rows, cols int32, screenANSI string, clientID string, err error) {
	// Default client size if not specified
	if clientRows <= 0 {
		clientRows = 24
	}
	if clientCols <= 0 {
		clientCols = 80
	}

	// Multi-client size handling
	if len(h.attachedClients) == 0 {
		// First client: resize terminal to match their size
		h.rows = int(clientRows)
		h.cols = int(clientCols)
		h.vt.SetSize(h.rows, h.cols)
		if h.ptyPair != nil {
			h.ptyPair.SetSize(uint16(h.rows), uint16(h.cols))
		}
	} else {
		// Subsequent clients: must have terminal >= current size
		if int(clientRows) < h.rows || int(clientCols) < h.cols {
			return nil, nil, 0, 0, "", "", fmt.Errorf(
				"terminal too small (need %dx%d, have %dx%d) - resize your terminal or wait for other clients to disconnect",
				h.cols, h.rows, clientCols, clientRows)
		}
	}

	// Create output pipe (PTY -> client)
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, 0, 0, "", "", fmt.Errorf("creating output pipe: %w", err)
	}

	// Create input pipe (client -> PTY)
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		outputRead.Close()
		outputWrite.Close()
		return nil, nil, 0, 0, "", "", fmt.Errorf("creating input pipe: %w", err)
	}

	// Generate unique client ID
	h.nextClientID++
	clientID = fmt.Sprintf("c%d", h.nextClientID)

	// Snapshot screen state while holding the lock
	screenANSI = h.vt.GetScreenANSI()
	// Append cursor positioning - ANSI uses 1-based coordinates
	curRow, curCol := h.vt.GetCursor()
	screenANSI += fmt.Sprintf("\x1b[%d;%dH", curRow+1, curCol+1)
	rows = int32(h.rows)
	cols = int32(h.cols)

	// Create client record
	client := &attachedClient{
		id:        clientID,
		output:    outputWrite,
		inputPipe: inputRead,
		rows:      int(clientRows),
		cols:      int(clientCols),
		clientFDs: []*os.File{outputRead, inputWrite},
	}
	h.attachedClients[clientID] = client

	// Start goroutine to forward input pipe -> PTY master
	go h.forwardAttachedInput(clientID, inputRead)

	return outputRead, inputWrite, rows, cols, screenANSI, clientID, nil
}

// forwardAttachedInput reads from the input pipe and writes to PTY master.
// When the input pipe closes (client disconnected), it cleans up that specific client.
func (h *TTYHost) forwardAttachedInput(clientID string, input *os.File) {
	defer func() {
		input.Close()
		// Clean up this specific client
		h.mu.Lock()
		if client, ok := h.attachedClients[clientID]; ok {
			if client.output != nil {
				client.output.Close()
			}
			delete(h.attachedClients, clientID)
		}
		h.mu.Unlock()
	}()

	buf := make([]byte, 4096)
	for {
		n, err := input.Read(buf)
		if err != nil {
			break
		}
		if n > 0 {
			h.mu.Lock()
			ptyPair := h.ptyPair
			h.mu.Unlock()
			if ptyPair != nil {
				ptyPair.Master().Write(buf[:n])
			}
		}
	}
}

// Detach disconnects a specific client by ID.
func (h *TTYHost) Detach(clientID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	client, ok := h.attachedClients[clientID]
	if !ok {
		return fmt.Errorf("client %s not attached", clientID)
	}

	if client.output != nil {
		client.output.Close()
	}
	if client.inputPipe != nil {
		client.inputPipe.Close()
	}
	delete(h.attachedClients, clientID)
	return nil
}

// GetAttachedClients returns info about currently attached clients.
func (h *TTYHost) GetAttachedClients() (count int32, masterRows, masterCols int32, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return int32(len(h.attachedClients)), int32(h.rows), int32(h.cols), nil
}

// RunTask starts the task process and waits for it to complete.
func (h *TTYHost) RunTask(ctx context.Context) error {
	// Create restart channel
	h.mu.Lock()
	h.restartCh = make(chan struct{}, 1)
	h.mu.Unlock()

	for {
		doneChan, err := h.startTTYProcess()
		if err != nil {
			return fmt.Errorf("starting process: %w", err)
		}

		h.mu.Lock()
		h.doneCh = doneChan
		h.mu.Unlock()

		// Emit lifecycle event
		if err := journal.EmitStarted(h.events, h.sessionID, h.command); err != nil {
			return fmt.Errorf("emitting started event: %w", err)
		}

		select {
		case <-doneChan:
			// Task exited normally
			return nil
		case <-h.restartCh:
			// Restart requested - kill current task and loop
			fmt.Fprintf(os.Stderr, "Restart requested, killing task\n")
			h.Kill()
			// Close PTY to unblock reader
			h.closePTYMaster()
			<-doneChan // Wait for task to actually exit
			fmt.Fprintf(os.Stderr, "Starting new task\n")
			// Loop continues to start new task
		case <-ctx.Done():
			h.Kill()
			// Close PTY to unblock reader
			h.closePTYMaster()
			<-doneChan
			return ctx.Err()
		}
	}
}

// startTTYProcess starts the task subprocess with PTY via the
func (h *TTYHost) startTTYProcess() (chan struct{}, error) {
	// Create PTY pair using the injected opener
	ptyPair, err := h.openPTY()
	if err != nil {
		return nil, fmt.Errorf("opening pty: %w", err)
	}

	// Set initial size
	if err := ptyPair.SetSize(uint16(h.rows), uint16(h.cols)); err != nil {
		ptyPair.Close()
		return nil, fmt.Errorf("setting pty size: %w", err)
	}

	// Get the slave file for the executor
	slave := os.NewFile(uintptr(ptyPair.SlaveFd()), "pty-slave")

	// Start the process using the executor
	proc, err := h.executor.StartPTY(h.command, slave)
	if err != nil {
		ptyPair.Close()
		return nil, fmt.Errorf("starting process: %w", err)
	}

	// Close the slave side - child now owns it
	ptyPair.CloseSlave()

	// Store proc and ptyPair for SendInput, Resize, Kill
	h.mu.Lock()
	h.proc = proc
	h.ptyPair = ptyPair
	h.running = true
	h.mu.Unlock()

	doneChan := make(chan struct{})

	// Read from PTY master and feed to vterm (and attached clients)
	go func() {
		master := ptyPair.Master()
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if err != nil {
				if err != io.EOF {
					// Log but don't fail - PTY read errors are expected on exit
				}
				break
			}
			if n > 0 {
				// Feed to vterm (vterm has its own lock)
				h.vt.Write(buf[:n])
				// Broadcast to all attached clients
				h.mu.Lock()
				for _, client := range h.attachedClients {
					if client.output != nil {
						// Write to each client; ignore errors (client may have disconnected)
						client.output.Write(buf[:n])
					}
				}
				h.mu.Unlock()
			}
		}

		// Wait for process to exit and get exit code
		exitCode, _ := proc.Wait()

		h.mu.Lock()
		h.exitCode = &exitCode
		h.mu.Unlock()

		// Persist final screen state to the event log (with ANSI codes for colors)
		if h.vt != nil && h.events != nil {
			screenANSI := h.vt.GetScreenANSI()
			h.mu.Lock()
			rows, cols := h.rows, h.cols
			h.mu.Unlock()
			if err := journal.EmitScreen(h.events, h.sessionID, screenANSI, rows, cols); err != nil {
				fmt.Fprintf(os.Stderr, "error: failed to emit screen: %v\n", err)
			}
		}

		// Emit lifecycle event
		if err := journal.EmitExited(h.events, h.sessionID, exitCode, h.command); err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to emit exited event: %v\n", err)
		}

		h.mu.Lock()
		h.running = false
		h.proc = nil
		if h.ptyPair != nil {
			h.ptyPair.Close()
		}
		h.ptyPair = nil
		h.mu.Unlock()

		close(doneChan)
	}()

	return doneChan, nil
}

// Close releases resources.
func (h *TTYHost) Close() {
	h.mu.Lock()
	if h.ptyPair != nil {
		h.ptyPair.Close()
		h.ptyPair = nil
	}
	h.mu.Unlock()
	if h.vt != nil {
		h.vt.Free()
		h.vt = nil
	}
}

// Run starts the D-Bus host and runs until the task exits or a signal is received.
func (h *TTYHost) Run() error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to D-Bus: %w", err)
	}
	defer conn.Close()

	busName := fmt.Sprintf("%s.%s", DBusNamePrefix, h.sessionID)
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("requesting bus name: %w", err)
	}

	conn.ExportAll(h, dbus.ObjectPath(DBusPath), DBusNamePrefix)

	// Set up context that cancels on SIGTERM/SIGINT
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case sig := <-sigChan:
			fmt.Fprintf(os.Stderr, "Received %v, killing task\n", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	err = h.RunTask(ctx)

	// Emit exit signal to attached clients before closing D-Bus connection
	h.mu.Lock()
	exitCode := h.exitCode
	h.mu.Unlock()
	if exitCode != nil {
		conn.Emit(dbus.ObjectPath(DBusPath), DBusNamePrefix+".Exited", int32(*exitCode))
	}

	return err
}
