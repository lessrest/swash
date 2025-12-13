// Package source provides EventSource implementations for reading from journals.
package journal

import (
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"

	"swa.sh/pkg/journalfile"
)

// JournalfileSource implements EventSource using the pure Go journalfile reader.
// This works without CGO or libsystemd, making it portable to any platform.
type JournalfileSource struct {
	path   string
	reader *journalfile.Reader
}

var _ EventSource = (*JournalfileSource)(nil)

// NewJournalfileSource creates a source that reads from a journal file.
func NewJournalfileSource(path string) (*JournalfileSource, error) {
	reader, err := journalfile.OpenRead(path)
	if err != nil {
		return nil, fmt.Errorf("opening journal file %s: %w", path, err)
	}
	return &JournalfileSource{
		path:   path,
		reader: reader,
	}, nil
}

// Poll reads entries matching filters since cursor.
func (s *JournalfileSource) Poll(ctx context.Context, filters []EventFilter, cursor string) ([]EventRecord, string, error) {
	// Refresh to see latest entries
	if err := s.reader.Refresh(); err != nil {
		return nil, "", fmt.Errorf("refreshing journal: %w", err)
	}

	// Apply matches
	s.reader.FlushMatches()
	for _, f := range filters {
		s.reader.AddMatch(f.Field, f.Value)
	}

	// Seek to position
	if cursor != "" {
		if err := s.reader.SeekCursor(cursor); err != nil {
			s.reader.SeekHead()
		}
	} else {
		s.reader.SeekHead()
	}

	var entries []EventRecord
	var lastCursor string

	for {
		entry, err := s.reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("reading journal: %w", err)
		}

		record := EventRecord{
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
// Uses the Reader's Wait method for efficient file change detection.
func (s *JournalfileSource) Follow(ctx context.Context, filters []EventFilter) iter.Seq[EventRecord] {
	return func(yield func(EventRecord) bool) {
		slog.Debug("JournalfileSource.Follow starting", "path", s.path, "filters", len(filters))

		// Apply matches
		s.reader.FlushMatches()
		for _, f := range filters {
			s.reader.AddMatch(f.Field, f.Value)
		}

		s.reader.SeekHead()

		for {
			entry, err := s.reader.Next()
			if err == io.EOF {
				// No more entries, wait for file changes
				if err := s.reader.Wait(ctx); err != nil {
					if ctx.Err() != nil {
						// Context cancelled - normal shutdown
						return
					}
					slog.Debug("JournalfileSource.Follow wait error", "error", err)
					return
				}
				if err := s.reader.Refresh(); err != nil {
					slog.Debug("JournalfileSource.Follow refresh error", "error", err)
					return
				}
				continue
			}
			if err != nil {
				slog.Debug("JournalfileSource.Follow read error", "error", err)
				return
			}

			record := EventRecord{
				Cursor:    journalfile.GetCursor(entry),
				Timestamp: entry.Realtime,
				Message:   entry.Fields["MESSAGE"],
				Fields:    entry.Fields,
			}

			if !yield(record) {
				return
			}
		}
	}
}

// Close releases resources.
func (s *JournalfileSource) Close() error {
	if s.reader != nil {
		return s.reader.Close()
	}
	return nil
}

// Path returns the journal file path.
func (s *JournalfileSource) Path() string {
	return s.path
}

// Refresh reloads the journal to see new entries.
func (s *JournalfileSource) Refresh() error {
	return s.reader.Refresh()
}
