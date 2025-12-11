package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mbrock/swash/cmd/swash/templates"
	"github.com/mbrock/swash/internal/eventlog"
	systemdproc "github.com/mbrock/swash/internal/platform/systemd/process"
	swrt "github.com/mbrock/swash/internal/runtime"
	"golang.org/x/net/websocket"
)

// cmdHTTP handles the "swash http" subcommand and its sub-subcommands.
func cmdHTTP(args []string) {
	if len(args) == 0 {
		// Default: run the server
		runHTTPServer()
		return
	}

	switch args[0] {
	case "install":
		port := "8484"
		if len(args) > 1 {
			port = args[1]
		}
		httpInstall(port)
	case "uninstall":
		httpUninstall()
	case "status":
		httpStatus()
	default:
		fmt.Fprintf(os.Stderr, "unknown http subcommand: %s\n", args[0])
		fmt.Fprintf(os.Stderr, "usage: swash http [install [port]|uninstall|status]\n")
		os.Exit(1)
	}
}

func httpInstall(port string) {
	exe, err := os.Executable()
	if err != nil {
		fatal("finding executable: %v", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		fatal("resolving executable path: %v", err)
	}

	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		fatal("finding config dir: %v", err)
	}
	unitDir := filepath.Join(userConfigDir, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		fatal("creating unit dir: %v", err)
	}

	// Socket unit
	socketUnit := fmt.Sprintf(`[Unit]
Description=swash HTTP API socket

[Socket]
ListenStream=0.0.0.0:%s

[Install]
WantedBy=sockets.target
`, port)

	socketPath := filepath.Join(unitDir, "swash-http.socket")
	if err := os.WriteFile(socketPath, []byte(socketUnit), 0644); err != nil {
		fatal("writing socket unit: %v", err)
	}
	fmt.Printf("wrote %s\n", socketPath)

	// Service unit
	serviceUnit := fmt.Sprintf(`[Unit]
Description=swash HTTP API server

[Service]
ExecStart=%s http
`, exe)

	servicePath := filepath.Join(unitDir, "swash-http.service")
	if err := os.WriteFile(servicePath, []byte(serviceUnit), 0644); err != nil {
		fatal("writing service unit: %v", err)
	}
	fmt.Printf("wrote %s\n", servicePath)

	// Connect to systemd and enable
	ctx := context.Background()
	sd, err := systemdproc.ConnectUserSystemd(ctx)
	if err != nil {
		fatal("connecting to systemd: %v", err)
	}
	defer sd.Close()

	if err := sd.Reload(ctx); err != nil {
		fatal("daemon-reload: %v", err)
	}
	fmt.Println("reloaded systemd")

	if err := sd.EnableUnits(ctx, []string{"swash-http.socket"}); err != nil {
		fatal("enabling socket: %v", err)
	}
	fmt.Println("enabled swash-http.socket")

	if err := sd.StartUnit(ctx, systemdproc.UnitName("swash-http.socket")); err != nil {
		fatal("starting socket: %v", err)
	}
	fmt.Printf("started swash-http.socket on port %s\n", port)
}

func httpUninstall() {
	ctx := context.Background()
	sd, err := systemdproc.ConnectUserSystemd(ctx)
	if err != nil {
		fatal("connecting to systemd: %v", err)
	}
	defer sd.Close()

	// Stop and disable
	sd.StopUnit(ctx, systemdproc.UnitName("swash-http.service"))
	sd.StopUnit(ctx, systemdproc.UnitName("swash-http.socket"))
	sd.DisableUnits(ctx, []string{"swash-http.socket"})
	fmt.Println("stopped and disabled swash-http")

	// Remove unit files
	userConfigDir, _ := os.UserConfigDir()
	unitDir := filepath.Join(userConfigDir, "systemd", "user")
	os.Remove(filepath.Join(unitDir, "swash-http.socket"))
	os.Remove(filepath.Join(unitDir, "swash-http.service"))
	fmt.Println("removed unit files")

	sd.Reload(ctx)
	fmt.Println("reloaded systemd")
}

func httpStatus() {
	ctx := context.Background()
	sd, err := systemdproc.ConnectUserSystemd(ctx)
	if err != nil {
		fatal("connecting to systemd: %v", err)
	}
	defer sd.Close()

	socketUnit, err := sd.GetUnit(ctx, systemdproc.UnitName("swash-http.socket"))
	if err != nil {
		fmt.Println("swash-http.socket: not installed")
	} else {
		fmt.Printf("swash-http.socket: %s\n", socketUnit.State)
	}

	serviceUnit, err := sd.GetUnit(ctx, systemdproc.UnitName("swash-http.service"))
	if err != nil {
		fmt.Println("swash-http.service: not running")
	} else {
		fmt.Printf("swash-http.service: %s (PID %d)\n", serviceUnit.State, serviceUnit.MainPID)
	}
}

// HTTP Server implementation (moved from cmd/swash-http/main.go)

func runHTTPServer() {
	var err error
	rt, err = swrt.DefaultRuntime(context.Background())
	if err != nil {
		fatal("initializing runtime: %v", err)
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
		fatal("getting listener: %v", err)
	}

	fmt.Printf("swash http listening on %s\n", ln.Addr())

	if err := http.Serve(ln, mux); err != nil {
		fatal("http server: %v", err)
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
	templates.NewSessionPage().Render(r.Context(), w)
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

	hostCommand := findHostCommand()
	opts := swrt.SessionOptions{TTY: tty}

	sessionID, err := rt.StartSessionWithOptions(r.Context(), command, hostCommand, opts)
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

func handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := rt.ListSessions(r.Context())
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

func handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	session, err := getSessionByID(r.Context(), sessionID)
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

func getSessionByID(ctx context.Context, id string) (*swrt.Session, error) {
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

	// Check if this is an htmx request
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

	if wantsHTML(r) {
		w.Header().Set("Content-Type", "text/html")
		templates.HistoryPage(sessions).Render(r.Context(), w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func handleSessionOutput(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	filters := []eventlog.EventFilter{eventlog.FilterBySession(sessionID)}

	for entry := range rt.Events.Follow(r.Context(), filters) {
		if entry.Fields[eventlog.FieldEvent] == eventlog.EventExited {
			fmt.Fprintf(w, "event: exit\ndata: %s\n\n", entry.Fields[eventlog.FieldExitCode])
			flusher.Flush()
			return
		}

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
	templates.TerminalPage(sessionID).Render(r.Context(), w)
}

func handleAttach(ws *websocket.Conn) {
	path := ws.Request().URL.Path
	parts := strings.Split(path, "/")
	if len(parts) < 4 {
		return
	}
	sessionID := parts[2]

	client, err := rt.Control.ConnectTTYSession(sessionID)
	if err != nil {
		return
	}
	defer client.Close()

	outputFD, inputFD, _, _, screenANSI, _, err := client.Attach(24, 80)
	if err != nil {
		return
	}

	output := os.NewFile(uintptr(outputFD), "pty-output")
	input := os.NewFile(uintptr(inputFD), "pty-input")
	defer output.Close()
	defer input.Close()

	websocket.Message.Send(ws, screenANSI)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := output.Read(buf)
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

		input.Write([]byte(msg))
	}
}
