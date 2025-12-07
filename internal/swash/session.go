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
}

// SessionClient provides D-Bus operations on a running session's SwashService.
type SessionClient interface {
	// SendInput sends input to the process stdin.
	SendInput(input string) error

	// Kill sends SIGKILL to the process.
	Kill() error

	// Gist returns session status.
	Gist() (map[string]any, error)

	// SessionID returns the session ID.
	SessionID() string

	// Close releases the D-Bus connection.
	Close() error
}

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

func (c *sessionClient) SessionID() string {
	return c.sessionID
}

func (c *sessionClient) SendInput(input string) error {
	var result string
	err := c.obj.Call(DBusNamePrefix+".SendInput", 0, input).Store(&result)
	if err != nil {
		return fmt.Errorf("calling SendInput: %w", err)
	}

	// Parse result JSON to check for errors
	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}
	if errMsg, ok := resp["error"].(string); ok {
		return fmt.Errorf("%s", errMsg)
	}
	return nil
}

func (c *sessionClient) Kill() error {
	var result string
	err := c.obj.Call(DBusNamePrefix+".Kill", 0).Store(&result)
	if err != nil {
		return fmt.Errorf("calling Kill: %w", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}
	if errMsg, ok := resp["error"].(string); ok {
		return fmt.Errorf("%s", errMsg)
	}
	return nil
}

func (c *sessionClient) Gist() (map[string]any, error) {
	var result string
	err := c.obj.Call(DBusNamePrefix+".Gist", 0).Store(&result)
	if err != nil {
		return nil, fmt.Errorf("calling Gist: %w", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return resp, nil
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
func SendInput(sessionID string, input string) error {
	client, err := ConnectSession(sessionID)
	if err != nil {
		return err
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
