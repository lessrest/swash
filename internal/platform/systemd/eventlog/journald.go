package journald

import (
	"os"

	"github.com/mbrock/swash/internal/eventlog"
	"github.com/mbrock/swash/internal/eventlog/sink"
	"github.com/mbrock/swash/internal/eventlog/source"
)

// JournalDir can be set via ldflags to read from a specific journal directory.
// If empty, reads from the default system journal.
// Example: -ldflags "-X github.com/mbrock/swash/internal/platform/systemd/eventlog.JournalDir=/path/to/journal"
var JournalDir string

func init() {
	// Check for journal directory override for reading.
	if dir := os.Getenv("SWASH_JOURNAL_DIR"); dir != "" {
		JournalDir = dir
	}
}

// Open opens the backing event log using SocketSink for writing
// and SDJournalSource for reading.
func Open() (eventlog.EventLog, error) {
	// Determine socket path - use env override or default journald socket
	socketPath := sink.DefaultSocketPath
	if socket := os.Getenv("SWASH_JOURNAL_SOCKET"); socket != "" {
		socketPath = socket
	}

	// Create sink (write to journald socket)
	snk := sink.NewSocketSink(socketPath)

	// Create source (read via sdjournal/libsystemd)
	src, err := source.NewSDJournalSource(JournalDir)
	if err != nil {
		snk.Close()
		return nil, err
	}

	return eventlog.NewCombinedEventLog(snk, src), nil
}
