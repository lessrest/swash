package journal

import "testing"

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
