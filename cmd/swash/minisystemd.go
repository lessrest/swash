package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mbrock/swash/internal/minisystemd"
)

// cmdMinisystemd runs a minimal systemd-compatible daemon for testing.
// It implements enough of systemd's D-Bus interface to run swash sessions
// without requiring real systemd.
func cmdMinisystemd() {
	fs := flag.NewFlagSet("minisystemd", flag.ExitOnError)
	journalDir := fs.String("journal-dir", "", "Directory for journal files (default: $XDG_RUNTIME_DIR/mini-systemd/journal)")
	journalSocket := fs.String("journal-socket", "", "Path for journal socket (default: $XDG_RUNTIME_DIR/mini-systemd/journal.socket)")
	machineIDFlag := fs.String("machine-id", "", "Override machine ID (32 hex chars)")
	bootIDFlag := fs.String("boot-id", "", "Override boot ID (32 hex chars)")
	fs.Parse(os.Args[2:])

	// Configure slog level from environment
	logLevel := slog.LevelInfo
	if os.Getenv("MINISYSTEMD_DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := minisystemd.Run(ctx, minisystemd.Options{
		JournalDir:    *journalDir,
		JournalSocket: *journalSocket,
		MachineID:     *machineIDFlag,
		BootID:        *bootIDFlag,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "swash minisystemd: %v\n", err)
		os.Exit(1)
	}
}
