package swash

import (
	"context"
	"fmt"
	"iter"
	"strconv"
	"sync"
	"time"
)

// FakeJournal is an in-memory implementation of Journal for unit tests.
type FakeJournal struct {
	mu      sync.RWMutex
	entries []JournalEntry
	cursor  int64 // Next cursor to assign
	closed  bool
}

// NewFakeJournal creates a new FakeJournal with empty state.
func NewFakeJournal() *FakeJournal {
	return &FakeJournal{}
}

// Write sends a structured entry to the journal.
func (f *FakeJournal) Write(message string, fields map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return fmt.Errorf("journal closed")
	}

	f.cursor++
	entry := JournalEntry{
		Cursor:    strconv.FormatInt(f.cursor, 10),
		Timestamp: time.Now(),
		Message:   message,
		Fields:    copyFields(fields),
	}
	f.entries = append(f.entries, entry)
	return nil
}

// AddEntry adds a pre-built entry to the journal (for test setup).
func (f *FakeJournal) AddEntry(entry JournalEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cursor++
	if entry.Cursor == "" {
		entry.Cursor = strconv.FormatInt(f.cursor, 10)
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	f.entries = append(f.entries, entry)
}

// Poll reads entries matching filters since cursor.
func (f *FakeJournal) Poll(ctx context.Context, matches []JournalMatch, cursor string) ([]JournalEntry, string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.closed {
		return nil, "", fmt.Errorf("journal closed")
	}

	startIdx := 0
	if cursor != "" {
		// Find the cursor position and start after it
		for i, e := range f.entries {
			if e.Cursor == cursor {
				startIdx = i + 1
				break
			}
		}
	}

	var result []JournalEntry
	var lastCursor string

	for i := startIdx; i < len(f.entries); i++ {
		entry := f.entries[i]
		if matchesFilters(entry, matches) {
			result = append(result, copyEntry(entry))
			lastCursor = entry.Cursor
		}
	}

	return result, lastCursor, nil
}

// Follow returns an iterator over entries matching filters.
func (f *FakeJournal) Follow(ctx context.Context, matches []JournalMatch) iter.Seq[JournalEntry] {
	return func(yield func(JournalEntry) bool) {
		idx := 0
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			f.mu.RLock()
			if f.closed {
				f.mu.RUnlock()
				return
			}

			// Yield any new matching entries
			for idx < len(f.entries) {
				entry := f.entries[idx]
				idx++
				if matchesFilters(entry, matches) {
					f.mu.RUnlock()
					if !yield(copyEntry(entry)) {
						return
					}
					f.mu.RLock()
				}
			}
			f.mu.RUnlock()

			// Small sleep to avoid busy loop in tests
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
}

// Close releases any resources.
func (f *FakeJournal) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// Entries returns a copy of all entries in the journal.
func (f *FakeJournal) Entries() []JournalEntry {
	f.mu.RLock()
	defer f.mu.RUnlock()
	result := make([]JournalEntry, len(f.entries))
	for i, e := range f.entries {
		result[i] = copyEntry(e)
	}
	return result
}

// IsClosed returns whether Close has been called.
func (f *FakeJournal) IsClosed() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.closed
}

// Clear removes all entries from the journal.
func (f *FakeJournal) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = nil
	f.cursor = 0
}

// matchesFilters checks if an entry matches all the given filters.
// Empty matches matches all entries.
func matchesFilters(entry JournalEntry, matches []JournalMatch) bool {
	for _, m := range matches {
		value, ok := entry.Fields[m.Field]
		if !ok || value != m.Value {
			return false
		}
	}
	return true
}

// copyFields creates a copy of the fields map.
func copyFields(fields map[string]string) map[string]string {
	if fields == nil {
		return nil
	}
	result := make(map[string]string, len(fields))
	for k, v := range fields {
		result[k] = v
	}
	return result
}

// copyEntry creates a deep copy of a journal entry.
func copyEntry(e JournalEntry) JournalEntry {
	return JournalEntry{
		Cursor:    e.Cursor,
		Timestamp: e.Timestamp,
		Message:   e.Message,
		Fields:    copyFields(e.Fields),
	}
}
