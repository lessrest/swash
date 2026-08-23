package host

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecProcessSignalTerminatesProcessSessionTree(t *testing.T) {
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdoutRead.Close()

	proc, err := Default().Start(
		[]string{os.Args[0], "-test.run=^TestProcessTreeHelper$", "--", "parent"},
		strings.NewReader(""), stdoutWrite, io.Discard)
	stdoutWrite.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Kill()

	line, err := bufio.NewReader(stdoutRead).ReadString('\n')
	if err != nil {
		t.Fatalf("reading descendant pid: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parsing descendant pid %q: %v", line, err)
	}
	childPGID, err := syscall.Getpgid(childPID)
	if err != nil {
		t.Fatalf("reading descendant process group: %v", err)
	}
	if childPGID != childPID {
		t.Fatalf("descendant pid %d did not create its own process group (pgid %d)", childPID, childPGID)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling task session: %v", err)
	}
	if _, err := proc.Wait(); err != nil {
		t.Fatalf("waiting for task leader: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for processAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(childPID) {
		t.Fatalf("descendant pid %d in its own process group survived session SIGTERM", childPID)
	}
}

func TestProcessTreeHelper(t *testing.T) {
	mode := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
			break
		}
	}
	if mode == "" {
		return
	}

	switch mode {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$", "--", "child")
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stdout.WriteString(strconv.Itoa(child.Process.Pid) + "\n")
		_ = child.Wait()
	case "child":
		for {
			time.Sleep(time.Hour)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
