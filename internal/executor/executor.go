// Package executor provides an abstraction for starting processes.
package executor

import (
	"io"
	"os"
	"os/exec"
	"syscall"
)

// Process represents a running process.
type Process interface {
	// Wait blocks until the process exits and returns the exit code.
	// Returns 0 for success, non-zero for failure.
	Wait() (exitCode int, err error)
	// Kill sends SIGKILL to the process.
	Kill() error
}

// Executor starts processes.
type Executor interface {
	// Start starts a command with the given I/O configuration.
	Start(cmd []string, stdin io.Reader, stdout, stderr io.Writer) (Process, error)

	// StartPTY starts a command connected to a PTY slave.
	// The slave file is used for stdin/stdout/stderr and the process
	// becomes the session leader with the PTY as its controlling terminal.
	StartPTY(cmd []string, slave *os.File) (Process, error)
}

// ExecExecutor is the default Executor that uses os/exec.
type ExecExecutor struct{}

// execProcess wraps exec.Cmd to implement Process.
type execProcess struct {
	cmd *exec.Cmd
}

func (p *execProcess) Wait() (int, error) {
	err := p.cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

// Start implements Executor.Start using os/exec.
func (e *ExecExecutor) Start(cmdArgs []string, stdin io.Reader, stdout, stderr io.Writer) (Process, error) {
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &execProcess{cmd: cmd}, nil
}

// StartPTY implements Executor.StartPTY using os/exec with PTY setup.
func (e *ExecExecutor) StartPTY(cmdArgs []string, slave *os.File) (Process, error) {
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &execProcess{cmd: cmd}, nil
}

// Default returns the default ExecExecutor.
func Default() Executor {
	return &ExecExecutor{}
}
