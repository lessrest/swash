package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// JournalSocketListener listens on a Unix datagram socket for journal messages
// using the same protocol as systemd-journald.
type JournalSocketListener struct {
	conn    *net.UnixConn
	path    string
	journal *JournalService
}

// NewJournalSocketListener creates a socket listener at the given path.
// The socket accepts messages in journald's native protocol format.
func NewJournalSocketListener(socketPath string, journal *JournalService) (*JournalSocketListener, error) {
	// Ensure directory exists
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	// Remove existing socket
	os.Remove(socketPath)

	addr := &net.UnixAddr{Name: socketPath, Net: "unixgram"}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on socket: %w", err)
	}

	// Make socket world-writable so any process can log
	if err := os.Chmod(socketPath, 0666); err != nil {
		conn.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}

	return &JournalSocketListener{
		conn:    conn,
		path:    socketPath,
		journal: journal,
	}, nil
}

// Run starts the socket listener loop. Call this in a goroutine.
func (l *JournalSocketListener) Run() {
	buf := make([]byte, 64*1024) // journald uses 64KB max

	for {
		n, _, err := l.conn.ReadFromUnix(buf)
		if err != nil {
			// Socket closed
			if strings.Contains(err.Error(), "use of closed") {
				return
			}
			fmt.Fprintf(os.Stderr, "journal socket read error: %v\n", err)
			continue
		}

		fields, err := parseJournalMessage(buf[:n])
		if err != nil {
			fmt.Fprintf(os.Stderr, "journal socket parse error: %v\n", err)
			continue
		}

		// Extract MESSAGE and send to journal
		message := fields["MESSAGE"]
		delete(fields, "MESSAGE")
		delete(fields, "PRIORITY") // We don't use priority levels

		l.journal.Send(message, fields)
	}
}

// Close stops the listener and removes the socket.
func (l *JournalSocketListener) Close() error {
	err := l.conn.Close()
	os.Remove(l.path)
	return err
}

// parseJournalMessage parses a message in journald's socket protocol.
// Format is a series of fields, each either:
//   - NAME=value\n (simple case)
//   - NAME\n<8-byte LE length><data>\n (binary/multiline case)
func parseJournalMessage(data []byte) (map[string]string, error) {
	fields := make(map[string]string)

	for len(data) > 0 {
		// Find the next newline or equals sign
		eqIdx := bytes.IndexByte(data, '=')
		nlIdx := bytes.IndexByte(data, '\n')

		if eqIdx == -1 && nlIdx == -1 {
			// Malformed - no delimiter found
			break
		}

		if eqIdx != -1 && (nlIdx == -1 || eqIdx < nlIdx) {
			// Simple format: NAME=value\n
			name := string(data[:eqIdx])
			data = data[eqIdx+1:]

			// Find end of value
			endIdx := bytes.IndexByte(data, '\n')
			if endIdx == -1 {
				// Value extends to end
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

			// Skip trailing newline if present
			if len(data) > 0 && data[0] == '\n' {
				data = data[1:]
			}
		}
	}

	return fields, nil
}
