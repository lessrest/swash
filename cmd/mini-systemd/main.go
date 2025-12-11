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

func main() {
	// Parse flags
	journalDir := flag.String("journal-dir", "", "Directory for journal files (default: $XDG_RUNTIME_DIR/mini-systemd/journal)")
	journalSocket := flag.String("journal-socket", "", "Path for journal socket (default: $XDG_RUNTIME_DIR/mini-systemd/journal.socket)")
	machineIDFlag := flag.String("machine-id", "", "Override machine ID (32 hex chars)")
	bootIDFlag := flag.String("boot-id", "", "Override boot ID (32 hex chars)")
	flag.Parse()

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
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
