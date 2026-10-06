package host

import (
	"os"
	"strconv"
	"strings"
)

// preferOOMVictim makes the task the kernel's first choice when its
// session runs out of memory, so the host survives to record the exit.
// The host shares the session's cgroup and would otherwise be just as
// likely a victim. Raising oom_score_adj needs no privileges; children
// of the task inherit the value.
func preferOOMVictim(pid int) {
	cur, err := os.ReadFile("/proc/self/oom_score_adj")
	if err != nil {
		return
	}
	adj, _ := strconv.Atoi(strings.TrimSpace(string(cur)))
	adj = min(adj+500, 1000)
	_ = os.WriteFile("/proc/"+strconv.Itoa(pid)+"/oom_score_adj", []byte(strconv.Itoa(adj)), 0)
}
