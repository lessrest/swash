//go:build linux

package host

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func sessionProcessIDs(sid int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	pids := make([]int, 0)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		processSID, err := unix.Getsid(pid)
		if err == nil && processSID == sid {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
