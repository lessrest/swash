package fake

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/mbrock/swash/internal/eventlog"
	eventlogfake "github.com/mbrock/swash/internal/eventlog/fake"
	systemdapi "github.com/mbrock/swash/internal/platform/systemd/process"
	"github.com/mbrock/swash/internal/process"
)

type UnitName = systemdapi.UnitName
type Unit = systemdapi.Unit
type UnitState = systemdapi.UnitState
type ExitNotification = systemdapi.ExitNotification
type TransientSpec = systemdapi.TransientSpec
type FakeJournal = eventlogfake.FakeJournal
type EventRecord = eventlog.EventRecord

type ProcessSpec = process.ProcessSpec
type ProcessRef = process.ProcessRef
type ProcessFilter = process.ProcessFilter
type ProcessStatus = process.ProcessStatus
type ProcessExit = process.ProcessExit

const (
	UnitStateActive   = systemdapi.UnitStateActive
	UnitStateInactive = systemdapi.UnitStateInactive
	UnitStateFailed   = systemdapi.UnitStateFailed
)

var TaskUnit = systemdapi.TaskUnit
var SessionSlice = systemdapi.SessionSlice

// FakeCommand is a function that simulates a command execution.
// It receives stdin, stdout, stderr and should return an exit code.
// The context is cancelled when the process should be killed.
type FakeCommand func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int

// fakeProcess tracks a running fake process.
type fakeProcess struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// FakeSystemd is an in-memory implementation of Systemd for unit tests.
type FakeSystemd struct {
	mu        sync.RWMutex
	units     map[UnitName]*Unit
	commands  map[string]FakeCommand // command name -> handler
	processes map[UnitName]*fakeProcess
	journal   *FakeJournal // Optional: for writing systemd-style exit events

	// Exit notification subscriptions
	exitSubs map[UnitName][]chan ExitNotification
}

// NewFakeSystemd creates a new FakeSystemd with empty state.
func NewFakeSystemd() *FakeSystemd {
	return &FakeSystemd{
		units:     make(map[UnitName]*Unit),
		commands:  make(map[string]FakeCommand),
		processes: make(map[UnitName]*fakeProcess),
		exitSubs:  make(map[UnitName][]chan ExitNotification),
	}
}

// NewTestFakes creates a connected FakeSystemd and FakeJournal for testing.
// It also registers a handler for the test binary to handle notify-exit subcommand.
func NewTestFakes() (*FakeSystemd, *FakeJournal) {
	systemd := NewFakeSystemd()
	journal := eventlogfake.NewFakeJournal()
	systemd.journal = journal

	// Register handler for the test binary (used by ExecStopPost)
	// When ExecStopPost runs "{selfExe} notify-exit {sessionID} {exitCode} {result}",
	// this handler parses the args and calls EmitUnitExit.
	selfExe, _ := os.Executable()
	systemd.RegisterCommand(selfExe, func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int {
		// args = ["/path/to/test.binary", "notify-exit", sessionID, exitStatus, serviceResult]
		if len(args) >= 5 && args[1] == "notify-exit" {
			sessionID := args[2]
			exitStatus := 0
			fmt.Sscanf(args[3], "%d", &exitStatus)
			serviceResult := args[4]
			taskUnit := TaskUnit(sessionID)
			systemd.EmitUnitExit(context.Background(), taskUnit, exitStatus, serviceResult)
		}
		return 0
	})

	return systemd, journal
}

// RegisterCommand registers a fake command implementation.
// The name should match spec.Command[0] when StartTransient is called.
func (f *FakeSystemd) RegisterCommand(name string, cmd FakeCommand) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands[name] = cmd
}

// AddUnit adds a unit to the fake state.
func (f *FakeSystemd) AddUnit(unit Unit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.units[unit.Name] = &unit
}

// ListUnits returns units matching patterns in given states.
func (f *FakeSystemd) ListUnits(ctx context.Context, patterns []UnitName, states []UnitState) ([]Unit, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	var result []Unit
	for _, unit := range f.units {
		if matchesPatterns(unit.Name, patterns) && matchesStates(unit.State, states) {
			result = append(result, *unit)
		}
	}
	return result, nil
}

// GetUnit retrieves a single unit's properties.
func (f *FakeSystemd) GetUnit(ctx context.Context, name UnitName) (*Unit, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	unit, ok := f.units[name]
	if !ok {
		return nil, fmt.Errorf("unit %s not found", name)
	}
	u := *unit
	return &u, nil
}

// StopUnit gracefully stops a unit.
func (f *FakeSystemd) StopUnit(ctx context.Context, name UnitName) error {
	f.mu.Lock()
	proc := f.processes[name]
	f.mu.Unlock()

	if proc != nil {
		proc.cancel()
		<-proc.done
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	unit, ok := f.units[name]
	if !ok {
		return fmt.Errorf("unit %s not found", name)
	}
	unit.State = UnitStateInactive
	return nil
}

// StartUnit starts a persistent unit (no-op in fake - only transient units supported).
func (f *FakeSystemd) StartUnit(ctx context.Context, name UnitName) error {
	return nil
}

// Reload tells systemd to reload configuration (no-op in fake).
func (f *FakeSystemd) Reload(ctx context.Context) error {
	return nil
}

// EnableUnits enables unit files (no-op in fake).
func (f *FakeSystemd) EnableUnits(ctx context.Context, units []string) error {
	return nil
}

// DisableUnits disables unit files (no-op in fake).
func (f *FakeSystemd) DisableUnits(ctx context.Context, units []string) error {
	return nil
}

// KillUnit sends a signal to all processes in a unit.
func (f *FakeSystemd) KillUnit(ctx context.Context, name UnitName, signal syscall.Signal) error {
	f.mu.Lock()
	proc := f.processes[name]
	f.mu.Unlock()

	if proc != nil && (signal == syscall.SIGKILL || signal == syscall.SIGTERM) {
		proc.cancel()
		<-proc.done
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	unit, ok := f.units[name]
	if !ok {
		return fmt.Errorf("unit %s not found", name)
	}

	if signal == syscall.SIGKILL || signal == syscall.SIGTERM {
		unit.State = UnitStateInactive
	}
	return nil
}

// StartTransient creates and starts a transient unit.
func (f *FakeSystemd) StartTransient(ctx context.Context, spec TransientSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if existing, ok := f.units[spec.Unit]; ok && existing.State == UnitStateActive {
		return fmt.Errorf("unit %s already exists", spec.Unit)
	}

	unit := &Unit{
		Name:        spec.Unit,
		State:       UnitStateActive,
		Description: spec.Description,
		Started:     time.Now(),
		WorkingDir:  spec.WorkingDir,
	}
	f.units[spec.Unit] = unit

	// Look up command handler
	if len(spec.Command) == 0 {
		return fmt.Errorf("empty command")
	}
	cmdName := spec.Command[0]
	handler, ok := f.commands[cmdName]
	if !ok {
		delete(f.units, spec.Unit)
		return fmt.Errorf("executable %q not found", cmdName)
	}

	// Set up stdio by duplicating the file descriptors.
	// This is necessary because the caller will close its copies after StartTransient returns,
	// but the fake process goroutine needs to keep using them.
	var stdin io.Reader
	var stdout, stderr io.Writer
	var stdinFile, stdoutFile, stderrFile *os.File

	if spec.Stdin != nil {
		newFd, err := syscall.Dup(*spec.Stdin)
		if err != nil {
			delete(f.units, spec.Unit)
			return fmt.Errorf("dup stdin: %w", err)
		}
		stdinFile = os.NewFile(uintptr(newFd), "stdin")
		stdin = stdinFile
	} else {
		stdin = &nopReader{}
	}

	if spec.Stdout != nil {
		newFd, err := syscall.Dup(*spec.Stdout)
		if err != nil {
			if stdinFile != nil {
				stdinFile.Close()
			}
			delete(f.units, spec.Unit)
			return fmt.Errorf("dup stdout: %w", err)
		}
		stdoutFile = os.NewFile(uintptr(newFd), "stdout")
		stdout = stdoutFile
	} else {
		stdout = io.Discard
	}

	if spec.Stderr != nil {
		newFd, err := syscall.Dup(*spec.Stderr)
		if err != nil {
			if stdinFile != nil {
				stdinFile.Close()
			}
			if stdoutFile != nil {
				stdoutFile.Close()
			}
			delete(f.units, spec.Unit)
			return fmt.Errorf("dup stderr: %w", err)
		}
		stderrFile = os.NewFile(uintptr(newFd), "stderr")
		stderr = stderrFile
	} else {
		stderr = io.Discard
	}

	// Create cancellable context for process lifetime
	procCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	proc := &fakeProcess{
		cancel: cancel,
		done:   done,
	}
	f.processes[spec.Unit] = proc

	// Run the command in a goroutine
	go func() {
		defer close(done)

		exitCode := handler(procCtx, stdin, stdout, stderr, spec.Command)

		// Close our duplicated file descriptors
		if stdinFile != nil {
			stdinFile.Close()
		}
		if stdoutFile != nil {
			stdoutFile.Close()
		}
		if stderrFile != nil {
			stderrFile.Close()
		}

		// Determine service result
		var serviceResult string
		if procCtx.Err() != nil {
			serviceResult = "signal"
		} else if exitCode == 0 {
			serviceResult = "success"
		} else {
			serviceResult = "exit-code"
		}

		// Update unit state
		f.mu.Lock()
		journal := f.journal
		if unit, ok := f.units[spec.Unit]; ok {
			unit.ExitStatus = int32(exitCode)
			if procCtx.Err() != nil {
				// Killed
				unit.State = UnitStateFailed
			} else if exitCode == 0 {
				unit.State = UnitStateInactive
			} else {
				unit.State = UnitStateFailed
			}
		}
		delete(f.processes, spec.Unit)
		f.mu.Unlock()

		// Run ExecStopPost commands (like real systemd does)
		for _, cmd := range spec.ExecStopPost {
			if len(cmd) == 0 {
				continue
			}
			// Substitute $EXIT_STATUS and $SERVICE_RESULT in arguments
			args := make([]string, len(cmd))
			for i, arg := range cmd {
				switch arg {
				case "$EXIT_STATUS":
					args[i] = fmt.Sprintf("%d", exitCode)
				case "$SERVICE_RESULT":
					args[i] = serviceResult
				default:
					args[i] = arg
				}
			}
			// Look up handler for this command
			f.mu.RLock()
			handler, ok := f.commands[args[0]]
			f.mu.RUnlock()
			if ok {
				// Run it (ignore exit code - ExecStopPost failures don't affect unit)
				handler(context.Background(), nil, io.Discard, io.Discard, args)
			}
		}

		// Write systemd-style exit event to journal (like real systemd does)
		if journal != nil {
			journal.AddEntry(EventRecord{
				Message: fmt.Sprintf("%s: Main process exited, code=exited, status=%d", spec.Unit, exitCode),
				Fields: map[string]string{
					"USER_UNIT":   spec.Unit.String(),
					"EXIT_STATUS": fmt.Sprintf("%d", exitCode),
					"EXIT_CODE":   "exited",
				},
			})
		}
	}()

	return nil
}

// WaitUnit blocks until the unit is no longer active.
// Useful in tests to synchronize with process completion.
func (f *FakeSystemd) WaitUnit(ctx context.Context, name UnitName) error {
	f.mu.RLock()
	proc := f.processes[name]
	f.mu.RUnlock()

	if proc == nil {
		return nil
	}

	select {
	case <-proc.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close releases resources and kills all running processes.
func (f *FakeSystemd) Close() error {
	f.mu.Lock()
	procs := make([]*fakeProcess, 0, len(f.processes))
	for _, p := range f.processes {
		procs = append(procs, p)
	}
	f.mu.Unlock()

	// Cancel all processes
	for _, p := range procs {
		p.cancel()
	}
	// Wait for all to finish
	for _, p := range procs {
		<-p.done
	}

	return nil
}

// nopReader is a reader that always returns EOF.
type nopReader struct{}

func (nopReader) Read(p []byte) (int, error) {
	return 0, io.EOF
}

// matchesPatterns checks if a unit name matches any of the patterns.
func matchesPatterns(name UnitName, patterns []UnitName) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		ps := string(p)
		ns := string(name)
		if ps == "*" || ps == ns {
			return true
		}
		if len(ps) > 1 && ps[len(ps)-1] == '*' {
			prefix := ps[:len(ps)-1]
			if len(ns) >= len(prefix) && ns[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}

// matchesStates checks if a state matches any of the given states.
func matchesStates(state UnitState, states []UnitState) bool {
	if len(states) == 0 {
		return true
	}
	for _, s := range states {
		if state == s {
			return true
		}
	}
	return false
}

// SubscribeUnitExit returns a channel that receives when the specified unit exits.
func (f *FakeSystemd) SubscribeUnitExit(ctx context.Context, unit UnitName) (<-chan ExitNotification, error) {
	ch := make(chan ExitNotification, 1)

	f.mu.Lock()
	f.exitSubs[unit] = append(f.exitSubs[unit], ch)
	f.mu.Unlock()

	// Clean up subscription when context is cancelled
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		defer f.mu.Unlock()
		subs := f.exitSubs[unit]
		for i, sub := range subs {
			if sub == ch {
				f.exitSubs[unit] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		close(ch)
	}()

	return ch, nil
}

// EmitUnitExit sends an exit notification for a unit to all subscribers.
func (f *FakeSystemd) EmitUnitExit(ctx context.Context, unit UnitName, exitCode int, serviceResult string) error {
	f.mu.Lock()
	subs := f.exitSubs[unit]
	f.mu.Unlock()

	notification := ExitNotification{
		Unit:          unit,
		ExitCode:      exitCode,
		ServiceResult: serviceResult,
	}

	for _, ch := range subs {
		select {
		case ch <- notification:
		default:
			// Channel full, skip
		}
	}

	return nil
}

// --- ProcessBackend implementation (semantic wrapper) ---

// Start launches a workload described by ProcessSpec.
func (f *FakeSystemd) Start(ctx context.Context, spec ProcessSpec) error {
	tSpec := TransientSpec{
		Unit:         unitNameForRef(spec.Ref),
		Slice:        SessionSlice(spec.Ref.SessionID),
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

	return f.StartTransient(ctx, tSpec)
}

// Stop stops a workload.
func (f *FakeSystemd) Stop(ctx context.Context, ref ProcessRef) error {
	return f.StopUnit(ctx, unitNameForRef(ref))
}

// Kill sends a signal to a workload.
func (f *FakeSystemd) Kill(ctx context.Context, ref ProcessRef, signal syscall.Signal) error {
	return f.KillUnit(ctx, unitNameForRef(ref), signal)
}

// List lists workloads matching the filter.
func (f *FakeSystemd) List(ctx context.Context, filter ProcessFilter) ([]ProcessStatus, error) {
	units, err := f.ListUnits(ctx, patternsForRoles(filter.Roles), statesForFilter(filter.States))
	if err != nil {
		return nil, err
	}

	var statuses []ProcessStatus
	for _, u := range units {
		ref, ok := refFromUnit(u.Name)
		if !ok {
			continue
		}
		statuses = append(statuses, ProcessStatus{
			Ref:         ref,
			State:       processStateFromUnit(u.State, u.ExitStatus),
			Description: u.Description,
			Started:     u.Started,
			PID:         u.MainPID,
			WorkingDir:  u.WorkingDir,
			ExitStatus:  u.ExitStatus,
		})
	}
	return statuses, nil
}

// Describe returns info about a single workload.
func (f *FakeSystemd) Describe(ctx context.Context, ref ProcessRef) (*ProcessStatus, error) {
	unit, err := f.GetUnit(ctx, unitNameForRef(ref))
	if err != nil {
		return nil, err
	}

	return &ProcessStatus{
		Ref:         ref,
		State:       processStateFromUnit(unit.State, unit.ExitStatus),
		Description: unit.Description,
		Started:     unit.Started,
		PID:         unit.MainPID,
		WorkingDir:  unit.WorkingDir,
		ExitStatus:  unit.ExitStatus,
	}, nil
}

// SubscribeExit subscribes to workload exit notifications.
func (f *FakeSystemd) SubscribeExit(ctx context.Context, ref ProcessRef) (<-chan ProcessExit, error) {
	unitCh, err := f.SubscribeUnitExit(ctx, unitNameForRef(ref))
	if err != nil {
		return nil, err
	}
	out := make(chan ProcessExit, 1)
	go func() {
		defer close(out)
		for n := range unitCh {
			out <- ProcessExit{
				Ref:      ref,
				ExitCode: n.ExitCode,
				Result:   n.ServiceResult,
			}
		}
	}()
	return out, nil
}

// EmitExit emits an exit notification for a workload.
func (f *FakeSystemd) EmitExit(ctx context.Context, ref ProcessRef, exitCode int, result string) error {
	return f.EmitUnitExit(ctx, unitNameForRef(ref), exitCode, result)
}
