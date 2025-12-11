package fake

import (
	systemd "github.com/mbrock/swash/internal/platform/systemd/process"
	"github.com/mbrock/swash/internal/process"
)

func unitNameForRef(ref process.ProcessRef) systemd.UnitName {
	switch ref.Role {
	case process.ProcessRoleHost:
		return systemd.HostUnit(ref.SessionID)
	default:
		return systemd.TaskUnit(ref.SessionID)
	}
}

func refFromUnit(name systemd.UnitName) (process.ProcessRef, bool) {
	switch name.Type() {
	case systemd.UnitTypeHost:
		return process.HostProcess(name.SessionID()), true
	case systemd.UnitTypeTask:
		return process.TaskProcess(name.SessionID()), true
	default:
		return process.ProcessRef{}, false
	}
}

func patternsForRoles(roles []process.ProcessRole) []systemd.UnitName {
	if len(roles) == 0 {
		return []systemd.UnitName{"swash-host-*.service", "swash-task-*.service"}
	}

	var patterns []systemd.UnitName
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

func statesForFilter(states []process.ProcessState) []systemd.UnitState {
	if len(states) == 0 {
		return nil
	}

	var out []systemd.UnitState
	for _, st := range states {
		switch st {
		case process.ProcessStateRunning:
			out = append(out, systemd.UnitStateActive)
		case process.ProcessStateStarting:
			out = append(out, systemd.UnitStateActivating)
		case process.ProcessStateExited:
			out = append(out, systemd.UnitStateInactive)
		case process.ProcessStateFailed:
			out = append(out, systemd.UnitStateFailed)
		}
	}
	return out
}

func processStateFromUnit(state systemd.UnitState, exitStatus int32) process.ProcessState {
	switch state {
	case systemd.UnitStateActive:
		return process.ProcessStateRunning
	case systemd.UnitStateActivating:
		return process.ProcessStateStarting
	case systemd.UnitStateFailed:
		return process.ProcessStateFailed
	default:
		if exitStatus == 0 {
			return process.ProcessStateExited
		}
		return process.ProcessStateFailed
	}
}
