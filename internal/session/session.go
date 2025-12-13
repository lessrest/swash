package session

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"

	"github.com/mbrock/swash/internal/protocol"
)

// Session represents a running swash session (high-level view).
type Session struct {
	ID      string `json:"id"`
	Unit    string `json:"unit"`
	PID     uint32 `json:"pid"`
	CWD     string `json:"cwd"`
	Status  string `json:"status"`
	Command string `json:"command"`
	Started string `json:"started"`
}

// GenSessionID generates a short random session ID like "KXO284".
func GenSessionID() string {
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

// SessionOptions configures a new session.
type SessionOptions struct {
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

// SessionController defines the methods exposed over D-Bus for controlling a session.
// Both Host (server) and sessionClient (client) implement this interface.
// This provides compile-time checking that signatures match.
type SessionController interface {
	// SendInput sends input to the process stdin.
	// Returns the number of bytes written.
	SendInput(input string) (int, error)

	// Kill sends SIGKILL to the process.
	Kill() error

	// Restart kills the task and spawns a new one with the same command.
	// The session stays alive; only the task process is replaced.
	Restart() error

	// Gist returns session status.
	Gist() (HostStatus, error)

	// SessionID returns the session ID.
	SessionID() (string, error)
}

// SessionClient extends SessionController with connection management.
type SessionClient interface {
	SessionController

	// Close releases the D-Bus connection.
	Close() error
}

// Compile-time check that sessionClient implements SessionClient.
var _ SessionClient = (*sessionClientDBus)(nil)

// sessionClientDBus implements SessionClient via D-Bus.
type sessionClientDBus struct {
	conn      *dbus.Conn
	obj       dbus.BusObject
	sessionID string
}

// ConnectSession connects to a running session's D-Bus service.
func ConnectSession(sessionID string) (SessionClient, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connecting to session bus: %w", err)
	}

	busName := fmt.Sprintf("%s.%s", DBusNamePrefix, sessionID)
	obj := conn.Object(busName, dbus.ObjectPath(DBusPath))

	return &sessionClientDBus{
		conn:      conn,
		obj:       obj,
		sessionID: sessionID,
	}, nil
}

func (c *sessionClientDBus) Close() error {
	return c.conn.Close()
}

func (c *sessionClientDBus) SessionID() (string, error) {
	return c.sessionID, nil
}

func (c *sessionClientDBus) SendInput(input string) (int, error) {
	var n int
	err := c.obj.Call(DBusNamePrefix+".SendInput", 0, input).Store(&n)
	if err != nil {
		return 0, fmt.Errorf("calling SendInput: %w", err)
	}
	return n, nil
}

func (c *sessionClientDBus) Kill() error {
	return c.obj.Call(DBusNamePrefix+".Kill", 0).Err
}

func (c *sessionClientDBus) Restart() error {
	return c.obj.Call(DBusNamePrefix+".Restart", 0).Err
}

func (c *sessionClientDBus) Gist() (HostStatus, error) {
	var status HostStatus
	err := c.obj.Call(DBusNamePrefix+".Gist", 0).Store(&status)
	if err != nil {
		return HostStatus{}, fmt.Errorf("calling Gist: %w", err)
	}
	return status, nil
}

// TTYClient extends SessionClient with terminal-specific methods.
// These are only available for sessions started with --tty.
type TTYClient interface {
	SessionClient

	// GetScreenText returns the current screen content as plain text.
	GetScreenText() (string, error)

	// GetScreenANSI returns the screen with ANSI escape codes for colors.
	GetScreenANSI() (string, error)

	// GetCursor returns the current cursor position (row, col).
	GetCursor() (int32, int32, error)

	// GetTitle returns the terminal title.
	GetTitle() (string, error)

	// GetMode returns whether alternate screen mode is active.
	GetMode() (bool, error)

	// Resize changes the terminal size.
	Resize(rows, cols int32) error

	// Attach connects to the TTY session for interactive use.
	// clientRows/clientCols specify the attaching client's terminal size.
	// Returns a dedicated byte stream, current size, screen snapshot, and client ID.
	// If other clients are attached and clientRows/clientCols are too small, returns an error.
	Attach(clientRows, clientCols int32) (*TTYAttachment, error)

	// Detach disconnects a specific client by ID.
	Detach(clientID string) error

	// GetAttachedClients returns info about currently attached clients.
	GetAttachedClients() (count int32, masterRows, masterCols int32, err error)

	// WaitExited waits for the session to exit and returns the exit code.
	// Returns a channel that receives the exit code when the session exits.
	WaitExited() <-chan int32
}

// ttyClientDBus implements TTYClient via D-Bus.
type ttyClientDBus struct {
	*sessionClientDBus
}

// connectTTYSessionViaDBusBackend connects to a running TTY session's D-Bus service.
func ConnectTTYSession(sessionID string) (TTYClient, error) {
	client, err := ConnectSession(sessionID)
	if err != nil {
		return nil, err
	}
	return &ttyClientDBus{sessionClientDBus: client.(*sessionClientDBus)}, nil
}

func (c *ttyClientDBus) GetScreenText() (string, error) {
	var text string
	err := c.obj.Call(DBusNamePrefix+".GetScreenText", 0).Store(&text)
	if err != nil {
		return "", fmt.Errorf("calling GetScreenText: %w", err)
	}
	return text, nil
}

func (c *ttyClientDBus) GetScreenANSI() (string, error) {
	var text string
	err := c.obj.Call(DBusNamePrefix+".GetScreenANSI", 0).Store(&text)
	if err != nil {
		return "", fmt.Errorf("calling GetScreenANSI: %w", err)
	}
	return text, nil
}

func (c *ttyClientDBus) GetCursor() (int32, int32, error) {
	var row, col int32
	err := c.obj.Call(DBusNamePrefix+".GetCursor", 0).Store(&row, &col)
	if err != nil {
		return 0, 0, fmt.Errorf("calling GetCursor: %w", err)
	}
	return row, col, nil
}

func (c *ttyClientDBus) GetTitle() (string, error) {
	var title string
	err := c.obj.Call(DBusNamePrefix+".GetTitle", 0).Store(&title)
	if err != nil {
		return "", fmt.Errorf("calling GetTitle: %w", err)
	}
	return title, nil
}

func (c *ttyClientDBus) GetMode() (bool, error) {
	var altScreen bool
	err := c.obj.Call(DBusNamePrefix+".GetMode", 0).Store(&altScreen)
	if err != nil {
		return false, fmt.Errorf("calling GetMode: %w", err)
	}
	return altScreen, nil
}

func (c *ttyClientDBus) Resize(rows, cols int32) error {
	return c.obj.Call(DBusNamePrefix+".Resize", 0, rows, cols).Err
}

func (c *ttyClientDBus) Attach(clientRows, clientCols int32) (*TTYAttachment, error) {
	var outputFD, inputFD dbus.UnixFD
	var rows, cols int32
	var screenANSI, clientID string

	if err := c.obj.Call(DBusNamePrefix+".Attach", 0, clientRows, clientCols).Store(&outputFD, &inputFD, &rows, &cols, &screenANSI, &clientID); err != nil {
		return nil, fmt.Errorf("calling Attach: %w", err)
	}

	output := os.NewFile(uintptr(outputFD), "tty-output")
	input := os.NewFile(uintptr(inputFD), "tty-input")
	if output == nil || input == nil {
		if output != nil {
			output.Close()
		}
		if input != nil {
			input.Close()
		}
		return nil, fmt.Errorf("invalid file descriptor(s) returned by Attach")
	}

	return &TTYAttachment{
		Conn:       &splitConn{r: output, w: input},
		Rows:       rows,
		Cols:       cols,
		ScreenANSI: screenANSI,
		ClientID:   clientID,
	}, nil
}

func (c *ttyClientDBus) Detach(clientID string) error {
	return c.obj.Call(DBusNamePrefix+".Detach", 0, clientID).Err
}

func (c *ttyClientDBus) GetAttachedClients() (count int32, masterRows, masterCols int32, err error) {
	err = c.obj.Call(DBusNamePrefix+".GetAttachedClients", 0).Store(&count, &masterRows, &masterCols)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("calling GetAttachedClients: %w", err)
	}
	return count, masterRows, masterCols, nil
}

func (c *ttyClientDBus) WaitExited() <-chan int32 {
	exitCh := make(chan int32, 1)

	// Add match rule to receive the Exited signal
	c.conn.AddMatchSignal(
		dbus.WithMatchInterface(DBusNamePrefix),
		dbus.WithMatchMember("Exited"),
		dbus.WithMatchObjectPath(dbus.ObjectPath(DBusPath)),
	)

	sigChan := make(chan *dbus.Signal, 1)
	c.conn.Signal(sigChan)

	go func() {
		defer c.conn.RemoveMatchSignal(
			dbus.WithMatchInterface(DBusNamePrefix),
			dbus.WithMatchMember("Exited"),
			dbus.WithMatchObjectPath(dbus.ObjectPath(DBusPath)),
		)
		defer c.conn.RemoveSignal(sigChan)
		for sig := range sigChan {
			// Check if this is the Exited signal from our session
			if sig.Path == dbus.ObjectPath(DBusPath) && sig.Name == DBusNamePrefix+".Exited" {
				if len(sig.Body) > 0 {
					if exitCode, ok := sig.Body[0].(int32); ok {
						exitCh <- exitCode
						return
					}
				}
				// Signal received but couldn't parse exit code
				exitCh <- -1
				return
			}
		}
	}()

	return exitCh
}

// MustJSON marshals v to JSON, panicking on error.
func MustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
