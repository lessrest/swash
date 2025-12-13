package host

import (
	"crypto/rand"
	"encoding/json"
	"io"

	"github.com/mbrock/swash/internal/protocol"
)

// D-Bus constants for the swash host interface.
const (
	DBusNamePrefix = "sh.swa.Swash"
	DBusPath       = "/sh/swa/Swash"
)

// Job represents a running swash session (high-level view).
type Job struct {
	ID      string `json:"id"`
	Unit    string `json:"unit"`
	PID     uint32 `json:"pid"`
	CWD     string `json:"cwd"`
	Status  string `json:"status"`
	Command string `json:"command"`
	Started string `json:"started"`
}

// GenID generates a short random session ID like "KXO284".
func GenID() string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const digits = "0123456789"

	b := make([]byte, 6)
	rand.Read(b)

	id := make([]byte, 6)
	for i := range 3 {
		id[i] = letters[int(b[i])%len(letters)]
	}
	for i := 3; i < 6; i++ {
		id[i] = digits[int(b[i])%len(digits)]
	}
	return string(id)
}

// Options configures a new session.
type Options struct {
	Protocol protocol.Protocol // shell (default), sse
	Tags     map[string]string // Extra journal fields
	TTY      bool              // Use PTY mode with terminal emulation
	Rows     int               // Terminal rows (for TTY mode)
	Cols     int               // Terminal columns (for TTY mode)
}

// HostStatus represents the current state of a Host session.
type HostStatus struct {
	Running  bool     `json:"running"`
	ExitCode *int     `json:"exit_code"`
	Command  []string `json:"command"`
}

// Controller defines the methods exposed for controlling a session.
// Both Host (server) and Client (proxy) implement this interface.
type Controller interface {
	// SendInput sends input to the process stdin.
	SendInput(input string) (int, error)

	// Kill sends SIGKILL to the process.
	Kill() error

	// Restart kills the task and spawns a new one with the same command.
	Restart() error

	// Gist returns session status.
	Gist() (HostStatus, error)

	// SessionID returns the session ID.
	SessionID() (string, error)
}

// Client extends Controller with connection management.
type Client interface {
	Controller
	Close() error
}

// TTYClient extends Client with terminal-specific methods.
type TTYClient interface {
	Client

	GetScreenText() (string, error)
	GetScreenANSI() (string, error)
	GetCursor() (int32, int32, error)
	GetTitle() (string, error)
	GetMode() (bool, error)
	Resize(rows, cols int32) error
	Attach(clientRows, clientCols int32) (*TTYAttachment, error)
	Detach(clientID string) error
	GetAttachedClients() (count int32, masterRows, masterCols int32, err error)
	WaitExited() <-chan int32
}

// TTYAttachment is the result of attaching to a running TTY session.
type TTYAttachment struct {
	Conn       io.ReadWriteCloser
	Rows, Cols int32
	ScreenANSI string
	ClientID   string
}

// SplitConn adapts separate read/write streams into a single ReadWriteCloser.
type SplitConn struct {
	R io.ReadCloser
	W io.WriteCloser
}

func (c *SplitConn) Read(p []byte) (int, error)  { return c.R.Read(p) }
func (c *SplitConn) Write(p []byte) (int, error) { return c.W.Write(p) }
func (c *SplitConn) Close() error {
	if c == nil {
		return nil
	}
	var errW, errR error
	if c.W != nil {
		errW = c.W.Close()
	}
	if c.R != nil {
		errR = c.R.Close()
	}
	if errW != nil {
		return errW
	}
	return errR
}

// MustJSON marshals v to JSON, panicking on error.
func MustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
