package systemd

import (
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"

	"swa.sh/go/swash/internal/host"
)

// dbusClient implements host.Client via D-Bus.
type dbusClient struct {
	conn      *dbus.Conn
	obj       dbus.BusObject
	sessionID string
}

var _ host.Client = (*dbusClient)(nil)

// Connect connects to a running session's D-Bus service.
func Connect(sessionID string) (host.Client, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connecting to session bus: %w", err)
	}

	busName := fmt.Sprintf("%s.%s", host.DBusNamePrefix, sessionID)
	obj := conn.Object(busName, dbus.ObjectPath(host.DBusPath))

	return &dbusClient{
		conn:      conn,
		obj:       obj,
		sessionID: sessionID,
	}, nil
}

func (c *dbusClient) Close() error {
	return c.conn.Close()
}

func (c *dbusClient) SessionID() (string, error) {
	return c.sessionID, nil
}

func (c *dbusClient) SendInput(input string) (int, error) {
	var n int
	err := c.obj.Call(host.DBusNamePrefix+".SendInput", 0, input).Store(&n)
	if err != nil {
		return 0, fmt.Errorf("calling SendInput: %w", err)
	}
	return n, nil
}

func (c *dbusClient) Kill() error {
	return c.obj.Call(host.DBusNamePrefix+".Kill", 0).Err
}

func (c *dbusClient) Restart() error {
	return c.obj.Call(host.DBusNamePrefix+".Restart", 0).Err
}

func (c *dbusClient) Gist() (host.HostStatus, error) {
	var status host.HostStatus
	err := c.obj.Call(host.DBusNamePrefix+".Gist", 0).Store(&status)
	if err != nil {
		return host.HostStatus{}, fmt.Errorf("calling Gist: %w", err)
	}
	return status, nil
}

// ttyClientDBus implements host.TTYClient via D-Bus.
type ttyClientDBus struct {
	*dbusClient
}

var _ host.TTYClient = (*ttyClientDBus)(nil)

// ConnectTTY connects to a running TTY session's D-Bus service.
func ConnectTTY(sessionID string) (host.TTYClient, error) {
	client, err := Connect(sessionID)
	if err != nil {
		return nil, err
	}
	return &ttyClientDBus{dbusClient: client.(*dbusClient)}, nil
}

func (c *ttyClientDBus) GetScreenText() (string, error) {
	var text string
	err := c.obj.Call(host.DBusNamePrefix+".GetScreenText", 0).Store(&text)
	if err != nil {
		return "", fmt.Errorf("calling GetScreenText: %w", err)
	}
	return text, nil
}

func (c *ttyClientDBus) GetScreenANSI() (string, error) {
	var text string
	err := c.obj.Call(host.DBusNamePrefix+".GetScreenANSI", 0).Store(&text)
	if err != nil {
		return "", fmt.Errorf("calling GetScreenANSI: %w", err)
	}
	return text, nil
}

func (c *ttyClientDBus) GetCursor() (int32, int32, error) {
	var row, col int32
	err := c.obj.Call(host.DBusNamePrefix+".GetCursor", 0).Store(&row, &col)
	if err != nil {
		return 0, 0, fmt.Errorf("calling GetCursor: %w", err)
	}
	return row, col, nil
}

func (c *ttyClientDBus) GetTitle() (string, error) {
	var title string
	err := c.obj.Call(host.DBusNamePrefix+".GetTitle", 0).Store(&title)
	if err != nil {
		return "", fmt.Errorf("calling GetTitle: %w", err)
	}
	return title, nil
}

func (c *ttyClientDBus) GetMode() (bool, error) {
	var altScreen bool
	err := c.obj.Call(host.DBusNamePrefix+".GetMode", 0).Store(&altScreen)
	if err != nil {
		return false, fmt.Errorf("calling GetMode: %w", err)
	}
	return altScreen, nil
}

func (c *ttyClientDBus) Resize(rows, cols int32) error {
	return c.obj.Call(host.DBusNamePrefix+".Resize", 0, rows, cols).Err
}

func (c *ttyClientDBus) Attach(clientRows, clientCols int32) (*host.TTYAttachment, error) {
	var outputFD, inputFD dbus.UnixFD
	var rows, cols int32
	var screenANSI, clientID string

	if err := c.obj.Call(host.DBusNamePrefix+".Attach", 0, clientRows, clientCols).Store(&outputFD, &inputFD, &rows, &cols, &screenANSI, &clientID); err != nil {
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

	return &host.TTYAttachment{
		Conn:       &host.SplitConn{R: output, W: input},
		Rows:       rows,
		Cols:       cols,
		ScreenANSI: screenANSI,
		ClientID:   clientID,
	}, nil
}

func (c *ttyClientDBus) Detach(clientID string) error {
	return c.obj.Call(host.DBusNamePrefix+".Detach", 0, clientID).Err
}

func (c *ttyClientDBus) GetAttachedClients() (count int32, masterRows, masterCols int32, err error) {
	err = c.obj.Call(host.DBusNamePrefix+".GetAttachedClients", 0).Store(&count, &masterRows, &masterCols)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("calling GetAttachedClients: %w", err)
	}
	return count, masterRows, masterCols, nil
}

func (c *ttyClientDBus) WaitExited() <-chan int32 {
	exitCh := make(chan int32, 1)

	c.conn.AddMatchSignal(
		dbus.WithMatchInterface(host.DBusNamePrefix),
		dbus.WithMatchMember("Exited"),
		dbus.WithMatchObjectPath(dbus.ObjectPath(host.DBusPath)),
	)

	sigChan := make(chan *dbus.Signal, 1)
	c.conn.Signal(sigChan)

	go func() {
		defer c.conn.RemoveMatchSignal(
			dbus.WithMatchInterface(host.DBusNamePrefix),
			dbus.WithMatchMember("Exited"),
			dbus.WithMatchObjectPath(dbus.ObjectPath(host.DBusPath)),
		)
		defer c.conn.RemoveSignal(sigChan)
		for sig := range sigChan {
			if sig.Path == dbus.ObjectPath(host.DBusPath) && sig.Name == host.DBusNamePrefix+".Exited" {
				if len(sig.Body) > 0 {
					if exitCode, ok := sig.Body[0].(int32); ok {
						exitCh <- exitCode
						return
					}
				}
				exitCh <- -1
				return
			}
		}
	}()

	return exitCh
}
