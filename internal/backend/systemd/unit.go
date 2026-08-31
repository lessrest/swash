package systemd

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// rootSlicePrefix returns the slice prefix from env or default "swash"
func rootSlicePrefix() string {
	if prefix := os.Getenv("SWASH_ROOT_SLICE"); prefix != "" {
		return prefix
	}
	return "swash"
}

// UnitName is a typed systemd unit name with semantic methods.
type UnitName string

// HostUnit returns the unit name for a session's D-Bus host service.
func HostUnit(sessionID string) UnitName {
	return UnitName(fmt.Sprintf("swash-host-%s.service", sessionID))
}

// SessionID extracts the session ID from a unit name.
// e.g., "swash-host-ABC123.service" -> "ABC123"
func (u UnitName) SessionID() string {
	s := string(u)
	s = strings.TrimSuffix(s, ".service")
	s = strings.TrimPrefix(s, "swash-host-")
	return s
}

// String returns the unit name as a string.
func (u UnitName) String() string {
	return string(u)
}

// SliceName is a typed systemd slice name.
type SliceName string

// RootSlice returns the shared slice containing all swash sessions.
func RootSlice() SliceName {
	return SliceName(rootSlicePrefix() + ".slice")
}

// String returns the slice name as a string.
func (s SliceName) String() string {
	return string(s)
}

// UnitState represents the systemd active state.
type UnitState string

const (
	UnitStateActive       UnitState = "active"
	UnitStateActivating   UnitState = "activating"
	UnitStateDeactivating UnitState = "deactivating"
	UnitStateInactive     UnitState = "inactive"
	UnitStateFailed       UnitState = "failed"
)

// Unit represents a live systemd unit with its properties.
type Unit struct {
	Name        UnitName
	State       UnitState
	Description string
	Started     time.Time
	MainPID     uint32
	WorkingDir  string
	Slice       string
}

// TransientSpec defines properties for starting a transient unit.
type TransientSpec struct {
	Unit        UnitName
	Slice       SliceName
	ServiceType string // "dbus", "simple", etc.
	BusName     string // for dbus services
	WorkingDir  string
	Description string
	Environment map[string]string
	Command     []string
	Collect     bool // --collect: unload unit after it exits
	KillMode    string
	TimeoutStop time.Duration
}
