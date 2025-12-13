package systemd

import (
	"context"
	"strings"
	"syscall"
)

// ProcessManager adapts the low-level Systemd interface to the semantic ProcessBackend.
type ProcessManager struct {
	systemd Systemd
}

var _ ProcessBackend = (*ProcessManager)(nil)

// NewProcessManager wraps a Systemd connection in a ProcessBackend.
func NewProcessManager(sd Systemd) *ProcessManager {
	return &ProcessManager{systemd: sd}
}

// Start launches a workload by translating a ProcessSpec into a transient unit.
func (b *ProcessManager) Start(ctx context.Context, spec ProcessSpec) error {
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
func (b *ProcessManager) Stop(ctx context.Context, ref ProcessRef) error {
	return b.systemd.StopUnit(ctx, unitNameForRef(ref))
}

// Kill sends a signal to a workload.
func (b *ProcessManager) Kill(ctx context.Context, ref ProcessRef, signal syscall.Signal) error {
	return b.systemd.KillUnit(ctx, unitNameForRef(ref), signal)
}

// ResetFailed resets a failed workload so it can be restarted.
func (b *ProcessManager) ResetFailed(ctx context.Context, ref ProcessRef) error {
	return b.systemd.ResetFailedUnit(ctx, unitNameForRef(ref))
}

// List lists workloads matching the filter.
func (b *ProcessManager) List(ctx context.Context, filter ProcessFilter) ([]ProcessStatus, error) {
	patterns := patternsForRoles(filter.Roles)
	states := statesForFilter(filter.States)

	units, err := b.systemd.ListUnits(ctx, patterns, states)
	if err != nil {
		return nil, err
	}

	// Only include units whose slice matches the current root slice prefix.
	// This prevents test sessions (in swashtest*.slice) from appearing in
	// normal listings (which expect swash-*.slice).
	slicePrefix := rootSlicePrefix() + "-"

	var result []ProcessStatus
	for _, u := range units {
		// Filter by slice - unit's slice should start with our root slice prefix
		if !strings.HasPrefix(u.Slice, slicePrefix) {
			continue
		}

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
func (b *ProcessManager) Describe(ctx context.Context, ref ProcessRef) (*ProcessStatus, error) {
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
func (b *ProcessManager) Close() error {
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
