package file

import (
	"context"
	"crypto/rand"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"
	"github.com/mbrock/swash/internal/eventlog"
	"github.com/mbrock/swash/pkg/journalfile"
)

// FileEventLog implements eventlog.EventLog backed by a single *.journal file.
//
// It supports:
// - writing via pkg/journalfile (when opened in create mode)
// - reading/following via sdjournal.NewJournalFromFiles
//
// When opened read-only, Write returns an error.
type FileEventLog struct {
	path string

	mu     sync.Mutex
	writer *journalfile.File
	reader *sdjournal.Journal
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

	r, err := sdjournal.NewJournalFromFiles(path)
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
	r, err := sdjournal.NewJournalFromFiles(path)
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

	return l.writer.AppendEntry(entry)
}

// Poll reads entries matching filters since cursor.
func (l *FileEventLog) Poll(ctx context.Context, filters []eventlog.EventFilter, cursor string) ([]eventlog.EventRecord, string, error) {
	j := l.reader
	if j == nil {
		return nil, "", fmt.Errorf("journal reader not initialized")
	}

	// Apply matches
	j.FlushMatches()
	for _, f := range filters {
		if err := j.AddMatch(f.Field + "=" + f.Value); err != nil {
			return nil, "", fmt.Errorf("adding match %s=%s: %w", f.Field, f.Value, err)
		}
	}

	// Seek to position
	if cursor != "" {
		if err := j.SeekCursor(cursor); err == nil {
			_, _ = j.Next() // Skip the cursor entry itself
		} else {
			_ = j.SeekHead()
		}
	} else {
		_ = j.SeekHead()
	}

	var entries []eventlog.EventRecord
	var lastCursor string

	for {
		n, err := j.Next()
		if err != nil {
			return nil, "", fmt.Errorf("reading journal: %w", err)
		}
		if n == 0 {
			break
		}

		entry, err := parseEntry(j)
		if err != nil {
			continue
		}
		entries = append(entries, entry)
		lastCursor = entry.Cursor
	}

	return entries, lastCursor, nil
}

// Follow returns an iterator over entries matching filters.
func (l *FileEventLog) Follow(ctx context.Context, filters []eventlog.EventFilter) iter.Seq[eventlog.EventRecord] {
	j := l.reader
	return func(yield func(eventlog.EventRecord) bool) {
		if j == nil {
			return
		}

		// Apply matches
		j.FlushMatches()
		for _, f := range filters {
			if err := j.AddMatch(f.Field + "=" + f.Value); err != nil {
				return // Can't report error from iterator, just stop
			}
		}

		_ = j.SeekHead()

		for {
			n, err := j.Next()
			if err != nil {
				return
			}

			if n == 0 {
				// No entries available, wait for new ones (cancellable)
				waitCh := make(chan struct{})
				go func() {
					j.Wait(5 * time.Second)
					close(waitCh)
				}()

				select {
				case <-ctx.Done():
					return
				case <-waitCh:
					continue
				}
			}

			entry, err := parseEntry(j)
			if err != nil {
				continue
			}

			if !yield(entry) {
				return // Consumer broke out of loop
			}
		}
	}
}

func parseEntry(j *sdjournal.Journal) (eventlog.EventRecord, error) {
	raw, err := j.GetEntry()
	if err != nil {
		return eventlog.EventRecord{}, err
	}

	cursor, _ := j.GetCursor()

	return eventlog.EventRecord{
		Cursor:    cursor,
		Timestamp: time.Unix(int64(raw.RealtimeTimestamp/1000000), int64((raw.RealtimeTimestamp%1000000)*1000)),
		Message:   raw.Fields["MESSAGE"],
		Fields:    raw.Fields,
	}, nil
}
