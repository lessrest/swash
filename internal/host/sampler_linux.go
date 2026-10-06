package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	sampleInterval = time.Second
	rateWindow     = 5 * time.Second
	clockTicks     = 100 // USER_HZ; fixed at 100 on Linux
)

// Sampler periodically measures the session's resource use. Processes
// come and go, so per-process disk and network counters are remembered by
// identity and summed, which keeps the totals for processes that already
// exited (as of their last sample).
type Sampler struct {
	mu        sync.Mutex
	start     time.Time
	cgroupDir string // "" when the host isn't in its own swash cgroup
	sid       int    // task's process session, used without a cgroup

	disk    map[procKey][2]uint64 // read_bytes, write_bytes
	net     map[uint64][2]uint64  // socket inode -> bytes received, acked
	cpu     map[procKey]uint64    // utime+stime ticks at last sample
	cpuUsec uint64                // without a cgroup: summed process CPU
	history []Stats
}

type procKey struct {
	pid   int
	start uint64 // start time in ticks, guards against pid reuse
}

// NewSampler finds the host's cgroup; sessions started by the systemd
// backend each live in their own swash-host-ID.service cgroup.
func NewSampler() *Sampler {
	s := &Sampler{
		start: time.Now(),
		disk:  make(map[procKey][2]uint64),
		net:   make(map[uint64][2]uint64),
		cpu:   make(map[procKey]uint64),
	}
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if path, ok := strings.CutPrefix(line, "0::"); ok && strings.Contains(path, "/swash-host-") {
				s.cgroupDir = filepath.Join("/sys/fs/cgroup", path)
			}
		}
	}
	return s
}

// SetTaskPID tells the sampler which process session to follow when there
// is no dedicated cgroup.
func (s *Sampler) SetTaskPID(pid int) {
	s.mu.Lock()
	s.sid = pid
	s.mu.Unlock()
}

// Run samples until ctx is done.
func (s *Sampler) Run(ctx context.Context) {
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	s.Sample()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sample()
		}
	}
}

// Latest returns the most recent snapshot, sampling if there is none yet.
func (s *Sampler) Latest() Stats {
	s.mu.Lock()
	n := len(s.history)
	var latest Stats
	if n > 0 {
		latest = s.history[n-1]
	}
	s.mu.Unlock()
	if n == 0 {
		return s.Sample()
	}
	return latest
}

// Sample takes a measurement now.
func (s *Sampler) Sample() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	st := Stats{Time: now, Elapsed: now.Sub(s.start).Seconds()}
	self := os.Getpid()

	var pids []int
	if s.cgroupDir != "" {
		pids = readPids(filepath.Join(s.cgroupDir, "cgroup.procs"), self)
		s.readCgroup(&st)
	} else if s.sid > 0 {
		pids = sessionPids(s.sid)
	}
	st.Procs = len(pids)

	var prev *Stats
	if len(s.history) > 0 {
		prev = &s.history[len(s.history)-1]
	}

	inodes := make(map[uint64]bool)
	cpu := make(map[procKey]uint64, len(pids))
	var rss uint64
	for _, pid := range pids {
		stat, ok := readProcStat(pid)
		if !ok {
			continue
		}
		key := procKey{pid, stat.start}
		ticks := stat.utime + stat.stime
		cpu[key] = ticks
		if s.cgroupDir == "" {
			if last, seen := s.cpu[key]; seen {
				s.cpuUsec += (ticks - last) * 1e6 / clockTicks
			} else {
				s.cpuUsec += ticks * 1e6 / clockTicks
			}
			rss += stat.rss * uint64(os.Getpagesize())
		}
		if prev != nil {
			if last, seen := s.cpu[key]; seen && ticks >= last {
				dt := now.Sub(prev.Time).Seconds()
				pct := float64(ticks-last) / clockTicks / dt * 100
				st.Top = append(st.Top, ProcStats{PID: pid, Comm: stat.comm, CPUPercent: pct})
			}
		}
		if r, w, ok := readProcIO(pid); ok {
			s.disk[key] = [2]uint64{r, w}
		}
		socketInodes(pid, inodes)
	}
	s.cpu = cpu
	sort.Slice(st.Top, func(i, j int) bool { return st.Top[i].CPUPercent > st.Top[j].CPUPercent })
	if len(st.Top) > 3 {
		st.Top = st.Top[:3]
	}
	if s.cgroupDir == "" {
		st.CPUUsec = s.cpuUsec
		st.MemCurrent = rss
	}

	if len(inodes) > 0 {
		for ino, counts := range tcpCounters(inodes) {
			s.net[ino] = counts
		}
		for ino := range inodes {
			if _, ok := s.net[ino]; ok {
				st.NetConns++
			}
		}
	}
	for _, d := range s.disk {
		st.DiskRead += d[0]
		st.DiskWrite += d[1]
	}
	for _, n := range s.net {
		st.NetRx += n[0]
		st.NetTx += n[1]
	}

	s.history = append(s.history, st)
	cutoff := now.Add(-rateWindow)
	for len(s.history) > 2 && s.history[1].Time.Before(cutoff) {
		s.history = s.history[1:]
	}
	if base := s.history[0]; len(s.history) > 1 {
		dt := now.Sub(base.Time).Seconds()
		rate := func(cur, old uint64) float64 {
			if cur < old {
				return 0
			}
			return float64(cur-old) / dt
		}
		st.CPUPercent = rate(st.CPUUsec, base.CPUUsec) / 1e4
		st.DiskReadRate = rate(st.DiskRead, base.DiskRead)
		st.DiskWriteRate = rate(st.DiskWrite, base.DiskWrite)
		st.NetRxRate = rate(st.NetRx, base.NetRx)
		st.NetTxRate = rate(st.NetTx, base.NetTx)
		s.history[len(s.history)-1] = st
	}
	return st
}

func (s *Sampler) readCgroup(st *Stats) {
	file := func(name string) string {
		data, _ := os.ReadFile(filepath.Join(s.cgroupDir, name))
		return strings.TrimSpace(string(data))
	}
	number := func(name string) uint64 {
		v, _ := strconv.ParseUint(file(name), 10, 64)
		return v
	}
	keyed := func(name, key string) string {
		for _, line := range strings.Split(file(name), "\n") {
			if k, v, ok := strings.Cut(line, " "); ok && k == key {
				return v
			}
		}
		return ""
	}
	st.CPUUsec, _ = strconv.ParseUint(keyed("cpu.stat", "usage_usec"), 10, 64)
	st.MemCurrent = number("memory.current")
	st.MemPeak = number("memory.peak")
	st.MemMax = number("memory.max") // "max" parses as 0, meaning unlimited
	st.Swap = number("memory.swap.current")
	st.SwapPeak = number("memory.swap.peak")
	st.OOMKills, _ = strconv.ParseUint(keyed("memory.events", "oom_kill"), 10, 64)
	if quota, period, ok := strings.Cut(file("cpu.max"), " "); ok && quota != "max" {
		q, _ := strconv.ParseFloat(quota, 64)
		p, _ := strconv.ParseFloat(period, 64)
		if p > 0 {
			st.CPULimit = q / p * 100
		}
	}
	st.CPUPressure = psiSome(file("cpu.pressure"))
	st.MemPressure = psiSome(file("memory.pressure"))
	st.IOPressure = psiSome(file("io.pressure"))
}

// psiSome extracts avg10 from the "some" line of a pressure file.
func psiSome(text string) float64 {
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, "some "); ok {
			for _, field := range strings.Fields(rest) {
				if v, ok := strings.CutPrefix(field, "avg10="); ok {
					f, _ := strconv.ParseFloat(v, 64)
					return f
				}
			}
		}
	}
	return 0
}

func readPids(path string, exclude int) []int {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		if pid, err := strconv.Atoi(field); err == nil && pid != exclude {
			pids = append(pids, pid)
		}
	}
	return pids
}

// sessionPids lists the processes in process session sid.
func sessionPids(sid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if stat, ok := readProcStat(pid); ok && stat.session == sid {
			pids = append(pids, pid)
		}
	}
	return pids
}

type procStat struct {
	comm         string
	session      int
	utime, stime uint64
	start        uint64
	rss          uint64 // pages
}

func readProcStat(pid int) (procStat, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, false
	}
	// comm is parenthesized and may contain spaces or parens itself.
	open, end := bytes.IndexByte(data, '('), bytes.LastIndexByte(data, ')')
	if open < 0 || end < open {
		return procStat{}, false
	}
	fields := strings.Fields(string(data[end+1:]))
	// fields[0] is field 3 (state) in proc(5) numbering.
	if len(fields) < 22 {
		return procStat{}, false
	}
	num := func(field int) uint64 {
		v, _ := strconv.ParseUint(fields[field-3], 10, 64)
		return v
	}
	return procStat{
		comm:    string(data[open+1 : end]),
		session: int(num(6)),
		utime:   num(14),
		stime:   num(15),
		start:   num(22),
		rss:     num(24),
	}, true
}

func readProcIO(pid int) (read, write uint64, ok bool) {
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/io")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, _ := strings.Cut(scanner.Text(), ": ")
		v, _ := strconv.ParseUint(value, 10, 64)
		switch key {
		case "read_bytes":
			read = v
		case "write_bytes":
			write = v
		}
	}
	return read, write, true
}

func socketInodes(pid int, into map[uint64]bool) {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if rest, ok := strings.CutPrefix(target, "socket:["); ok {
			if ino, err := strconv.ParseUint(strings.TrimSuffix(rest, "]"), 10, 64); err == nil {
				into[ino] = true
			}
		}
	}
}

// inet_diag_req_v2 from linux/inet_diag.h.
type inetDiagReqV2 struct {
	Family   uint8
	Protocol uint8
	Ext      uint8
	Pad      uint8
	States   uint32
	ID       [48]byte // inet_diag_sockid, zero = match all
}

const (
	sizeofInetDiagMsg = 72 // inet_diag_msg
	inetDiagInfo      = 2  // INET_DIAG_INFO attribute
)

// tcpCounters asks the kernel (sock_diag) for byte counters of the TCP
// sockets among the given inodes: bytes received and bytes acked by the
// peer. This needs no privileges for the caller's own sockets.
func tcpCounters(want map[uint64]bool) map[uint64][2]uint64 {
	result := make(map[uint64][2]uint64)
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return result
	}
	defer unix.Close(fd)
	for _, family := range []uint8{unix.AF_INET, unix.AF_INET6} {
		req := inetDiagReqV2{
			Family:   family,
			Protocol: unix.IPPROTO_TCP,
			Ext:      1 << (inetDiagInfo - 1),
			States:   0xffffffff,
		}
		hdr := unix.NlMsghdr{
			Len:   uint32(unix.SizeofNlMsghdr + unsafe.Sizeof(req)),
			Type:  unix.SOCK_DIAG_BY_FAMILY,
			Flags: unix.NLM_F_REQUEST | unix.NLM_F_DUMP,
		}
		msg := make([]byte, 0, hdr.Len)
		msg = append(msg, (*[unix.SizeofNlMsghdr]byte)(unsafe.Pointer(&hdr))[:]...)
		msg = append(msg, (*[unsafe.Sizeof(req)]byte)(unsafe.Pointer(&req))[:]...)
		if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
			continue
		}
		readDiagDump(fd, want, result)
	}
	return result
}

func readDiagDump(fd int, want map[uint64]bool, result map[uint64][2]uint64) {
	buf := make([]byte, 1<<16)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil || n == 0 {
			return
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return
		}
		for _, m := range msgs {
			if m.Header.Type == unix.NLMSG_DONE || m.Header.Type == unix.NLMSG_ERROR {
				return
			}
			if len(m.Data) < sizeofInetDiagMsg {
				continue
			}
			inode := uint64(binary.NativeEndian.Uint32(m.Data[68:72]))
			if !want[inode] {
				continue
			}
			for _, a := range parseDiagAttrs(m.Data[sizeofInetDiagMsg:]) {
				if a.Attr.Type != inetDiagInfo {
					continue
				}
				var info unix.TCPInfo
				copy((*[unsafe.Sizeof(info)]byte)(unsafe.Pointer(&info))[:], a.Value)
				result[inode] = [2]uint64{info.Bytes_received, info.Bytes_acked}
			}
		}
	}
}

// parseDiagAttrs walks rtattr-style attributes directly.
func parseDiagAttrs(b []byte) []syscall.NetlinkRouteAttr {
	var attrs []syscall.NetlinkRouteAttr
	for len(b) >= unix.SizeofRtAttr {
		l := int(binary.NativeEndian.Uint16(b[0:2]))
		t := binary.NativeEndian.Uint16(b[2:4])
		if l < unix.SizeofRtAttr || l > len(b) {
			break
		}
		attrs = append(attrs, syscall.NetlinkRouteAttr{
			Attr:  syscall.RtAttr{Len: uint16(l), Type: t},
			Value: b[unix.SizeofRtAttr:l],
		})
		b = b[(l+unix.RTA_ALIGNTO-1) & ^(unix.RTA_ALIGNTO-1):]
	}
	return attrs
}
