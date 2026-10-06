package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"swa.sh/internal/backend"
	"swa.sh/internal/host"
	"swa.sh/internal/journal"
)

// statsHistory returns a session's recorded stats samples, oldest first.
func statsHistory(sessionID string) []host.Stats {
	events, _, err := bk.PollEvents(context.Background(), []backend.EventFilter{
		journal.FilterBySession(sessionID),
		journal.FilterByEvent(journal.EventStats),
	}, "")
	if err != nil {
		return nil
	}
	history := make([]host.Stats, 0, len(events))
	for _, e := range events {
		var st host.Stats
		if json.Unmarshal([]byte(e.Fields[journal.FieldStats]), &st) == nil {
			history = append(history, st)
		}
	}
	return history
}

// metric is one chartable series of a stats history.
type metric struct {
	name   string
	value  func(host.Stats) float64
	format func(float64) string
	peak   bool    // aggregate buckets by max (levels) instead of mean (rates)
	floor  float64 // minimum full-scale value, so small noise stays small
}

func bytesFmt(v float64) string { return host.FormatBytes(uint64(v)) }
func rateFmt(v float64) string  { return host.FormatBytes(uint64(v)) + "/s" }
func pctFmt(v float64) string   { return fmt.Sprintf("%.0f%%", v) }

var metrics = []metric{
	{name: "cpu", value: func(s host.Stats) float64 { return s.CPUPercent }, format: pctFmt, floor: 100},
	{name: "mem", value: func(s host.Stats) float64 { return float64(s.MemCurrent) }, format: bytesFmt, peak: true},
	{name: "swap", value: func(s host.Stats) float64 { return float64(s.Swap) }, format: bytesFmt, peak: true},
	{name: "disk r", value: func(s host.Stats) float64 { return s.DiskReadRate }, format: rateFmt, floor: 1 << 20},
	{name: "disk w", value: func(s host.Stats) float64 { return s.DiskWriteRate }, format: rateFmt, floor: 1 << 20},
	{name: "net ↓", value: func(s host.Stats) float64 { return s.NetRxRate }, format: rateFmt, floor: 1 << 20},
	{name: "net ↑", value: func(s host.Stats) float64 { return s.NetTxRate }, format: rateFmt, floor: 1 << 20},
	{name: "procs", value: func(s host.Stats) float64 { return float64(s.Procs) }, format: func(v float64) string { return fmt.Sprintf("%.0f", v) }, peak: true},
	{name: "stall", value: func(s host.Stats) float64 { return max(s.CPUPressure, s.MemPressure, s.IOPressure) }, format: pctFmt, floor: 100},
}

var sparkLevels = []rune("▁▂▃▄▅▆▇█")

// sparkline draws one column per value, scaled to full. Values under 2%
// of full sit on the baseline; anything above that gets at least one step.
func sparkline(values []float64, full float64) string {
	var b strings.Builder
	top := len(sparkLevels) - 1
	for _, v := range values {
		level := 0
		if full > 0 && v > full*0.02 {
			level = min(max(int(v/full*float64(top)+0.5), 1), top)
		}
		b.WriteRune(sparkLevels[level])
	}
	return b.String()
}

// bucket squeezes values into at most width columns, by mean or max.
func bucket(values []float64, width int, peak bool) []float64 {
	if len(values) <= width {
		return values
	}
	out := make([]float64, width)
	for i := range out {
		lo, hi := i*len(values)/width, (i+1)*len(values)/width
		var acc float64
		for _, v := range values[lo:hi] {
			if peak {
				acc = max(acc, v)
			} else {
				acc += v
			}
		}
		if !peak {
			acc /= float64(hi - lo)
		}
		out[i] = acc
	}
	return out
}

// renderChart prints one sparkline row per non-idle metric.
func renderChart(history []host.Stats, width int, live bool) {
	if len(history) == 0 {
		return
	}
	span := time.Duration((history[len(history)-1].Elapsed - history[0].Elapsed) * float64(time.Second))
	cols := min(len(history), width)
	perCol := span / time.Duration(max(cols-1, 1))
	fmt.Printf("  %d samples over %s, ~%s per column\n", len(history), host.FormatDuration(span), host.FormatDuration(perCol))

	last := history[len(history)-1]
	nowLabel := "last"
	if live {
		nowLabel = "now "
	}
	for _, m := range metrics {
		values := make([]float64, len(history))
		var top float64
		for i, st := range history {
			values[i] = m.value(st)
			top = max(top, values[i])
		}
		if top <= m.floor*0.02 {
			continue // idle or noise
		}
		note := "peak " + m.format(top)
		switch m.name {
		case "mem":
			if last.MemMax > 0 {
				note += " of " + host.FormatBytes(last.MemMax)
			}
		case "cpu":
			if last.CPULimit > 0 {
				note += " of " + pctFmt(last.CPULimit)
			}
		}
		fmt.Printf("  %-6s %s  %s %-7s %s\n", m.name,
			sparkline(bucket(values, width, m.peak), max(top, m.floor)),
			nowLabel, m.format(m.value(last)), note)
	}
}

// cpuSpark is a short CPU sparkline of a session's recent samples.
func cpuSpark(history []host.Stats, n int) string {
	if len(history) > n {
		history = history[len(history)-n:]
	}
	values := make([]float64, len(history))
	top := 100.0
	for i, st := range history {
		values[i] = st.CPUPercent
		top = max(top, values[i])
	}
	return sparkline(values, top)
}
