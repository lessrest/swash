// Package server provides the HTTP API server for swash.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"github.com/mbrock/swash/cmd/swash/templates"
	"github.com/mbrock/swash/internal/backend"
)

// Server is the HTTP API server for swash.
type Server struct {
	backend backend.Backend
	mux     *http.ServeMux
	server  *http.Server
}

// New creates a new HTTP server with the given backend.
func New(bk backend.Backend) *Server {
	s := &Server{
		backend: bk,
		mux:     http.NewServeMux(),
	}
	s.registerRoutes()
	s.server = &http.Server{Handler: s.mux}
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /sessions", s.handleListSessions)
	s.mux.HandleFunc("POST /sessions", s.handleStartSession)
	s.mux.HandleFunc("GET /sessions/new", s.handleNewSessionForm)
	s.mux.HandleFunc("GET /sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("DELETE /sessions/{id}", s.handleStopSession)
	s.mux.HandleFunc("POST /sessions/{id}/kill", s.handleKillSession)
	s.mux.HandleFunc("POST /sessions/{id}/input", s.handleSendInput)
	s.mux.HandleFunc("GET /sessions/{id}/output", s.handleSessionOutput)
	s.mux.HandleFunc("GET /sessions/{id}/screen", s.handleGetScreen)
	s.mux.HandleFunc("GET /history", s.handleHistory)
	s.mux.HandleFunc("GET /terminal/{id}", s.handleTerminalPage)
	s.mux.Handle("GET /sessions/{id}/attach", websocket.Handler(s.handleAttach))
}

// Serve starts the server on the given listener.
func (s *Server) Serve(ln net.Listener) error {
	return s.server.Serve(ln)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// GetListener returns a listener based on environment.
// Supports systemd socket activation (LISTEN_FDS=1) or falls back to the given address.
func GetListener(socketPath, defaultAddr string) (net.Listener, error) {
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
	// Unix socket if specified
	if socketPath != "" {
		os.Remove(socketPath) // clean up stale socket
		return net.Listen("unix", socketPath)
	}
	return net.Listen("tcp", defaultAddr)
}

// Handlers

func (s *Server) handleNewSessionForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	templates.NewSessionPage().Render(r.Context(), w)
}

func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
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

	opts := backend.SessionOptions{TTY: tty}

	sessionID, err := s.backend.StartSession(r.Context(), command, opts)
	if err != nil {
		http.Error(w, "starting session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if wantsHTML(r) || strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if tty {
			http.Redirect(w, r, "/terminal/"+sessionID, http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/sessions/"+sessionID, http.StatusSeeOther)
		}
		return
	}

	session, err := s.getSessionByID(r.Context(), sessionID)
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

func wantsJSON(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")
}

func wantsHTML(r *http.Request) bool {
	return !wantsJSON(r)
}

func parseStarted(s string) time.Time {
	t, _ := time.Parse("Mon 2006-01-02 15:04:05 MST", s)
	return t
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.backend.ListSessions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sort.Slice(sessions, func(i, j int) bool {
		return parseStarted(sessions[i].Started).After(parseStarted(sessions[j].Started))
	})

	if wantsHTML(r) {
		w.Header().Set("Content-Type", "text/html")
		templates.SessionsPage(sessions).Render(r.Context(), w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	session, err := s.getSessionByID(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if wantsHTML(r) {
		w.Header().Set("Content-Type", "text/html")
		templates.SessionDetailPage(session).Render(r.Context(), w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(session)
}

func (s *Server) getSessionByID(ctx context.Context, id string) (*backend.Session, error) {
	sessions, err := s.backend.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, sess := range sessions {
		if sess.ID == id {
			return &sess, nil
		}
	}
	return nil, fmt.Errorf("session %s not found", id)
}

func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := s.backend.StopSession(r.Context(), sessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := s.backend.KillSession(r.Context(), sessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html")
		templates.SessionKilled().Render(r.Context(), w)
		return
	}

	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		http.Redirect(w, r, "/sessions", http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "killed"})
}

func (s *Server) handleSendInput(w http.ResponseWriter, r *http.Request) {
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

	n, err := s.backend.SendInput(r.Context(), sessionID, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		http.Redirect(w, r, "/sessions/"+sessionID, http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"bytes": n})
}

func (s *Server) handleGetScreen(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	screen, err := s.backend.GetScreen(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(screen))
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.backend.ListHistory(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if wantsHTML(r) {
		w.Header().Set("Content-Type", "text/html")
		templates.HistoryPage(sessions).Render(r.Context(), w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func (s *Server) handleSessionOutput(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	cursor := ""
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()

	for {
		events, newCursor, err := s.backend.PollSessionOutput(r.Context(), sessionID, cursor)
		if err == nil {
			cursor = newCursor
			for _, e := range events {
				data, _ := json.Marshal(map[string]any{
					"fd":   e.FD,
					"text": e.Text,
				})
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}

		if code, ok := s.sessionExitCode(r.Context(), sessionID); ok {
			fmt.Fprintf(w, "event: exit\ndata: %s\n\n", code)
			flusher.Flush()
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) sessionExitCode(ctx context.Context, sessionID string) (string, bool) {
	if client, err := s.backend.ConnectSession(sessionID); err == nil {
		defer client.Close()
		st, err := client.Gist()
		if err == nil && !st.Running && st.ExitCode != nil {
			return fmt.Sprintf("%d", *st.ExitCode), true
		}
	}

	hist, err := s.backend.ListHistory(ctx)
	if err != nil {
		return "", false
	}
	for _, h := range hist {
		if h.ID != sessionID {
			continue
		}
		if h.ExitCode != nil {
			return fmt.Sprintf("%d", *h.ExitCode), true
		}
		return "", true
	}
	return "", false
}

func (s *Server) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	w.Header().Set("Content-Type", "text/html")
	templates.TerminalPage(sessionID).Render(r.Context(), w)
}

func (s *Server) handleAttach(ws *websocket.Conn) {
	path := ws.Request().URL.Path
	parts := strings.Split(path, "/")
	if len(parts) < 4 {
		return
	}
	sessionID := parts[2]

	client, err := s.backend.ConnectTTYSession(sessionID)
	if err != nil {
		return
	}
	defer client.Close()

	att, err := client.Attach(24, 80)
	if err != nil {
		return
	}
	if att == nil || att.Conn == nil {
		return
	}
	defer att.Conn.Close()

	websocket.Message.Send(ws, att.ScreenANSI)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := att.Conn.Read(buf)
			if err != nil {
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

	for {
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			if err != io.EOF {
				// log error
			}
			return
		}

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

		if _, err := att.Conn.Write([]byte(msg)); err != nil {
			return
		}
	}
}
