package swash

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"
)

// Systemd provides operations on systemd units via D-Bus.
type Systemd interface {
	// ListUnits returns units matching patterns in given states.
	ListUnits(ctx context.Context, patterns []UnitName, states []UnitState) ([]Unit, error)

	// GetUnit retrieves a single unit's properties.
	GetUnit(ctx context.Context, name UnitName) (*Unit, error)

	// StopUnit gracefully stops a unit, blocking until complete.
	StopUnit(ctx context.Context, name UnitName) error

	// KillUnit sends a signal to all processes in a unit.
	KillUnit(ctx context.Context, name UnitName, signal syscall.Signal) error

	// StartTransient creates and starts a transient unit via D-Bus API.
	StartTransient(ctx context.Context, spec TransientSpec) error

	// WaitUnitExit waits for a unit to exit and returns its exit code.
	WaitUnitExit(ctx context.Context, name UnitName) (int, error)

	// Close releases the D-Bus connection.
	Close() error
}

// systemdConn implements Systemd using go-systemd/dbus.
type systemdConn struct {
	conn *dbus.Conn
}

// ConnectUserSystemd connects to the user's systemd instance.
func ConnectUserSystemd(ctx context.Context) (Systemd, error) {
	conn, err := dbus.NewUserConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to user systemd: %w", err)
	}
	return &systemdConn{conn: conn}, nil
}

// Close releases the D-Bus connection.
func (s *systemdConn) Close() error {
	s.conn.Close()
	return nil
}

// ListUnits returns units matching patterns in given states.
func (s *systemdConn) ListUnits(
	ctx context.Context,
	patterns []UnitName,
	states []UnitState,
) ([]Unit, error) {
	// Convert typed slices to string slices
	patternStrs := make([]string, len(patterns))
	for i, p := range patterns {
		patternStrs[i] = p.String()
	}
	stateStrs := make([]string, len(states))
	for i, st := range states {
		stateStrs[i] = string(st)
	}

	units, err := s.conn.ListUnitsByPatternsContext(ctx, stateStrs, patternStrs)
	if err != nil {
		return nil, fmt.Errorf("listing units: %w", err)
	}

	result := make([]Unit, 0, len(units))
	for _, u := range units {
		unit, err := s.GetUnit(ctx, UnitName(u.Name))
		if err != nil {
			continue // Skip units we can't query
		}
		result = append(result, *unit)
	}
	return result, nil
}

// GetUnit retrieves a single unit's properties.
func (s *systemdConn) GetUnit(ctx context.Context, name UnitName) (*Unit, error) {
	unitProps, err := s.conn.GetUnitPropertiesContext(ctx, name.String())
	if err != nil {
		return nil, fmt.Errorf("getting unit properties: %w", err)
	}

	serviceProps, err := s.conn.GetUnitTypePropertiesContext(ctx, name.String(), "Service")
	if err != nil {
		return nil, fmt.Errorf("getting service properties: %w", err)
	}

	unit := &Unit{
		Name:  name,
		State: UnitState(unitProps["ActiveState"].(string)),
	}

	if desc, ok := unitProps["Description"].(string); ok {
		unit.Description = desc
	}

	if ts, ok := unitProps["ActiveEnterTimestamp"].(uint64); ok && ts > 0 {
		unit.Started = time.Unix(int64(ts/1000000), int64((ts%1000000)*1000))
	}

	if pid, ok := serviceProps["MainPID"].(uint32); ok {
		unit.MainPID = pid
	}

	if wd, ok := serviceProps["WorkingDirectory"].(string); ok {
		unit.WorkingDir = wd
	}

	if es, ok := serviceProps["ExecMainStatus"].(int32); ok {
		unit.ExitStatus = es
	}

	return unit, nil
}

// StopUnit gracefully stops a unit, blocking until complete.
func (s *systemdConn) StopUnit(ctx context.Context, name UnitName) error {
	resultChan := make(chan string, 1)
	_, err := s.conn.StopUnitContext(ctx, name.String(), "replace", resultChan)
	if err != nil {
		return fmt.Errorf("stopping unit: %w", err)
	}

	select {
	case result := <-resultChan:
		if result != "done" {
			return fmt.Errorf("stop job failed: %s", result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// KillUnit sends a signal to all processes in a unit.
func (s *systemdConn) KillUnit(ctx context.Context, name UnitName, signal syscall.Signal) error {
	s.conn.KillUnitWithTarget(ctx, name.String(), dbus.All, int32(signal))
	return nil
}

// WaitUnitExit waits for a unit to exit and returns its exit code.
// Uses D-Bus PropertiesChanged signals to detect when ActiveState becomes inactive/failed.
// Calls Ref/Unref on the unit to prevent systemd from garbage collecting it before we can read the exit status.
func (s *systemdConn) WaitUnitExit(ctx context.Context, name UnitName) (int, error) {
	// Connect to session bus for signal watching
	sigConn, err := godbus.ConnectSessionBus()
	if err != nil {
		return 0, fmt.Errorf("connecting to session bus: %w", err)
	}
	defer sigConn.Close()

	unitPath := godbus.ObjectPath("/org/freedesktop/systemd1/unit/" + escapeUnitName(name.String()))
	unitObj := sigConn.Object("org.freedesktop.systemd1", unitPath)

	// Ref the unit to prevent garbage collection
	if err := unitObj.Call("org.freedesktop.systemd1.Unit.Ref", 0).Err; err != nil {
		return 0, fmt.Errorf("ref unit: %w", err)
	}
	defer unitObj.Call("org.freedesktop.systemd1.Unit.Unref", 0)

	// Subscribe to PropertiesChanged signals for this unit
	matchRule := fmt.Sprintf(
		"type='signal',interface='org.freedesktop.DBus.Properties',member='PropertiesChanged',path='%s'",
		unitPath,
	)
	if err := sigConn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, matchRule).Err; err != nil {
		return 0, fmt.Errorf("adding match rule: %w", err)
	}
	defer sigConn.BusObject().Call("org.freedesktop.DBus.RemoveMatch", 0, matchRule)

	// Create signal channel
	sigChan := make(chan *godbus.Signal, 10)
	sigConn.Signal(sigChan)
	defer sigConn.RemoveSignal(sigChan)

	// Check if already exited by querying unit properties
	unit, err := s.GetUnit(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("getting unit: %w", err)
	}
	if unit.State == UnitStateInactive || unit.State == UnitStateFailed {
		return int(unit.ExitStatus), nil
	}

	// Watch for state changes
	for {
		select {
		case sig := <-sigChan:
			if sig.Path != unitPath {
				continue
			}
			if sig.Name != "org.freedesktop.DBus.Properties.PropertiesChanged" {
				continue
			}
			// Signal body: (interface_name, changed_properties, invalidated_properties)
			if len(sig.Body) < 2 {
				continue
			}
			changedProps, ok := sig.Body[1].(map[string]godbus.Variant)
			if !ok {
				continue
			}
			if stateVar, ok := changedProps["ActiveState"]; ok {
				if state, ok := stateVar.Value().(string); ok {
					if state == "inactive" || state == "failed" {
						return s.getExitStatus(ctx, name)
					}
				}
			}
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// escapeUnitName escapes a unit name for use in D-Bus object paths.
// Systemd escapes non-alphanumeric characters as _XX where XX is the hex code.
func escapeUnitName(name string) string {
	var result []byte
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			result = append(result, c)
		} else {
			result = append(result, '_')
			result = append(result, "0123456789abcdef"[c>>4])
			result = append(result, "0123456789abcdef"[c&0xf])
		}
	}
	return string(result)
}

// getExitStatus queries ExecMainStatus from the unit's service properties.
func (s *systemdConn) getExitStatus(ctx context.Context, name UnitName) (int, error) {
	props, err := s.conn.GetUnitTypePropertiesContext(ctx, name.String(), "Service")
	if err != nil {
		return 0, fmt.Errorf("getting service properties: %w", err)
	}
	if exitStatus, ok := props["ExecMainStatus"].(int32); ok {
		return int(exitStatus), nil
	}
	return 0, fmt.Errorf("ExecMainStatus not found")
}

// StartTransient creates and starts a transient unit via D-Bus API.
func (s *systemdConn) StartTransient(ctx context.Context, spec TransientSpec) error {
	props := []dbus.Property{
		dbus.PropExecStart(spec.Command, false),
		dbus.PropDescription(spec.Description),
	}

	if spec.Slice != "" {
		props = append(props, dbus.PropSlice(spec.Slice.String()))
	}

	if spec.ServiceType != "" {
		props = append(props, dbus.PropType(spec.ServiceType))
	}

	if spec.WorkingDir != "" {
		props = append(props, dbus.Property{
			Name:  "WorkingDirectory",
			Value: godbus.MakeVariant(spec.WorkingDir),
		})
	}

	if spec.BusName != "" {
		props = append(props, dbus.Property{
			Name:  "BusName",
			Value: godbus.MakeVariant(spec.BusName),
		})
	}

	if len(spec.Environment) > 0 {
		envList := make([]string, 0, len(spec.Environment))
		for k, v := range spec.Environment {
			envList = append(envList, k+"="+v)
		}
		props = append(props, dbus.Property{
			Name:  "Environment",
			Value: godbus.MakeVariant(envList),
		})
	}

	// Handle stdio - either pass file descriptors or default to journal
	if spec.Stdin != nil {
		props = append(props, dbus.Property{
			Name:  "StandardInputFileDescriptor",
			Value: godbus.MakeVariant(godbus.UnixFD(*spec.Stdin)),
		})
	}

	if spec.Stdout != nil {
		props = append(props, dbus.Property{
			Name:  "StandardOutputFileDescriptor",
			Value: godbus.MakeVariant(godbus.UnixFD(*spec.Stdout)),
		})
	} else {
		props = append(props, dbus.Property{
			Name:  "StandardOutput",
			Value: godbus.MakeVariant("journal"),
		})
	}

	if spec.Stderr != nil {
		props = append(props, dbus.Property{
			Name:  "StandardErrorFileDescriptor",
			Value: godbus.MakeVariant(godbus.UnixFD(*spec.Stderr)),
		})
	} else {
		props = append(props, dbus.Property{
			Name:  "StandardError",
			Value: godbus.MakeVariant("journal"),
		})
	}

	if spec.Collect {
		props = append(props, dbus.Property{
			Name:  "CollectMode",
			Value: godbus.MakeVariant("inactive-or-failed"),
		})
	}

	// TTY support - set TTYPath when using a PTY
	if spec.TTYPath != "" {
		props = append(props, dbus.Property{
			Name:  "TTYPath",
			Value: godbus.MakeVariant(spec.TTYPath),
		})
	}

	resultChan := make(chan string, 1)
	_, err := s.conn.StartTransientUnitContext(
		ctx,
		spec.Unit.String(),
		"replace",
		props,
		resultChan,
	)
	if err != nil {
		return fmt.Errorf("starting transient unit: %w", err)
	}

	select {
	case result := <-resultChan:
		// "done" means started successfully
		// "failed" can mean either:
		//   1. Actually failed to start (binary not found, etc.)
		//   2. Started and exited quickly with non-zero exit code
		// We treat both as success here - the caller should use WaitUnitExit
		// to determine if the process ran and get its exit code.
		if result != "done" && result != "failed" {
			return fmt.Errorf("start job failed: %s", result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
