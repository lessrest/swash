package systemd

import (
	"context"
	"syscall"
	"time"
)

// ProcessStatus describes a workload.
type ProcessStatus struct {
	SessionID   string
	Description string
	Started     time.Time
	PID         uint32
	WorkingDir  string
}

// ProcessSpec defines how to start a workload.
type ProcessSpec struct {
	SessionID   string
	Command     []string
	Description string
	WorkingDir  string
	Environment map[string]string
	BusName     string
	Collect     bool
}

// ProcessBackend is a semantic interface for running swash workloads.
// Concrete backends can be backed by systemd, a fake, or anything else.
type ProcessBackend interface {
	List(ctx context.Context) ([]ProcessStatus, error)

	Start(ctx context.Context, spec ProcessSpec) error
	Stop(ctx context.Context, sessionID string) error
	Kill(ctx context.Context, sessionID string, signal syscall.Signal) error

	Close() error
}
