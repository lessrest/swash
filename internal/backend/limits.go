package backend

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Limits caps the resources a session may use. Zero fields mean "no limit".
type Limits struct {
	CPUs       float64 // CPU time quota in cores (2.5 = 250%)
	CPUWeight  uint64  // relative CPU priority (systemd default is 100)
	MemoryHigh uint64  // bytes; above this the kernel throttles and reclaims
	MemoryMax  uint64  // bytes; above this the session's processes are OOM-killed
	SwapMax    uint64  // bytes of swap the session may use
	NoSwap     bool    // forbid swap entirely (SwapMax 0 means unlimited)
	TasksMax   uint64  // max processes+threads
}

// IsZero reports whether no limit is set.
func (l Limits) IsZero() bool { return l == Limits{} }

// DefaultLimits keeps one session from taking over the machine while
// leaving ordinary heavy work (big builds, test suites) unaffected:
// all cores but two, half priority under contention, memory throttled at
// half of RAM and killed at three quarters (with at most half the swap
// space on top), and a fork-bomb cap.
func DefaultLimits() Limits {
	l := Limits{CPUWeight: 50, TasksMax: 4096}
	if n := runtime.NumCPU(); n > 2 {
		l.CPUs = float64(n - 2)
	}
	if total := meminfo("MemTotal:"); total > 0 {
		l.MemoryHigh = total / 2
		l.MemoryMax = total / 4 * 3
	}
	l.SwapMax = meminfo("SwapTotal:") / 2
	return l
}

// SliceLimits is the looser backstop applied to all swash sessions
// together, so many sessions can't add up to more than the machine.
func SliceLimits() Limits {
	var l Limits
	if n := runtime.NumCPU(); n > 1 {
		l.CPUs = float64(n - 1)
	}
	if total := meminfo("MemTotal:"); total > 0 {
		l.MemoryMax = total / 10 * 9
	}
	return l
}

// ParseLimits applies a comma-separated spec like "cpus=4,mem=8G,tasks=512"
// on top of base. "off", "none" and "unlimited" clear every limit; a key
// set to "off" clears that one. mem accepts sizes (512M, 8G) or a share of
// RAM (25%), and also sets MemoryHigh to 90% of it unless "high" is given,
// and forbids swap unless "swap" is given, so the cap really caps.
// swap=0 forbids swapping; swap=off lifts the swap limit.
func ParseLimits(spec string, base Limits) (Limits, error) {
	l := base
	spec = strings.TrimSpace(spec)
	switch spec {
	case "":
		return l, nil
	case "off", "none", "unlimited":
		return Limits{}, nil
	}
	highSet, swapSet := false, false
	for _, part := range strings.Split(spec, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return l, fmt.Errorf("limit %q: want key=value", part)
		}
		off := value == "off" || value == "none" || value == "0"
		var err error
		switch key {
		case "cpus", "cpu":
			if off {
				l.CPUs = 0
			} else if l.CPUs, err = strconv.ParseFloat(value, 64); err == nil && l.CPUs <= 0 {
				err = fmt.Errorf("must be positive")
			}
		case "weight":
			if off {
				l.CPUWeight = 0
			} else {
				l.CPUWeight, err = strconv.ParseUint(value, 10, 64)
			}
		case "mem", "memory":
			if off {
				l.MemoryMax, l.MemoryHigh = 0, 0
			} else if l.MemoryMax, err = ParseSize(value); err == nil {
				if !highSet {
					l.MemoryHigh = l.MemoryMax / 10 * 9
				}
				if !swapSet {
					l.SwapMax, l.NoSwap = 0, true
				}
			}
		case "swap":
			swapSet = true
			switch value {
			case "off", "none":
				l.SwapMax, l.NoSwap = 0, false
			case "0":
				l.SwapMax, l.NoSwap = 0, true
			default:
				l.NoSwap = false
				l.SwapMax, err = ParseSize(value)
			}
		case "high":
			highSet = true
			if off {
				l.MemoryHigh = 0
			} else {
				l.MemoryHigh, err = ParseSize(value)
			}
		case "tasks":
			if off {
				l.TasksMax = 0
			} else {
				l.TasksMax, err = strconv.ParseUint(value, 10, 64)
			}
		default:
			return l, fmt.Errorf("unknown limit %q (want cpus, weight, mem, high, swap, tasks)", key)
		}
		if err != nil {
			return l, fmt.Errorf("limit %s=%s: %w", key, value, err)
		}
	}
	return l, nil
}

// ParseSize parses byte sizes like 512M, 8G, 1.5T (powers of 1024), plain
// byte counts, or a percentage of physical memory like 25%.
func ParseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if pct, ok := strings.CutSuffix(s, "%"); ok {
		p, err := strconv.ParseFloat(pct, 64)
		if err != nil || p <= 0 || p > 100 {
			return 0, fmt.Errorf("bad percentage %q", s)
		}
		total := meminfo("MemTotal:")
		if total == 0 {
			return 0, fmt.Errorf("can't read total memory for %q", s)
		}
		return uint64(float64(total) * p / 100), nil
	}
	mult := 1.0
	if n := len(s); n > 0 {
		switch strings.ToUpper(s[n-1:]) {
		case "K":
			mult = 1 << 10
		case "M":
			mult = 1 << 20
		case "G":
			mult = 1 << 30
		case "T":
			mult = 1 << 40
		}
		if mult != 1 {
			s = s[:n-1]
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return uint64(v * mult), nil
}

// meminfo reads a /proc/meminfo field in bytes.
func meminfo(field string) uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == field {
			kb, _ := strconv.ParseUint(fields[1], 10, 64)
			return kb * 1024
		}
	}
	return 0
}
