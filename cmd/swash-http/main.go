// swash-http - HTTP API for swash sessions
//
// Provides REST, SSE, and WebSocket access to swash sessions.
// Designed for socket activation via systemd.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mbrock/swash/internal/swash"
	"golang.org/x/net/websocket"
)

var rt *swash.Runtime

func main() {
	var err error
	rt, err = swash.DefaultRuntime(context.Background())
	if err != nil {
		log.Fatalf("initializing runtime: %v", err)
	}
	defer rt.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions", handleListSessions)
	mux.HandleFunc("POST /sessions", handleStartSession)
	mux.HandleFunc("GET /sessions/new", handleNewSessionForm)
	mux.HandleFunc("GET /sessions/{id}", handleGetSession)
	mux.HandleFunc("DELETE /sessions/{id}", handleStopSession)
	mux.HandleFunc("POST /sessions/{id}/kill", handleKillSession)
	mux.HandleFunc("POST /sessions/{id}/input", handleSendInput)
	mux.HandleFunc("GET /sessions/{id}/output", handleSessionOutput)
	mux.HandleFunc("GET /sessions/{id}/screen", handleGetScreen)
	mux.HandleFunc("GET /history", handleHistory)
	mux.HandleFunc("GET /terminal/{id}", handleTerminalPage)
	mux.Handle("GET /sessions/{id}/attach", websocket.Handler(handleAttach))

	ln, err := getListener()
	if err != nil {
		log.Fatalf("getting listener: %v", err)
	}

	log.Printf("swash-http listening on %s", ln.Addr())

	if err := http.Serve(ln, mux); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

func getListener() (net.Listener, error) {
	// systemd socket activation: LISTEN_FDS=1 means fd 3 is our socket
	if os.Getenv("LISTEN_FDS") == "1" {
		f := os.NewFile(3, "systemd-socket")
		ln, err := net.FileListener(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("socket activation: %w", err)
		}
		return ln, nil
	}
	return net.Listen("tcp", "0.0.0.0:8484")
}

func handleNewSessionForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>New Session</title>
<style>
  body { font-family: system-ui; max-width: 600px; margin: 2rem auto; padding: 0 1rem; }
  label { display: block; margin-top: 1rem; font-weight: bold; }
  input[type=text] { width: 100%%; padding: 0.5rem; margin-top: 0.25rem; }
  input[type=checkbox] { margin-right: 0.5rem; }
  button { margin-top: 1rem; padding: 0.5rem 1rem; cursor: pointer; }
  nav { margin-bottom: 1rem; }
  a { color: #07c; }
</style>
</head><body>
<nav><a href="/sessions">← Sessions</a></nav>
<h1>New Session</h1>
<form method="POST" action="/sessions">
  <label>Command</label>
  <input type="text" name="command" placeholder="e.g. htop" required>
  <label><input type="checkbox" name="tty" value="true"> TTY mode</label>
  <button type="submit">Start</button>
</form>
</body></html>`)
}

func handleStartSession(w http.ResponseWriter, r *http.Request) {
	var command []string
	var tty bool

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		r.ParseForm()
		cmdStr := r.FormValue("command")
		if cmdStr == "" {
			http.Error(w, "command required", http.StatusBadRequest)
			return
		}
		// Simple shell-style split (TODO: proper parsing)
		command = strings.Fields(cmdStr)
		tty = r.FormValue("tty") == "true"
	} else {
		var req struct {
			Command []string          `json:"command"`
			TTY     bool              `json:"tty,omitempty"`
			Rows    int               `json:"rows,omitempty"`
			Cols    int               `json:"cols,omitempty"`
			Tags    map[string]string `json:"tags,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Command) == 0 {
			http.Error(w, "command required", http.StatusBadRequest)
			return
		}
		command = req.Command
		tty = req.TTY
	}

	// Find the swash binary for the host command
	hostCommand, err := findHostCommand()
	if err != nil {
		http.Error(w, "finding host command: "+err.Error(), http.StatusInternalServerError)
		return
	}

	opts := swash.SessionOptions{TTY: tty}

	sessionID, err := rt.StartSessionWithOptions(r.Context(), command, hostCommand, opts)
	if err != nil {
		http.Error(w, "starting session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// For HTML form submission, redirect to the session or terminal
	if wantsHTML(r) || strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if tty {
			http.Redirect(w, r, "/terminal/"+sessionID, http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/sessions/"+sessionID, http.StatusSeeOther)
		}
		return
	}

	// JSON response
	session, err := getSessionByID(r.Context(), sessionID)
	if err != nil {
		w.Header().Set("Location", "/sessions/"+sessionID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": sessionID})
		return
	}

	w.Header().Set("Location", "/sessions/"+sessionID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(session)
}

func findHostCommand() ([]string, error) {
	// Look for swash binary next to swash-http, or in PATH
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// swash-http -> swash (same directory)
	swashPath := self[:len(self)-5] // remove "-http"
	if _, err := os.Stat(swashPath); err == nil {
		return []string{swashPath, "host"}, nil
	}
	// Fall back to PATH
	return []string{"swash", "host"}, nil
}

func wantsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html")
}

func parseStarted(s string) time.Time {
	// Parse "Mon 2006-01-02 15:04:05 MST" format
	t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := rt.ListSessions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Sort by creation time, newest first
	sort.Slice(sessions, func(i, j int) bool {
		return parseStarted(sessions[i].Started).After(parseStarted(sessions[j].Started))
	})

	if wantsHTML(r) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>swash sessions</title>
<style>
  body { font-family: system-ui; max-width: 900px; margin: 2rem auto; padding: 0 1rem; }
  table { width: 100%%; border-collapse: collapse; }
  th, td { text-align: left; padding: 0.5rem; border-bottom: 1px solid #ddd; }
  a { color: #07c; }
  .actions { display: flex; gap: 0.5rem; }
  .time { color: #666; font-size: 0.9em; }
</style>
</head><body>
<h1>Sessions</h1>
<p><a href="/history">View history</a> · <a href="/sessions/new">+ New Session</a></p>
<table>
<tr><th>ID</th><th>Command</th><th>Started</th><th>Status</th><th>Actions</th></tr>`)
		for _, s := range sessions {
			started := parseStarted(s.Started)
			timeStr := started.Format("15:04:05")
			if time.Since(started) > 24*time.Hour {
				timeStr = started.Format("Jan 2 15:04")
			}
			fmt.Fprintf(w, `<tr>
<td><a href="/sessions/%s">%s</a></td>
<td>%s</td>
<td class="time">%s</td>
<td>%s</td>
<td class="actions">
  <a href="/sessions/%s/output">output</a>
  <a href="/sessions/%s/screen">screen</a>
  <a href="/terminal/%s">terminal</a>
</td>
</tr>`, s.ID, s.ID, s.Command, timeStr, s.Status, s.ID, s.ID, s.ID)
		}
		fmt.Fprint(w, `</table></body></html>`)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	session, err := getSessionByID(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if wantsHTML(r) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>swash - %s</title>
<style>
  body { font-family: system-ui; max-width: 800px; margin: 2rem auto; padding: 0 1rem; }
  dl { display: grid; grid-template-columns: auto 1fr; gap: 0.5rem; }
  dt { font-weight: bold; }
  a { color: #07c; }
  .actions { margin-top: 1rem; display: flex; gap: 1rem; flex-wrap: wrap; }
  .actions form { display: inline; }
  input, button { padding: 0.5rem; }
  button { cursor: pointer; }
  nav { margin-bottom: 1rem; }
</style>
</head><body>
<nav><a href="/sessions">← All Sessions</a></nav>
<h1>Session %s</h1>
<dl>
  <dt>Command</dt><dd>%s</dd>
  <dt>Status</dt><dd>%s</dd>
  <dt>PID</dt><dd>%d</dd>
  <dt>Started</dt><dd>%s</dd>
  <dt>Unit</dt><dd>%s</dd>
</dl>
<div class="actions">
  <a href="/terminal/%s">Open Terminal</a>
  <a href="/sessions/%s/output">Stream Output</a>
  <a href="/sessions/%s/screen">View Screen</a>
</div>
<div class="actions">
  <form method="POST" action="/sessions/%s/input">
    <input name="data" placeholder="Send input...">
    <button type="submit">Send</button>
  </form>
  <form method="POST" action="/sessions/%s/kill" onsubmit="return confirm('Kill this session?')">
    <button type="submit">Kill</button>
  </form>
  <form method="POST" action="/sessions/%s?_method=DELETE" onsubmit="return confirm('Stop this session?')">
    <button type="submit">Stop</button>
  </form>
</div>
</body></html>`,
			session.ID, session.ID, session.Command, session.Status,
			session.PID, session.Started, session.Unit,
			session.ID, session.ID, session.ID, session.ID, session.ID, session.ID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(session)
}

func getSessionByID(ctx context.Context, id string) (*swash.Session, error) {
	sessions, err := rt.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range sessions {
		if s.ID == id {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("session %s not found", id)
}

func handleStopSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := rt.StopSession(r.Context(), sessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

func handleKillSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := rt.KillSession(sessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		http.Redirect(w, r, "/sessions", http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "killed"})
}

func handleSendInput(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	var data string
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		r.ParseForm()
		data = r.FormValue("data")
	} else {
		var req struct {
			Data string `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		data = req.Data
	}

	n, err := rt.SendInput(sessionID, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect back to session page for form submissions
	if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		http.Redirect(w, r, "/sessions/"+sessionID, http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"bytes": n})
}

func handleGetScreen(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	screen, err := rt.GetScreen(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(screen))
}

func handleHistory(w http.ResponseWriter, r *http.Request) {
	sessions, err := rt.ListHistory(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func handleSessionOutput(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Use journal Follow to stream output
	matches := []swash.JournalMatch{swash.MatchSession(sessionID)}

	for entry := range rt.Journal.Follow(r.Context(), matches) {
		// Check for exit event
		if entry.Fields[swash.FieldEvent] == swash.EventExited {
			fmt.Fprintf(w, "event: exit\ndata: %s\n\n", entry.Fields[swash.FieldExitCode])
			flusher.Flush()
			return
		}

		// Output lines have FD field
		if entry.Fields["FD"] != "" && entry.Message != "" {
			data, _ := json.Marshal(map[string]any{
				"fd":   entry.Fields["FD"],
				"text": entry.Message,
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, terminalPageHTML, sessionID, sessionID)
}

func handleAttach(ws *websocket.Conn) {
	// Extract session ID from URL path: /sessions/{id}/attach
	path := ws.Request().URL.Path
	parts := strings.Split(path, "/")
	// parts: ["", "sessions", "{id}", "attach"]
	if len(parts) < 4 {
		log.Printf("attach: invalid path %s", path)
		return
	}
	sessionID := parts[2]

	// Connect to TTY session
	client, err := rt.ConnectTTYSession(sessionID)
	if err != nil {
		log.Printf("attach %s: connect failed: %v", sessionID, err)
		return
	}
	defer client.Close()

	// Attach to get FDs (request reasonable default size)
	outputFD, inputFD, rows, cols, screenANSI, _, err := client.Attach(24, 80)
	if err != nil {
		log.Printf("attach %s: attach failed: %v", sessionID, err)
		return
	}

	output := os.NewFile(uintptr(outputFD), "pty-output")
	input := os.NewFile(uintptr(inputFD), "pty-input")
	defer output.Close()
	defer input.Close()

	log.Printf("attach %s: connected %dx%d", sessionID, cols, rows)

	// Send initial screen state
	websocket.Message.Send(ws, screenANSI)

	// Bridge output FD -> WebSocket
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := output.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("attach %s: read error: %v", sessionID, err)
				}
				ws.Close()
				return
			}
			if n > 0 {
				if err := websocket.Message.Send(ws, string(buf[:n])); err != nil {
					return
				}
			}
		}
	}()

	// Bridge WebSocket -> input FD (with resize support)
	for {
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			if err != io.EOF {
				log.Printf("attach %s: ws receive error: %v", sessionID, err)
			}
			return
		}

		// Check for JSON control message
		if strings.HasPrefix(msg, "{") {
			var ctrl struct {
				Type string `json:"type"`
				Rows int32  `json:"rows"`
				Cols int32  `json:"cols"`
			}
			if json.Unmarshal([]byte(msg), &ctrl) == nil && ctrl.Type == "resize" {
				client.Resize(ctrl.Rows, ctrl.Cols)
				continue
			}
		}

		input.Write([]byte(msg))
	}
}

const terminalPageHTML = `<!DOCTYPE html>
<html>
<head>
  <title>swash - %s</title>
  <link rel="stylesheet" href="https://unpkg.com/xterm@5.3.0/css/xterm.css">
  <style>
    @import url('https://fonts.googleapis.com/css2?family=Input+Mono&display=swap');
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: #111;
      min-height: 100vh;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 1rem;
    }
    #terminal {
      border: 1px solid #333;
      border-radius: 4px;
      padding: 0.5rem;
      background: #000;
      max-width: calc(100vw - 2rem);
      max-height: calc(100vh - 2rem);
    }
    .xterm { height: 100%%; }
  </style>
</head>
<body>
  <div id="terminal"></div>
  <script src="https://unpkg.com/xterm@5.3.0/lib/xterm.js"></script>
  <script src="https://unpkg.com/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
  <script src="https://unpkg.com/xterm-addon-webgl@0.16.0/lib/xterm-addon-webgl.js"></script>
  <script>
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: '"Input Mono", monospace',
      fontSize: 14
    });
    const fitAddon = new FitAddon.FitAddon();
    const webglAddon = new WebglAddon.WebglAddon();
    term.loadAddon(fitAddon);
    term.open(document.getElementById('terminal'));
    term.loadAddon(webglAddon);
    fitAddon.fit();

    const ws = new WebSocket('ws://' + location.host + '/sessions/%s/attach');
    ws.onmessage = (e) => term.write(e.data);
    ws.onclose = () => term.write('\r\n[disconnected]');
    term.onData((data) => ws.send(data));

    function sendResize() {
      fitAddon.fit();
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({type: 'resize', rows: term.rows, cols: term.cols}));
      }
    }
    ws.onopen = sendResize;
    window.addEventListener('resize', sendResize);
  </script>
</body>
</html>`
