// mini-systemd - A minimal process manager with systemd-compatible D-Bus API
//
// This is a lightweight alternative to systemd for environments where systemd
// isn't available (containers, gVisor, testing). It implements just enough of
// the org.freedesktop.systemd1 D-Bus interface for swash to work.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/godbus/dbus/v5"
)

const (
	busName    = "org.freedesktop.systemd1"
	objectPath = "/org/freedesktop/systemd1"
	managerIface = "org.freedesktop.systemd1.Manager"
)

func main() {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: connecting to session bus: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	mgr := NewManager(conn)

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

	fmt.Printf("mini-systemd running on %s\n", busName)

	// Handle SIGCHLD to reap children
	go mgr.reapChildren()

	// Wait for termination
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("mini-systemd shutting down")
}
