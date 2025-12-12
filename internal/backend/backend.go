package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mbrock/swash/internal/session"
)

// Kind identifies a backend implementation.
type Kind string

const (
	KindSystemd Kind = "systemd"
	KindPosix   Kind = "posix"
)

// Config configures a backend implementation.
type Config struct {
	Kind Kind

	// StateDir defaults to $XDG_STATE_HOME/swash or ~/.local/state/swash.
	StateDir string

	// RuntimeDir defaults to $XDG_RUNTIME_DIR/swash or os.TempDir()/swash.
	RuntimeDir string

	// HostCommand is how to run the host process (usually {selfExe, "host"}).
	HostCommand []string
}

// Backend provides semantic operations for swash sessions.
type Backend interface {
	Close() error

	ListSessions(ctx context.Context) ([]Session, error)
	ListHistory(ctx context.Context) ([]HistorySession, error)

	StartSession(ctx context.Context, command []string, opts SessionOptions) (sessionID string, err error)
	StopSession(ctx context.Context, sessionID string) error
	KillSession(ctx context.Context, sessionID string) error
	SendInput(ctx context.Context, sessionID, input string) (int, error)

	PollSessionOutput(ctx context.Context, sessionID, cursor string) ([]Event, string, error)
	FollowSession(ctx context.Context, sessionID string, timeout time.Duration, outputLimit int) (exitCode int, result FollowResult)

	GetScreen(ctx context.Context, sessionID string) (string, error)

	ConnectSession(sessionID string) (session.SessionClient, error)
	ConnectTTYSession(sessionID string) (session.TTYClient, error)

	// Context management
	CreateContext(ctx context.Context) (contextID string, dir string, err error)
	ListContexts(ctx context.Context) ([]Context, error)
	GetContextDir(ctx context.Context, contextID string) (string, error)
	ListContextSessions(ctx context.Context, contextID string) ([]string, error)
}

type opener func(ctx context.Context, cfg Config) (Backend, error)

var openers = map[Kind]opener{}

// Register makes a backend implementation available to Open.
// Implementations should call this from init().
func Register(kind Kind, o opener) {
	if kind == "" {
		panic("backend: register with empty kind")
	}
	if o == nil {
		panic("backend: register with nil opener")
	}
	if _, exists := openers[kind]; exists {
		panic("backend: duplicate register for kind " + string(kind))
	}
	openers[kind] = o
}

// Open constructs a backend from cfg. The requested Kind must be registered.
func Open(ctx context.Context, cfg Config) (Backend, error) {
	cfg = withDefaults(cfg)
	o, ok := openers[cfg.Kind]
	if !ok {
		return nil, fmt.Errorf("unknown backend %q", cfg.Kind)
	}
	return o(ctx, cfg)
}

// DetectKind returns the appropriate backend based on environment.
// Returns systemd if D-Bus session bus is available, otherwise posix.
func DetectKind() Kind {
	if hasDBusSessionBus() {
		return KindSystemd
	}
	return KindPosix
}

// hasDBusSessionBus checks if a D-Bus session bus is available.
// It checks DBUS_SESSION_BUS_ADDRESS first, then looks for the standard
// socket at /run/user/<uid>/bus (common on systemd systems even when
// the environment variable isn't set).
func hasDBusSessionBus() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}

	// Check for standard systemd user bus socket
	uid := os.Getuid()
	socketPath := fmt.Sprintf("/run/user/%d/bus", uid)
	if _, err := os.Stat(socketPath); err == nil {
		return true
	}

	return false
}

// Default constructs the backend selected by environment variable SWASH_BACKEND,
// or auto-detects based on environment if not set.
func Default(ctx context.Context) (Backend, error) {
	kind := Kind(os.Getenv("SWASH_BACKEND"))
	if kind == "" {
		kind = DetectKind()
	}
	return Open(ctx, Config{Kind: kind})
}

func withDefaults(cfg Config) Config {
	if cfg.Kind == "" {
		cfg.Kind = DetectKind()
	}
	if cfg.StateDir == "" {
		cfg.StateDir = defaultStateDir()
	}
	if cfg.RuntimeDir == "" {
		cfg.RuntimeDir = defaultRuntimeDir()
	}
	if len(cfg.HostCommand) == 0 {
		if exe, err := os.Executable(); err == nil {
			cfg.HostCommand = []string{exe, "host"}
		}
	}
	return cfg
}

func defaultStateDir() string {
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

func defaultRuntimeDir() string {
	if v := os.Getenv("SWASH_RUNTIME_DIR"); v != "" {
		return v
	}
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		return filepath.Join(base, "swash")
	}
	return filepath.Join(os.TempDir(), "swash")
}

// ValidateHostCommand returns a user-facing error if HostCommand is unusable.
func ValidateHostCommand(hostCmd []string) error {
	if len(hostCmd) == 0 {
		return errors.New("host command is empty")
	}
	if filepath.Base(hostCmd[0]) == "" {
		return fmt.Errorf("invalid host command executable %q", hostCmd[0])
	}
	return nil
}
