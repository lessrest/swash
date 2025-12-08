package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/godbus/dbus/v5"
)

const (
	busName      = "org.freedesktop.systemd1"
	objectPath   = "/org/freedesktop/systemd1"
	managerIface = "org.freedesktop.systemd1.Manager"
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

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: connecting to session bus: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Determine journal directory
	jdir := *journalDir
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = "/tmp"
	}
	if jdir == "" {
		jdir = filepath.Join(runtimeDir, "mini-systemd", "journal")
	}

	// Resolve IDs from flags/env (env as fallback)
	midStr := *machineIDFlag
	if midStr == "" {
		midStr = os.Getenv("MINISYSTEMD_MACHINE_ID")
	}
	bidStr := *bootIDFlag
	if bidStr == "" {
		bidStr = os.Getenv("MINISYSTEMD_BOOT_ID")
	}

	machineID, err := parseID128Hex(midStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid machine-id: %v\n", err)
		os.Exit(1)
	}
	bootID, err := parseID128Hex(bidStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid boot-id: %v\n", err)
		os.Exit(1)
	}

	// Create journal service with file storage
	journal, err := NewJournalServiceWithFile(jdir, machineID, bootID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to create journal file, using in-memory only: %v\n", err)
		journal = NewJournalService(machineID, bootID)
	} else {
		defer journal.Close()
		fmt.Printf("Journal directory: %s\n", jdir)
	}

	mgr := NewManager(conn, journal)

	// Start journal socket listener
	socketPath := *journalSocket
	if socketPath == "" {
		socketPath = filepath.Join(runtimeDir, "mini-systemd", "journal.socket")
	}
	socketListener, err := NewJournalSocketListener(socketPath, journal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to create journal socket: %v\n", err)
	} else {
		defer socketListener.Close()
		go socketListener.Run()
		fmt.Printf("Journal socket: %s\n", socketPath)
	}

	// Request the systemd bus name
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: requesting bus name: %v\n", err)
		os.Exit(1)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		fmt.Fprintf(os.Stderr, "error: bus name %s already taken\n", busName)
		os.Exit(1)
	}

	// Export the manager on D-Bus
	if err := conn.Export(mgr, objectPath, managerIface); err != nil {
		fmt.Fprintf(os.Stderr, "error: exporting manager: %v\n", err)
		os.Exit(1)
	}

	// Also export our custom log interface
	if err := conn.Export(mgr, objectPath, "sh.swa.MiniSystemd.Logs"); err != nil {
		fmt.Fprintf(os.Stderr, "error: exporting logs interface: %v\n", err)
		os.Exit(1)
	}

	// Export journal service
	if err := conn.Export(journal, objectPath, "sh.swa.MiniSystemd.Journal"); err != nil {
		fmt.Fprintf(os.Stderr, "error: exporting journal interface: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("mini-systemd running on %s\n", busName)

	// Wait for termination
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("mini-systemd shutting down")
}
