// Package minisystemd provides a small, session-bus-scoped implementation of a
// subset of the systemd D-Bus Manager interface and journald socket protocol.
//
// It is primarily used as an isolated test harness for swash (integration
// tests), but can also serve as a lightweight “systemd-like” supervisor in
// environments where user systemd is unavailable.
package minisystemd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	busName      = "org.freedesktop.systemd1"
	objectPath   = "/org/freedesktop/systemd1"
	managerIface = "org.freedesktop.systemd1.Manager"
)

// Options configures the mini-systemd daemon.
type Options struct {
	// JournalDir is the directory to write journal files into. If empty,
	// defaults to $XDG_RUNTIME_DIR/mini-systemd/journal (or /tmp/...).
	JournalDir string

	// JournalSocket is the journald native socket path. If empty, defaults to
	// $XDG_RUNTIME_DIR/mini-systemd/journal.socket (or /tmp/...).
	JournalSocket string

	// MachineID is an optional 32-hex-character ID128 override. If empty,
	// MINISYSTEMD_MACHINE_ID is consulted, otherwise a random ID is generated.
	MachineID string

	// BootID is an optional 32-hex-character ID128 override. If empty,
	// MINISYSTEMD_BOOT_ID is consulted, otherwise a random ID is generated.
	BootID string
}

// Run starts the mini-systemd D-Bus service and blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to session bus: %w", err)
	}
	defer conn.Close()

	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = "/tmp"
	}

	jdir := opts.JournalDir
	if jdir == "" {
		jdir = filepath.Join(runtimeDir, "mini-systemd", "journal")
	}

	socketPath := opts.JournalSocket
	if socketPath == "" {
		socketPath = filepath.Join(runtimeDir, "mini-systemd", "journal.socket")
	}

	// Resolve IDs from options/env (env as fallback)
	midStr := opts.MachineID
	if midStr == "" {
		midStr = os.Getenv("MINISYSTEMD_MACHINE_ID")
	}
	bidStr := opts.BootID
	if bidStr == "" {
		bidStr = os.Getenv("MINISYSTEMD_BOOT_ID")
	}

	machineID, err := parseID128Hex(midStr)
	if err != nil {
		return fmt.Errorf("invalid machine-id: %w", err)
	}
	bootID, err := parseID128Hex(bidStr)
	if err != nil {
		return fmt.Errorf("invalid boot-id: %w", err)
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
		return fmt.Errorf("requesting bus name: %w", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("bus name %s already taken", busName)
	}

	// Export the manager on D-Bus
	if err := conn.Export(mgr, objectPath, managerIface); err != nil {
		return fmt.Errorf("exporting manager: %w", err)
	}

	// Also export our custom log interface
	if err := conn.Export(mgr, objectPath, "sh.swa.MiniSystemd.Logs"); err != nil {
		return fmt.Errorf("exporting logs interface: %w", err)
	}

	// Export journal service
	if err := conn.Export(journal, objectPath, "sh.swa.MiniSystemd.Journal"); err != nil {
		return fmt.Errorf("exporting journal interface: %w", err)
	}

	fmt.Printf("mini-systemd running on %s\n", busName)
	<-ctx.Done()
	fmt.Println("mini-systemd shutting down")

	// Gracefully stop all units so they can flush coverage data
	mgr.StopAllUnits(2 * time.Second)

	return nil
}
