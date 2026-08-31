package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasSystemdRuntime(t *testing.T) {
	runtimeDir := t.TempDir()
	if hasSystemdRuntime("", runtimeDir) {
		t.Fatal("missing D-Bus address should select POSIX")
	}
	if hasSystemdRuntime("unix:path=/broken", runtimeDir) {
		t.Fatal("missing systemd runtime endpoint should select POSIX")
	}

	private := filepath.Join(runtimeDir, "systemd", "private")
	if err := os.MkdirAll(filepath.Dir(private), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(private, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !hasSystemdRuntime("unix:path=/broken", runtimeDir) {
		t.Fatal("present endpoints should select systemd without probing them")
	}
}

func TestPrepareSystemdUserEnvironment(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")

	private := filepath.Join(runtimeDir, "systemd", "private")
	if err := os.MkdirAll(filepath.Dir(private), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(private, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bus := filepath.Join(runtimeDir, "bus")
	if err := os.WriteFile(bus, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if !prepareSystemdUserEnvironment() {
		t.Fatal("conventional user bus should select systemd")
	}
	if got, want := os.Getenv("DBUS_SESSION_BUS_ADDRESS"), "unix:path="+bus; got != want {
		t.Fatalf("DBUS_SESSION_BUS_ADDRESS = %q, want %q", got, want)
	}
}
