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
