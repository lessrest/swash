package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"swa.sh/internal/backend"
	"swa.sh/internal/host"
	"swa.sh/internal/journal"
)

// Resource flags
var (
	cpusFlag      float64
	memFlag       string
	tasksFlag     uint64
	unlimitedFlag bool
	statsFlag     time.Duration
)

func registerResourceFlags() {
	flag.Float64Var(&cpusFlag, "cpus", 0, "CPU quota in cores, e.g. 4 or 0.5 (default: all but two)")
	flag.StringVar(&memFlag, "mem", "", "Memory cap, e.g. 8G or 25% of RAM (default: throttle at 50%, kill at 75%)")
	flag.Uint64Var(&tasksFlag, "tasks", 0, "Max processes+threads (default 4096)")
	flag.BoolVar(&unlimitedFlag, "unlimited", false, "No resource limits for this session (same as SWASH_LIMITS=off)")
	flag.DurationVar(&statsFlag, "stats", 0, "While waiting, print resource usage at this interval, e.g. 10s")
}

// sessionLimits combines the defaults, SWASH_LIMITS and the flags.
func sessionLimits() backend.Limits {
	if unlimitedFlag {
		return backend.Limits{}
	}
	limits, err := backend.ParseLimits(os.Getenv("SWASH_LIMITS"), backend.DefaultLimits())
	if err != nil {
		fatal("SWASH_LIMITS: %v", err)
	}
	var overrides []string
	if flag.CommandLine.Changed("cpus") {
		overrides = append(overrides, "cpus="+strconv.FormatFloat(cpusFlag, 'g', -1, 64))
	}
	if memFlag != "" {
		overrides = append(overrides, "mem="+memFlag)
	}
	if flag.CommandLine.Changed("tasks") {
		overrides = append(overrides, "tasks="+strconv.FormatUint(tasksFlag, 10))
	}
	limits, err = backend.ParseLimits(strings.Join(overrides, ","), limits)
	if err != nil {
		fatal("%v", err)
	}
	return limits
}

// provenance describes who started a session, for its started event.
func provenance() map[string]string {
	fields := map[string]string{}
	if cwd, err := os.Getwd(); err == nil {
		fields["SWASH_CWD"] = cwd
	}
	if parent := os.Getenv("SWASH_SESSION"); parent != "" {
		fields["SWASH_PARENT"] = parent
	}
	if loginFlag {
		fields["SWASH_LOGIN"] = "1"
	}
	origin := os.Getenv("SWASH_ORIGIN")
	if origin == "" {
		if id := os.Getenv("CLAUDE_CODE_SESSION_ID"); id != "" {
			origin = "claude-code:" + id
		}
	}
	if origin != "" {
		fields["SWASH_ORIGIN"] = origin
	}
	ppid := os.Getppid()
	fields["SWASH_CALLER_PID"] = strconv.Itoa(ppid)
	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", ppid)); err == nil {
		fields["SWASH_CALLER_EXE"] = exe
	}
	if chain := ancestry(ppid); chain != "" {
		fields["SWASH_CALLER_CHAIN"] = chain
	}
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if path, ok := strings.CutPrefix(line, "0::"); ok {
				fields["SWASH_CALLER_CGROUP"] = path
			}
		}
	}
	return fields
}

// ancestry lists process names from pid up toward init, like
// "bash < claude < claude-desktop < bwrap < systemd".
func ancestry(pid int) string {
	var names []string
	for i := 0; pid > 1 && i < 12; i++ {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			break
		}
		open, end := strings.IndexByte(string(data), '('), strings.LastIndexByte(string(data), ')')
		if open < 0 || end < open {
			break
		}
		names = append(names, filepath.Base(string(data[open+1:end])))
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 2 {
			break
		}
		pid, _ = strconv.Atoi(fields[1])
	}
	return strings.Join(names, " < ")
}

// sessionStats fetches live stats from a running session's host.
func sessionStats(sessionID string) (host.Stats, error) {
	client, err := bk.ConnectSession(sessionID)
	if err != nil {
		return host.Stats{}, err
	}
	defer client.Close()
	text, err := client.Stats()
	if err != nil {
		return host.Stats{}, err
	}
	var st host.Stats
	err = json.Unmarshal([]byte(text), &st)
	return st, err
}

// exitRecord finds a session's exited event, if it has one.
func exitRecord(sessionID string) (journal.EventRecord, bool) {
	events, _, err := bk.PollEvents(context.Background(), []backend.EventFilter{
		journal.FilterBySession(sessionID),
		journal.FilterByEvent(journal.EventExited),
	}, "")
	if err != nil || len(events) == 0 {
		return journal.EventRecord{}, false
	}
	return events[len(events)-1], true
}

// exitSummary describes a finished session's totals.
func exitSummary(sessionID string) string {
	rec, ok := exitRecord(sessionID)
	if !ok {
		return ""
	}
	return fmt.Sprintf("exited %s · %s", rec.Fields[journal.FieldExitCode],
		host.StatsFromExitFields(rec.Fields).Summary())
}

// reportStats prints a stats line for the session every interval until
// ctx is done.
func reportStats(ctx context.Context, sessionID string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if st, err := sessionStats(sessionID); err == nil {
				fmt.Fprintf(os.Stderr, "swash: [%s] %s\n",
					host.FormatDuration(time.Duration(st.Elapsed*float64(time.Second))), st.Line())
			}
		}
	}
}

// cmdStats prints one line per session: live usage for running sessions,
// totals for finished ones. With no IDs it covers all running sessions.
func cmdStats(ids []string) {
	initBackend()
	defer bk.Close()

	sessions, err := bk.ListSessions(context.Background())
	if err != nil {
		fatal("listing sessions: %v", err)
	}
	running := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		running[s.ID] = true
	}
	if len(ids) == 0 {
		if len(sessions) == 0 {
			fmt.Println("no sessions")
			return
		}
		for _, s := range sessions {
			ids = append(ids, s.ID)
		}
	}
	for _, id := range ids {
		st, err := sessionStats(id)
		switch {
		case err == nil:
			fmt.Printf("%s  [%s] %s\n", id,
				host.FormatDuration(time.Duration(st.Elapsed*float64(time.Second))), st.Line())
		case running[id]:
			fmt.Printf("%s  running, no stats (%v)\n", id, err)
		default:
			if summary := exitSummary(id); summary != "" {
				fmt.Printf("%s  %s\n", id, summary)
			} else {
				fmt.Printf("%s  not running, no exit recorded (killed, or unknown)\n", id)
			}
		}
	}
}
