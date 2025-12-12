package file

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/mbrock/swash/internal/eventlog"
	"github.com/mbrock/swash/pkg/journalfile"
)

func TestFileEventLog_PollCursorAndFilters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.journal")

	// Create journal file directly for test setup
	var machineID, bootID journalfile.ID128
	rand.Read(machineID[:])
	rand.Read(bootID[:])
	jf, err := journalfile.Create(path, machineID, bootID)
	if err != nil {
		t.Fatalf("journalfile.Create: %v", err)
	}

	sessionID := "TEST01"

	// Write test entries directly to journal file
	jf.AppendEntry(map[string]string{
		"MESSAGE":             "Session started",
		eventlog.FieldSession: sessionID,
		eventlog.FieldEvent:   eventlog.EventStarted,
	})
	jf.AppendEntry(map[string]string{
		"MESSAGE":             "hello",
		eventlog.FieldSession: sessionID,
		"FD":                  "1",
	})
	jf.AppendEntry(map[string]string{
		"MESSAGE":              "Session exited",
		eventlog.FieldSession:  sessionID,
		eventlog.FieldEvent:    eventlog.EventExited,
		eventlog.FieldExitCode: "0",
	})
	jf.Sync()
	jf.Close()

	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Poll by session
	recs, cursor, err := r.Poll(ctx, []eventlog.EventFilter{eventlog.FilterBySession(sessionID)}, "")
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(recs) < 3 {
		t.Fatalf("expected >= 3 records, got %d", len(recs))
	}
	if cursor == "" {
		t.Fatalf("expected non-empty cursor from Poll")
	}

	// Poll again from cursor should return no new entries.
	recs2, _, err := r.Poll(ctx, []eventlog.EventFilter{eventlog.FilterBySession(sessionID)}, cursor)
	if err != nil {
		t.Fatalf("Poll(cursor): %v", err)
	}
	if len(recs2) != 0 {
		t.Fatalf("expected 0 new records after cursor, got %d", len(recs2))
	}

	// Poll by exited event.
	exited, _, err := r.Poll(ctx, []eventlog.EventFilter{
		eventlog.FilterByEvent(eventlog.EventExited),
		eventlog.FilterBySession(sessionID),
	}, "")
	if err != nil {
		t.Fatalf("Poll(exited): %v", err)
	}
	if len(exited) != 1 {
		t.Fatalf("expected 1 exited record, got %d", len(exited))
	}
	if exited[0].Fields[eventlog.FieldExitCode] != "0" {
		t.Fatalf("expected exit code field 0, got %q", exited[0].Fields[eventlog.FieldExitCode])
	}
}
