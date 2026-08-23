//go:build darwin

package host

import "golang.org/x/sys/unix"

func sessionProcessIDs(sid int) ([]int, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}

	pids := make([]int, 0)
	for _, process := range processes {
		pid := int(process.Proc.P_pid)
		processSID, err := unix.Getsid(pid)
		if err == nil && processSID == sid {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
