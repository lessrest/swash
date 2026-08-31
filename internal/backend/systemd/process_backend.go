package systemd

import (
	"context"
	"syscall"
	"time"
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
	tSpec := TransientSpec{
		Unit:        HostUnit(spec.SessionID),
		Slice:       RootSlice(),
		ServiceType: "notify",
		BusName:     spec.BusName,
		WorkingDir:  spec.WorkingDir,
		Description: spec.Description,
		Environment: spec.Environment,
		Command:     spec.Command,
		Collect:     spec.Collect,
		KillMode:    "mixed",
		TimeoutStop: 5 * time.Second,
	}

	return b.systemd.StartTransient(ctx, tSpec)
}

// Stop stops a workload.
func (b *ProcessManager) Stop(ctx context.Context, sessionID string) error {
	return b.systemd.StopUnit(ctx, HostUnit(sessionID))
}

// Kill sends a signal to a workload.
func (b *ProcessManager) Kill(ctx context.Context, sessionID string, signal syscall.Signal) error {
	return b.systemd.KillUnit(ctx, HostUnit(sessionID), signal)
}

// List lists active swash host services in the configured root slice.
func (b *ProcessManager) List(ctx context.Context) ([]ProcessStatus, error) {
	units, err := b.systemd.ListUnits(ctx,
		[]UnitName{"swash-host-*.service"},
		[]UnitState{UnitStateActive, UnitStateActivating, UnitStateDeactivating},
	)
	if err != nil {
		return nil, err
	}

	var result []ProcessStatus
	for _, u := range units {
		// Keep test sessions in their isolated root slice out of normal listings.
		if u.Slice != RootSlice().String() {
			continue
		}

		result = append(result, ProcessStatus{
			SessionID:   u.Name.SessionID(),
			Description: u.Description,
			Started:     u.Started,
			PID:         u.MainPID,
			WorkingDir:  u.WorkingDir,
		})
	}

	return result, nil
}

// Close releases the underlying Systemd connection.
func (b *ProcessManager) Close() error {
	if b.systemd == nil {
		return nil
	}
	return b.systemd.Close()
}
