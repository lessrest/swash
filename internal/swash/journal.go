package swash

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/journal"
	"github.com/coreos/go-systemd/v22/sdjournal"
)

// Event represents an event read from the journal.
type Event struct {
	Cursor    string
	Timestamp int64
	Text      string
	FD        int // 1=stdout, 2=stderr
}

// JournalOutput logs output to the journal with just the FD field.
func JournalOutput(fd int, text string) error {
	return journal.Send(text, journal.PriInfo, map[string]string{
		"FD": strconv.Itoa(fd),
	})
}

// PollJournal reads events from the journal since the given cursor.
func PollJournal(sessionID, cursor string) ([]Event, string, error) {
	j, err := sdjournal.NewJournal()
	if err != nil {
		return nil, "", fmt.Errorf("opening journal: %w", err)
	}
	defer j.Close()

	// Filter by session slice (matches all units in the session)
	slice := fmt.Sprintf("swash-%s.slice", sessionID)
	if err := j.AddMatch("_SYSTEMD_USER_SLICE=" + slice); err != nil {
		return nil, "", fmt.Errorf("adding match: %w", err)
	}

	// Seek to cursor or head
	if cursor != "" {
		if err := j.SeekCursor(cursor); err == nil {
			j.Next() // Skip the cursor entry itself
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

		event := parseJournalEntry(j)
		if event != nil {
			events = append(events, *event)
			lastCursor = event.Cursor
		}
	}

	return events, lastCursor, nil
}

// FollowJournal follows the journal until the process exits.
func FollowJournal(sessionID string) error {
	j, err := sdjournal.NewJournal()
	if err != nil {
		return fmt.Errorf("opening journal: %w", err)
	}
	defer j.Close()

	// Filter by session slice (for output with FD field)
	slice := fmt.Sprintf("swash-%s.slice", sessionID)
	if err := j.AddMatch("_SYSTEMD_USER_SLICE=" + slice); err != nil {
		return fmt.Errorf("adding match: %w", err)
	}

	// Also match by task unit name (for exit events from systemd)
	taskUnit := fmt.Sprintf("swash-task-%s.service", sessionID)
	j.AddDisjunction()
	j.AddMatch("USER_UNIT=" + taskUnit)

	j.SeekHead()

	for {
		n, err := j.Next()
		if err != nil {
			return fmt.Errorf("reading journal: %w", err)
		}

		if n == 0 {
			// No more entries, wait for new ones
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

func parseJournalEntry(j *sdjournal.Journal) *Event {
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

// HistorySession represents a session from history (journal).
type HistorySession struct {
	ID       string
	Status   string // "running", "exited", "killed"
	ExitCode *int
	Command  string
	Started  string
}

// ListHistory queries the journal for all swash sessions using systemd's native journal entries.
// It looks at task unit exit events which contain the actual command's exit code.
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

		unit := entry.Fields["USER_UNIT"]
		// Only match new naming: swash-host-* or swash-task-*
		if !strings.HasPrefix(unit, "swash-host-") && !strings.HasPrefix(unit, "swash-task-") {
			continue
		}

		// Extract session ID from unit name:
		// swash-host-ABC123.service -> ABC123
		// swash-task-ABC123.service -> ABC123
		sessionID := strings.TrimSuffix(unit, ".service")
		sessionID = strings.TrimPrefix(sessionID, "swash-host-")
		sessionID = strings.TrimPrefix(sessionID, "swash-task-")

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

		unit := entry.Fields["USER_UNIT"]
		if !strings.HasPrefix(unit, "swash-task-") {
			continue
		}

		// Extract session ID from unit name
		sessionID := strings.TrimSuffix(unit, ".service")
		sessionID = strings.TrimPrefix(sessionID, "swash-task-")

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
