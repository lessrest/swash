package file

import (
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/mbrock/swash/internal/eventlog"
	"github.com/mbrock/swash/pkg/journalfile"
)

// FileEventLog implements eventlog.EventLog backed by a single *.journal file.
// It provides read-only access via pkg/journalfile.Reader (pure Go, works on all platforms).
// Write operations return an error.
type FileEventLog struct {
	path string

	mu     sync.Mutex
	writer *journalfile.File
	reader *journalfile.Reader
}

var _ eventlog.EventLog = (*FileEventLog)(nil)

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
	return l.writeEntry(message, fields)
}

// WriteSync appends an entry to the journal file (same as Write for file-based storage).
func (l *FileEventLog) WriteSync(message string, fields map[string]string) error {
	return l.writeEntry(message, fields)
}

func (l *FileEventLog) writeEntry(message string, fields map[string]string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer == nil {
		return fmt.Errorf("eventlog is read-only")
	}

	// Copy fields and always include MESSAGE like journald does.
	entry := make(map[string]string, len(fields)+1)
	maps.Copy(entry, fields)
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
	return FollowReader(ctx, l.reader, l.path, filters)
}

// FollowReader returns an iterator over entries matching filters using the given reader.
// This is exported so it can be reused by other packages (e.g., socket eventlog).
// If reader is nil, it will be opened from journalPath on first use.
func FollowReader(ctx context.Context, r *journalfile.Reader, journalPath string, filters []eventlog.EventFilter) iter.Seq[eventlog.EventRecord] {
	return func(yield func(eventlog.EventRecord) bool) {
		slog.Debug("FollowReader starting", "journalPath", journalPath, "filters", len(filters), "readerNil", r == nil)

		// If reader is nil, try to open it
		if r == nil {
			var err error
			r, err = journalfile.OpenRead(journalPath)
			if err != nil {
				slog.Debug("FollowReader failed to open journal", "error", err)
				return
			}
			defer r.Close()
			slog.Debug("FollowReader opened journal", "entries", r.NEntries())
		}

		// Apply matches
		r.FlushMatches()
		for _, f := range filters {
			r.AddMatch(f.Field, f.Value)
			slog.Debug("FollowReader added match", "field", f.Field, "value", f.Value)
		}

		r.SeekHead()
		yielded := 0
		refreshes := 0

		for {
			entry, err := r.Next()
			if err == io.EOF {
				// No more entries, wait and refresh
				select {
				case <-ctx.Done():
					slog.Debug("FollowReader context done", "yielded", yielded, "refreshes", refreshes)
					return
				case <-time.After(100 * time.Millisecond):
					refreshes++
					if refreshes%10 == 0 {
						slog.Debug("FollowReader refreshing", "refreshes", refreshes, "yielded", yielded, "tailSeqnum", r.TailSeqnum())
					}
					// Refresh to pick up new entries
					if err := r.Refresh(); err != nil {
						slog.Debug("FollowReader refresh error", "error", err)
						return
					}
					continue
				}
			}
			if err != nil {
				slog.Debug("FollowReader read error", "error", err)
				return
			}

			record := eventlog.EventRecord{
				Cursor:    journalfile.GetCursor(entry),
				Timestamp: entry.Realtime,
				Message:   entry.Fields["MESSAGE"],
				Fields:    entry.Fields,
			}

			slog.Debug("FollowReader yielding entry",
				"seqnum", entry.Seqnum,
				"session", entry.Fields["SWASH_SESSION"],
				"event", entry.Fields["SWASH_EVENT"],
				"message", truncate(record.Message, 30))
			yielded++

			if !yield(record) {
				slog.Debug("FollowReader consumer stopped", "yielded", yielded)
				return // Consumer broke out of loop
			}
		}
	}
}

// truncate returns at most n characters of s, adding "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
