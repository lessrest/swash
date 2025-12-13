package journal

import "os"

// JournalDir can be set via ldflags to read from a specific journal directory.
// If empty, reads from the default system journal.
var JournalDir string

func init() {
	// Check for journal directory override for reading.
	if dir := os.Getenv("SWASH_JOURNAL_DIR"); dir != "" {
		JournalDir = dir
	}
}

// OpenSystemd opens an event log for the systemd backend using
// SocketSink for writing and SDJournalSource for reading.
func OpenSystemd() (EventLog, error) {
	// Determine socket path - use env override or default journald socket
	socketPath := DefaultSocketPath
	if socket := os.Getenv("SWASH_JOURNAL_SOCKET"); socket != "" {
		socketPath = socket
	}

	// Create sink (write to journald socket)
	snk := NewSocketSink(socketPath)

	// Create source (read via sdjournal/libsystemd)
	src, err := NewSDJournalSource(JournalDir)
	if err != nil {
		snk.Close()
		return nil, err
	}

	return NewCombinedEventLog(snk, src), nil
}
