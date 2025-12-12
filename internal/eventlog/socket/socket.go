// Package socket provides an EventLog implementation that sends entries
// to a journald-compatible socket (like swash-journald or real journald).
//
// For reading, it opens the journal file directly using the file-based reader.
package socket

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/coreos/go-systemd/v22/journal"

	"github.com/mbrock/swash/internal/dirs"
	"github.com/mbrock/swash/internal/eventlog"
	"github.com/mbrock/swash/internal/eventlog/file"
	"github.com/mbrock/swash/pkg/journalfile"
)

// SocketEventLog implements eventlog.EventLog by writing to a journald socket
// and reading from the journal file directly.
type SocketEventLog struct {
	socketPath  string
	journalPath string
	reader      *journalfile.Reader
}

var _ eventlog.EventLog = (*SocketEventLog)(nil)

// Config holds configuration for the socket-based event log.
type Config struct {
	// SocketPath is the Unix socket to send entries to.
	SocketPath string

	// JournalPath is the journal file to read from.
	JournalPath string
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		SocketPath:  filepath.Join(dirs.RuntimeDir(), "journal.socket"),
		JournalPath: filepath.Join(dirs.StateDir(), "swash.journal"),
	}
}

// Open creates a new SocketEventLog with the given configuration.
// It configures the journal library to use the specified socket for writing
// and opens the journal file for reading.
func Open(cfg Config) (*SocketEventLog, error) {
	slog.Debug("socket eventlog opening", "socket", cfg.SocketPath, "journal", cfg.JournalPath)

	// Configure go-systemd/journal to use our socket
	journal.SetSocketPath(cfg.SocketPath)

	// Open journal file for reading (may not exist yet)
	var reader *journalfile.Reader
	if _, err := os.Stat(cfg.JournalPath); err == nil {
		reader, err = journalfile.OpenRead(cfg.JournalPath)
		if err != nil {
			return nil, fmt.Errorf("open journal reader: %w", err)
		}
		slog.Debug("socket eventlog opened reader", "entries", reader.NEntries())
	} else {
		slog.Debug("socket eventlog journal file does not exist yet", "path", cfg.JournalPath)
	}

	return &SocketEventLog{
		socketPath:  cfg.SocketPath,
		journalPath: cfg.JournalPath,
		reader:      reader,
	}, nil
}

// Close releases resources.
func (l *SocketEventLog) Close() error {
	if l.reader != nil {
		return l.reader.Close()
	}
	return nil
}

// Write sends an entry to the journald socket (fire-and-forget).
// Use for high-volume streaming data like process output.
func (l *SocketEventLog) Write(message string, fields map[string]string) error {
	slog.Debug("socket eventlog writing", "message", truncate(message, 50), "session", fields["SWASH_SESSION"], "event", fields["SWASH_EVENT"])
	return journal.Send(message, journal.PriInfo, fields)
}

// WriteSync sends an entry to the journald socket and waits until it's readable.
// Use for lifecycle events that need read-after-write consistency.
func (l *SocketEventLog) WriteSync(message string, fields map[string]string) error {
	// Generate a unique nonce to identify this specific write
	nonce := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	fieldsWithNonce := make(map[string]string, len(fields)+1)
	maps.Copy(fieldsWithNonce, fields)
	fieldsWithNonce["SWASH_WRITE_NONCE"] = nonce

	slog.Debug("socket eventlog writing sync", "message", truncate(message, 50), "session", fields["SWASH_SESSION"], "event", fields["SWASH_EVENT"], "nonce", nonce)

	if err := journal.Send(message, journal.PriInfo, fieldsWithNonce); err != nil {
		return err
	}

	// Wait until we can read back an entry with our nonce
	return l.waitForNonce(nonce)
}

// waitForNonce polls the journal until an entry with the given nonce is visible.
func (l *SocketEventLog) waitForNonce(nonce string) error {
	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		// Open a fresh reader to see new entries
		reader, err := journalfile.OpenRead(l.journalPath)
		if err != nil {
			// Journal may not exist yet, keep waiting
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Look for our nonce
		reader.FlushMatches()
		reader.AddMatch("SWASH_WRITE_NONCE", nonce)
		reader.SeekHead()

		// Scan for matching entry
		for {
			entry, err := reader.Next()
			if err != nil {
				break
			}
			if entry.Fields["SWASH_WRITE_NONCE"] == nonce {
				reader.Close()
				slog.Debug("socket eventlog write confirmed", "nonce", nonce)
				return nil
			}
		}
		reader.Close()

		time.Sleep(10 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for journal entry to be readable")
}

// truncate returns at most n characters of s, adding "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ensureReader opens the reader if not already open (journal file may be created after us).
func (l *SocketEventLog) ensureReader() error {
	if l.reader != nil {
		return nil
	}

	if _, err := os.Stat(l.journalPath); os.IsNotExist(err) {
		return fmt.Errorf("journal file does not exist: %s", l.journalPath)
	}

	reader, err := journalfile.OpenRead(l.journalPath)
	if err != nil {
		return fmt.Errorf("open journal reader: %w", err)
	}
	l.reader = reader
	return nil
}

// Poll reads entries matching filters since cursor.
func (l *SocketEventLog) Poll(ctx context.Context, filters []eventlog.EventFilter, cursor string) ([]eventlog.EventRecord, string, error) {
	slog.Debug("socket eventlog polling", "filters", len(filters), "cursor", cursor)

	if err := l.ensureReader(); err != nil {
		return nil, "", err
	}

	// Refresh to see latest entries
	if err := l.reader.Refresh(); err != nil {
		return nil, "", fmt.Errorf("refreshing journal: %w", err)
	}

	entries, newCursor, err := pollReader(l.reader, filters, cursor)
	slog.Debug("socket eventlog poll result", "entries", len(entries), "newCursor", newCursor, "error", err)
	return entries, newCursor, err
}

// Follow returns an iterator over entries matching filters.
func (l *SocketEventLog) Follow(ctx context.Context, filters []eventlog.EventFilter) iter.Seq[eventlog.EventRecord] {
	return file.FollowReader(ctx, l.reader, l.journalPath, filters)
}

// pollReader is shared logic for polling a journal reader.
func pollReader(r *journalfile.Reader, filters []eventlog.EventFilter, cursor string) ([]eventlog.EventRecord, string, error) {
	r.FlushMatches()
	for _, f := range filters {
		r.AddMatch(f.Field, f.Value)
	}

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
		if err != nil {
			break
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
