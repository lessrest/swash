package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// unixSessionClient implements SessionClient over HTTP+JSON on a unix socket.
type unixSessionClient struct {
	sessionID   string
	socketPath  string
	httpClient  *http.Client
	closeOnce   sync.Once
	closed      chan struct{}
	closedError error
}

var _ SessionClient = (*unixSessionClient)(nil)

// unixTTYClient implements TTYClient over HTTP+JSON on a unix socket.
type unixTTYClient struct {
	*unixSessionClient
}

var _ TTYClient = (*unixTTYClient)(nil)

// ConnectUnixSession connects to a session control socket.
func ConnectUnixSession(sessionID, socketPath string) (SessionClient, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is empty")
	}
	if socketPath == "" {
		return nil, fmt.Errorf("socket path is empty")
	}

	c := &unixSessionClient{
		sessionID:  sessionID,
		socketPath: socketPath,
		closed:     make(chan struct{}),
	}
	c.httpClient = newUnixHTTPClient(socketPath)
	return c, nil
}

// ConnectUnixTTYSession connects to a TTY session control socket.
func ConnectUnixTTYSession(sessionID, socketPath string) (TTYClient, error) {
	c, err := ConnectUnixSession(sessionID, socketPath)
	if err != nil {
		return nil, err
	}
	return &unixTTYClient{unixSessionClient: c.(*unixSessionClient)}, nil
}

func newUnixHTTPClient(socketPath string) *http.Client {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: false,
	}
	return &http.Client{Transport: tr}
}

func (c *unixSessionClient) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
	})
	// http.Client has no Close; connections will close when idle or on process exit.
	return c.closedError
}

func (c *unixSessionClient) SessionID() (string, error) { return c.sessionID, nil }

func (c *unixSessionClient) Gist() (HostStatus, error) {
	var out HostStatus
	if err := c.doJSON(context.Background(), http.MethodGet, "/gist", nil, &out); err != nil {
		return HostStatus{}, err
	}
	return out, nil
}

func (c *unixSessionClient) Kill() error {
	return c.doJSON(context.Background(), http.MethodPost, "/kill", nil, nil)
}

func (c *unixSessionClient) Restart() error {
	return c.doJSON(context.Background(), http.MethodPost, "/restart", nil, nil)
}

func (c *unixSessionClient) SendInput(input string) (int, error) {
	req, err := c.newRequest(context.Background(), http.MethodPost, "/input", strings.NewReader(input))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("send input: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}

	var out struct {
		Bytes int `json:"bytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decoding send input response: %w", err)
	}
	return out.Bytes, nil
}

func (c *unixTTYClient) GetScreenText() (string, error) {
	return c.getText("/tty/screen?format=text")
}

func (c *unixTTYClient) GetScreenANSI() (string, error) {
	return c.getText("/tty/screen?format=ansi")
}

func (c *unixTTYClient) GetCursor() (int32, int32, error) {
	var out struct {
		Row int32 `json:"row"`
		Col int32 `json:"col"`
	}
	if err := c.doJSON(context.Background(), http.MethodGet, "/tty/cursor", nil, &out); err != nil {
		return 0, 0, err
	}
	return out.Row, out.Col, nil
}

func (c *unixTTYClient) GetTitle() (string, error) {
	return c.getText("/tty/title")
}

func (c *unixTTYClient) GetMode() (bool, error) {
	var out struct {
		AltScreen bool `json:"alt_screen"`
	}
	if err := c.doJSON(context.Background(), http.MethodGet, "/tty/mode", nil, &out); err != nil {
		return false, err
	}
	return out.AltScreen, nil
}

func (c *unixTTYClient) Resize(rows, cols int32) error {
	body := map[string]int32{"rows": rows, "cols": cols}
	return c.doJSON(context.Background(), http.MethodPost, "/tty/resize", body, nil)
}

func (c *unixTTYClient) Attach(clientRows, clientCols int32) (*TTYAttachment, error) {
	// We implement Attach as a simple HTTP tunnel:
	// - Client sends GET /tty/attach?rows=..&cols=..
	// - Server replies with headers containing size + client ID and a Content-Length body
	//   containing the initial screen snapshot (ANSI)
	// - After the body, the connection becomes a raw bidirectional byte stream
	//   (read = PTY output, write = PTY input)
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, err
	}

	// Build request line manually so we can keep using the connection as a stream.
	u := &url.URL{Path: "/tty/attach"}
	q := u.Query()
	q.Set("rows", fmt.Sprintf("%d", clientRows))
	q.Set("cols", fmt.Sprintf("%d", clientCols))
	u.RawQuery = q.Encode()

	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: swash\r\nConnection: keep-alive\r\n\r\n", u.String())
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("attach: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}

	rows, _ := strconv.ParseInt(resp.Header.Get("X-Swash-Rows"), 10, 32)
	cols, _ := strconv.ParseInt(resp.Header.Get("X-Swash-Cols"), 10, 32)
	clientID := resp.Header.Get("X-Swash-Client-Id")

	screenBytes, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		conn.Close()
		return nil, err
	}

	// After consuming the body, br may still have buffered bytes that are part of the stream.
	stream := &bufferedConn{Conn: conn, r: br}

	return &TTYAttachment{
		Conn:       stream,
		Rows:       int32(rows),
		Cols:       int32(cols),
		ScreenANSI: string(screenBytes),
		ClientID:   clientID,
	}, nil
}

func (c *unixTTYClient) Detach(clientID string) error {
	body := map[string]string{"client_id": clientID}
	return c.doJSON(context.Background(), http.MethodPost, "/tty/detach", body, nil)
}

func (c *unixTTYClient) GetAttachedClients() (count int32, masterRows, masterCols int32, err error) {
	var out struct {
		Count int32 `json:"count"`
		Rows  int32 `json:"rows"`
		Cols  int32 `json:"cols"`
	}
	if err := c.doJSON(context.Background(), http.MethodGet, "/tty/clients", nil, &out); err != nil {
		return 0, 0, 0, err
	}
	return out.Count, out.Rows, out.Cols, nil
}

func (c *unixTTYClient) WaitExited() <-chan int32 {
	exitCh := make(chan int32, 1)

	go func() {
		defer close(exitCh)
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()

		for {
			select {
			case <-c.closed:
				return
			case <-t.C:
				st, err := c.Gist()
				if err != nil {
					exitCh <- -1
					return
				}
				if !st.Running && st.ExitCode != nil {
					exitCh <- int32(*st.ExitCode)
					return
				}
			}
		}
	}()

	return exitCh
}

func (c *unixSessionClient) getText(path string) (string, error) {
	req, err := c.newRequest(context.Background(), http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

func (c *unixSessionClient) doJSON(ctx context.Context, method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}

	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *unixSessionClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u := &url.URL{
		Scheme: "http",
		Host:   "unix",
		Path:   path,
	}
	return http.NewRequestWithContext(ctx, method, u.String(), body)
}

// bufferedConn uses a bufio.Reader for reads, preserving any bytes already buffered.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}
