// Package dirs provides standard directory resolution for swash.
// It handles XDG base directories with appropriate fallbacks for
// platforms where XDG isn't fully supported (e.g., macOS).
package dirs

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
)

// StateDir returns the directory for persistent state data (journals, etc.).
// Priority: $SWASH_STATE_DIR > $XDG_STATE_HOME/swash > ~/.local/state/swash
func StateDir() string {
	if v := os.Getenv("SWASH_STATE_DIR"); v != "" {
		return v
	}
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "swash")
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", "swash")
	}
	return filepath.Join(os.TempDir(), "swash-state")
}

// RuntimeDir returns the directory for ephemeral runtime data (sockets, PIDs).
// Priority: $SWASH_RUNTIME_DIR > best available runtime dir > $TMPDIR/swash-$USER
func RuntimeDir() string {
	if v := os.Getenv("SWASH_RUNTIME_DIR"); v != "" {
		return v
	}

	// Try to find a suitable runtime directory
	if base := findRuntimeBase(); base != "" {
		return filepath.Join(base, "swash")
	}

	// Fall back to temp dir with username suffix for uniqueness
	username := "unknown"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	return filepath.Join(os.TempDir(), "swash-"+username)
}

// findRuntimeBase finds the best available runtime directory base.
// On Linux this is typically /run/user/$UID, on macOS/BSD we check candidates.
func findRuntimeBase() string {
	// First check XDG_RUNTIME_DIR (set by systemd on Linux, sometimes on macOS)
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}

	currentUser, err := user.Current()
	if err != nil {
		return ""
	}

	// Build candidate directories in priority order
	candidates := []string{
		filepath.Join("/run/user", currentUser.Uid),
		filepath.Join("/var/run/user", currentUser.Uid),
	}

	// FreeBSD uses a different convention
	if runtime.GOOS == "freebsd" {
		candidates = append([]string{
			filepath.Join("/var/run/xdg", currentUser.Username),
		}, candidates...)
	}

	// Find the first candidate that exists
	for _, dir := range candidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}

	return ""
}
