// mini-systemd - A minimal process manager with systemd-compatible D-Bus API
//
// This is a lightweight alternative to systemd for environments where systemd
// isn't available (containers, gVisor, testing). It implements just enough of
// the org.freedesktop.systemd1 D-Bus interface for swash to work.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/godbus/dbus/v5"
)

const (
	busName    = "org.freedesktop.systemd1"
	objectPath = "/org/freedesktop/systemd1"
	managerIface = "org.freedesktop.systemd1.Manager"
)

func main() {
	// Parse flags
	journalDir := flag.String("journal-dir", "", "Directory for journal files (default: $XDG_RUNTIME_DIR/mini-systemd/journal)")
	flag.Parse()

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: connecting to session bus: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Determine journal directory
	jdir := *journalDir
	if jdir == "" {
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDir == "" {
			runtimeDir = "/tmp"
		}
		jdir = filepath.Join(runtimeDir, "mini-systemd", "journal")
	}

	// Create journal service with file storage
	journal, err := NewJournalServiceWithFile(jdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to create journal file, using in-memory only: %v\n", err)
		journal = NewJournalService()
	} else {
		defer journal.Close()
		fmt.Printf("Journal directory: %s\n", jdir)
	}

	mgr := NewManager(conn, journal)

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

	// Handle SIGCHLD to reap children
	go mgr.reapChildren()

	// Wait for termination
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("mini-systemd shutting down")
}
