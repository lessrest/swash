package host

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"time"

	"swa.sh/internal/journal"
)

// statsRecordInterval is how often a session's resource usage is written
// to the journal, which is what swash stats draws its history from.
const statsRecordInterval = 5 * time.Second

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

// recordStats writes a stats event to the journal every
// statsRecordInterval until ctx is done.
func recordStats(ctx context.Context, s *Sampler, events journal.EventLog, tags map[string]string) {
	ticker := time.NewTicker(statsRecordInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			st := s.Latest()
			data, err := json.Marshal(st)
			if err != nil {
				continue
			}
			fields := mergeFields(tags, map[string]string{
				journal.FieldEvent: journal.EventStats,
				journal.FieldStats: string(data),
			})
			if err := events.Write(st.Line(), fields); err != nil {
				slog.Debug("recordStats write failed", "error", err)
			}
		}
	}
}
