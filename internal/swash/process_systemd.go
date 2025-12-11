package swash

import (
	"context"
	"syscall"
)

// SystemdBackend adapts the low-level Systemd interface to the semantic ProcessBackend.
type SystemdBackend struct {
	systemd Systemd
}

// NewSystemdBackend wraps a Systemd connection in a ProcessBackend.
func NewSystemdBackend(sd Systemd) *SystemdBackend {
	return &SystemdBackend{systemd: sd}
}

// Start launches a workload by translating a ProcessSpec into a transient unit.
func (b *SystemdBackend) Start(ctx context.Context, spec ProcessSpec) error {
	unit := unitNameForRef(spec.Ref)
	slice := SessionSlice(spec.Ref.SessionID)

	tSpec := TransientSpec{
		Unit:         unit,
		Slice:        slice,
		WorkingDir:   spec.WorkingDir,
		Description:  spec.Description,
		Environment:  spec.Environment,
		Command:      spec.Command,
		Collect:      spec.Collect,
		TTYPath:      spec.TTYPath,
		ExecStopPost: spec.PostStop,
	}

	if spec.IO.Stdin != nil {
		tSpec.Stdin = spec.IO.Stdin
	}
	if spec.IO.Stdout != nil {
		tSpec.Stdout = spec.IO.Stdout
	}
	if spec.IO.Stderr != nil {
		tSpec.Stderr = spec.IO.Stderr
	}

	switch spec.LaunchKind {
	case LaunchKindService:
		tSpec.ServiceType = "dbus"
		tSpec.BusName = spec.BusName
	default:
		tSpec.ServiceType = "exec"
	}

	if len(spec.Dependencies) > 0 {
		deps := make([]UnitName, 0, len(spec.Dependencies))
		for _, ref := range spec.Dependencies {
			deps = append(deps, unitNameForRef(ref))
		}
		tSpec.BindsTo = deps
		tSpec.After = deps
	}

	return b.systemd.StartTransient(ctx, tSpec)
}

// Stop stops a workload.
func (b *SystemdBackend) Stop(ctx context.Context, ref ProcessRef) error {
	return b.systemd.StopUnit(ctx, unitNameForRef(ref))
}

// Kill sends a signal to a workload.
func (b *SystemdBackend) Kill(ctx context.Context, ref ProcessRef, signal syscall.Signal) error {
	return b.systemd.KillUnit(ctx, unitNameForRef(ref), signal)
}

// SubscribeExit subscribes to exit notifications for a workload.
func (b *SystemdBackend) SubscribeExit(ctx context.Context, ref ProcessRef) (<-chan ProcessExit, error) {
	unit := unitNameForRef(ref)
	ch, err := b.systemd.SubscribeUnitExit(ctx, unit)
	if err != nil {
		return nil, err
	}

	out := make(chan ProcessExit, 1)
	go func() {
		defer close(out)
		for n := range ch {
			out <- ProcessExit{
				Ref:      ref,
				ExitCode: n.ExitCode,
				Result:   n.ServiceResult,
			}
		}
	}()
	return out, nil
}

// EmitExit emits an exit notification (used by notify-exit).
func (b *SystemdBackend) EmitExit(ctx context.Context, ref ProcessRef, exitCode int, result string) error {
	return b.systemd.EmitUnitExit(ctx, unitNameForRef(ref), exitCode, result)
}

// List lists workloads matching the filter.
func (b *SystemdBackend) List(ctx context.Context, filter ProcessFilter) ([]ProcessStatus, error) {
	patterns := patternsForRoles(filter.Roles)
	states := statesForFilter(filter.States)

	units, err := b.systemd.ListUnits(ctx, patterns, states)
	if err != nil {
		return nil, err
	}

	var result []ProcessStatus
	for _, u := range units {
		ref, ok := refFromUnit(u.Name)
		if !ok {
			continue
		}
		result = append(result, ProcessStatus{
			Ref:         ref,
			State:       processStateFromUnit(u.State, u.ExitStatus),
			Description: u.Description,
			Started:     u.Started,
			PID:         u.MainPID,
			WorkingDir:  u.WorkingDir,
			ExitStatus:  u.ExitStatus,
		})
	}

	return result, nil
}

// Describe returns a single workload status.
func (b *SystemdBackend) Describe(ctx context.Context, ref ProcessRef) (*ProcessStatus, error) {
	unit := unitNameForRef(ref)
	u, err := b.systemd.GetUnit(ctx, unit)
	if err != nil {
		return nil, err
	}

	return &ProcessStatus{
		Ref:         ref,
		State:       processStateFromUnit(u.State, u.ExitStatus),
		Description: u.Description,
		Started:     u.Started,
		PID:         u.MainPID,
		WorkingDir:  u.WorkingDir,
		ExitStatus:  u.ExitStatus,
	}, nil
}

// Close releases the underlying Systemd connection.
func (b *SystemdBackend) Close() error {
	if b.systemd == nil {
		return nil
	}
	return b.systemd.Close()
}

func unitNameForRef(ref ProcessRef) UnitName {
	switch ref.Role {
	case ProcessRoleHost:
		return HostUnit(ref.SessionID)
	default:
		return TaskUnit(ref.SessionID)
	}
}

func refFromUnit(name UnitName) (ProcessRef, bool) {
	switch name.Type() {
	case UnitTypeHost:
		return HostProcess(name.SessionID()), true
	case UnitTypeTask:
		return TaskProcess(name.SessionID()), true
	default:
		return ProcessRef{}, false
	}
}

func patternsForRoles(roles []ProcessRole) []UnitName {
	if len(roles) == 0 {
		return []UnitName{"swash-host-*.service", "swash-task-*.service"}
	}

	var patterns []UnitName
	for _, role := range roles {
		switch role {
		case ProcessRoleHost:
			patterns = append(patterns, "swash-host-*.service")
		case ProcessRoleTask:
			patterns = append(patterns, "swash-task-*.service")
		}
	}
	return patterns
}

func statesForFilter(states []ProcessState) []UnitState {
	if len(states) == 0 {
		return nil
	}

	var out []UnitState
	for _, st := range states {
		switch st {
		case ProcessStateRunning:
			out = append(out, UnitStateActive)
		case ProcessStateStarting:
			out = append(out, UnitStateActivating)
		case ProcessStateExited:
			out = append(out, UnitStateInactive)
		case ProcessStateFailed:
			out = append(out, UnitStateFailed)
		}
	}
	return out
}

func processStateFromUnit(state UnitState, exitStatus int32) ProcessState {
	switch state {
	case UnitStateActive:
		return ProcessStateRunning
	case UnitStateActivating:
		return ProcessStateStarting
	case UnitStateFailed:
		return ProcessStateFailed
	default:
		if exitStatus == 0 {
			return ProcessStateExited
		}
		return ProcessStateFailed
	}
}
