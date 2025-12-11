package protocol

import (
	"bytes"
	"io"
	"strings"
)

// Protocol defines how to interpret process output.
type Protocol string

const (
	ProtocolShell Protocol = "shell" // Line-oriented (default)
	ProtocolSSE   Protocol = "sse"   // Server-Sent Events
)

// OutputHandler is called for each parsed output unit.
type OutputHandler func(fd int, text string, fields map[string]string)

// ProtocolReader wraps an io.Reader and parses output according to protocol.
type ProtocolReader struct {
	protocol Protocol
	fd       int
	handler  OutputHandler
	fields   map[string]string

	// SSE state
	buf bytes.Buffer
}

// NewProtocolReader creates a reader for the given protocol.
func NewProtocolReader(protocol Protocol, fd int, handler OutputHandler, fields map[string]string) *ProtocolReader {
	return &ProtocolReader{
		protocol: protocol,
		fd:       fd,
		handler:  handler,
		fields:   fields,
	}
}

// Process reads from r and emits parsed output via the handler.
func (p *ProtocolReader) Process(r io.Reader) {
	switch p.protocol {
	case ProtocolSSE:
		p.processSSE(r)
	default:
		p.processLines(r)
	}
}

// processLines handles line-oriented shell protocol.
func (p *ProtocolReader) processLines(r io.Reader) {
	var lineBuf bytes.Buffer
	b := make([]byte, 4096)

	for {
		n, err := r.Read(b)
		if n > 0 {
			lineBuf.Write(b[:n])
			// Emit complete lines
			for {
				line, err := lineBuf.ReadString('\n')
				if err != nil {
					// No complete line, put back what we read
					lineBuf.Reset()
					lineBuf.WriteString(line)
					break
				}
				// Trim the newline and emit
				text := strings.TrimSuffix(line, "\n")
				p.handler(p.fd, text, p.fields)
			}
		}
		if err != nil {
			// Flush remaining content
			if lineBuf.Len() > 0 {
				p.handler(p.fd, lineBuf.String(), p.fields)
			}
			return
		}
	}
}

// processSSE handles Server-Sent Events protocol.
// SSE events are delimited by \n\n and contain data: lines.
func (p *ProtocolReader) processSSE(r io.Reader) {
	b := make([]byte, 4096)

	for {
		n, err := r.Read(b)
		if n > 0 {
			p.buf.Write(b[:n])
			p.emitSSEEvents()
		}
		if err != nil {
			// Flush any remaining partial event
			if p.buf.Len() > 0 {
				p.parseSSEEvent(p.buf.String())
			}
			return
		}
	}
}

// emitSSEEvents extracts complete SSE events from the buffer.
func (p *ProtocolReader) emitSSEEvents() {
	for {
		chunk := p.buf.String()
		idx := strings.Index(chunk, "\n\n")
		if idx < 0 {
			return
		}

		event := chunk[:idx]
		p.buf.Reset()
		p.buf.WriteString(chunk[idx+2:])

		p.parseSSEEvent(event)
	}
}

// parseSSEEvent parses an SSE event block and emits data lines.
func (p *ProtocolReader) parseSSEEvent(event string) {
	var dataLines []string

	for _, line := range strings.Split(event, "\n") {
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ") // Optional space after colon
			dataLines = append(dataLines, data)
		}
		// Ignore event:, id:, retry:, and comments (:)
	}

	if len(dataLines) > 0 {
		// Join multiple data lines with newline (per SSE spec)
		text := strings.Join(dataLines, "\n")
		p.handler(p.fd, text, p.fields)
	}
}
