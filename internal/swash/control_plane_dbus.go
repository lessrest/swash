package swash

// DBusControlPlane implements ControlPlane using the existing D-Bus clients.
type DBusControlPlane struct{}

func NewDBusControlPlane() *DBusControlPlane { return &DBusControlPlane{} }

func (DBusControlPlane) ConnectSession(sessionID string) (SessionClient, error) {
	return connectSessionViaDBusBackend(sessionID)
}

func (DBusControlPlane) ConnectTTYSession(sessionID string) (TTYClient, error) {
	return connectTTYSessionViaDBusBackend(sessionID)
}
