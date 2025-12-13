// Package sink provides EventSink implementations for writing to journal.
package journal

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
)

// DefaultSocketPath is the standard journald socket location.
const DefaultSocketPath = "/run/systemd/journal/socket"

// SocketSink implements EventSink by writing to a journald-compatible socket.
// This works with both real journald and mini-journal.
type SocketSink struct {
	socketPath string

	mu   sync.Mutex
	conn *net.UnixConn
}

var _ EventSink = (*SocketSink)(nil)

// NewSocketSink creates a sink that writes to the given socket path.
// If socketPath is empty, uses DefaultSocketPath.
func NewSocketSink(socketPath string) *SocketSink {
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	return &SocketSink{
		socketPath: socketPath,
	}
}

// Write sends a structured entry to the journald socket.
// The journald protocol is simple:
//   - For fields without newlines: KEY=value\n
//   - For fields with newlines: KEY\n<8-byte-little-endian-length><binary-value>
func (s *SocketSink) Write(message string, fields map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureConn(); err != nil {
		return err
	}

	// Build the datagram
	data := s.encodeEntry(message, fields)

	_, err := s.conn.Write(data)
	if err != nil {
		// Connection may have gone stale, close and let next write reconnect
		s.conn.Close()
		s.conn = nil
		return fmt.Errorf("writing to journal socket: %w", err)
	}

	return nil
}

// Close releases the socket connection.
func (s *SocketSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		err := s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

// ensureConn opens the socket connection if not already open.
// Caller must hold s.mu.
func (s *SocketSink) ensureConn() error {
	if s.conn != nil {
		return nil
	}

	addr := &net.UnixAddr{Name: s.socketPath, Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return fmt.Errorf("connecting to journal socket %s: %w", s.socketPath, err)
	}
	s.conn = conn
	return nil
}

// encodeEntry builds the journald protocol datagram.
func (s *SocketSink) encodeEntry(message string, fields map[string]string) []byte {
	var buf []byte

	// Always include MESSAGE field
	buf = appendField(buf, "MESSAGE", message)

	// Add PRIORITY (informational)
	buf = appendField(buf, "PRIORITY", "6")

	// Add all custom fields
	for k, v := range fields {
		buf = appendField(buf, k, v)
	}

	return buf
}

// appendField appends a field to the buffer using journald's protocol.
func appendField(buf []byte, key, value string) []byte {
	if strings.ContainsAny(value, "\n") {
		// Binary format for values with newlines:
		// KEY\n<8-byte-little-endian-length><binary-value>\n
		buf = append(buf, key...)
		buf = append(buf, '\n')
		var lenBuf [8]byte
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(value)))
		buf = append(buf, lenBuf[:]...)
		buf = append(buf, value...)
		buf = append(buf, '\n')
	} else {
		// Simple format: KEY=value\n
		buf = append(buf, key...)
		buf = append(buf, '=')
		buf = append(buf, value...)
		buf = append(buf, '\n')
	}
	return buf
}

// SocketPath returns the configured socket path.
func (s *SocketSink) SocketPath() string {
	return s.socketPath
}

// SetSocketPathFromEnv checks SWASH_JOURNAL_SOCKET and updates the sink's path.
// Returns the path being used.
func SetSocketPathFromEnv() string {
	if path := os.Getenv("SWASH_JOURNAL_SOCKET"); path != "" {
		return path
	}
	return DefaultSocketPath
}
