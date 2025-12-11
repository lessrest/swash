package control

import "github.com/mbrock/swash/internal/session"

// ControlPlane abstracts how clients connect to running sessions.
// The default implementation uses D-Bus, but tests/fakes can swap in-memory
// transports without exposing D-Bus directly to callers.
type ControlPlane interface {
	ConnectSession(sessionID string) (session.SessionClient, error)
	ConnectTTYSession(sessionID string) (session.TTYClient, error)
}
