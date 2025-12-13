package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"swa.sh/go/swash/internal/backend"
	"swa.sh/go/swash/internal/server"
)

// cmdWebUI handles the "swash webui" subcommand.
func cmdWebUI(args []string) {
	if len(args) == 0 {
		cmdWebUIStatus()
		return
	}

	switch args[0] {
	case "serve":
		cmdWebUIServe(args[1:])
	case "start":
		cmdWebUIStart()
	case "stop":
		cmdWebUIStop()
	case "status":
		cmdWebUIStatus()
	default:
		fatal("unknown webui command: %s", args[0])
	}
}

// webUISocketPath returns the path to the webui Unix socket.
func webUISocketPath() string {
	return runtimeDir() + "/webui.sock"
}

// cmdWebUIServe runs the webui server.
func cmdWebUIServe(args []string) {
	socketPath := webUISocketPath()

	// Parse flags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--socket", "-s":
			if i+1 >= len(args) {
				fatal("--socket requires a path")
			}
			i++
			socketPath = args[i]
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
	}()

	initBackend()
	defer bk.Close()

	srv := server.New(bk)

	// Clean up stale socket
	os.Remove(socketPath)

	fmt.Fprintf(os.Stderr, "swash webui: listening on %s\n", socketPath)
	if err := srv.ServeUnix(ctx, socketPath); err != nil && err != context.Canceled {
		fatal("webui server: %v", err)
	}
}

// cmdWebUIStart starts the webui service as a swash session.
func cmdWebUIStart() {
	ctx := context.Background()

	bk, err := backend.Default(ctx)
	if err != nil {
		fatal("initializing backend: %v", err)
	}
	defer bk.Close()

	// Check if already running
	if isWebUIRunning(ctx) {
		fmt.Println("webui service already running")
		return
	}

	// Find our executable path
	exe, err := os.Executable()
	if err != nil {
		fatal("finding executable: %v", err)
	}

	// Start the webui server as a swash session
	sessionID, err := bk.StartSession(ctx, []string{exe, "webui", "serve"}, backend.SessionOptions{
		ServiceType: "webui",
	})
	if err != nil {
		fatal("starting webui service: %v", err)
	}

	fmt.Printf("started webui service (session %s)\n", sessionID)

	// Wait for it to be ready
	for range 50 {
		time.Sleep(100 * time.Millisecond)
		if isWebUIRunning(ctx) {
			fmt.Printf("ready at %s\n", webUISocketPath())
			return
		}
	}

	fmt.Println("warning: service started but not responding yet")
}

// cmdWebUIStop stops the webui service session.
func cmdWebUIStop() {
	ctx := context.Background()

	bk, err := backend.Default(ctx)
	if err != nil {
		fatal("initializing backend: %v", err)
	}
	defer bk.Close()

	// Find the webui service session
	sessionID, err := findServiceSession(ctx, bk, "webui")
	if err != nil {
		fmt.Printf("webui service not found: %v\n", err)
		return
	}

	if err := bk.StopSession(ctx, sessionID); err != nil {
		fatal("stopping webui service: %v", err)
	}

	fmt.Printf("stopped webui service (session %s)\n", sessionID)
}

// cmdWebUIStatus shows the status of the webui service.
func cmdWebUIStatus() {
	ctx := context.Background()
	socketPath := webUISocketPath()

	if isWebUIRunning(ctx) {
		fmt.Println("webui service: running")
		fmt.Printf("socket: %s\n", socketPath)
	} else {
		fmt.Println("webui service: not running")
		fmt.Printf("socket: %s\n", socketPath)
		fmt.Println("\nstart with: swash webui start")
	}
}

// isWebUIRunning checks if the webui service is running by testing the socket.
func isWebUIRunning(ctx context.Context) bool {
	socketPath := webUISocketPath()
	// Simple check: see if the socket file exists and is connectable
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return false
	}
	// Try to connect
	client := server.NewClient(socketPath)
	return client.Health(ctx) == nil
}

// runtimeDir returns the runtime directory for swash sockets.
func runtimeDir() string {
	// Use XDG_RUNTIME_DIR if available, otherwise fall back to state dir
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir + "/swash"
	}
	home, _ := os.UserHomeDir()
	return home + "/.local/state/swash/runtime"
}
