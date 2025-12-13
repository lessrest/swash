package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"swa.sh/go/swash/internal/journal"
)

// cmdMinijournald runs a minimal journald-compatible daemon for the POSIX backend.
// It listens on a Unix socket and writes log entries to a single shared journal file.
func cmdMinijournald() {
	// Configure slog level from environment
	logLevel := slog.LevelInfo
	if os.Getenv("SWASH_DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	cfg := journal.DefaultConfig()

	fs := flag.NewFlagSet("minijournald", flag.ExitOnError)
	fs.StringVar(&cfg.SocketPath, "socket", cfg.SocketPath, "Unix socket path")
	fs.StringVar(&cfg.JournalPath, "journal", cfg.JournalPath, "Journal file path")
	fs.Parse(os.Args[2:])

	// Check for inherited fd from graceful restart
	if fdStr := os.Getenv("SWASH_JOURNALD_FD"); fdStr != "" {
		fd, err := strconv.Atoi(fdStr)
		if err == nil && fd > 0 {
			cfg.InheritedFD = fd
		}
		os.Unsetenv("SWASH_JOURNALD_FD")
	}

	daemon, err := journal.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swash minijournald: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "swash minijournald: listening on %s\n", daemon.SocketPath())
	fmt.Fprintf(os.Stderr, "swash minijournald: writing to %s\n", daemon.JournalPath())

	if err := daemon.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "swash minijournald: %v\n", err)
		os.Exit(1)
	}
}
