package systemd

import (
	"context"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	backendpkg "swa.sh/internal/backend"
	"swa.sh/internal/journal"
)

type fakeSystemd struct {
	mu         sync.Mutex
	units      []Unit
	listStates []UnitState
	started    TransientSpec
	stopped    UnitName
	killed     UnitName
	killSignal syscall.Signal
}

func (f *fakeSystemd) ListUnits(_ context.Context, _ []UnitName, states []UnitState) ([]Unit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listStates = append([]UnitState(nil), states...)
	return append([]Unit(nil), f.units...), nil
}

func (f *fakeSystemd) setUnits(units []Unit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.units = units
}

func (f *fakeSystemd) StopUnit(_ context.Context, name UnitName) error {
	f.stopped = name
	return nil
}

func (f *fakeSystemd) KillUnit(_ context.Context, name UnitName, signal syscall.Signal) error {
	f.killed = name
	f.killSignal = signal
	return nil
}

func (f *fakeSystemd) StartTransient(_ context.Context, spec TransientSpec) error {
	f.started = spec
	return nil
}

func (f *fakeSystemd) Close() error { return nil }

func TestProcessManagerStartsNotifyServiceInSharedSlice(t *testing.T) {
	t.Setenv("SWASH_ROOT_SLICE", "swashtest")
	systemd := &fakeSystemd{}
	manager := NewProcessManager(systemd)

	err := manager.Start(context.Background(), ProcessSpec{
		SessionID: "ABC123",
		Command:   []string{"/bin/swash", "host"},
		BusName:   "sh.swa.Swash.ABC123",
		Collect:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := systemd.started.Unit, UnitName("swash-host-ABC123.service"); got != want {
		t.Fatalf("unit = %q, want %q", got, want)
	}
	if got, want := systemd.started.Slice, SliceName("swashtest.slice"); got != want {
		t.Fatalf("slice = %q, want %q", got, want)
	}
	if systemd.started.ServiceType != "notify" || systemd.started.KillMode != "mixed" {
		t.Fatalf("service policy = Type:%q KillMode:%q", systemd.started.ServiceType, systemd.started.KillMode)
	}
	if systemd.started.TimeoutStop != 5*time.Second {
		t.Fatalf("stop timeout = %v", systemd.started.TimeoutStop)
	}
}

func TestProcessManagerUsesSystemdForHardKill(t *testing.T) {
	systemd := &fakeSystemd{}
	manager := NewProcessManager(systemd)

	if err := manager.Kill(context.Background(), "ABC123", syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if systemd.killed != HostUnit("ABC123") || systemd.killSignal != syscall.SIGKILL {
		t.Fatalf("kill = %q signal %v", systemd.killed, systemd.killSignal)
	}
}

func TestProcessManagerListsOnlyConfiguredRootSlice(t *testing.T) {
	t.Setenv("SWASH_ROOT_SLICE", "swash")
	systemd := &fakeSystemd{units: []Unit{
		{Name: HostUnit("KEEP01"), State: UnitStateActive, Slice: "swash.slice"},
		{Name: HostUnit("SKIP01"), State: UnitStateActive, Slice: "swashtest.slice"},
	}}
	manager := NewProcessManager(systemd)

	statuses, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].SessionID != "KEEP01" {
		t.Fatalf("statuses = %#v", statuses)
	}
	if !slices.Contains(systemd.listStates, UnitStateDeactivating) {
		t.Fatalf("list states = %#v, want deactivating", systemd.listStates)
	}
}

func TestFollowSessionReturnsWhenHostDisappears(t *testing.T) {
	events := journal.NewFakeJournal()
	if err := journal.EmitStarted(events, "KILL01", []string{"sleep", "60"}, nil); err != nil {
		t.Fatal(err)
	}
	systemd := &fakeSystemd{units: []Unit{
		{Name: HostUnit("KILL01"), State: UnitStateActive, Slice: "swash.slice"},
	}}
	bk := &SystemdBackend{
		processes: NewProcessManager(systemd),
		events:    events,
	}

	result := make(chan backendpkg.FollowResult, 1)
	go func() {
		_, followResult := bk.FollowSession(context.Background(), "KILL01", 0, 0)
		result <- followResult
	}()
	time.Sleep(150 * time.Millisecond)
	systemd.setUnits(nil)

	select {
	case got := <-result:
		if got != backendpkg.FollowKilled {
			t.Fatalf("follow result = %v, want killed", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follow remained blocked after host disappeared")
	}
}

func TestListHistoryReportsStartedSessionMissingItsHostAsKilled(t *testing.T) {
	events := journal.NewFakeJournal()
	if err := journal.EmitStarted(events, "KILL01", []string{"sleep", "60"}, nil); err != nil {
		t.Fatal(err)
	}
	backend := &SystemdBackend{
		processes: NewProcessManager(&fakeSystemd{}),
		events:    events,
	}

	history, err := backend.ListHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != "KILL01" || history[0].Status != "killed" {
		t.Fatalf("history = %#v", history)
	}
}

func TestListHistoryOmitsRunningSession(t *testing.T) {
	events := journal.NewFakeJournal()
	if err := journal.EmitStarted(events, "LIVE01", []string{"sleep", "60"}, nil); err != nil {
		t.Fatal(err)
	}
	backend := &SystemdBackend{
		processes: NewProcessManager(&fakeSystemd{units: []Unit{
			{Name: HostUnit("LIVE01"), State: UnitStateActive, Slice: "swash.slice"},
		}}),
		events: events,
	}

	history, err := backend.ListHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("history = %#v", history)
	}
}

func TestListHistoryIgnoresExitFromBeforeLatestRestart(t *testing.T) {
	events := journal.NewFakeJournal()
	firstStart := time.Now().Add(-3 * time.Second)
	firstExit := firstStart.Add(time.Second)
	restart := firstExit.Add(time.Second)
	events.AddEntry(journal.EventRecord{Timestamp: firstStart, Fields: map[string]string{
		journal.FieldSession: "RETRY1", journal.FieldEvent: journal.EventStarted, journal.FieldCommand: "attempt one",
	}})
	events.AddEntry(journal.EventRecord{Timestamp: firstExit, Fields: map[string]string{
		journal.FieldSession: "RETRY1", journal.FieldEvent: journal.EventExited, journal.FieldExitCode: "0",
	}})
	events.AddEntry(journal.EventRecord{Timestamp: restart, Fields: map[string]string{
		journal.FieldSession: "RETRY1", journal.FieldEvent: journal.EventStarted, journal.FieldCommand: "attempt two",
	}})
	backend := &SystemdBackend{
		processes: NewProcessManager(&fakeSystemd{}),
		events:    events,
	}

	history, err := backend.ListHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Status != "killed" || history[0].Command != "attempt two" || history[0].ExitCode != nil {
		t.Fatalf("history = %#v", history)
	}
}
