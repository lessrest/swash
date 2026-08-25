package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// SQLiteLog persists portable-backend events directly in a SQLite database.
// WAL mode allows session hosts and clients to read and write concurrently
// without a coordinating daemon.
type SQLiteLog struct {
	path string
	db   *sql.DB
}

var _ EventLog = (*SQLiteLog)(nil)

// OpenSQLite opens or creates an event log at path.
func OpenSQLite(path string) (*SQLiteLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating event database directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("creating event database: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("closing event database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("securing event database: %w", err)
	}

	u := &url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(250)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()

	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("opening event database: %w", err)
	}
	// Each process needs only one connection. SQLite WAL coordinates the
	// separate host/client processes, while this avoids redundant per-connection
	// journal-mode setup within a process.
	db.SetMaxOpenConns(1)
	err = retrySQLiteBusy(func() error {
		_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp_ns INTEGER NOT NULL,
			message TEXT NOT NULL,
			fields_json TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS event_fields (
			event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (event_id, name)
		);
		CREATE INDEX IF NOT EXISTS event_fields_lookup
			ON event_fields(name, value, event_id);
	`)
		return err
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing event database: %w", err)
	}

	return &SQLiteLog{path: path, db: db}, nil
}

func (s *SQLiteLog) Write(message string, fields map[string]string) error {
	storedFields := make(map[string]string, len(fields)+2)
	for key, value := range fields {
		storedFields[key] = value
	}
	storedFields["MESSAGE"] = message
	storedFields["PRIORITY"] = "6"

	encoded, err := json.Marshal(storedFields)
	if err != nil {
		return fmt.Errorf("encoding event fields: %w", err)
	}

	return retrySQLiteBusy(func() error {
		return s.write(encoded, storedFields, message)
	})
}

func (s *SQLiteLog) write(encoded []byte, storedFields map[string]string, message string) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("starting event transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(`INSERT INTO events(timestamp_ns, message, fields_json) VALUES (?, ?, ?)`,
		time.Now().UnixNano(), message, string(encoded))
	if err != nil {
		return fmt.Errorf("inserting event: %w", err)
	}
	eventID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("reading event ID: %w", err)
	}

	keys := make([]string, 0, len(storedFields))
	for key := range storedFields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.Exec(`INSERT INTO event_fields(event_id, name, value) VALUES (?, ?, ?)`,
			eventID, key, storedFields[key]); err != nil {
			return fmt.Errorf("inserting event field %s: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing event: %w", err)
	}
	return nil
}

func retrySQLiteBusy(operation func() error) error {
	for {
		err := operation()
		if !isSQLiteBusy(err) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	// Extended result codes retain the primary result code in the low byte.
	code := sqliteErr.Code() & 0xff
	return code == 5 || code == 6 // SQLITE_BUSY or SQLITE_LOCKED
}

// SQLite commits are immediately visible to other connections, so WriteSync
// needs no acknowledgement protocol.
func (s *SQLiteLog) WriteSync(message string, fields map[string]string) error {
	return s.Write(message, fields)
}

func (s *SQLiteLog) Poll(ctx context.Context, filters []EventFilter, cursor string) ([]EventRecord, string, error) {
	afterID, _ := strconv.ParseInt(cursor, 10, 64)
	query := strings.Builder{}
	query.WriteString(`SELECT e.id, e.timestamp_ns, e.message, e.fields_json FROM events e WHERE e.id > ?`)
	args := []any{afterID}

	// Matches for the same field are alternatives, as in the systemd journal;
	// matches for different fields are combined.
	grouped := make(map[string][]string)
	var fields []string
	for _, filter := range filters {
		if _, exists := grouped[filter.Field]; !exists {
			fields = append(fields, filter.Field)
		}
		grouped[filter.Field] = append(grouped[filter.Field], filter.Value)
	}
	sort.Strings(fields)
	for _, field := range fields {
		values := grouped[field]
		query.WriteString(` AND EXISTS (SELECT 1 FROM event_fields f WHERE f.event_id = e.id AND f.name = ? AND f.value IN (`)
		args = append(args, field)
		for i, value := range values {
			if i > 0 {
				query.WriteByte(',')
			}
			query.WriteByte('?')
			args = append(args, value)
		}
		query.WriteString(`))`)
	}
	query.WriteString(` ORDER BY e.id`)

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, cursor, fmt.Errorf("querying events: %w", err)
	}
	defer rows.Close()

	var records []EventRecord
	lastCursor := cursor
	for rows.Next() {
		var id, timestampNS int64
		var record EventRecord
		var encoded string
		if err := rows.Scan(&id, &timestampNS, &record.Message, &encoded); err != nil {
			return nil, cursor, fmt.Errorf("reading event: %w", err)
		}
		if err := json.Unmarshal([]byte(encoded), &record.Fields); err != nil {
			return nil, cursor, fmt.Errorf("decoding event %d: %w", id, err)
		}
		record.Cursor = strconv.FormatInt(id, 10)
		record.Timestamp = time.Unix(0, timestampNS)
		records = append(records, record)
		lastCursor = record.Cursor
	}
	if err := rows.Err(); err != nil {
		return nil, cursor, fmt.Errorf("iterating events: %w", err)
	}
	return records, lastCursor, nil
}

func (s *SQLiteLog) Follow(ctx context.Context, filters []EventFilter, cursor string) iter.Seq[EventRecord] {
	return func(yield func(EventRecord) bool) {
		current := cursor
		for {
			records, next, err := s.Poll(ctx, filters, current)
			if err != nil {
				return
			}
			for _, record := range records {
				if !yield(record) {
					return
				}
			}
			current = next

			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (s *SQLiteLog) Close() error { return s.db.Close() }

func (s *SQLiteLog) Path() string { return s.path }
