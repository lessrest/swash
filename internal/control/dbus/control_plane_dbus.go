package dbus

import (
	"github.com/mbrock/swash/internal/control"
	"github.com/mbrock/swash/internal/session"
)

// DBusControlPlane implements ControlPlane using the existing D-Bus clients.
type DBusControlPlane struct{}

func NewDBusControlPlane() *DBusControlPlane { return &DBusControlPlane{} }

var _ control.ControlPlane = (*DBusControlPlane)(nil)

func (DBusControlPlane) ConnectSession(sessionID string) (session.SessionClient, error) {
	return session.ConnectSession(sessionID)
}

func (DBusControlPlane) ConnectTTYSession(sessionID string) (session.TTYClient, error) {
	return session.ConnectTTYSession(sessionID)
}
