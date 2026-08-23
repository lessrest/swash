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

func TestLifecycleEventsCarrySessionTags(t *testing.T) {
	log := NewFakeJournal()
	tags := map[string]string{
		FieldSession: "WRONG",
		FieldEvent:   "wrong",
		"LUV_KIND":   "LISP",
		"LUV_ROOT":   "/work/luv",
	}
	if err := EmitStarted(log, "LISP01", []string{"sbcl", "--load", "server.lisp"}, tags); err != nil {
		t.Fatal(err)
	}
	if err := EmitExited(log, "LISP01", 7, []string{"sbcl", "--load", "server.lisp"}, tags); err != nil {
		t.Fatal(err)
	}

	entries := log.Entries()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.Fields[FieldSession] != "LISP01" {
			t.Fatalf("session = %q", entry.Fields[FieldSession])
		}
		if entry.Fields["LUV_KIND"] != "LISP" || entry.Fields["LUV_ROOT"] != "/work/luv" {
			t.Fatalf("lifecycle tags = %#v", entry.Fields)
		}
	}
	if entries[0].Fields[FieldEvent] != EventStarted {
		t.Fatalf("started event = %q", entries[0].Fields[FieldEvent])
	}
	if entries[1].Fields[FieldEvent] != EventExited || entries[1].Fields[FieldExitCode] != "7" {
		t.Fatalf("exited fields = %#v", entries[1].Fields)
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
