package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mbrock/swash/internal/backend"
	"github.com/mbrock/swash/internal/eventlog"
	"github.com/mbrock/swash/internal/graph"
	"github.com/mbrock/swash/pkg/oxigraph"
)

// cmdGraph handles the "swash graph" subcommand.
func cmdGraph(args []string) {
	if len(args) == 0 {
		cmdGraphStatus()
		return
	}

	switch args[0] {
	case "serve":
		cmdGraphServe(args[1:])
	case "start":
		cmdGraphStart(args[1:])
	case "stop":
		cmdGraphStop()
	case "query":
		cmdGraphQuery(args[1:])
	case "quads":
		cmdGraphQuads(args[1:])
	case "load":
		cmdGraphLoad(args[1:])
	case "status":
		cmdGraphStatus()
	case "restart":
		cmdGraphRestart()
	default:
		fatal("unknown graph command: %s", args[0])
	}
}

// cmdGraphServe runs the graph server.
func cmdGraphServe(args []string) {
	cfg := graph.DefaultConfig()

	// Parse flags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--socket", "-s":
			if i+1 >= len(args) {
				fatal("--socket requires a path")
			}
			i++
			cfg.SocketPath = args[i]
		}
	}

	ctx := context.Background()

	// Initialize backend to access lifecycle events
	bk, err := backend.Default(ctx)
	if err != nil {
		fatal("initializing backend: %v", err)
	}
	defer bk.Close()

	fmt.Fprintf(os.Stderr, "swash graph: initializing oxigraph runtime...\n")
	service, err := graph.New(ctx)
	if err != nil {
		fatal("creating graph service: %v", err)
	}
	defer service.Close()

	// Load existing lifecycle events from journal
	fmt.Fprintf(os.Stderr, "swash graph: loading events from journal...\n")
	events, _, err := bk.PollLifecycleEvents(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "swash graph: warning: failed to load events: %v\n", err)
	} else {
		var quadCount int
		for _, e := range events {
			quads := eventlog.EventToQuads(e)
			if len(quads) > 0 {
				if err := service.AddQuads(quads); err != nil {
					fmt.Fprintf(os.Stderr, "swash graph: warning: failed to add quads: %v\n", err)
				} else {
					quadCount += len(quads)
				}
			}
		}
		fmt.Fprintf(os.Stderr, "swash graph: loaded %d events (%d quads)\n", len(events), quadCount)
	}

	// Start following new events in background
	go func() {
		fmt.Fprintf(os.Stderr, "swash graph: following journal for new events...\n")
		for e := range bk.FollowLifecycleEvents(ctx) {
			quads := eventlog.EventToQuads(e)
			if len(quads) > 0 {
				if err := service.AddQuads(quads); err != nil {
					fmt.Fprintf(os.Stderr, "swash graph: warning: failed to add quads: %v\n", err)
				}
			}
		}
	}()

	server := graph.NewServer(service, cfg.SocketPath)

	fmt.Fprintf(os.Stderr, "swash graph: listening on %s\n", cfg.SocketPath)
	if err := server.Run(ctx); err != nil {
		fatal("running graph server: %v", err)
	}
}

// cmdGraphStart starts the graph service as a swash session.
func cmdGraphStart(args []string) {
	ctx := context.Background()

	bk, err := backend.Default(ctx)
	if err != nil {
		fatal("initializing backend: %v", err)
	}
	defer bk.Close()

	// Check if already running
	cfg := graph.DefaultConfig()
	client := graph.NewClient(cfg.SocketPath)
	if err := client.Health(ctx); err == nil {
		fmt.Println("graph service already running")
		return
	}

	// Find our executable path for the command
	exe, err := os.Executable()
	if err != nil {
		fatal("finding executable: %v", err)
	}

	// Start the graph server as a swash session
	sessionID, err := bk.StartSession(ctx, []string{exe, "graph", "serve"}, backend.SessionOptions{
		ServiceType: "graph",
	})
	if err != nil {
		fatal("starting graph service: %v", err)
	}

	fmt.Printf("started graph service (session %s)\n", sessionID)

	// Wait for it to be ready
	for range 50 {
		time.Sleep(100 * time.Millisecond)
		if err := client.Health(ctx); err == nil {
			stats, _ := client.Stats(ctx)
			fmt.Printf("ready, loaded %d quads\n", stats.Quads)
			return
		}
	}

	fmt.Println("warning: service started but not responding yet")
}

// cmdGraphStop stops the graph service session.
func cmdGraphStop() {
	ctx := context.Background()

	bk, err := backend.Default(ctx)
	if err != nil {
		fatal("initializing backend: %v", err)
	}
	defer bk.Close()

	// Find the graph service session
	sessionID, err := findServiceSession(ctx, bk, "graph")
	if err != nil {
		fmt.Printf("graph service not found: %v\n", err)
		return
	}

	if err := bk.StopSession(ctx, sessionID); err != nil {
		fatal("stopping graph service: %v", err)
	}

	fmt.Printf("stopped graph service (session %s)\n", sessionID)
}

// findServiceSession finds a running session by service type.
func findServiceSession(ctx context.Context, bk backend.Backend, serviceType string) (string, error) {
	// Query the graph for sessions with this service type
	// Use title case for the service type in the RDF class name
	className := strings.ToUpper(serviceType[:1]) + serviceType[1:] + "Service"
	sparql := fmt.Sprintf(`
		PREFIX swa: <https://swa.sh/ns#>
		PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
		SELECT ?session WHERE {
			?session rdf:type swa:%s .
			FILTER NOT EXISTS { ?session swa:exitedAt ?exit }
		} LIMIT 1
	`, className)

	results, err := bk.GraphQuery(ctx, sparql)
	if err != nil {
		// Graph not available, fall back to listing sessions
		return findServiceSessionFallback(ctx, bk, serviceType)
	}

	if len(results) == 0 {
		return "", fmt.Errorf("no running %s service found", serviceType)
	}

	// Extract session ID from URN (urn:swash:session:ID)
	sessionTerm, ok := results[0].Get("session")
	if !ok {
		return "", fmt.Errorf("no session binding in result")
	}
	sessionURI := sessionTerm.Value()
	const prefix = "urn:swash:session:"
	if !strings.HasPrefix(sessionURI, prefix) {
		return "", fmt.Errorf("invalid session URI: %s", sessionURI)
	}

	return sessionURI[len(prefix):], nil
}

// findServiceSessionFallback finds a service session by listing running sessions
// and checking their service type. Used when graph is not available.
func findServiceSessionFallback(ctx context.Context, bk backend.Backend, serviceType string) (string, error) {
	sessions, err := bk.ListSessions(ctx)
	if err != nil {
		return "", err
	}

	// Look for a session running "swash graph serve" for the graph service
	for _, s := range sessions {
		if serviceType == "graph" && strings.Contains(s.Command, "graph serve") {
			return s.ID, nil
		}
	}

	return "", fmt.Errorf("no running %s service found", serviceType)
}

// cmdGraphQuery executes a SPARQL query against the graph service.
func cmdGraphQuery(args []string) {
	if len(args) == 0 {
		fatal("usage: swash graph query <sparql>")
	}

	ctx := context.Background()

	bk, err := backend.Default(ctx)
	if err != nil {
		fatal("initializing backend: %v", err)
	}
	defer bk.Close()

	results, err := bk.GraphQuery(ctx, args[0])
	if err != nil {
		fatal("query failed: %v", err)
	}

	// Print as simple rows
	for _, sol := range results {
		vars := sol.Variables()
		for i, v := range vars {
			if i > 0 {
				fmt.Print("\t")
			}
			if term, ok := sol.Get(v); ok {
				fmt.Print(term.Value())
			}
		}
		fmt.Println()
	}
}

// cmdGraphQuads queries quads with an optional pattern.
func cmdGraphQuads(args []string) {
	ctx := context.Background()

	// Parse pattern flags
	var pattern oxigraph.Pattern
	format := oxigraph.TriG // default
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-s", "--subject":
			if i+1 >= len(args) {
				fatal("-s requires a value")
			}
			i++
			pattern.Subject = parseTermArg(args[i])
		case "-p", "--predicate":
			if i+1 >= len(args) {
				fatal("-p requires a value")
			}
			i++
			n := oxigraph.IRI(args[i])
			pattern.Predicate = &n
		case "-o", "--object":
			if i+1 >= len(args) {
				fatal("-o requires a value")
			}
			i++
			pattern.Object = parseTermArg(args[i])
		case "-f", "--format":
			if i+1 >= len(args) {
				fatal("-f requires a value (trig, nquads)")
			}
			i++
			if args[i] == "nquads" {
				format = oxigraph.NQuads
			}
		}
	}

	bk, err := backend.Default(ctx)
	if err != nil {
		fatal("initializing backend: %v", err)
	}
	defer bk.Close()

	data, err := bk.GraphSerialize(ctx, pattern, format)
	if err != nil {
		fatal("quads failed: %v", err)
	}

	os.Stdout.Write(data)
}

// cmdGraphLoad loads RDF data from stdin or a file.
func cmdGraphLoad(args []string) {
	ctx := context.Background()

	format := oxigraph.NTriples
	var data []byte
	var err error

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--format":
			if i+1 >= len(args) {
				fatal("-f requires a format (ntriples, turtle)")
			}
			i++
			switch args[i] {
			case "ntriples", "nt":
				format = oxigraph.NTriples
			case "turtle", "ttl":
				format = oxigraph.Turtle
			default:
				fatal("unknown format: %s", args[i])
			}
		case "-":
			// Read from stdin
			data, err = readAll(os.Stdin)
			if err != nil {
				fatal("reading stdin: %v", err)
			}
		default:
			// Treat as file path
			data, err = os.ReadFile(args[i])
			if err != nil {
				fatal("reading file: %v", err)
			}
		}
	}

	if len(data) == 0 {
		// Default to stdin
		data, err = readAll(os.Stdin)
		if err != nil {
			fatal("reading stdin: %v", err)
		}
	}

	bk, err := backend.Default(ctx)
	if err != nil {
		fatal("initializing backend: %v", err)
	}
	defer bk.Close()

	if err := bk.GraphLoad(ctx, data, format); err != nil {
		fatal("load failed: %v", err)
	}
	fmt.Println("loaded")
}

// cmdGraphStatus shows the status of the graph service.
func cmdGraphStatus() {
	cfg := graph.DefaultConfig()
	client := graph.NewClient(cfg.SocketPath)

	ctx := context.Background()

	// Check health
	if err := client.Health(ctx); err != nil {
		fmt.Printf("graph service: not running (%v)\n", err)
		fmt.Printf("socket: %s\n", cfg.SocketPath)
		fmt.Println("\nstart with: swash graph start")
		return
	}

	stats, err := client.Stats(ctx)
	if err != nil {
		fmt.Printf("graph service: running (stats unavailable: %v)\n", err)
		return
	}

	fmt.Printf("graph service: running\n")
	fmt.Printf("socket: %s\n", cfg.SocketPath)
	fmt.Printf("quads: %d\n", stats.Quads)
}

// cmdGraphRestart restarts the graph service to reload data from the journal.
func cmdGraphRestart() {
	ctx := context.Background()
	cfg := graph.DefaultConfig()

	// Check if service is running
	graphClient := graph.NewClient(cfg.SocketPath)
	if err := graphClient.Health(ctx); err != nil {
		fmt.Println("graph service not running")
		return
	}

	bk, err := backend.Default(ctx)
	if err != nil {
		fatal("initializing backend: %v", err)
	}
	defer bk.Close()

	// Find the graph service session
	sessionID, err := findServiceSession(ctx, bk, "graph")
	if err != nil {
		fatal("graph service session not found: %v", err)
	}

	// Connect to the session and call Restart
	sessionClient, err := bk.ConnectSession(sessionID)
	if err != nil {
		fatal("connecting to session: %v", err)
	}
	defer sessionClient.Close()

	fmt.Printf("restarting graph service (session %s)...\n", sessionID)
	if err := sessionClient.Restart(); err != nil {
		fatal("restart failed: %v", err)
	}

	// Wait for service to come back up
	for range 50 {
		time.Sleep(100 * time.Millisecond)
		if err := graphClient.Health(ctx); err == nil {
			break
		}
	}

	stats, _ := graphClient.Stats(ctx)
	fmt.Printf("restarted, loaded %d quads\n", stats.Quads)
}

// parseTermArg parses a term from a command line argument.
func parseTermArg(s string) oxigraph.Term {
	// Simple heuristics for now
	if len(s) > 0 && s[0] == '"' {
		// Literal
		return oxigraph.StringLiteral(s[1 : len(s)-1])
	}
	// Default to IRI
	return oxigraph.IRI(s)
}

// readAll reads all bytes from a reader.
func readAll(r *os.File) ([]byte, error) {
	var buf []byte
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return buf, err
		}
	}
	return buf, nil
}
