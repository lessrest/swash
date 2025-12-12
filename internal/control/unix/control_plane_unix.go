package unix

import (
	"path/filepath"

	"github.com/mbrock/swash/internal/control"
	"github.com/mbrock/swash/internal/session"
)

// UnixControlPlane implements control.ControlPlane over a per-session unix socket.
//
// Socket path convention:
//
//	<RuntimeDir>/sessions/<sessionID>.sock
type UnixControlPlane struct {
	RuntimeDir string
}

var _ control.ControlPlane = (*UnixControlPlane)(nil)

func NewUnixControlPlane(runtimeDir string) *UnixControlPlane {
	return &UnixControlPlane{RuntimeDir: runtimeDir}
}

func (p *UnixControlPlane) socketPath(sessionID string) string {
	return filepath.Join(p.RuntimeDir, "sessions", sessionID+".sock")
}

func (p *UnixControlPlane) ConnectSession(sessionID string) (session.SessionClient, error) {
	return session.ConnectUnixSession(sessionID, p.socketPath(sessionID))
}

func (p *UnixControlPlane) ConnectTTYSession(sessionID string) (session.TTYClient, error) {
	return session.ConnectUnixTTYSession(sessionID, p.socketPath(sessionID))
}
