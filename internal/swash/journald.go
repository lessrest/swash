package swash

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/journal"
	"github.com/coreos/go-systemd/v22/sdjournal"
)

// JournalEntry represents a single journal entry.
type JournalEntry struct {
	Cursor    string
	Timestamp time.Time
	Message   string
	Fields    map[string]string
}

// JournalMatch represents a filter criterion for journal queries.
type JournalMatch struct {
	Field string // e.g., "_SYSTEMD_USER_SLICE", "USER_UNIT", "FD"
	Value string
}

// MatchSlice creates a match for a session's slice.
func MatchSlice(slice SliceName) JournalMatch {
	return JournalMatch{
		Field: "_SYSTEMD_USER_SLICE",
		Value: slice.String(),
	}
}

// MatchUnit creates a match for a specific unit.
func MatchUnit(unit UnitName) JournalMatch {
	return JournalMatch{
		Field: "USER_UNIT",
		Value: unit.String(),
	}
}

// Journal provides operations on the systemd journal.
type Journal interface {
	// Write sends a structured entry to the journal.
	Write(message string, fields map[string]string) error

	// Poll reads entries matching filters since cursor.
	// Returns entries, the new cursor position, and any error.
	Poll(ctx context.Context, matches []JournalMatch, cursor string) ([]JournalEntry, string, error)

	// Follow streams entries matching filters until context is cancelled.
	Follow(ctx context.Context, matches []JournalMatch, handler func(JournalEntry)) error

	// Close releases any resources.
	Close() error
}

// journalImpl implements Journal using go-systemd/sdjournal.
type journalImpl struct {
	j *sdjournal.Journal
}

// OpenJournal opens a connection to the systemd journal.
func OpenJournal() (Journal, error) {
	j, err := sdjournal.NewJournal()
	if err != nil {
		return nil, fmt.Errorf("opening journal: %w", err)
	}
	return &journalImpl{j: j}, nil
}

// Close releases the journal resources.
func (ji *journalImpl) Close() error {
	return ji.j.Close()
}

// Write sends a structured entry to the journal.
func (ji *journalImpl) Write(message string, fields map[string]string) error {
	return journal.Send(message, journal.PriInfo, fields)
}

// Poll reads entries matching filters since cursor.
func (ji *journalImpl) Poll(
	ctx context.Context,
	matches []JournalMatch,
	cursor string,
) ([]JournalEntry, string, error) {
	// Apply matches
	ji.j.FlushMatches()
	for _, m := range matches {
		if err := ji.j.AddMatch(m.Field + "=" + m.Value); err != nil {
			return nil, "", fmt.Errorf("adding match %s=%s: %w", m.Field, m.Value, err)
		}
	}

	// Seek to position
	if cursor != "" {
		if err := ji.j.SeekCursor(cursor); err == nil {
			ji.j.Next() // Skip the cursor entry itself
		} else {
			ji.j.SeekHead()
		}
	} else {
		ji.j.SeekHead()
	}

	var entries []JournalEntry
	var lastCursor string

	for {
		n, err := ji.j.Next()
		if err != nil {
			return nil, "", fmt.Errorf("reading journal: %w", err)
		}
		if n == 0 {
			break
		}

		entry, err := ji.parseEntry()
		if err != nil {
			continue
		}
		entries = append(entries, entry)
		lastCursor = entry.Cursor
	}

	return entries, lastCursor, nil
}

// Follow streams entries matching filters until context is cancelled.
func (ji *journalImpl) Follow(
	ctx context.Context,
	matches []JournalMatch,
	handler func(JournalEntry),
) error {
	// Apply matches
	ji.j.FlushMatches()
	for _, m := range matches {
		if err := ji.j.AddMatch(m.Field + "=" + m.Value); err != nil {
			return fmt.Errorf("adding match %s=%s: %w", m.Field, m.Value, err)
		}
	}

	ji.j.SeekHead()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := ji.j.Next()
		if err != nil {
			return fmt.Errorf("reading journal: %w", err)
		}

		if n == 0 {
			// No more entries, wait for new ones
			ji.j.Wait(time.Second)
			continue
		}

		entry, err := ji.parseEntry()
		if err != nil {
			continue
		}
		handler(entry)
	}
}

// parseEntry parses the current journal position into a JournalEntry.
func (ji *journalImpl) parseEntry() (JournalEntry, error) {
	raw, err := ji.j.GetEntry()
	if err != nil {
		return JournalEntry{}, err
	}

	cursor, _ := ji.j.GetCursor()

	return JournalEntry{
		Cursor:    cursor,
		Timestamp: time.Unix(int64(raw.RealtimeTimestamp/1000000), int64((raw.RealtimeTimestamp%1000000)*1000)),
		Message:   raw.Fields["MESSAGE"],
		Fields:    raw.Fields,
	}, nil
}

// -----------------------------------------------------------------------------
// Convenience functions for common journal operations
// -----------------------------------------------------------------------------

// WriteOutput writes process output to the journal with an FD field.
func WriteOutput(fd int, text string) error {
	return WriteOutputWithFields(fd, text, nil)
}

// WriteOutputWithFields writes process output to the journal with FD and extra fields.
func WriteOutputWithFields(fd int, text string, extraFields map[string]string) error {
	fields := map[string]string{
		"FD": fmt.Sprintf("%d", fd),
	}
	for k, v := range extraFields {
		fields[k] = v
	}
	return journal.Send(text, journal.PriInfo, fields)
}

// Event represents a parsed output event from the journal.
type Event struct {
	Cursor    string
	Timestamp int64
	Text      string
	FD        int // 1=stdout, 2=stderr
}

// PollSessionOutput reads output events from a session's journal since cursor.
func PollSessionOutput(sessionID, cursor string) ([]Event, string, error) {
	j, err := sdjournal.NewJournal()
	if err != nil {
		return nil, "", fmt.Errorf("opening journal: %w", err)
	}
	defer j.Close()

	// Filter by session slice
	slice := SessionSlice(sessionID)
	if err := j.AddMatch("_SYSTEMD_USER_SLICE=" + slice.String()); err != nil {
		return nil, "", fmt.Errorf("adding match: %w", err)
	}

	// Seek to cursor or head
	if cursor != "" {
		if err := j.SeekCursor(cursor); err == nil {
			j.Next()
		} else {
			j.SeekHead()
		}
	} else {
		j.SeekHead()
	}

	var events []Event
	var lastCursor string

	for {
		n, err := j.Next()
		if err != nil {
			return nil, "", fmt.Errorf("reading journal: %w", err)
		}
		if n == 0 {
			break
		}

		event := parseOutputEvent(j)
		if event != nil {
			events = append(events, *event)
			lastCursor = event.Cursor
		}
	}

	return events, lastCursor, nil
}

// FollowSession follows a session's journal until the task exits.
func FollowSession(sessionID string) error {
	j, err := sdjournal.NewJournal()
	if err != nil {
		return fmt.Errorf("opening journal: %w", err)
	}
	defer j.Close()

	// Filter by session slice (for output with FD field)
	slice := SessionSlice(sessionID)
	if err := j.AddMatch("_SYSTEMD_USER_SLICE=" + slice.String()); err != nil {
		return fmt.Errorf("adding match: %w", err)
	}

	// Also match by task unit name (for exit events from systemd)
	taskUnit := TaskUnit(sessionID)
	if err := j.AddDisjunction(); err != nil {
		return fmt.Errorf("adding disjunction: %w", err)
	}
	if err := j.AddMatch("USER_UNIT=" + taskUnit.String()); err != nil {
		return fmt.Errorf("adding match: %w", err)
	}

	j.SeekHead()

	for {
		n, err := j.Next()
		if err != nil {
			return fmt.Errorf("reading journal: %w", err)
		}

		if n == 0 {
			j.Wait(time.Second)
			continue
		}

		entry, err := j.GetEntry()
		if err != nil {
			continue
		}

		// Print output (MESSAGE field with FD tag)
		if msg := entry.Fields["MESSAGE"]; msg != "" {
			if entry.Fields["FD"] != "" {
				fmt.Println(msg)
			}
		}

		// Check for exit (systemd adds EXIT_CODE when task unit exits)
		if entry.Fields["EXIT_CODE"] != "" {
			return nil
		}
	}
}

func parseOutputEvent(j *sdjournal.Journal) *Event {
	entry, err := j.GetEntry()
	if err != nil {
		return nil
	}

	// Only care about messages with FD field (our output)
	message := entry.Fields["MESSAGE"]
	fdStr := entry.Fields["FD"]
	if message == "" || fdStr == "" {
		return nil
	}

	cursor, _ := j.GetCursor()
	fd, _ := strconv.Atoi(fdStr)

	return &Event{
		Cursor:    cursor,
		Timestamp: int64(entry.RealtimeTimestamp / 1000000),
		Text:      message,
		FD:        fd,
	}
}

// -----------------------------------------------------------------------------
// History functions
// -----------------------------------------------------------------------------

// HistorySession represents a session from history (journal).
type HistorySession struct {
	ID       string
	Status   string // "running", "exited", "killed"
	ExitCode *int
	Command  string
	Started  string
}

// ListHistory queries the journal for all swash sessions using systemd's native journal entries.
func ListHistory() ([]HistorySession, error) {
	j, err := sdjournal.NewJournal()
	if err != nil {
		return nil, fmt.Errorf("opening journal: %w", err)
	}
	defer j.Close()

	j.SeekTail()

	sessions := make(map[string]*HistorySession)
	var order []string

	// First pass: find exit events
	for range 2000 {
		n, err := j.Previous()
		if err != nil {
			return nil, fmt.Errorf("reading journal: %w", err)
		}
		if n == 0 {
			break
		}

		entry, err := j.GetEntry()
		if err != nil {
			continue
		}

		exitCode := entry.Fields["EXIT_CODE"]
		exitStatus := entry.Fields["EXIT_STATUS"]
		if exitCode == "" || exitStatus == "" {
			continue
		}

		unitStr := entry.Fields["USER_UNIT"]
		// Only match swash units
		if !strings.HasPrefix(unitStr, "swash-host-") && !strings.HasPrefix(unitStr, "swash-task-") {
			continue
		}

		// Extract session ID from unit name
		unit := UnitName(unitStr)
		sessionID := unit.SessionID()

		if _, exists := sessions[sessionID]; exists {
			continue
		}

		ts := time.Unix(int64(entry.RealtimeTimestamp/1000000), 0)

		code, _ := strconv.Atoi(exitStatus)
		status := "exited"
		if exitCode == "killed" {
			status = "killed"
		}

		sess := &HistorySession{
			ID:       sessionID,
			Status:   status,
			ExitCode: &code,
			Started:  ts.Format("Mon 2006-01-02 15:04:05 MST"),
		}

		sessions[sessionID] = sess
		order = append(order, sessionID)
	}

	// Second pass: find commands from "Started" messages
	j.FlushMatches()
	if err := j.AddMatch("JOB_RESULT=done"); err != nil {
		return nil, fmt.Errorf("adding match: %w", err)
	}
	if err := j.AddMatch("JOB_TYPE=start"); err != nil {
		return nil, fmt.Errorf("adding match: %w", err)
	}
	j.SeekTail()

	for range 2000 {
		n, err := j.Previous()
		if err != nil || n == 0 {
			break
		}

		entry, err := j.GetEntry()
		if err != nil {
			continue
		}

		unitStr := entry.Fields["USER_UNIT"]
		if !strings.HasPrefix(unitStr, "swash-task-") {
			continue
		}

		unit := UnitName(unitStr)
		sessionID := unit.SessionID()

		sess, exists := sessions[sessionID]
		if !exists || sess.Command != "" {
			continue
		}

		// Parse command from MESSAGE: "Started swash-task-ABC.service - /usr/bin/bash -c \"command\"."
		msg := entry.Fields["MESSAGE"]
		if idx := strings.Index(msg, " - "); idx != -1 {
			sess.Command = strings.TrimSuffix(msg[idx+3:], ".")
		}
	}

	// Build result in order (most recent first)
	var result []HistorySession
	for _, id := range order {
		result = append(result, *sessions[id])
	}

	return result, nil
}
