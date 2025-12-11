package swash

import (
	"context"
	"syscall"
	"time"
)

// ProcessRole distinguishes the two workloads that make up a swash session.
// Host = the D-Bus host shim, Task = the user command.
type ProcessRole string

const (
	ProcessRoleHost ProcessRole = "host"
	ProcessRoleTask ProcessRole = "task"
)

// ProcessRef identifies a workload that belongs to a session.
type ProcessRef struct {
	SessionID string
	Role      ProcessRole
}

// HostProcess returns the host workload for a session.
func HostProcess(sessionID string) ProcessRef {
	return ProcessRef{SessionID: sessionID, Role: ProcessRoleHost}
}

// TaskProcess returns the task workload for a session.
func TaskProcess(sessionID string) ProcessRef {
	return ProcessRef{SessionID: sessionID, Role: ProcessRoleTask}
}

// ProcessState is a simplified view of a workload state.
type ProcessState string

const (
	ProcessStateRunning  ProcessState = "running"
	ProcessStateStarting ProcessState = "starting"
	ProcessStateExited   ProcessState = "exited"
	ProcessStateFailed   ProcessState = "failed"
)

// ProcessStatus describes a workload.
type ProcessStatus struct {
	Ref         ProcessRef
	State       ProcessState
	Description string
	Started     time.Time
	PID         uint32
	WorkingDir  string
	ExitStatus  int32
}

// IODescriptor captures optional stdio handles for a workload.
type IODescriptor struct {
	Stdin  *int
	Stdout *int
	Stderr *int
}

// ProcessLaunchKind hints at how the workload should be wired up by the backend.
// Exec is a plain process; Service exposes a bus name (used for the host shim).
type ProcessLaunchKind string

const (
	LaunchKindExec    ProcessLaunchKind = "exec"
	LaunchKindService ProcessLaunchKind = "service"
)

// ProcessSpec defines how to start a workload.
type ProcessSpec struct {
	Ref         ProcessRef
	Command     []string
	Description string
	WorkingDir  string
	Environment map[string]string
	IO          IODescriptor
	TTYPath     string
	BusName     string
	Collect     bool

	Dependencies []ProcessRef // Stop when these stop; start after them.
	PostStop     [][]string   // Commands to run after the workload exits.

	LaunchKind ProcessLaunchKind
}

// ProcessFilter narrows down backend queries.
type ProcessFilter struct {
	Roles  []ProcessRole
	States []ProcessState
}

// ProcessExit describes an observed workload exit.
type ProcessExit struct {
	Ref      ProcessRef
	ExitCode int
	Result   string
}

// ProcessBackend is a semantic interface for running swash workloads.
// Concrete backends can be backed by systemd, a fake, or anything else.
type ProcessBackend interface {
	List(ctx context.Context, filter ProcessFilter) ([]ProcessStatus, error)
	Describe(ctx context.Context, ref ProcessRef) (*ProcessStatus, error)

	Start(ctx context.Context, spec ProcessSpec) error
	Stop(ctx context.Context, ref ProcessRef) error
	Kill(ctx context.Context, ref ProcessRef, signal syscall.Signal) error

	SubscribeExit(ctx context.Context, ref ProcessRef) (<-chan ProcessExit, error)
	EmitExit(ctx context.Context, ref ProcessRef, exitCode int, result string) error

	Close() error
}
