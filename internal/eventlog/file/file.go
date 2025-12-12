package file

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mbrock/swash/internal/eventlog"
	"github.com/mbrock/swash/pkg/journalfile"
)

// FileEventLog implements eventlog.EventLog backed by a single *.journal file.
//
// It supports:
// - writing via pkg/journalfile (when opened in create mode)
// - reading/following via pkg/journalfile.Reader (pure Go, works on all platforms)
//
// When opened read-only, Write returns an error.
type FileEventLog struct {
	path string

	mu     sync.Mutex
	writer *journalfile.File
	reader *journalfile.Reader
}

var _ eventlog.EventLog = (*FileEventLog)(nil)

// Create creates a new journal file at path and returns an EventLog that can
// write to it and read/follow from it.
func Create(path string) (eventlog.EventLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating eventlog dir: %w", err)
	}

	var machineID, bootID journalfile.ID128
	_, _ = rand.Read(machineID[:])
	_, _ = rand.Read(bootID[:])

	w, err := journalfile.Create(path, machineID, bootID)
	if err != nil {
		return nil, err
	}

	r, err := journalfile.OpenRead(path)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("opening journal reader: %w", err)
	}

	return &FileEventLog{
		path:   path,
		writer: w,
		reader: r,
	}, nil
}

// Open opens an existing journal file at path for reading/following.
// Write is not supported.
func Open(path string) (eventlog.EventLog, error) {
	r, err := journalfile.OpenRead(path)
	if err != nil {
		return nil, fmt.Errorf("opening journal reader: %w", err)
	}
	return &FileEventLog{
		path:   path,
		reader: r,
	}, nil
}

// Close releases resources.
func (l *FileEventLog) Close() error {
	var firstErr error
	l.mu.Lock()
	w := l.writer
	l.writer = nil
	l.mu.Unlock()

	if w != nil {
		if err := w.Sync(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if l.reader != nil {
		if err := l.reader.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Write appends an entry to the journal file.
func (l *FileEventLog) Write(message string, fields map[string]string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer == nil {
		return fmt.Errorf("eventlog is read-only")
	}

	// Copy fields and always include MESSAGE like journald does.
	entry := make(map[string]string, len(fields)+1)
	for k, v := range fields {
		entry[k] = v
	}
	entry["MESSAGE"] = message

	if err := l.writer.AppendEntry(entry); err != nil {
		return err
	}

	// Sync to make entry visible to readers immediately.
	// This is important for Follow() to work correctly.
	return l.writer.Sync()
}

// Poll reads entries matching filters since cursor.
func (l *FileEventLog) Poll(ctx context.Context, filters []eventlog.EventFilter, cursor string) ([]eventlog.EventRecord, string, error) {
	r := l.reader
	if r == nil {
		return nil, "", fmt.Errorf("journal reader not initialized")
	}

	// Refresh to see latest entries
	if err := r.Refresh(); err != nil {
		return nil, "", fmt.Errorf("refreshing journal: %w", err)
	}

	// Apply matches
	r.FlushMatches()
	for _, f := range filters {
		r.AddMatch(f.Field, f.Value)
	}

	// Seek to position
	if cursor != "" {
		if err := r.SeekCursor(cursor); err != nil {
			r.SeekHead()
		}
	} else {
		r.SeekHead()
	}

	var entries []eventlog.EventRecord
	var lastCursor string

	for {
		entry, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("reading journal: %w", err)
		}

		record := eventlog.EventRecord{
			Cursor:    journalfile.GetCursor(entry),
			Timestamp: entry.Realtime,
			Message:   entry.Fields["MESSAGE"],
			Fields:    entry.Fields,
		}
		entries = append(entries, record)
		lastCursor = record.Cursor
	}

	return entries, lastCursor, nil
}

// Follow returns an iterator over entries matching filters.
func (l *FileEventLog) Follow(ctx context.Context, filters []eventlog.EventFilter) iter.Seq[eventlog.EventRecord] {
	r := l.reader
	return func(yield func(eventlog.EventRecord) bool) {
		if r == nil {
			return
		}

		// Apply matches
		r.FlushMatches()
		for _, f := range filters {
			r.AddMatch(f.Field, f.Value)
		}

		r.SeekHead()

		for {
			entry, err := r.Next()
			if err == io.EOF {
				// No more entries, wait and refresh
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
					// Refresh to pick up new entries
					if err := r.Refresh(); err != nil {
						return
					}
					continue
				}
			}
			if err != nil {
				return
			}

			record := eventlog.EventRecord{
				Cursor:    journalfile.GetCursor(entry),
				Timestamp: entry.Realtime,
				Message:   entry.Fields["MESSAGE"],
				Fields:    entry.Fields,
			}

			if !yield(record) {
				return // Consumer broke out of loop
			}
		}
	}
}
