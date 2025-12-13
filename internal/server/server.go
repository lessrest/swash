// Package server provides the WebUI server for swash.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"swa.sh/go/swash/cmd/swash/templates"
	"swa.sh/go/swash/internal/backend"
)

// Server is the WebUI server for swash.
type Server struct {
	backend backend.Backend
	mux     *http.ServeMux
	server  *http.Server
}

// New creates a new WebUI server with the given backend.
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
	// Health check for service discovery
	s.mux.HandleFunc("GET /health", s.handleHealth)

	// Session management
	s.mux.HandleFunc("GET /", s.handleListSessions)
	s.mux.HandleFunc("GET /sessions", s.handleListSessions)
	s.mux.HandleFunc("POST /sessions", s.handleStartSession)
	s.mux.HandleFunc("GET /sessions/new", s.handleNewSessionForm)
	s.mux.HandleFunc("GET /sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("POST /sessions/{id}/kill", s.handleKillSession)
	s.mux.HandleFunc("POST /sessions/{id}/input", s.handleSendInput)
	s.mux.HandleFunc("GET /sessions/{id}/output", s.handleSessionOutput)
	s.mux.HandleFunc("GET /sessions/{id}/screen", s.handleGetScreen)
	s.mux.HandleFunc("GET /history", s.handleHistory)
	s.mux.HandleFunc("GET /terminal/{id}", s.handleTerminalPage)
	s.mux.Handle("GET /sessions/{id}/attach", websocket.Handler(s.handleAttach))
}

// ServeUnix starts the server on a Unix socket, respecting context cancellation.
func (s *Server) ServeUnix(ctx context.Context, socketPath string) error {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}

	// Shutdown when context is cancelled
	go func() {
		<-ctx.Done()
		s.server.Shutdown(context.Background())
	}()

	err = s.server.Serve(ln)
	if err == http.ErrServerClosed {
		return ctx.Err()
	}
	return err
}

// Handlers

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok"))
}

func (s *Server) handleNewSessionForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	templates.NewSessionPage().Render(r.Context(), w)
}

func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	cmdStr := r.FormValue("command")
	if cmdStr == "" {
		http.Error(w, "command required", http.StatusBadRequest)
		return
	}
	command := strings.Fields(cmdStr)
	tty := r.FormValue("tty") == "true"

	opts := backend.SessionOptions{TTY: tty}

	sessionID, err := s.backend.StartSession(r.Context(), command, opts)
	if err != nil {
		http.Error(w, "starting session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if tty {
		http.Redirect(w, r, "/terminal/"+sessionID, http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/sessions/"+sessionID, http.StatusSeeOther)
	}
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

	w.Header().Set("Content-Type", "text/html")
	templates.SessionsPage(sessions).Render(r.Context(), w)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	session, err := s.getSessionByID(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	templates.SessionDetailPage(session).Render(r.Context(), w)
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

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := s.backend.KillSession(r.Context(), sessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// HTMX request - return partial
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html")
		templates.SessionKilled().Render(r.Context(), w)
		return
	}

	// Form submission - redirect
	http.Redirect(w, r, "/sessions", http.StatusSeeOther)
}

func (s *Server) handleSendInput(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	r.ParseForm()
	data := r.FormValue("data")

	_, err := s.backend.SendInput(r.Context(), sessionID, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/sessions/"+sessionID, http.StatusSeeOther)
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

	w.Header().Set("Content-Type", "text/html")
	templates.HistoryPage(sessions).Render(r.Context(), w)
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

// Client is a client for the WebUI server.
type Client struct {
	socketPath string
	httpClient *http.Client
}

// NewClient creates a new client for the WebUI server.
func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
			Timeout: 5 * time.Second,
		},
	}
}

// Health checks if the WebUI server is running.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost/health", nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %d", resp.StatusCode)
	}
	return nil
}
