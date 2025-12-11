package systemd

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"
)

// ExitNotification contains exit information for a unit.
type ExitNotification struct {
	Unit          UnitName
	ExitCode      int
	ServiceResult string
}

// Systemd provides operations on systemd units via D-Bus.
type Systemd interface {
	// ListUnits returns units matching patterns in given states.
	ListUnits(ctx context.Context, patterns []UnitName, states []UnitState) ([]Unit, error)

	// GetUnit retrieves a single unit's properties.
	GetUnit(ctx context.Context, name UnitName) (*Unit, error)

	// StartUnit starts a persistent unit, blocking until complete.
	StartUnit(ctx context.Context, name UnitName) error

	// StopUnit gracefully stops a unit, blocking until complete.
	StopUnit(ctx context.Context, name UnitName) error

	// KillUnit sends a signal to all processes in a unit.
	KillUnit(ctx context.Context, name UnitName, signal syscall.Signal) error

	// StartTransient creates and starts a transient unit via D-Bus API.
	StartTransient(ctx context.Context, spec TransientSpec) error

	// Reload tells systemd to reload its configuration (daemon-reload).
	Reload(ctx context.Context) error

	// EnableUnits enables unit files so they start on boot or socket activation.
	EnableUnits(ctx context.Context, units []string) error

	// DisableUnits disables unit files.
	DisableUnits(ctx context.Context, units []string) error

	// SubscribeUnitExit returns a channel that receives when the specified unit exits.
	// The channel is closed when the subscription ends.
	SubscribeUnitExit(ctx context.Context, unit UnitName) (<-chan ExitNotification, error)

	// EmitUnitExit sends an exit notification for a unit.
	// This is called by the notify-exit command to signal that a task has exited.
	EmitUnitExit(ctx context.Context, unit UnitName, exitCode int, serviceResult string) error

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

	// Try to get service-specific properties (will fail for non-service units like sockets)
	serviceProps, err := s.conn.GetUnitTypePropertiesContext(ctx, name.String(), "Service")
	if err == nil {
		if pid, ok := serviceProps["MainPID"].(uint32); ok {
			unit.MainPID = pid
		}

		if wd, ok := serviceProps["WorkingDirectory"].(string); ok {
			unit.WorkingDir = wd
		}

		if es, ok := serviceProps["ExecMainStatus"].(int32); ok {
			unit.ExitStatus = es
		}
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

// StartUnit starts a persistent unit, blocking until complete.
func (s *systemdConn) StartUnit(ctx context.Context, name UnitName) error {
	resultChan := make(chan string, 1)
	_, err := s.conn.StartUnitContext(ctx, name.String(), "replace", resultChan)
	if err != nil {
		return fmt.Errorf("starting unit: %w", err)
	}

	select {
	case result := <-resultChan:
		if result != "done" {
			return fmt.Errorf("start job failed: %s", result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reload tells systemd to reload its configuration (daemon-reload).
func (s *systemdConn) Reload(ctx context.Context) error {
	return s.conn.ReloadContext(ctx)
}

// EnableUnits enables unit files so they start on boot or socket activation.
func (s *systemdConn) EnableUnits(ctx context.Context, units []string) error {
	_, _, err := s.conn.EnableUnitFilesContext(ctx, units, false, true)
	if err != nil {
		return fmt.Errorf("enabling units: %w", err)
	}
	return nil
}

// DisableUnits disables unit files.
func (s *systemdConn) DisableUnits(ctx context.Context, units []string) error {
	_, err := s.conn.DisableUnitFilesContext(ctx, units, false)
	if err != nil {
		return fmt.Errorf("disabling units: %w", err)
	}
	return nil
}

// KillUnit sends a signal to all processes in a unit.
func (s *systemdConn) KillUnit(ctx context.Context, name UnitName, signal syscall.Signal) error {
	s.conn.KillUnitWithTarget(ctx, name.String(), dbus.All, int32(signal))
	return nil
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

	// Unit dependencies
	if len(spec.BindsTo) > 0 {
		units := make([]string, len(spec.BindsTo))
		for i, u := range spec.BindsTo {
			units[i] = u.String()
		}
		props = append(props, dbus.Property{
			Name:  "BindsTo",
			Value: godbus.MakeVariant(units),
		})
	}

	if len(spec.After) > 0 {
		units := make([]string, len(spec.After))
		for i, u := range spec.After {
			units[i] = u.String()
		}
		props = append(props, dbus.Property{
			Name:  "After",
			Value: godbus.MakeVariant(units),
		})
	}

	// ExecStopPost - commands to run after main process exits
	// Format: a(sasb) - array of (path, argv, ignore-failure)
	if len(spec.ExecStopPost) > 0 {
		type execCmd struct {
			Path             string
			Args             []string
			UncleanIsFailure bool
		}
		cmds := make([]execCmd, len(spec.ExecStopPost))
		for i, cmd := range spec.ExecStopPost {
			cmds[i] = execCmd{
				Path:             cmd[0],
				Args:             cmd,
				UncleanIsFailure: false, // Don't fail the unit if ExecStopPost fails
			}
		}
		props = append(props, dbus.Property{
			Name:  "ExecStopPost",
			Value: godbus.MakeVariant(cmds),
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

// swashExitSignalInterface is the D-Bus interface for exit notifications.
const swashExitSignalInterface = "sh.swa.Swash.Exit"

// swashExitSignalPath is the D-Bus path for exit notifications.
const swashExitSignalPath = "/sh/swa/Swash/Exit"

// SubscribeUnitExit returns a channel that receives when the specified unit exits.
// It subscribes to D-Bus signals emitted by the notify-exit command.
func (s *systemdConn) SubscribeUnitExit(ctx context.Context, unit UnitName) (<-chan ExitNotification, error) {
	ch := make(chan ExitNotification, 1)

	// Connect to session bus for signal watching
	sigConn, err := godbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connecting to session bus: %w", err)
	}

	// Subscribe to exit signals using the proper AddMatchSignal API
	if err := sigConn.AddMatchSignal(
		godbus.WithMatchInterface(swashExitSignalInterface),
		godbus.WithMatchMember("UnitExited"),
		godbus.WithMatchObjectPath(godbus.ObjectPath(swashExitSignalPath)),
	); err != nil {
		sigConn.Close()
		return nil, fmt.Errorf("adding match signal: %w", err)
	}

	sigChan := make(chan *godbus.Signal, 10)
	sigConn.Signal(sigChan)

	// Watch for signals in background
	go func() {
		defer sigConn.Close()
		defer sigConn.RemoveSignal(sigChan)
		defer sigConn.RemoveMatchSignal(
			godbus.WithMatchInterface(swashExitSignalInterface),
			godbus.WithMatchMember("UnitExited"),
			godbus.WithMatchObjectPath(godbus.ObjectPath(swashExitSignalPath)),
		)

		for {
			select {
			case sig := <-sigChan:
				if sig == nil {
					return
				}
				if sig.Name != swashExitSignalInterface+".UnitExited" {
					continue
				}
				// Signal body: (unitName string, exitCode int32, serviceResult string)
				if len(sig.Body) < 3 {
					continue
				}
				sigUnit, ok1 := sig.Body[0].(string)
				exitCode, ok2 := sig.Body[1].(int32)
				serviceResult, ok3 := sig.Body[2].(string)
				if !ok1 || !ok2 || !ok3 {
					continue
				}
				// Check if this is for our unit
				if UnitName(sigUnit) == unit {
					select {
					case ch <- ExitNotification{
						Unit:          unit,
						ExitCode:      int(exitCode),
						ServiceResult: serviceResult,
					}:
					default:
					}
					return // Got our notification, done
				}
			case <-ctx.Done():
				close(ch)
				return
			}
		}
	}()

	return ch, nil
}

// EmitUnitExit sends an exit notification for a unit via D-Bus signal.
func (s *systemdConn) EmitUnitExit(ctx context.Context, unit UnitName, exitCode int, serviceResult string) error {
	// Connect to session bus to emit signal
	conn, err := godbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to session bus: %w", err)
	}
	defer conn.Close()

	// Emit the signal
	return conn.Emit(
		godbus.ObjectPath(swashExitSignalPath),
		swashExitSignalInterface+".UnitExited",
		unit.String(),
		int32(exitCode),
		serviceResult,
	)
}
