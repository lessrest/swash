package job

import "io"

// TTYAttachment is the result of attaching to a running TTY session.
//
// Conn is a dedicated byte stream:
//   - Read yields terminal output bytes
//   - Write sends terminal input bytes
//
// Closing Conn should detach the client.
type TTYAttachment struct {
	Conn       io.ReadWriteCloser
	Rows, Cols int32

	// ScreenANSI is a synchronized initial screen snapshot (ANSI).
	ScreenANSI string

	// ClientID identifies this attachment for multi-client sessions (Detach).
	ClientID string
}

// splitConn adapts separate read/write streams into a single ReadWriteCloser.
// This is useful when an attach mechanism naturally yields two fds/pipes.
type splitConn struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (c *splitConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func (c *splitConn) Write(p []byte) (int, error) {
	return c.w.Write(p)
}

func (c *splitConn) Close() error {
	if c == nil {
		return nil
	}
	var errW, errR error
	if c.w != nil {
		errW = c.w.Close()
	}
	if c.r != nil {
		errR = c.r.Close()
	}
	if errW != nil {
		return errW
	}
	return errR
}
