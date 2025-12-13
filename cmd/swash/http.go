package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	systemdproc "github.com/mbrock/swash/internal/backend/systemd"
	"github.com/mbrock/swash/internal/server"
)

var httpSocket string

// cmdHTTP handles the "swash http" subcommand and its sub-subcommands.
func cmdHTTP(args []string) {
	// Parse flags
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		switch {
		case strings.HasPrefix(args[0], "--socket="):
			httpSocket = strings.TrimPrefix(args[0], "--socket=")
			args = args[1:]
		case args[0] == "--socket" && len(args) > 1:
			httpSocket = args[1]
			args = args[2:]
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", args[0])
			os.Exit(1)
		}
	}

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

func runHTTPServer() {
	initBackend()
	defer bk.Close()

	srv := server.New(bk)

	ln, err := server.GetListener(httpSocket, "0.0.0.0:8484")
	if err != nil {
		fatal("getting listener: %v", err)
	}

	// Handle SIGTERM for graceful shutdown (allows coverage data flush)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	fmt.Printf("swash http listening on %s\n", ln.Addr())

	if err := srv.Serve(ln); err != nil && err.Error() != "http: Server closed" {
		fatal("http server: %v", err)
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
