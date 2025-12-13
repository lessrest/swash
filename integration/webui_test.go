package integration

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/websocket"
)

// TestWebUI runs all WebUI server tests sequentially with a single server instance
func TestWebUI(t *testing.T) {
	e := getEnv(t)

	// Skip on posix backend - TTY attach over HTTP not fully supported yet
	if e.mode == "posix" {
		t.Skip("WebUI tests not yet supported on posix backend")
	}

	// Use Unix socket in temp dir
	socketPath := filepath.Join(e.tmpDir, "webui.sock")

	// Start WebUI server with Unix socket
	cmd := exec.Command(e.swashBin, "webui", "serve", "--socket", socketPath)
	cmd.Env = e.getEnvVars()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting webui server: %v", err)
	}
	defer func() {
		// SIGTERM to allow coverage flush
		syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
		// Wait for process to exit, with timeout
		done := make(chan struct{})
		go func() {
			cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}()

	// HTTP client that connects via Unix socket
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}
	// Base URL is fake - we connect via Unix socket
	baseURL := "http://localhost"

	// Wait for server to be ready
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Helper to get parsed HTML
	getDoc := func(path string) *goquery.Document {
		resp, err := client.Get(baseURL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		doc, err := goquery.NewDocumentFromReader(resp.Body)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		return doc
	}

	t.Run("Health", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/health")
		if err != nil {
			t.Fatalf("GET /health: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("SessionsList", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/sessions")
		if err != nil {
			t.Fatalf("GET /sessions: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			var buf bytes.Buffer
			buf.ReadFrom(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, buf.String())
		}

		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		body := buf.String()

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}

		// Verify page structure
		if doc.Find("h1").Length() == 0 {
			t.Errorf("expected h1 heading, got: %s", body[:min(500, len(body))])
		}
	})

	t.Run("History", func(t *testing.T) {
		// Run a command that exits
		e.runSwash("run", "echo", "for-history")
		time.Sleep(300 * time.Millisecond)

		doc := getDoc("/history")

		if doc.Find("h1").Length() == 0 {
			t.Error("expected h1 heading")
		}
	})

	t.Run("GetScreen", func(t *testing.T) {
		// Start a TTY session that outputs something
		stdout, _, err := e.runSwash("start", "--tty", "--rows", "10", "--cols", "40", "--", "sh", "-c", "echo SCREENTEST; sleep 30")
		if err != nil {
			t.Fatalf("start tty session: %v", err)
		}
		sessionID := strings.TrimSpace(strings.Fields(stdout)[0])
		defer e.runSwash("kill", sessionID)
		time.Sleep(500 * time.Millisecond)

		resp, err := client.Get(baseURL + "/sessions/" + sessionID + "/screen")
		if err != nil {
			t.Fatalf("GET screen: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}

		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		screen := buf.String()

		if !strings.Contains(screen, "SCREENTEST") {
			t.Errorf("expected SCREENTEST in screen, got: %q", screen)
		}
	})

	t.Run("WebSocketAttach", func(t *testing.T) {
		// Start a TTY session
		stdout, _, err := e.runSwash("start", "--tty", "--rows", "10", "--cols", "40", "--", "cat")
		if err != nil {
			t.Fatalf("start tty session: %v", err)
		}
		sessionID := strings.TrimSpace(strings.Fields(stdout)[0])
		defer e.runSwash("kill", sessionID)
		time.Sleep(300 * time.Millisecond)

		// Connect WebSocket via Unix socket
		wsConfig, err := websocket.NewConfig("ws://localhost/sessions/"+sessionID+"/attach", "http://localhost/")
		if err != nil {
			t.Fatalf("websocket config: %v", err)
		}
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			t.Fatalf("dial unix: %v", err)
		}
		ws, err := websocket.NewClient(wsConfig, conn)
		if err != nil {
			conn.Close()
			t.Fatalf("websocket client: %v", err)
		}
		defer ws.Close()

		// Should receive initial screen
		var initialScreen string
		ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := websocket.Message.Receive(ws, &initialScreen); err != nil {
			t.Fatalf("receive initial screen: %v", err)
		}

		// Send some input
		testInput := "WSTEST123\n"
		if err := websocket.Message.Send(ws, testInput); err != nil {
			t.Fatalf("send input: %v", err)
		}

		// Read output (cat echoes back)
		var output string
		ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		for range 5 {
			var msg string
			if err := websocket.Message.Receive(ws, &msg); err != nil {
				break
			}
			output += msg
			if strings.Contains(output, "WSTEST123") {
				break
			}
		}

		if !strings.Contains(output, "WSTEST123") {
			t.Errorf("expected echo of input, got: %q", output)
		}
	})
}
