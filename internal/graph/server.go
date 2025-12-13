package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/mbrock/swash/pkg/oxigraph"
)

// Server is an HTTP server for the graph service.
type Server struct {
	service    *Service
	socketPath string
	listener   net.Listener
	server     *http.Server
}

// NewServer creates a new HTTP server for the graph service.
func NewServer(service *Service, socketPath string) *Server {
	s := &Server{
		service:    service,
		socketPath: socketPath,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sparql", s.handleSPARQL)
	mux.HandleFunc("GET /sparql", s.handleSPARQL) // Allow GET with ?query= param
	mux.HandleFunc("GET /quads", s.handleQuads)
	mux.HandleFunc("POST /load", s.handleLoad)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /stats", s.handleStats)

	s.server = &http.Server{Handler: mux}
	return s
}

// Run starts the server and blocks until stopped.
// It handles SIGTERM/SIGINT for graceful shutdown.
func (s *Server) Run(ctx context.Context) error {
	listener, err := s.getListener()
	if err != nil {
		return err
	}
	s.listener = listener

	slog.Info("graph server starting", "socket", s.socketPath)

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.server.Serve(listener)
	}()

	select {
	case err := <-errCh:
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-sigCh:
		slog.Info("graph server shutting down")
		return s.server.Shutdown(ctx)
	case <-ctx.Done():
		slog.Info("graph server context cancelled")
		return s.server.Shutdown(context.Background())
	}
}

// getListener returns a listener, using systemd socket activation if available.
func (s *Server) getListener() (net.Listener, error) {
	// systemd socket activation: LISTEN_FDS=1 means fd 3 is our socket
	if os.Getenv("LISTEN_FDS") == "1" {
		f := os.NewFile(3, "systemd-socket")
		ln, err := net.FileListener(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("socket activation: %w", err)
		}
		slog.Info("using socket activation")
		return ln, nil
	}

	// Create our own socket
	if err := os.MkdirAll(socketDir(s.socketPath), 0755); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	// Remove existing socket
	os.Remove(s.socketPath)

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on socket: %w", err)
	}

	// Make socket accessible
	if err := os.Chmod(s.socketPath, 0666); err != nil {
		listener.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}

	return listener, nil
}

// SocketPath returns the socket path.
func (s *Server) SocketPath() string {
	return s.socketPath
}

func socketDir(socketPath string) string {
	for i := len(socketPath) - 1; i >= 0; i-- {
		if socketPath[i] == '/' {
			return socketPath[:i]
		}
	}
	return "."
}

// handleSPARQL executes a SPARQL query.
// POST: query in body
// GET: query in ?query= param
// Accept header determines format: application/sparql-results+json (default),
// application/sparql-results+xml, text/csv, text/tab-separated-values
func (s *Server) handleSPARQL(w http.ResponseWriter, r *http.Request) {
	var query string

	if r.Method == http.MethodPost {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		query = string(body)
	} else {
		query = r.URL.Query().Get("query")
	}

	if query == "" {
		http.Error(w, "query required", http.StatusBadRequest)
		return
	}

	// Determine output format from Accept header
	format := oxigraph.ResultsJSON
	contentType := "application/sparql-results+json"

	accept := r.Header.Get("Accept")
	switch accept {
	case "application/sparql-results+xml":
		format = oxigraph.ResultsXML
		contentType = "application/sparql-results+xml"
	case "text/csv":
		format = oxigraph.ResultsCSV
		contentType = "text/csv"
	case "text/tab-separated-values":
		format = oxigraph.ResultsTSV
		contentType = "text/tab-separated-values"
	}

	data, err := s.service.QueryResults(query, format)
	if err != nil {
		http.Error(w, fmt.Sprintf("query failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Write(data)
}

// handleQuads returns quads matching a pattern in N-Quads or TriG format.
// Query params: s (subject), p (predicate), o (object), g (graph), format (nquads, trig)
// Each term can be an IRI (<...>) or omitted to match any.
func (s *Server) handleQuads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pattern := oxigraph.Pattern{}

	if subj := q.Get("s"); subj != "" {
		if term := parseTermParam(subj); term != nil {
			pattern.Subject = term
		}
	}
	if pred := q.Get("p"); pred != "" {
		if iri := parseIRIParam(pred); iri != nil {
			pattern.Predicate = iri
		}
	}
	if obj := q.Get("o"); obj != "" {
		if term := parseTermParam(obj); term != nil {
			pattern.Object = term
		}
	}
	if graph := q.Get("g"); graph != "" {
		if term := parseTermParam(graph); term != nil {
			pattern.Graph = term
		}
	}

	// Determine output format
	format := oxigraph.TriG
	contentType := "application/trig"
	if q.Get("format") == "nquads" {
		format = oxigraph.NQuads
		contentType = "application/n-quads"
	}

	data, err := s.service.Serialize(pattern, format)
	if err != nil {
		http.Error(w, fmt.Sprintf("serialization failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Write(data)
}

// handleLoad loads RDF data into the store.
// POST body is the RDF data, Content-Type specifies format.
func (s *Server) handleLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Determine format from Content-Type
	format := oxigraph.NTriples
	ct := r.Header.Get("Content-Type")
	switch ct {
	case "text/turtle", "application/x-turtle":
		format = oxigraph.Turtle
	case "application/n-triples", "text/plain":
		format = oxigraph.NTriples
	}

	if err := s.service.Load(data, format); err != nil {
		http.Error(w, fmt.Sprintf("load failed: %v", err), http.StatusInternalServerError)
		return
	}

	count, _ := s.service.Len()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"loaded":     true,
		"totalQuads": count,
	})
}

// handleHealth returns service health status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
	})
}

// handleStats returns service statistics.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	count, err := s.service.Len()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get stats: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"quads": count,
	})
}

// parseTermParam parses a term from a query parameter.
// Supports: <iri>, _:bnode, "literal", "literal"@lang, "literal"^^<type>
func parseTermParam(s string) oxigraph.Term {
	if len(s) == 0 {
		return nil
	}

	// IRI: <...>
	if s[0] == '<' && s[len(s)-1] == '>' {
		return oxigraph.IRI(s[1 : len(s)-1])
	}

	// Blank node: _:...
	if len(s) > 2 && s[0] == '_' && s[1] == ':' {
		return oxigraph.BlankNode{ID: s[2:]}
	}

	// Literal: "..."
	if s[0] == '"' {
		// Find closing quote
		for i := len(s) - 1; i > 0; i-- {
			if s[i] == '"' {
				val := s[1:i]
				rest := s[i+1:]

				if len(rest) > 0 && rest[0] == '@' {
					return oxigraph.LangLiteral(val, rest[1:])
				}
				if len(rest) > 4 && rest[:2] == "^^" && rest[2] == '<' && rest[len(rest)-1] == '>' {
					return oxigraph.TypedLiteral(val, rest[3:len(rest)-1])
				}
				return oxigraph.StringLiteral(val)
			}
		}
	}

	// Bare string treated as IRI
	return oxigraph.IRI(s)
}

// parseIRIParam parses an IRI from a query parameter.
func parseIRIParam(s string) *oxigraph.NamedNode {
	if len(s) == 0 {
		return nil
	}

	// IRI: <...>
	if s[0] == '<' && s[len(s)-1] == '>' {
		n := oxigraph.IRI(s[1 : len(s)-1])
		return &n
	}

	// Bare string treated as IRI
	n := oxigraph.IRI(s)
	return &n
}
