// vterm-service: Terminal emulator over D-Bus
// Go implementation using libvterm bindings
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/godbus/dbus/v5"
	"github.com/mbrock/swash/pkg/vterm"
)

const (
	dbusInterface  = "org.claude.VTerm"
	dbusObjectPath = "/org/claude/VTerm"
)

// VTermService implements the D-Bus interface for terminal emulation.
type VTermService struct {
	conn *dbus.Conn
	vt   *vterm.VTerm
	mu   sync.Mutex

	// PTY and process
	ptmx     *os.File
	cmd      *exec.Cmd
	running  bool
	exitCode int

	// Terminal state
	rows, cols      int
	scrollback      []string
	maxScrollback   int
	title           string
	alternateScreen bool

	// Signal emission control
	signalsReady bool
}

// NewVTermService creates a new VTerm D-Bus service.
func NewVTermService(conn *dbus.Conn, rows, cols int) *VTermService {
	s := &VTermService{
		conn:          conn,
		rows:          rows,
		cols:          cols,
		maxScrollback: 10000,
		exitCode:      -1,
	}

	s.vt = vterm.New(rows, cols)

	// Set up callbacks
	s.vt.OnDamage(func(startRow, endRow, startCol, endCol int) {
		s.emitDamage(startRow, endRow)
	})

	s.vt.OnMoveCursor(func(row, col int, visible bool) {
		s.emitCursorMoved(row, col)
	})

	s.vt.OnBell(func() {
		s.emitBell()
	})

	s.vt.OnPushLine(func(line string) {
		s.mu.Lock()
		s.scrollback = append(s.scrollback, line)
		if len(s.scrollback) > s.maxScrollback {
			s.scrollback = s.scrollback[1:]
		}
		s.mu.Unlock()
		s.emitScrollLine(line)
	})

	s.vt.OnTermProp(func(prop vterm.TermProp, val any) {
		switch prop {
		case vterm.PropTitle:
			if title, ok := val.(string); ok {
				s.mu.Lock()
				s.title = title
				s.mu.Unlock()
				s.emitTitleChanged(title)
			}
		case vterm.PropAltScreen:
			if alt, ok := val.(bool); ok {
				s.mu.Lock()
				s.alternateScreen = alt
				s.mu.Unlock()
				s.emitScreenMode(alt)
			}
		}
	})

	return s
}

// SpawnCommand spawns a command in the PTY.
func (s *VTermService) SpawnCommand(args []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("already running")
	}

	if len(args) == 0 {
		return nil
	}

	s.cmd = exec.Command(args[0], args[1:]...)
	s.cmd.Env = os.Environ()

	var err error
	s.ptmx, err = pty.StartWithSize(s.cmd, &pty.Winsize{
		Rows: uint16(s.rows),
		Cols: uint16(s.cols),
	})
	if err != nil {
		return fmt.Errorf("pty.Start: %w", err)
	}

	s.running = true

	// Start reader goroutine
	go s.readLoop()

	return nil
}

func (s *VTermService) readLoop() {
	buf := make([]byte, 4096)

	for {
		n, err := s.ptmx.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Printf("read error: %v", err)
			}
			break
		}

		if n > 0 {
			s.vt.Write(buf[:n])
		}
	}

	// Wait for process and get exit code
	if s.cmd != nil {
		if err := s.cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				s.exitCode = exitErr.ExitCode()
			}
		} else {
			s.exitCode = 0
		}
	}

	s.mu.Lock()
	s.running = false
	s.mu.Unlock()

	s.ptmx.Close()
	s.emitExited(s.exitCode)
}

// D-Bus signal emitters

func (s *VTermService) emitDamage(startRow, endRow int) {
	if !s.signalsReady {
		return
	}
	s.conn.Emit(dbusObjectPath, dbusInterface+".Damage", int32(startRow), int32(endRow))
}

func (s *VTermService) emitCursorMoved(row, col int) {
	if !s.signalsReady {
		return
	}
	s.conn.Emit(dbusObjectPath, dbusInterface+".CursorMoved", int32(row), int32(col))
}

func (s *VTermService) emitBell() {
	if !s.signalsReady {
		return
	}
	s.conn.Emit(dbusObjectPath, dbusInterface+".Bell")
}

func (s *VTermService) emitExited(code int) {
	if !s.signalsReady {
		return
	}
	s.conn.Emit(dbusObjectPath, dbusInterface+".Exited", int32(code))
}

func (s *VTermService) emitScrollLine(line string) {
	if !s.signalsReady {
		return
	}
	s.conn.Emit(dbusObjectPath, dbusInterface+".ScrollLine", line)
}

func (s *VTermService) emitTitleChanged(title string) {
	if !s.signalsReady {
		return
	}
	s.conn.Emit(dbusObjectPath, dbusInterface+".TitleChanged", title)
}

func (s *VTermService) emitScreenMode(alternate bool) {
	if !s.signalsReady {
		return
	}
	s.conn.Emit(dbusObjectPath, dbusInterface+".ScreenMode", alternate)
}

// D-Bus method implementations

func (s *VTermService) GetRowText(row int32) (string, *dbus.Error) {
	return s.vt.GetRowText(int(row)), nil
}

func (s *VTermService) GetScreenText() (string, *dbus.Error) {
	return s.vt.GetScreenText(), nil
}

func (s *VTermService) GetScreenHtml() (string, *dbus.Error) {
	// TODO: Implement HTML rendering
	return s.vt.GetScreenText(), nil
}

func (s *VTermService) GetScreenData() (string, *dbus.Error) {
	// TODO: Implement data format with color info
	return s.vt.GetScreenText(), nil
}

func (s *VTermService) SendInput(data string) *dbus.Error {
	s.mu.Lock()
	ptmx := s.ptmx
	s.mu.Unlock()

	if ptmx != nil {
		ptmx.WriteString(data)
	}
	return nil
}

func (s *VTermService) Kill(sig int32) *dbus.Error {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		cmd.Process.Signal(syscall.Signal(sig))
	}
	return nil
}

func (s *VTermService) Resize(rows, cols int32) *dbus.Error {
	s.mu.Lock()
	s.rows = int(rows)
	s.cols = int(cols)
	ptmx := s.ptmx
	s.mu.Unlock()

	s.vt.SetSize(int(rows), int(cols))

	if ptmx != nil {
		pty.Setsize(ptmx, &pty.Winsize{
			Rows: uint16(rows),
			Cols: uint16(cols),
		})
	}
	return nil
}

func (s *VTermService) GetCursor() (int32, int32, *dbus.Error) {
	row, col := s.vt.GetCursor()
	return int32(row), int32(col), nil
}

func (s *VTermService) IsRunning() (bool, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, nil
}

func (s *VTermService) GetExitCode() (int32, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int32(s.exitCode), nil
}

func (s *VTermService) GetScrollback(n int32) ([]string, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if n <= 0 || len(s.scrollback) == 0 {
		return []string{}, nil
	}

	count := int(n)
	if count > len(s.scrollback) {
		count = len(s.scrollback)
	}

	return s.scrollback[len(s.scrollback)-count:], nil
}

func (s *VTermService) GetTitle() (string, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title, nil
}

func (s *VTermService) GetMode() (bool, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alternateScreen, nil
}

func (s *VTermService) RenderPNG() ([]byte, *dbus.Error) {
	// PNG rendering not implemented in Go version
	return []byte{}, nil
}

// Stop cleans up resources.
func (s *VTermService) Stop() {
	s.mu.Lock()
	ptmx := s.ptmx
	cmd := s.cmd
	s.mu.Unlock()

	if ptmx != nil {
		ptmx.Close()
	}
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
		cmd.Wait()
	}
	s.vt.Free()
}

func main() {
	busName := flag.String("name", "org.claude.VTerm", "D-Bus bus name")
	rows := flag.Int("rows", 24, "Terminal rows")
	cols := flag.Int("cols", 80, "Terminal columns")
	flag.Parse()

	// Everything after -- is the command
	var cmdArgs []string
	for i, arg := range os.Args {
		if arg == "--" {
			cmdArgs = os.Args[i+1:]
			break
		}
	}

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Fatalf("Failed to connect to session bus: %v", err)
	}
	defer conn.Close()

	reply, err := conn.RequestName(*busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		log.Fatalf("Failed to request name %s: %v", *busName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Fatalf("Name %s already taken", *busName)
	}

	service := NewVTermService(conn, *rows, *cols)
	defer service.Stop()

	// Export the service
	err = conn.Export(service, dbusObjectPath, dbusInterface)
	if err != nil {
		log.Fatalf("Failed to export service: %v", err)
	}

	// Enable signal emission
	service.signalsReady = true

	// Spawn command if provided
	if len(cmdArgs) > 0 {
		if err := service.SpawnCommand(cmdArgs); err != nil {
			log.Fatalf("Failed to spawn command: %v", err)
		}
	}

	fmt.Fprintf(os.Stderr, "vterm-service started: %s (%dx%d)", *busName, *rows, *cols)
	if len(cmdArgs) > 0 {
		fmt.Fprintf(os.Stderr, " running: %s", cmdArgs[0])
	}
	fmt.Fprintln(os.Stderr)

	// Wait for signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}
