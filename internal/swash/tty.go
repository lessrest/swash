package swash

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/godbus/dbus/v5"
	"github.com/mbrock/swash/pkg/vterm"
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

// TTYHost extends Host with terminal emulation capabilities.
// It uses a PTY instead of pipes and processes output through libvterm.
type TTYHost struct {
	sessionID  string
	command    []string
	rows, cols int
	tags       map[string]string

	systemd Systemd
	journal Journal

	mu              sync.Mutex
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
}

// TTYHostConfig holds the configuration for creating a TTYHost.
type TTYHostConfig struct {
	SessionID  string
	Command    []string
	Rows, Cols int
	Tags       map[string]string
	Systemd    Systemd
	Journal    Journal

	// OpenPTY is optional; defaults to OpenRealPTY if nil.
	// Provide a custom implementation for testing.
	OpenPTY func() (PTYPair, error)
}

// NewTTYHost creates a new TTYHost with the given configuration.
func NewTTYHost(cfg TTYHostConfig) *TTYHost {
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

	// Merge session ID into tags so output lines can be filtered
	tags := make(map[string]string)
	for k, v := range cfg.Tags {
		tags[k] = v
	}
	tags[FieldSession] = cfg.SessionID

	h := &TTYHost{
		sessionID:     cfg.SessionID,
		command:       cfg.Command,
		rows:          rows,
		cols:          cols,
		tags:          tags,
		systemd:       cfg.Systemd,
		journal:       cfg.Journal,
		maxScrollback: 10000,
		openPTY:       openPTY,
	}

	// Create vterm instance
	h.vt = vterm.New(rows, cols)

	// Set up vterm callbacks
	h.vt.OnPushLine(func(line string) {
		h.mu.Lock()
		// Only log to journal when not in alternate screen mode
		if !h.alternateScreen {
			h.scrollback = append(h.scrollback, line)
			if len(h.scrollback) > h.maxScrollback {
				h.scrollback = h.scrollback[1:]
			}
			// Write scrollback line to journal
			if h.journal != nil {
				WriteOutput(h.journal, 1, line, h.tags)
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

	return h
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
	ctx := context.Background()
	return h.systemd.KillUnit(ctx, TaskUnit(h.sessionID), 9) // SIGKILL
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

	count := int(n)
	if count > len(h.scrollback) {
		count = len(h.scrollback)
	}

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

// RunTask starts the task process and waits for it to complete.
func (h *TTYHost) RunTask(ctx context.Context) error {
	doneChan, err := h.startTTYProcess()
	if err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	// Emit lifecycle event
	if err := EmitStarted(h.journal, h.sessionID, h.command); err != nil {
		return fmt.Errorf("emitting started event: %w", err)
	}

	select {
	case <-doneChan:
		return nil
	case <-ctx.Done():
		h.systemd.KillUnit(context.Background(), TaskUnit(h.sessionID), 9)
		<-doneChan
		return ctx.Err()
	}
}

// startTTYProcess starts the task subprocess with PTY via systemd.
func (h *TTYHost) startTTYProcess() (chan struct{}, error) {
	ctx := context.Background()

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

	// Build environment
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "_") {
			continue
		}
		if idx := strings.Index(e, "="); idx > 0 {
			env[e[:idx]] = e[idx+1:]
		}
	}
	// Set TERM for proper terminal support
	env["TERM"] = "xterm-256color"

	cwd, _ := os.Getwd()

	// Pass the PTY slave FD for all three stdio streams
	slaveFd := ptyPair.SlaveFd()

	spec := TransientSpec{
		Unit:        TaskUnit(h.sessionID),
		Slice:       SessionSlice(h.sessionID),
		ServiceType: "exec",
		WorkingDir:  cwd,
		Description: strings.Join(h.command, " "),
		Environment: env,
		Command:     h.command,
		Collect:     false, // Keep unit around long enough to query exit status
		Stdin:       &slaveFd,
		Stdout:      &slaveFd,
		Stderr:      &slaveFd,
		TTYPath:     ptyPair.SlavePath(), // e.g., /dev/pts/5 (or empty for fakes)
	}

	if err := h.systemd.StartTransient(ctx, spec); err != nil {
		ptyPair.Close()
		return nil, err
	}

	// Close the slave side - systemd now owns it
	ptyPair.CloseSlave()

	// Store ptyPair for SendInput, Resize, and reading
	h.mu.Lock()
	h.ptyPair = ptyPair
	h.running = true
	h.mu.Unlock()

	doneChan := make(chan struct{})
	exitCodeChan := make(chan int, 1)

	// Watch for unit exit via D-Bus
	go func() {
		exitCode, err := h.systemd.WaitUnitExit(ctx, TaskUnit(h.sessionID))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to wait for unit exit: %v\n", err)
			os.Exit(1)
		}
		exitCodeChan <- exitCode
	}()

	// Read from PTY master and feed to vterm
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
				h.vt.Write(buf[:n])
			}
		}

		// Get exit code from D-Bus watcher
		exitCode := <-exitCodeChan
		h.mu.Lock()
		h.exitCode = &exitCode
		h.mu.Unlock()

		// Persist final screen state to journal (with ANSI codes for colors)
		if h.vt != nil && h.journal != nil {
			screenANSI := h.vt.GetScreenANSI()
			h.mu.Lock()
			rows, cols := h.rows, h.cols
			h.mu.Unlock()
			if err := EmitScreen(h.journal, h.sessionID, screenANSI, rows, cols); err != nil {
				fmt.Fprintf(os.Stderr, "error: failed to emit screen: %v\n", err)
			}
		}

		// Emit lifecycle event
		if err := EmitExited(h.journal, h.sessionID, exitCode, h.command); err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to emit exited event: %v\n", err)
		}

		h.mu.Lock()
		h.running = false
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

	return h.RunTask(ctx)
}
