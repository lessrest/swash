package journal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestSQLiteLogPersistsAndFiltersAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	writer, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if err := writer.Write("started", map[string]string{FieldSession: "one", FieldEvent: EventStarted}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteSync("exited", map[string]string{FieldSession: "one", FieldEvent: EventExited}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Write("other", map[string]string{FieldSession: "two", FieldEvent: EventStarted}); err != nil {
		t.Fatal(err)
	}

	records, cursor, err := reader.Poll(context.Background(), []EventFilter{
		FilterBySession("one"),
		FilterByEvent(EventStarted),
		FilterByEvent(EventExited),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Message != "started" || records[1].Message != "exited" {
		t.Fatalf("unexpected records: %#v", records)
	}
	if records[0].Fields["MESSAGE"] != "started" {
		t.Fatalf("MESSAGE field not persisted: %#v", records[0].Fields)
	}

	records, next, err := reader.Poll(context.Background(), nil, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Message != "other" || next == cursor {
		t.Fatalf("cursor did not advance: records=%#v cursor=%q next=%q", records, cursor, next)
	}
}

func TestSQLiteLogUsesWALAndFollowsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	log, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var mode string
	if err := log.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal mode = %q, want wal", mode)
	}
	var synchronous int
	if err := log.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 {
		t.Fatalf("synchronous = %d, want FULL (2)", synchronous)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database permissions = %o, want 600", info.Mode().Perm())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	received := make(chan EventRecord, 1)
	go func() {
		for record := range log.Follow(ctx, []EventFilter{FilterBySession("one")}, "") {
			received <- record
			return
		}
	}()

	if err := log.Write("hello", map[string]string{FieldSession: "one"}); err != nil {
		t.Fatal(err)
	}
	select {
	case record := <-received:
		if record.Message != "hello" {
			t.Fatalf("message = %q", record.Message)
		}
	case <-ctx.Done():
		t.Fatal("follow did not observe write")
	}
	walInfo, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if walInfo.Mode().Perm() != 0o600 {
		t.Fatalf("WAL permissions = %o, want 600", walInfo.Mode().Perm())
	}
}

func TestSQLiteLogConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	initial, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	initial.Close()

	const writers = 8
	const entriesPerWriter = 25
	const totalEntries = writers * entriesPerWriter
	reader, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	followCtx, cancelFollow := context.WithCancel(context.Background())
	defer cancelFollow()
	followed := make(chan EventRecord, totalEntries)
	go func() {
		for record := range reader.Follow(followCtx, nil, "") {
			followed <- record
		}
	}()

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for writer := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log, err := OpenSQLite(path)
			if err != nil {
				errs <- err
				return
			}
			defer log.Close()
			for entry := range entriesPerWriter {
				if err := log.Write(fmt.Sprintf("%d-%d", writer, entry), map[string]string{"WRITER": fmt.Sprint(writer)}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	seenCursors := make(map[string]bool, totalEntries)
	var lastCursor int64
	for range totalEntries {
		select {
		case record := <-followed:
			if seenCursors[record.Cursor] {
				t.Fatalf("duplicate followed cursor %q", record.Cursor)
			}
			seenCursors[record.Cursor] = true
			cursor, err := strconv.ParseInt(record.Cursor, 10, 64)
			if err != nil || cursor <= lastCursor {
				t.Fatalf("followed cursor %q after %d", record.Cursor, lastCursor)
			}
			lastCursor = cursor
		case <-time.After(2 * time.Second):
			t.Fatalf("follow received %d of %d records", len(seenCursors), totalEntries)
		}
	}
	cancelFollow()

	records, _, err := reader.Poll(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != totalEntries {
		t.Fatalf("record count = %d, want %d", len(records), totalEntries)
	}
}

func TestSQLiteLogRetriesBusyWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	blocker, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	writer, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	if _, err := blocker.db.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- writer.Write("after lock", nil) }()

	select {
	case err := <-done:
		t.Fatalf("write completed while lock was held: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	if _, err := blocker.db.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write did not complete after lock release")
	}
}
