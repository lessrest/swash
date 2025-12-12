package file

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mbrock/swash/internal/eventlog"
)

func TestFileEventLog_PollCursorAndFilters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.journal")

	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sessionID := "TEST01"

	// Write a couple of semantic events.
	if err := eventlog.EmitStarted(w, sessionID, []string{"echo", "hello"}); err != nil {
		t.Fatalf("EmitStarted: %v", err)
	}
	if err := eventlog.WriteOutput(w, 1, "hello", map[string]string{eventlog.FieldSession: sessionID}); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}
	if err := eventlog.EmitExited(w, sessionID, 0, []string{"echo", "hello"}); err != nil {
		t.Fatalf("EmitExited: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close(writer): %v", err)
	}

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
