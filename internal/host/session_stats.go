package host

import (
	"encoding/json"
	"maps"
)

// mergeFields returns a copy of base with extra layered on top.
func mergeFields(base, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	maps.Copy(merged, base)
	maps.Copy(merged, extra)
	return merged
}

// statsJSON encodes the latest periodic sample for the control plane.
// Sampling on demand instead would measure rates over whatever tiny
// interval had passed since the last tick.
func statsJSON(s *Sampler) (string, error) {
	data, err := json.Marshal(s.Latest())
	return string(data), err
}

// trackProcess points the sampler at a newly started task (for hosts
// without a cgroup of their own) and makes the task, not the host, the
// OOM killer's preferred victim.
func trackProcess(s *Sampler, proc Process) {
	if p, ok := proc.(interface{ PID() int }); ok {
		s.SetTaskPID(p.PID())
		preferOOMVictim(p.PID())
	}
}
