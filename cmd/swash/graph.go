package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mbrock/swash/internal/graph"
	systemdproc "github.com/mbrock/swash/internal/platform/systemd/process"
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
	case "query":
		cmdGraphQuery(args[1:])
	case "quads":
		cmdGraphQuads(args[1:])
	case "load":
		cmdGraphLoad(args[1:])
	case "status":
		cmdGraphStatus()
	case "install":
		cmdGraphInstall()
	case "uninstall":
		cmdGraphUninstall()
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

	fmt.Fprintf(os.Stderr, "swash graph: initializing oxigraph runtime...\n")
	service, err := graph.New(ctx)
	if err != nil {
		fatal("creating graph service: %v", err)
	}
	defer service.Close()

	server := graph.NewServer(service, cfg.SocketPath)

	fmt.Fprintf(os.Stderr, "swash graph: listening on %s\n", cfg.SocketPath)
	if err := server.Run(ctx); err != nil {
		fatal("running graph server: %v", err)
	}
}

// cmdGraphQuery executes a SPARQL query against the graph service.
func cmdGraphQuery(args []string) {
	if len(args) == 0 {
		fatal("usage: swash graph query <sparql>")
	}

	cfg := graph.DefaultConfig()
	client := graph.NewClient(cfg.SocketPath)

	ctx := context.Background()
	results, err := client.Query(ctx, args[0])
	if err != nil {
		fatal("query failed: %v", err)
	}

	// Pretty print results
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(results)
}

// cmdGraphQuads queries quads with an optional pattern.
func cmdGraphQuads(args []string) {
	cfg := graph.DefaultConfig()
	client := graph.NewClient(cfg.SocketPath)

	// Parse pattern flags
	var pattern oxigraph.Pattern
	format := "" // default to TriG
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
			format = args[i]
		}
	}

	ctx := context.Background()
	data, err := client.Quads(ctx, pattern, format)
	if err != nil {
		fatal("quads failed: %v", err)
	}

	os.Stdout.Write(data)
}

// cmdGraphLoad loads RDF data from stdin or a file.
func cmdGraphLoad(args []string) {
	cfg := graph.DefaultConfig()
	client := graph.NewClient(cfg.SocketPath)

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

	ctx := context.Background()
	if err := client.Load(ctx, data, format); err != nil {
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
		fmt.Println("\nstart with: swash graph serve")
		fmt.Println("or install: swash graph install")
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

// cmdGraphInstall installs the graph service as a socket-activated systemd user service.
func cmdGraphInstall() {
	exe, err := os.Executable()
	if err != nil {
		fatal("finding executable: %v", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		fatal("resolving executable path: %v", err)
	}

	cfg := graph.DefaultConfig()

	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		fatal("finding config dir: %v", err)
	}
	unitDir := filepath.Join(userConfigDir, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		fatal("creating unit dir: %v", err)
	}

	// Socket unit - listens on Unix socket
	socketUnit := fmt.Sprintf(`[Unit]
Description=swash graph service socket

[Socket]
ListenStream=%s
SocketMode=0666

[Install]
WantedBy=sockets.target
`, cfg.SocketPath)

	socketPath := filepath.Join(unitDir, "swash-graph.socket")
	if err := os.WriteFile(socketPath, []byte(socketUnit), 0644); err != nil {
		fatal("writing socket unit: %v", err)
	}
	fmt.Printf("wrote %s\n", socketPath)

	// Service unit
	serviceUnit := fmt.Sprintf(`[Unit]
Description=swash graph service (RDF knowledge graph)

[Service]
ExecStart=%s graph serve
`, exe)

	servicePath := filepath.Join(unitDir, "swash-graph.service")
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

	if err := sd.EnableUnits(ctx, []string{"swash-graph.socket"}); err != nil {
		fatal("enabling socket: %v", err)
	}
	fmt.Println("enabled swash-graph.socket")

	if err := sd.StartUnit(ctx, systemdproc.UnitName("swash-graph.socket")); err != nil {
		fatal("starting socket: %v", err)
	}
	fmt.Printf("started swash-graph.socket at %s\n", cfg.SocketPath)
}

// cmdGraphUninstall removes the graph service systemd units.
func cmdGraphUninstall() {
	ctx := context.Background()
	sd, err := systemdproc.ConnectUserSystemd(ctx)
	if err != nil {
		fatal("connecting to systemd: %v", err)
	}
	defer sd.Close()

	// Stop and disable
	sd.StopUnit(ctx, systemdproc.UnitName("swash-graph.service"))
	sd.StopUnit(ctx, systemdproc.UnitName("swash-graph.socket"))
	sd.DisableUnits(ctx, []string{"swash-graph.socket"})
	fmt.Println("stopped and disabled swash-graph")

	// Remove unit files
	userConfigDir, _ := os.UserConfigDir()
	unitDir := filepath.Join(userConfigDir, "systemd", "user")
	os.Remove(filepath.Join(unitDir, "swash-graph.socket"))
	os.Remove(filepath.Join(unitDir, "swash-graph.service"))
	fmt.Println("removed unit files")

	sd.Reload(ctx)
	fmt.Println("reloaded systemd")
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
