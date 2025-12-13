package systemd

import (
	"context"
	"strings"
	"syscall"

	"github.com/mbrock/swash/internal/process"
)

// SystemdBackend adapts the low-level Systemd interface to the semantic ProcessBackend.
type SystemdBackend struct {
	systemd Systemd
}

var _ process.ProcessBackend = (*SystemdBackend)(nil)

// NewSystemdBackend wraps a Systemd connection in a ProcessBackend.
func NewSystemdBackend(sd Systemd) *SystemdBackend {
	return &SystemdBackend{systemd: sd}
}

// Start launches a workload by translating a ProcessSpec into a transient unit.
func (b *SystemdBackend) Start(ctx context.Context, spec process.ProcessSpec) error {
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
	case process.LaunchKindService:
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
func (b *SystemdBackend) Stop(ctx context.Context, ref process.ProcessRef) error {
	return b.systemd.StopUnit(ctx, unitNameForRef(ref))
}

// Kill sends a signal to a workload.
func (b *SystemdBackend) Kill(ctx context.Context, ref process.ProcessRef, signal syscall.Signal) error {
	return b.systemd.KillUnit(ctx, unitNameForRef(ref), signal)
}

// ResetFailed resets a failed workload so it can be restarted.
func (b *SystemdBackend) ResetFailed(ctx context.Context, ref process.ProcessRef) error {
	return b.systemd.ResetFailedUnit(ctx, unitNameForRef(ref))
}

// List lists workloads matching the filter.
func (b *SystemdBackend) List(ctx context.Context, filter process.ProcessFilter) ([]process.ProcessStatus, error) {
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

	var result []process.ProcessStatus
	for _, u := range units {
		// Filter by slice - unit's slice should start with our root slice prefix
		if !strings.HasPrefix(u.Slice, slicePrefix) {
			continue
		}

		ref, ok := refFromUnit(u.Name)
		if !ok {
			continue
		}
		result = append(result, process.ProcessStatus{
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
func (b *SystemdBackend) Describe(ctx context.Context, ref process.ProcessRef) (*process.ProcessStatus, error) {
	unit := unitNameForRef(ref)
	u, err := b.systemd.GetUnit(ctx, unit)
	if err != nil {
		return nil, err
	}

	return &process.ProcessStatus{
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

func unitNameForRef(ref process.ProcessRef) UnitName {
	switch ref.Role {
	case process.ProcessRoleHost:
		return HostUnit(ref.SessionID)
	default:
		return TaskUnit(ref.SessionID)
	}
}

func refFromUnit(name UnitName) (process.ProcessRef, bool) {
	switch name.Type() {
	case UnitTypeHost:
		return process.HostProcess(name.SessionID()), true
	case UnitTypeTask:
		return process.TaskProcess(name.SessionID()), true
	default:
		return process.ProcessRef{}, false
	}
}

func patternsForRoles(roles []process.ProcessRole) []UnitName {
	if len(roles) == 0 {
		return []UnitName{"swash-host-*.service", "swash-task-*.service"}
	}

	var patterns []UnitName
	for _, role := range roles {
		switch role {
		case process.ProcessRoleHost:
			patterns = append(patterns, "swash-host-*.service")
		case process.ProcessRoleTask:
			patterns = append(patterns, "swash-task-*.service")
		}
	}
	return patterns
}

func statesForFilter(states []process.ProcessState) []UnitState {
	if len(states) == 0 {
		return nil
	}

	var out []UnitState
	for _, st := range states {
		switch st {
		case process.ProcessStateRunning:
			out = append(out, UnitStateActive)
		case process.ProcessStateStarting:
			out = append(out, UnitStateActivating)
		case process.ProcessStateExited:
			out = append(out, UnitStateInactive)
		case process.ProcessStateFailed:
			out = append(out, UnitStateFailed)
		}
	}
	return out
}

func processStateFromUnit(state UnitState, exitStatus int32) process.ProcessState {
	switch state {
	case UnitStateActive:
		return process.ProcessStateRunning
	case UnitStateActivating:
		return process.ProcessStateStarting
	case UnitStateFailed:
		return process.ProcessStateFailed
	default:
		if exitStatus == 0 {
			return process.ProcessStateExited
		}
		return process.ProcessStateFailed
	}
}
