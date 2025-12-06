go
package main

import (
    "crypto/rand"
    "encoding/hex"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/godbus/dbus/v5"
    "github.com/mbrock/swash/pkg/journalfile"
)

// JournalEntry represents a log entry with fields
type JournalEntry struct {
    Timestamp int64             // Microseconds since epoch
    Message   string            // MESSAGE field
    Fields    map[string]string // All fields (including MESSAGE)
    Cursor    string            // Stable journal-style cursor
}

// JournalService provides a D-Bus journal interface
type JournalService struct {
    mu      sync.RWMutex
    entries []JournalEntry
    seq     int64 // monotonic sequence per entry

    // Journal file for persistent storage
    journalFile *journalfile.File
    journalDir  string

    machineID journalfile.ID128
    bootID    journalfile.ID128
}

func isZeroID(id journalfile.ID128) bool {
    for _, b := range id {
        if b != 0 {
            return false
        }
    }
    return true
}

func randomID() (journalfile.ID128, error) {
    var id journalfile.ID128
    if _, err := rand.Read(id[:]); err != nil {
        return id, err
    }
    return id, nil
}

func ensureIDs(machineID, bootID journalfile.ID128) (journalfile.ID128, journalfile.ID128, error) {
    var err error
    if isZeroID(machineID) {
        machineID, err = randomID()
        if err != nil {
            return machineID, bootID, fmt.Errorf("generate machine ID: %w", err)
        }
    }
    if isZeroID(bootID) {
        bootID, err = randomID()
        if err != nil {
            return machineID, bootID, fmt.Errorf("generate boot ID: %w", err)
        }
    }
    return machineID, bootID, nil
}

func id128ToString(id journalfile.ID128) string {
    return hex.EncodeToString(id[:])
}

func parseID128Hex(s string) (journalfile.ID128, error) {
    var id journalfile.ID128
    if len(s) == 0 {
        return id, nil
    }
    if len(s) != 32 {
        return id, fmt.Errorf("id must be 32 hex chars")
    }
    b, err := hex.DecodeString(s)
    if err != nil {
        return id, err
    }
    copy(id[:], b)
    return id, nil
}

func makeCursor(machineID, bootID journalfile.ID128, seq int64, tsUsec int64) string {
    return fmt.Sprintf("s=%s;b=%s;i=%d;t=%d;", id128ToString(machineID), id128ToString(bootID), seq, tsUsec)
}

func parseCursor(cursor string) (int, error) {
    parts := strings.Split(cursor, ";")
    for _, p := range parts {
        if strings.HasPrefix(p, "i=") {
            v, err := strconv.ParseInt(strings.TrimPrefix(p, "i="), 10, 64)
            if err != nil {
                return 0, err
            }
            if v < 0 {
                return 0, fmt.Errorf("negative cursor")
            }
            return int(v), nil
        }
    }
    return 0, fmt.Errorf("cursor missing sequence")
}

func newJournalService(machineID, bootID journalfile.ID128) (*JournalService, error) {
    mid, bid, err := ensureIDs(machineID, bootID)
    if err != nil {
        return nil, err
    }
    return &JournalService{
        entries:   make([]JournalEntry, 0),
        machineID: mid,
        bootID:    bid,
    }, nil
}

// NewJournalService creates a new in-memory journal service
func NewJournalService(machineID, bootID journalfile.ID128) *JournalService {
    js, err := newJournalService(machineID, bootID)
    if err != nil {
        // If randomness fails, panic; this is only for test harnesses.
        panic(err)
    }
    return js
}

// NewJournalServiceWithFile creates a journal service with file-based storage
func NewJournalServiceWithFile(dir string, machineID, bootID journalfile.ID128) (*JournalService, error) {
    js, err := newJournalService(machineID, bootID)
    if err != nil {
        return nil, err
    }

    js.journalDir = dir

    if err := os.MkdirAll(dir, 0750); err != nil {
        return nil, fmt.Errorf("create journal directory: %w", err)
    }

    // Create journal file with timestamp
    filename := fmt.Sprintf("user-%d.journal", time.Now().UnixNano())
    path := filepath.Join(dir, filename)

    jf, err := journalfile.Create(path, js.machineID, js.bootID)
    if err != nil {
        return nil, fmt.Errorf("create journal file: %w", err)
    }

    js.journalFile = jf
    return js, nil
}

// Close closes the journal file
func (j *JournalService) Close() error {
    if j.journalFile != nil {
        return j.journalFile.Close()
    }
    return nil
}

// GetJournalDir returns the journal directory path (D-Bus method)
func (j *JournalService) GetJournalDir() (string, *dbus.Error) {
    return j.journalDir, nil
}

func (j *JournalService) addIDs(fields map[string]string) {
    bid := id128ToString(j.bootID)
    mid := id128ToString(j.machineID)
    if _, ok := fields["_BOOT_ID"]; !ok {
        fields["_BOOT_ID"] = bid
    }
    if _, ok := fields["_MACHINE_ID"]; !ok {
        fields["_MACHINE_ID"] = mid
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
    j.addIDs(allFields)

    ts := time.Now().UnixMicro()
    cursor := makeCursor(j.machineID, j.bootID, j.seq, ts)

    entry := JournalEntry{
        Timestamp: ts,
        Message:   message,
        Fields:    allFields,
        Cursor:    cursor,
    }

    j.entries = append(j.entries, entry)
    j.seq++

    // Write to journal file if available
    if j.journalFile != nil {
        if err := j.journalFile.AppendEntry(allFields); err != nil {
            // Log but don't fail - in-memory storage still works
            fmt.Fprintf(os.Stderr, "warning: failed to write to journal file: %%v\n", err)
        } else {
            // Sync immediately so external readers (journalctl) can access
            // Real journald syncs every 5 minutes, but we sync eagerly for testing
            if err := j.journalFile.Sync(); err != nil {
                fmt.Fprintf(os.Stderr, "warning: failed to sync journal file: %%v\n", err)
            }
        }
    }

    return nil
}

// Poll reads entries matching filters since cursor (D-Bus method)
// Returns: entries (array of (timestamp, message, fields)), new cursor
func (j *JournalService) Poll(matches map[string]string, cursor string) ([][]interface{}, string, *dbus.Error) {
    j.mu.RLock()
    defer j.mu.RUnlock()

    var result [][]interface{}

    start := 0
    if cursor != "" {
        seq, err := parseCursor(cursor)
        if err != nil {
            return nil, "", dbus.NewError("sh.swa.MiniSystemd.Journal.InvalidCursor", []interface{}{err.Error()})
        }
        start = seq + 1 // next entry after the provided cursor
    }

    var lastCursor string
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
        lastCursor = entry.Cursor
    }

    nextCursor := cursor
    if lastCursor != "" {
        nextCursor = lastCursor
    }

    return result, nextCursor, nil
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

    fields := map[string]string{
        "MESSAGE":             data,
        "FD":                  fd,
        "USER_UNIT":           unitName,
        "_SYSTEMD_USER_SLICE": sliceName,
    }
    j.addIDs(fields)

    ts := time.Now().UnixMicro()
    cursor := makeCursor(j.machineID, j.bootID, j.seq, ts)

    entry := JournalEntry{
        Timestamp: ts,
        Message:   data,
        Fields:    fields,
        Cursor:    cursor,
    }

    j.entries = append(j.entries, entry)
    j.seq++

    // Write to journal file if available
    if j.journalFile != nil {
        if err := j.journalFile.AppendEntry(fields); err != nil {
            fmt.Fprintf(os.Stderr, "warning: failed to write to journal file: %%v\n", err)
        } else {
            // Sync so external readers can access
            if err := j.journalFile.Sync(); err != nil {
                fmt.Fprintf(os.Stderr, "warning: failed to sync journal file: %%v\n", err)
            }
        }
    }
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