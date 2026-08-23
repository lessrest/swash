package journal

import (
	"context"
	"testing"
)

func TestEmitSessionEventProtectsIdentityFields(t *testing.T) {
	log := NewFakeJournal()
	err := EmitSessionEvent(log, "LISP01", "slynk-ready", "Slynk is ready", map[string]string{
		FieldSession: "WRONG",
		FieldEvent:   "wrong",
		"LUV_ROOT":   "/work/luv",
	})
	if err != nil {
		t.Fatal(err)
	}

	entries := log.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Message != "Slynk is ready" {
		t.Fatalf("message = %q", entry.Message)
	}
	if entry.Fields[FieldSession] != "LISP01" {
		t.Fatalf("session = %q", entry.Fields[FieldSession])
	}
	if entry.Fields[FieldEvent] != "slynk-ready" {
		t.Fatalf("event = %q", entry.Fields[FieldEvent])
	}
	if entry.Fields["LUV_ROOT"] != "/work/luv" {
		t.Fatalf("LUV_ROOT = %q", entry.Fields["LUV_ROOT"])
	}
}

func TestFakeJournalFollowStartsAfterCursor(t *testing.T) {
	log := NewFakeJournal()
	log.AddEntry(EventRecord{Message: "first", Fields: map[string]string{FieldSession: "LISP01"}})
	log.AddEntry(EventRecord{Message: "second", Fields: map[string]string{FieldSession: "LISP01"}})
	entries := log.Entries()

	var got []string
	log.Follow(context.Background(), []EventFilter{FilterBySession("LISP01")}, entries[0].Cursor)(func(entry EventRecord) bool {
		got = append(got, entry.Message)
		return false
	})
	if len(got) != 1 || got[0] != "second" {
		t.Fatalf("follow after cursor = %v, want [second]", got)
	}
}
