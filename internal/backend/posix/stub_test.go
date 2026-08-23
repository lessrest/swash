package posix

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUnixSocketPathUsesRuntimeDirWhenItFits(t *testing.T) {
	got := unixSocketPath("/tmp/swash", "ABC123.sock")
	want := filepath.Join("/tmp/swash", "ABC123.sock")
	if got != want {
		t.Fatalf("socket path = %q, want %q", got, want)
	}
}

func TestUnixSocketPathShortensLongRuntimeDir(t *testing.T) {
	runtimeDir := filepath.Join("/tmp", strings.Repeat("long-runtime-directory-", 8))
	got := unixSocketPath(runtimeDir, "ABC123.sock")
	if len(got) > conservativeUnixSocketPathLimit {
		t.Fatalf("socket path has %d bytes, limit is %d: %q",
			len(got), conservativeUnixSocketPathLimit, got)
	}
	if strings.HasPrefix(got, runtimeDir) {
		t.Fatalf("long runtime directory was not shortened: %q", got)
	}
	if got == unixSocketPath(runtimeDir+"-other", "ABC123.sock") {
		t.Fatalf("different runtime directories produced the same fallback path: %q", got)
	}
}
