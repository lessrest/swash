package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
)

// FakeCommand is a function that simulates a command execution.
// It receives the command arguments, stdin, stdout, stderr and should return an exit code.
// The context is cancelled when the process should be killed.
type FakeCommand func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) int

// FakeExecutor is a test implementation of Executor that runs registered fake commands.
type FakeExecutor struct {
	mu       sync.RWMutex
	commands map[string]FakeCommand
}

// NewFakeExecutor creates a new FakeExecutor.
func NewFakeExecutor() *FakeExecutor {
	return &FakeExecutor{
		commands: make(map[string]FakeCommand),
	}
}

// RegisterCommand registers a fake command implementation.
// The name should match the first element of the command slice.
func (e *FakeExecutor) RegisterCommand(name string, handler FakeCommand) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.commands[name] = handler
}

// fakeProcess implements Process for FakeExecutor.
type fakeProcess struct {
	cancel   context.CancelFunc
	done     chan struct{}
	exitCode int
	mu       sync.Mutex
}

func (p *fakeProcess) Wait() (int, error) {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode, nil
}

func (p *fakeProcess) Kill() error {
	p.cancel()
	return nil
}

func (p *fakeProcess) Signal(sig syscall.Signal) error {
	if sig == syscall.SIGTERM || sig == syscall.SIGKILL {
		p.cancel()
	}
	return nil
}

// Start implements Executor.Start for FakeExecutor.
func (e *FakeExecutor) Start(cmdArgs []string, stdin io.Reader, stdout, stderr io.Writer) (Process, error) {
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	e.mu.RLock()
	handler, ok := e.commands[cmdArgs[0]]
	e.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("executable %q not found", cmdArgs[0])
	}

	// Dup file descriptors if the inputs are *os.File, since the caller will close
	// them after Start returns but the handler goroutine needs to keep using them.
	var stdinFile, stdoutFile, stderrFile *os.File
	var stdinReader io.Reader = stdin
	var stdoutWriter io.Writer = stdout
	var stderrWriter io.Writer = stderr

	if f, ok := stdin.(*os.File); ok {
		newFd, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			return nil, fmt.Errorf("dup stdin: %w", err)
		}
		stdinFile = os.NewFile(uintptr(newFd), "stdin")
		stdinReader = stdinFile
	}

	if f, ok := stdout.(*os.File); ok {
		newFd, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			if stdinFile != nil {
				stdinFile.Close()
			}
			return nil, fmt.Errorf("dup stdout: %w", err)
		}
		stdoutFile = os.NewFile(uintptr(newFd), "stdout")
		stdoutWriter = stdoutFile
	}

	if f, ok := stderr.(*os.File); ok {
		newFd, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			if stdinFile != nil {
				stdinFile.Close()
			}
			if stdoutFile != nil {
				stdoutFile.Close()
			}
			return nil, fmt.Errorf("dup stderr: %w", err)
		}
		stderrFile = os.NewFile(uintptr(newFd), "stderr")
		stderrWriter = stderrFile
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	proc := &fakeProcess{
		cancel: cancel,
		done:   done,
	}

	go func() {
		defer func() {
			if stdinFile != nil {
				stdinFile.Close()
			}
			if stdoutFile != nil {
				stdoutFile.Close()
			}
			if stderrFile != nil {
				stderrFile.Close()
			}
		}()

		exitCode := handler(ctx, stdinReader, stdoutWriter, stderrWriter, cmdArgs)
		proc.mu.Lock()
		proc.exitCode = exitCode
		proc.mu.Unlock()
		close(done)
	}()

	return proc, nil
}

// StartPTY implements Executor.StartPTY for FakeExecutor.
// For testing, we use the slave file directly for I/O since we don't have a real PTY.
func (e *FakeExecutor) StartPTY(cmdArgs []string, slave *os.File) (Process, error) {
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	e.mu.RLock()
	handler, ok := e.commands[cmdArgs[0]]
	e.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("executable %q not found", cmdArgs[0])
	}

	// Dup the slave fd since the caller will close it after Start returns
	newFd, err := syscall.Dup(int(slave.Fd()))
	if err != nil {
		return nil, fmt.Errorf("dup slave: %w", err)
	}
	slaveFile := os.NewFile(uintptr(newFd), "slave")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	proc := &fakeProcess{
		cancel: cancel,
		done:   done,
	}

	go func() {
		defer slaveFile.Close()

		// For PTY mode, the slave file is used for all I/O
		exitCode := handler(ctx, slaveFile, slaveFile, slaveFile, cmdArgs)
		proc.mu.Lock()
		proc.exitCode = exitCode
		proc.mu.Unlock()
		close(done)
	}()

	return proc, nil
}
