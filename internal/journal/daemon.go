// Package journald provides a minimal journald-compatible daemon for the POSIX backend.
// It listens on a Unix datagram socket using the systemd journal native protocol
// and writes entries to a single shared journal file.
package journal

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"swa.sh/go/swash/internal/dirs"
	"swa.sh/go/swash/pkg/journalfile"
)

// Daemon is a minimal journald-compatible log daemon.
type Daemon struct {
	socketPath  string
	journalPath string

	mu      sync.Mutex
	conn    *net.UnixConn
	journal *journalfile.File

	stopCh chan struct{}
	doneCh chan struct{}
}

// Config holds configuration for the daemon.
type Config struct {
	// SocketPath is the Unix socket path to listen on.
	// Default: ~/.local/state/swash/journal.socket
	SocketPath string

	// JournalPath is the journal file path.
	// Default: ~/.local/state/swash/swash.journal
	JournalPath string

	// InheritedFD is an optional file descriptor to inherit from a previous
	// daemon instance during graceful restart. If > 0, the daemon will use
	// this fd instead of creating a new socket.
	InheritedFD int
}

// DefaultConfig returns the default daemon configuration.
func DefaultConfig() Config {
	return Config{
		SocketPath:  filepath.Join(dirs.RuntimeDir(), "journal.socket"),
		JournalPath: filepath.Join(dirs.StateDir(), "swash.journal"),
	}
}

// New creates a new daemon with the given configuration.
func New(cfg Config) (*Daemon, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), 0755); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.JournalPath), 0755); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}

	var conn *net.UnixConn
	var err error

	if cfg.InheritedFD > 0 {
		// Inherit socket from previous instance
		f := os.NewFile(uintptr(cfg.InheritedFD), "socket")
		fc, err := net.FileConn(f)
		if err != nil {
			return nil, fmt.Errorf("inherit socket fd: %w", err)
		}
		f.Close() // FileConn dups the fd
		conn = fc.(*net.UnixConn)
	} else {
		// Create new socket
		os.Remove(cfg.SocketPath)
		addr := &net.UnixAddr{Name: cfg.SocketPath, Net: "unixgram"}
		conn, err = net.ListenUnixgram("unixgram", addr)
		if err != nil {
			return nil, fmt.Errorf("listen on socket: %w", err)
		}
		// Make socket world-writable so any process can log
		if err := os.Chmod(cfg.SocketPath, 0666); err != nil {
			conn.Close()
			return nil, fmt.Errorf("chmod socket: %w", err)
		}
	}

	// Open or create journal file
	var journal *journalfile.File
	if _, err := os.Stat(cfg.JournalPath); os.IsNotExist(err) {
		var machineID, bootID journalfile.ID128
		rand.Read(machineID[:])
		rand.Read(bootID[:])
		journal, err = journalfile.Create(cfg.JournalPath, machineID, bootID)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("create journal: %w", err)
		}
	} else {
		journal, err = journalfile.OpenAppend(cfg.JournalPath)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("open journal: %w", err)
		}
	}

	return &Daemon{
		socketPath:  cfg.SocketPath,
		journalPath: cfg.JournalPath,
		conn:        conn,
		journal:     journal,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}, nil
}

// Run starts the daemon and blocks until it receives a stop signal.
// It handles SIGTERM for shutdown and SIGHUP for graceful restart.
func (d *Daemon) Run() error {
	slog.Debug("journald daemon starting", "socket", d.socketPath, "journal", d.journalPath)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	// Start read loop in background
	go d.readLoop()

	for {
		select {
		case sig := <-sigCh:
			slog.Debug("journald received signal", "signal", sig)
			switch sig {
			case syscall.SIGHUP:
				return d.gracefulRestart()
			case syscall.SIGTERM, syscall.SIGINT:
				return d.shutdown()
			}
		case <-d.stopCh:
			return nil
		}
	}
}

// readLoop reads messages from the socket and writes them to the journal.
func (d *Daemon) readLoop() {
	defer close(d.doneCh)
	slog.Debug("journald read loop starting")

	buf := make([]byte, 64*1024) // journald uses 64KB max
	entryCount := 0

	for {
		select {
		case <-d.stopCh:
			slog.Debug("journald read loop stopping", "entriesWritten", entryCount)
			return
		default:
		}

		n, _, err := d.conn.ReadFromUnix(buf)
		if err != nil {
			// Check if we're shutting down
			select {
			case <-d.stopCh:
				slog.Debug("journald read loop stopping on error", "entriesWritten", entryCount)
				return
			default:
			}
			// Log error but continue
			slog.Warn("journald read error", "error", err)
			continue
		}

		fields, err := parseJournalMessage(buf[:n])
		if err != nil {
			slog.Warn("journald parse error", "error", err)
			continue
		}

		// Extract key fields for logging
		sessionID := fields["SWASH_SESSION"]
		event := fields["SWASH_EVENT"]
		message := fields["MESSAGE"]
		slog.Debug("journald received entry",
			"session", sessionID,
			"event", event,
			"message", truncate(message, 50),
			"numFields", len(fields))

		d.mu.Lock()
		if d.journal != nil {
			if err := d.journal.AppendEntry(fields); err != nil {
				slog.Error("journald write error", "error", err)
			} else {
				entryCount++
				// Sync immediately so readers can see the entry.
				// This is important for waitReady() to detect fast-completing commands.
				if err := d.journal.Sync(); err != nil {
					slog.Error("journald sync error", "error", err)
				}
				slog.Debug("journald entry written", "entryCount", entryCount, "session", sessionID, "event", event)
			}
		}
		d.mu.Unlock()
	}
}

// truncate returns at most n characters of s, adding "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// shutdown stops the daemon cleanly.
func (d *Daemon) shutdown() error {
	close(d.stopCh)
	d.conn.Close()
	<-d.doneCh

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.journal != nil {
		d.journal.Sync()
		d.journal.Close()
	}
	return nil
}

// gracefulRestart re-execs the daemon while keeping the socket fd open.
// This ensures no messages are lost during restart.
func (d *Daemon) gracefulRestart() error {
	// Stop reading but keep socket open
	close(d.stopCh)
	<-d.doneCh

	// Sync journal before restart
	d.mu.Lock()
	if d.journal != nil {
		d.journal.Sync()
		d.journal.Close()
	}
	d.mu.Unlock()

	// Get socket fd for inheritance
	rawConn, err := d.conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("get raw conn: %w", err)
	}

	var fd uintptr
	rawConn.Control(func(f uintptr) {
		fd = f
	})

	// Clear close-on-exec so child inherits the fd
	syscall.CloseOnExec(int(fd))

	// Set environment for child to inherit the fd
	os.Setenv("SWASH_JOURNALD_FD", strconv.Itoa(int(fd)))

	// Re-exec ourselves
	execErr := syscall.Exec(os.Args[0], os.Args, os.Environ())
	return fmt.Errorf("exec failed: %w", execErr)
}

// SocketPath returns the socket path.
func (d *Daemon) SocketPath() string {
	return d.socketPath
}

// JournalPath returns the journal file path.
func (d *Daemon) JournalPath() string {
	return d.journalPath
}

// parseJournalMessage parses a message in journald's socket protocol.
// Format is a series of fields, each either:
//   - NAME=value\n (simple case)
//   - NAME\n<8-byte LE length><data>\n (binary/multiline case)
func parseJournalMessage(data []byte) (map[string]string, error) {
	fields := make(map[string]string)

	for len(data) > 0 {
		eqIdx := bytes.IndexByte(data, '=')
		nlIdx := bytes.IndexByte(data, '\n')

		if eqIdx == -1 && nlIdx == -1 {
			break
		}

		if eqIdx != -1 && (nlIdx == -1 || eqIdx < nlIdx) {
			// Simple format: NAME=value\n
			name := string(data[:eqIdx])
			data = data[eqIdx+1:]

			endIdx := bytes.IndexByte(data, '\n')
			if endIdx == -1 {
				fields[name] = string(data)
				break
			}

			fields[name] = string(data[:endIdx])
			data = data[endIdx+1:]
		} else {
			// Binary format: NAME\n<8-byte length><data>\n
			name := string(data[:nlIdx])
			data = data[nlIdx+1:]

			if len(data) < 8 {
				return nil, fmt.Errorf("truncated length field for %s", name)
			}

			length := binary.LittleEndian.Uint64(data[:8])
			data = data[8:]

			if uint64(len(data)) < length {
				return nil, fmt.Errorf("truncated value for %s", name)
			}

			fields[name] = string(data[:length])
			data = data[length:]

			if len(data) > 0 && data[0] == '\n' {
				data = data[1:]
			}
		}
	}

	return fields, nil
}
