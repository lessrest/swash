package exec

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"slices"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/mbrock/swash/internal/process"
)

// Backend is a portable ProcessBackend implementation backed by plain OS processes.
type Backend struct {
	mu    sync.Mutex
	procs map[process.ProcessRef]*procState
}

type procState struct {
	ref process.ProcessRef

	cmd *osexec.Cmd

	description string
	workingDir  string
	started     time.Time

	done      chan struct{}
	exitCode  int
	exitErr   error
	exitState process.ProcessState
}

var _ process.ProcessBackend = (*Backend)(nil)

func New() *Backend {
	return &Backend{
		procs: make(map[process.ProcessRef]*procState),
	}
}

func (b *Backend) Close() error {
	// Best-effort kill all running processes.
	b.mu.Lock()
	procs := make([]*procState, 0, len(b.procs))
	for _, p := range b.procs {
		procs = append(procs, p)
	}
	b.mu.Unlock()

	for _, p := range procs {
		_ = b.Kill(context.Background(), p.ref, syscall.SIGKILL)
	}
	return nil
}

func (b *Backend) List(ctx context.Context, filter process.ProcessFilter) ([]process.ProcessStatus, error) {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()

	var statuses []process.ProcessStatus
	for ref, p := range b.procs {
		// Filter by role/state if requested
		if len(filter.Roles) > 0 {
			ok := slices.Contains(filter.Roles, ref.Role)
			if !ok {
				continue
			}
		}
		if len(filter.States) > 0 {
			ok := slices.Contains(filter.States, p.exitState)
			if !ok {
				continue
			}
		}

		statuses = append(statuses, process.ProcessStatus{
			Ref:         ref,
			State:       p.exitState,
			Description: p.description,
			Started:     p.started,
			PID:         uint32(pidOf(p.cmd)),
			WorkingDir:  p.workingDir,
			ExitStatus:  int32(p.exitCode),
		})
	}

	// Stable order for callers/tests.
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].Ref.SessionID == statuses[j].Ref.SessionID {
			return statuses[i].Ref.Role < statuses[j].Ref.Role
		}
		return statuses[i].Ref.SessionID < statuses[j].Ref.SessionID
	})

	return statuses, nil
}

func (b *Backend) Describe(ctx context.Context, ref process.ProcessRef) (*process.ProcessStatus, error) {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.procs[ref]
	if !ok {
		return nil, fmt.Errorf("process not found")
	}
	return &process.ProcessStatus{
		Ref:         ref,
		State:       p.exitState,
		Description: p.description,
		Started:     p.started,
		PID:         uint32(pidOf(p.cmd)),
		WorkingDir:  p.workingDir,
		ExitStatus:  int32(p.exitCode),
	}, nil
}

func (b *Backend) Start(ctx context.Context, spec process.ProcessSpec) error {
	if len(spec.Command) == 0 {
		return fmt.Errorf("empty command")
	}

	b.mu.Lock()
	if _, exists := b.procs[spec.Ref]; exists {
		b.mu.Unlock()
		return fmt.Errorf("process already exists")
	}
	b.mu.Unlock()

	cmd := osexec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	cmd.Dir = spec.WorkingDir
	cmd.Env = envList(spec.Environment)

	// stdio wiring: use provided file descriptors, otherwise discard.
	if spec.IO.Stdin != nil {
		cmd.Stdin = os.NewFile(uintptr(*spec.IO.Stdin), "stdin")
	}
	if spec.IO.Stdout != nil {
		cmd.Stdout = os.NewFile(uintptr(*spec.IO.Stdout), "stdout")
	}
	if spec.IO.Stderr != nil {
		cmd.Stderr = os.NewFile(uintptr(*spec.IO.Stderr), "stderr")
	}

	// Give each task its own process group so we can kill it reliably.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	p := &procState{
		ref:         spec.Ref,
		cmd:         cmd,
		description: spec.Description,
		workingDir:  spec.WorkingDir,
		started:     time.Now(),
		done:        make(chan struct{}),
		exitState:   process.ProcessStateStarting,
	}

	b.mu.Lock()
	b.procs[spec.Ref] = p
	b.mu.Unlock()

	if err := cmd.Start(); err != nil {
		b.mu.Lock()
		delete(b.procs, spec.Ref)
		b.mu.Unlock()
		close(p.done)
		return err
	}

	b.mu.Lock()
	p.exitState = process.ProcessStateRunning
	b.mu.Unlock()

	go b.waitForExit(p)
	return nil
}

func (b *Backend) Stop(ctx context.Context, ref process.ProcessRef) error {
	_ = ctx
	return b.Kill(context.Background(), ref, syscall.SIGTERM)
}

func (b *Backend) ResetFailed(ctx context.Context, ref process.ProcessRef) error {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	// Remove the process record so it can be started again
	delete(b.procs, ref)
	return nil
}

func (b *Backend) Kill(ctx context.Context, ref process.ProcessRef, signal syscall.Signal) error {
	_ = ctx
	b.mu.Lock()
	p := b.procs[ref]
	b.mu.Unlock()
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return fmt.Errorf("process not running")
	}

	// Try process group first (negative PID), then fall back to direct process.
	pid := p.cmd.Process.Pid
	if pid > 0 {
		_ = syscall.Kill(-pid, signal)
	}
	return p.cmd.Process.Signal(signal)
}

func (b *Backend) waitForExit(p *procState) {
	err := p.cmd.Wait()

	exitCode := 0
	if err != nil {
		if ee, ok := err.(*osexec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
		}
	}

	state := process.ProcessStateExited
	if err != nil && exitCode != 0 {
		state = process.ProcessStateFailed
	}

	b.mu.Lock()
	p.exitErr = err
	p.exitCode = exitCode
	p.exitState = state
	b.mu.Unlock()

	close(p.done)
}

func envList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func pidOf(cmd *osexec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}
