package host

import (
	"errors"
	"syscall"
)

// signalProcessSession signals every process still belonging to the task's
// session, including descendants that created their own process groups. A
// process that deliberately creates a new session has escaped Swash's
// ownership domain.
func signalProcessSession(sid int, sig syscall.Signal) error {
	pids, err := sessionProcessIDs(sid)
	if err != nil {
		// Enumeration is an enhancement over the portable process-group
		// primitive. Retain that primitive as a safe fallback.
		return ignoreMissingProcess(syscall.Kill(-sid, sig))
	}

	var firstErr error
	signal := func(pid int) {
		if err := ignoreMissingProcess(syscall.Kill(pid, sig)); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Descendants first prevents a cooperative parent from spawning or
	// restarting children while it is shutting down.
	leaderPresent := false
	for _, pid := range pids {
		if pid == sid {
			leaderPresent = true
			continue
		}
		signal(pid)
	}
	if leaderPresent {
		signal(sid)
	}

	// Close the snapshot race: sweep anything created while the first pass
	// was in progress. This also works after the session leader has exited.
	if remaining, err := sessionProcessIDs(sid); err == nil {
		for _, pid := range remaining {
			signal(pid)
		}
	}

	return firstErr
}

func ignoreMissingProcess(err error) error {
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
