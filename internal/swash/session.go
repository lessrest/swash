package swash

import (
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/godbus/dbus/v5"
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
	for i := 0; i < 3; i++ {
		id[i] = letters[int(b[i])%len(letters)]
	}
	for i := 3; i < 6; i++ {
		id[i] = digits[int(b[i])%len(digits)]
	}
	return string(id)
}

// SessionOptions configures a new session.
type SessionOptions struct {
	Protocol Protocol          // shell (default), sse
	Tags     map[string]string // Extra journal fields
	TTY      bool              // Use PTY mode with terminal emulation
	Rows     int               // Terminal rows (for TTY mode)
	Cols     int               // Terminal columns (for TTY mode)
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

	// Gist returns session status.
	Gist() (HostStatus, error)

	// SessionID returns the session ID.
	SessionID() (string, error)
}

// Compile-time check that Host implements SessionController.
var _ SessionController = (*Host)(nil)

// SessionClient extends SessionController with connection management.
type SessionClient interface {
	SessionController

	// Close releases the D-Bus connection.
	Close() error
}

// Compile-time check that sessionClient implements SessionClient.
var _ SessionClient = (*sessionClient)(nil)

// sessionClient implements SessionClient via D-Bus.
type sessionClient struct {
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

	return &sessionClient{
		conn:      conn,
		obj:       obj,
		sessionID: sessionID,
	}, nil
}

func (c *sessionClient) Close() error {
	return c.conn.Close()
}

func (c *sessionClient) SessionID() (string, error) {
	return c.sessionID, nil
}

func (c *sessionClient) SendInput(input string) (int, error) {
	var n int
	err := c.obj.Call(DBusNamePrefix+".SendInput", 0, input).Store(&n)
	if err != nil {
		return 0, fmt.Errorf("calling SendInput: %w", err)
	}
	return n, nil
}

func (c *sessionClient) Kill() error {
	return c.obj.Call(DBusNamePrefix+".Kill", 0).Err
}

func (c *sessionClient) Gist() (HostStatus, error) {
	var status HostStatus
	err := c.obj.Call(DBusNamePrefix+".Gist", 0).Store(&status)
	if err != nil {
		return HostStatus{}, fmt.Errorf("calling Gist: %w", err)
	}
	return status, nil
}

// Convenience functions that open a connection, do the operation, and close.

// KillSession sends SIGKILL to the process in a session via D-Bus.
func KillSession(sessionID string) error {
	client, err := ConnectSession(sessionID)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Kill()
}

// SendInput sends input to the process via the swash D-Bus service.
func SendInput(sessionID string, input string) (int, error) {
	client, err := ConnectSession(sessionID)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	return client.SendInput(input)
}

// MustJSON marshals v to JSON, panicking on error.
func MustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
