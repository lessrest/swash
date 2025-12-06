package swash

import (
	"fmt"
	"strings"
	"time"
)

// UnitType distinguishes swash unit kinds.
type UnitType int

const (
	UnitTypeHost UnitType = iota // swash-host-*.service (D-Bus server)
	UnitTypeTask                 // swash-task-*.service (actual command)
)

// UnitName is a typed systemd unit name with semantic methods.
type UnitName string

// HostUnit returns the unit name for a session's D-Bus host service.
func HostUnit(sessionID string) UnitName {
	return UnitName(fmt.Sprintf("swash-host-%s.service", sessionID))
}

// TaskUnit returns the unit name for a session's task service.
func TaskUnit(sessionID string) UnitName {
	return UnitName(fmt.Sprintf("swash-task-%s.service", sessionID))
}

// SessionID extracts the session ID from a unit name.
// e.g., "swash-host-ABC123.service" -> "ABC123"
func (u UnitName) SessionID() string {
	s := string(u)
	s = strings.TrimSuffix(s, ".service")
	s = strings.TrimPrefix(s, "swash-host-")
	s = strings.TrimPrefix(s, "swash-task-")
	return s
}

// Type returns whether this is a host or task unit.
func (u UnitName) Type() UnitType {
	if strings.HasPrefix(string(u), "swash-task-") {
		return UnitTypeTask
	}
	return UnitTypeHost
}

// String returns the unit name as a string.
func (u UnitName) String() string {
	return string(u)
}

// SliceName is a typed systemd slice name.
type SliceName string

// SessionSlice returns the slice name for a session.
func SessionSlice(sessionID string) SliceName {
	return SliceName(fmt.Sprintf("swash-%s.slice", sessionID))
}

// SessionID extracts the session ID from a slice name.
func (s SliceName) SessionID() string {
	str := string(s)
	str = strings.TrimPrefix(str, "swash-")
	str = strings.TrimSuffix(str, ".slice")
	return str
}

// String returns the slice name as a string.
func (s SliceName) String() string {
	return string(s)
}

// UnitState represents the systemd active state.
type UnitState string

const (
	UnitStateActive     UnitState = "active"
	UnitStateActivating UnitState = "activating"
	UnitStateInactive   UnitState = "inactive"
	UnitStateFailed     UnitState = "failed"
)

// Unit represents a live systemd unit with its properties.
type Unit struct {
	Name        UnitName
	State       UnitState
	Description string
	Started     time.Time
	MainPID     uint32
	WorkingDir  string
	ExitStatus  int32
}

// TransientSpec defines properties for starting a transient unit.
type TransientSpec struct {
	Unit        UnitName
	Slice       SliceName
	ServiceType string            // "dbus", "simple", etc.
	BusName     string            // for dbus services
	WorkingDir  string
	Description string
	Environment map[string]string
	Command     []string
	Collect     bool // --collect: unload unit after it exits

	// Stdio file descriptors - when set, passed directly to the unit
	Stdin  *int // nil = default, set = pass this fd
	Stdout *int // nil = journal, set = pass this fd
	Stderr *int // nil = journal, set = pass this fd
}
