package swash

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
)

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
}

// NewFakeSystemd creates a new FakeSystemd with empty state.
func NewFakeSystemd() *FakeSystemd {
	return &FakeSystemd{
		units:     make(map[UnitName]*Unit),
		commands:  make(map[string]FakeCommand),
		processes: make(map[UnitName]*fakeProcess),
	}
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

		// Update unit state
		f.mu.Lock()
		defer f.mu.Unlock()

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
