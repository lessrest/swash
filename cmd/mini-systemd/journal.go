package main

import (
	"sort"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// JournalEntry represents a log entry with fields
type JournalEntry struct {
	Timestamp int64             // Microseconds since epoch
	Message   string            // MESSAGE field
	Fields    map[string]string // All fields (including MESSAGE)
}

// JournalService provides a D-Bus journal interface
type JournalService struct {
	mu      sync.RWMutex
	entries []JournalEntry
	cursor  int64 // Simple monotonic cursor
}

// NewJournalService creates a new journal service
func NewJournalService() *JournalService {
	return &JournalService{
		entries: make([]JournalEntry, 0),
	}
}

// Send writes a log entry (D-Bus method on sh.swa.MiniSystemd.Journal)
// Matches the fields that journal.Send would write
func (j *JournalService) Send(message string, fields map[string]string) *dbus.Error {
	j.mu.Lock()
	defer j.mu.Unlock()

	// Copy fields and add MESSAGE
	allFields := make(map[string]string)
	for k, v := range fields {
		allFields[k] = v
	}
	allFields["MESSAGE"] = message

	entry := JournalEntry{
		Timestamp: time.Now().UnixMicro(),
		Message:   message,
		Fields:    allFields,
	}

	j.entries = append(j.entries, entry)
	j.cursor++

	return nil
}

// Poll reads entries matching filters since cursor (D-Bus method)
// Returns: entries (array of (timestamp, message, fields)), new cursor
func (j *JournalService) Poll(matches map[string]string, cursor int64) ([][]interface{}, int64, *dbus.Error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	var result [][]interface{}

	start := int(cursor)
	if start < 0 {
		start = 0
	}

	for i := start; i < len(j.entries); i++ {
		entry := j.entries[i]

		// Check if entry matches all filters
		if !matchesFilters(entry, matches) {
			continue
		}

		// Convert fields to D-Bus compatible format
		fieldsMap := make(map[string]string)
		for k, v := range entry.Fields {
			fieldsMap[k] = v
		}

		result = append(result, []interface{}{
			entry.Timestamp,
			entry.Message,
			fieldsMap,
		})
	}

	return result, int64(len(j.entries)), nil
}

// GetEntries returns all entries (for debugging)
func (j *JournalService) GetEntries() ([][]interface{}, *dbus.Error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	var result [][]interface{}
	for _, entry := range j.entries {
		fieldsMap := make(map[string]string)
		for k, v := range entry.Fields {
			fieldsMap[k] = v
		}
		result = append(result, []interface{}{
			entry.Timestamp,
			entry.Message,
			fieldsMap,
		})
	}
	return result, nil
}

// matchesFilters checks if an entry matches all the given field filters
func matchesFilters(entry JournalEntry, matches map[string]string) bool {
	for field, value := range matches {
		if entryVal, ok := entry.Fields[field]; !ok || entryVal != value {
			return false
		}
	}
	return true
}

// AddFromUnit copies log entries from a unit's captured output into the journal
// This is called by the manager when it captures output
func (j *JournalService) AddFromUnit(unitName, sliceName, stream, data string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	// Determine FD based on stream
	fd := "1"
	if stream == "stderr" {
		fd = "2"
	}

	entry := JournalEntry{
		Timestamp: time.Now().UnixMicro(),
		Message:   data,
		Fields: map[string]string{
			"MESSAGE":              data,
			"FD":                   fd,
			"USER_UNIT":            unitName,
			"_SYSTEMD_USER_SLICE":  sliceName,
		},
	}

	j.entries = append(j.entries, entry)
	j.cursor++
}

// SearchBySlice returns entries for a given slice, sorted by timestamp
func (j *JournalService) SearchBySlice(sliceName string, cursor int64) ([]JournalEntry, int64) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	var result []JournalEntry

	start := int(cursor)
	if start < 0 {
		start = 0
	}

	for i := start; i < len(j.entries); i++ {
		entry := j.entries[i]
		if entry.Fields["_SYSTEMD_USER_SLICE"] == sliceName {
			result = append(result, entry)
		}
	}

	// Sort by timestamp
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp < result[j].Timestamp
	})

	return result, int64(len(j.entries))
}
