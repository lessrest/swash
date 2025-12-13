// Package executor provides an abstraction for starting processes.
package executor

import (
	"io"
	"log/slog"
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
	// Signal sends a signal to the process.
	Signal(sig syscall.Signal) error
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
		slog.Debug("execProcess.Kill no process")
		return nil
	}
	slog.Warn("execProcess.Kill sending SIGKILL", "pid", p.cmd.Process.Pid)
	return p.cmd.Process.Kill()
}

func (p *execProcess) Signal(sig syscall.Signal) error {
	if p.cmd.Process == nil {
		slog.Debug("execProcess.Signal no process", "signal", sig)
		return nil
	}
	slog.Debug("execProcess.Signal", "pid", p.cmd.Process.Pid, "signal", sig)
	return p.cmd.Process.Signal(sig)
}

// Start implements Executor.Start using os/exec.
func (e *ExecExecutor) Start(cmdArgs []string, stdin io.Reader, stdout, stderr io.Writer) (Process, error) {
	slog.Debug("ExecExecutor.Start", "cmd", cmdArgs)
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		slog.Debug("ExecExecutor.Start failed", "error", err)
		return nil, err
	}

	slog.Debug("ExecExecutor.Start success", "pid", cmd.Process.Pid)
	return &execProcess{cmd: cmd}, nil
}

// StartPTY implements Executor.StartPTY using os/exec with PTY setup.
func (e *ExecExecutor) StartPTY(cmdArgs []string, slave *os.File) (Process, error) {
	slog.Debug("ExecExecutor.StartPTY", "cmd", cmdArgs)
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
		slog.Debug("ExecExecutor.StartPTY failed", "error", err)
		return nil, err
	}

	slog.Debug("ExecExecutor.StartPTY success", "pid", cmd.Process.Pid)
	return &execProcess{cmd: cmd}, nil
}

// Default returns the default ExecExecutor.
func Default() Executor {
	return &ExecExecutor{}
}
