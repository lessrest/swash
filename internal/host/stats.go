package host

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Stats is a resource usage snapshot of a session: its whole cgroup when
// the host runs as a systemd unit, otherwise the task's process session.
// Totals are cumulative since the session started; rates and percentages
// cover roughly the last few seconds.
type Stats struct {
	Time    time.Time `json:"time"`
	Elapsed float64   `json:"elapsed"` // seconds since the host started

	CPUUsec    uint64  `json:"cpu_usec"`
	CPUPercent float64 `json:"cpu_percent"` // 100 = one full core
	CPULimit   float64 `json:"cpu_limit,omitempty"`

	MemCurrent uint64 `json:"mem_current"`
	MemPeak    uint64 `json:"mem_peak,omitempty"`
	MemMax     uint64 `json:"mem_max,omitempty"`
	Swap       uint64 `json:"swap,omitempty"`
	SwapPeak   uint64 `json:"swap_peak,omitempty"`
	OOMKills   uint64 `json:"oom_kills,omitempty"`

	Procs int `json:"procs"`

	DiskRead      uint64  `json:"disk_read"`
	DiskWrite     uint64  `json:"disk_write"`
	DiskReadRate  float64 `json:"disk_read_rate"`
	DiskWriteRate float64 `json:"disk_write_rate"`

	NetRx     uint64  `json:"net_rx"`
	NetTx     uint64  `json:"net_tx"`
	NetRxRate float64 `json:"net_rx_rate"`
	NetTxRate float64 `json:"net_tx_rate"`
	NetConns  int     `json:"net_conns"` // open TCP connections

	// Share of recent time some of the session's tasks were stalled
	// waiting on CPU, memory or I/O (PSI "some" avg10).
	CPUPressure float64 `json:"cpu_pressure,omitempty"`
	MemPressure float64 `json:"mem_pressure,omitempty"`
	IOPressure  float64 `json:"io_pressure,omitempty"`

	Top []ProcStats `json:"top,omitempty"` // busiest processes right now
}

// ProcStats is one process's recent CPU use.
type ProcStats struct {
	PID        int     `json:"pid"`
	Comm       string  `json:"comm"`
	CPUPercent float64 `json:"cpu_percent"`
}

// ExitFields renders the cumulative totals as journal fields for the
// session's exited event.
func (s Stats) ExitFields() map[string]string {
	fields := map[string]string{
		"SWASH_CPU_USEC":   strconv.FormatUint(s.CPUUsec, 10),
		"SWASH_DISK_READ":  strconv.FormatUint(s.DiskRead, 10),
		"SWASH_DISK_WRITE": strconv.FormatUint(s.DiskWrite, 10),
		"SWASH_NET_RX":     strconv.FormatUint(s.NetRx, 10),
		"SWASH_NET_TX":     strconv.FormatUint(s.NetTx, 10),
	}
	if s.MemPeak > 0 {
		fields["SWASH_MEM_PEAK"] = strconv.FormatUint(s.MemPeak, 10)
	}
	if s.SwapPeak > 0 {
		fields["SWASH_SWAP_PEAK"] = strconv.FormatUint(s.SwapPeak, 10)
	}
	if s.OOMKills > 0 {
		fields["SWASH_OOM_KILLS"] = strconv.FormatUint(s.OOMKills, 10)
	}
	return fields
}

// StatsFromExitFields reads back the totals recorded by ExitFields.
func StatsFromExitFields(fields map[string]string) Stats {
	get := func(name string) uint64 {
		v, _ := strconv.ParseUint(fields[name], 10, 64)
		return v
	}
	return Stats{
		CPUUsec:   get("SWASH_CPU_USEC"),
		MemPeak:   get("SWASH_MEM_PEAK"),
		SwapPeak:  get("SWASH_SWAP_PEAK"),
		OOMKills:  get("SWASH_OOM_KILLS"),
		DiskRead:  get("SWASH_DISK_READ"),
		DiskWrite: get("SWASH_DISK_WRITE"),
		NetRx:     get("SWASH_NET_RX"),
		NetTx:     get("SWASH_NET_TX"),
	}
}

// Line renders a live snapshot as one compact line, leaving out whatever
// is idle, e.g.
//
//	cpu 140% · mem 1.2G/24G · disk w 34M +2.1M/s · net ↓12M +1.1M/s ↑40K · 5 procs (cc1 92%, make 3%)
func (s Stats) Line() string {
	parts := []string{fmt.Sprintf("cpu %.0f%%", s.CPUPercent)}
	mem := "mem " + FormatBytes(s.MemCurrent)
	if s.MemMax > 0 {
		mem += "/" + FormatBytes(s.MemMax)
	}
	if s.Swap >= 1<<20 {
		mem += " +swap " + FormatBytes(s.Swap)
	}
	parts = append(parts, mem)
	if s.OOMKills > 0 {
		parts = append(parts, fmt.Sprintf("OOM-KILLED ×%d", s.OOMKills))
	}
	if d := ioPart("disk", "r", s.DiskRead, s.DiskReadRate, "w", s.DiskWrite, s.DiskWriteRate); d != "" {
		parts = append(parts, d)
	}
	if n := ioPart("net", "↓", s.NetRx, s.NetRxRate, "↑", s.NetTx, s.NetTxRate); n != "" {
		if s.NetConns > 0 {
			n += fmt.Sprintf(" (%d conn)", s.NetConns)
		}
		parts = append(parts, n)
	} else if s.NetConns > 0 {
		parts = append(parts, fmt.Sprintf("net idle (%d conn)", s.NetConns))
	}
	procs := fmt.Sprintf("%d procs", s.Procs)
	if s.Procs == 1 {
		procs = "1 proc"
	}
	var top []string
	for _, p := range s.Top {
		if p.CPUPercent >= 1 {
			top = append(top, fmt.Sprintf("%s %.0f%%", p.Comm, p.CPUPercent))
		}
	}
	if len(top) > 0 {
		procs += " (" + strings.Join(top, ", ") + ")"
	}
	parts = append(parts, procs)
	var stalls []string
	for _, p := range []struct {
		name string
		v    float64
	}{{"cpu", s.CPUPressure}, {"mem", s.MemPressure}, {"io", s.IOPressure}} {
		if p.v >= 5 {
			stalls = append(stalls, fmt.Sprintf("%s %.0f%%", p.name, p.v))
		}
	}
	if len(stalls) > 0 {
		parts = append(parts, "stalled on "+strings.Join(stalls, ", "))
	}
	return strings.Join(parts, " · ")
}

// Summary renders cumulative totals, as recorded when a session exits.
func (s Stats) Summary() string {
	parts := []string{"cpu " + FormatDuration(time.Duration(s.CPUUsec)*time.Microsecond)}
	if s.MemPeak > 0 {
		parts = append(parts, "mem peak "+FormatBytes(s.MemPeak))
	}
	if s.SwapPeak >= 1<<20 {
		parts = append(parts, "swap peak "+FormatBytes(s.SwapPeak))
	}
	if s.OOMKills > 0 {
		parts = append(parts, fmt.Sprintf("OOM-KILLED ×%d", s.OOMKills))
	}
	if d := ioPart("disk", "r", s.DiskRead, 0, "w", s.DiskWrite, 0); d != "" {
		parts = append(parts, d)
	}
	if n := ioPart("net", "↓", s.NetRx, 0, "↑", s.NetTx, 0); n != "" {
		parts = append(parts, n)
	}
	return strings.Join(parts, " · ")
}

func ioPart(name, inLabel string, in uint64, inRate float64, outLabel string, out uint64, outRate float64) string {
	var parts []string
	for _, d := range []struct {
		label string
		total uint64
		rate  float64
	}{{inLabel, in, inRate}, {outLabel, out, outRate}} {
		if d.total == 0 {
			continue
		}
		part := d.label
		if len(d.label) == 1 && d.label[0] < 0x80 {
			part += " "
		}
		part += FormatBytes(d.total)
		if d.rate >= 1024 {
			part += " +" + FormatBytes(uint64(d.rate)) + "/s"
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}
	return name + " " + strings.Join(parts, " ")
}

// FormatBytes renders a byte count compactly: 0, 512B, 12K, 3.4M, 1.2G.
func FormatBytes(n uint64) string {
	const units = "KMGTP"
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	v := float64(n)
	for i := 0; i < len(units); i++ {
		v /= 1024
		if v < 1024 || i == len(units)-1 {
			if v < 10 {
				return fmt.Sprintf("%.1f%c", v, units[i])
			}
			return fmt.Sprintf("%.0f%c", v, units[i])
		}
	}
	return fmt.Sprintf("%dB", n)
}

// FormatDuration renders durations like 850ms, 12s, 3m05s, 2h10m.
func FormatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
