package backend

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"swa.sh/internal/dirs"
	"swa.sh/internal/host"
	"swa.sh/internal/journal"
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
	// EmitSessionEvent appends structured application state independently of
	// process output.
	EmitSessionEvent(ctx context.Context, sessionID, event, message string, fields map[string]string) error
	PollEvents(ctx context.Context, filters []EventFilter, cursor string) ([]journal.EventRecord, string, error)
	FollowEvents(ctx context.Context, filters []EventFilter, cursor string) iter.Seq[journal.EventRecord]

	PollSessionOutput(ctx context.Context, sessionID, cursor string) ([]Event, string, error)
	FollowSession(ctx context.Context, sessionID string, timeout time.Duration, outputLimit int) (exitCode int, result FollowResult)

	GetScreen(ctx context.Context, sessionID string) (string, error)

	ConnectSession(sessionID string) (host.Client, error)
	ConnectTTYSession(sessionID string) (host.TTYClient, error)
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
// Returns systemd when the normal user D-Bus and systemd runtime endpoints are
// present, otherwise posix.
func DetectKind() Kind {
	if prepareSystemdUserEnvironment() {
		return KindSystemd
	}
	return KindPosix
}

// prepareSystemdUserEnvironment deliberately uses an instant heuristic instead
// of a D-Bus round trip. A present-but-broken service is selected and then
// fails loudly when opened; absence falls back to the portable backend
// immediately. This also supports service-launched callers which have access
// to a user manager but did not pass through a login session.
func prepareSystemdUserEnvironment() bool {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}

	busAddress := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if busAddress == "" {
		busPath := filepath.Join(runtimeDir, "bus")
		if _, err := os.Stat(busPath); err != nil {
			return false
		}
		busAddress = "unix:path=" + busPath
	}

	if !hasSystemdRuntime(busAddress, runtimeDir) {
		return false
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		_ = os.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		_ = os.Setenv("DBUS_SESSION_BUS_ADDRESS", busAddress)
	}
	return true
}

func hasSystemdRuntime(busAddress, runtimeDir string) bool {
	if busAddress == "" || runtimeDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(runtimeDir, "systemd", "private"))
	return err == nil
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
	if cfg.Kind == KindSystemd {
		prepareSystemdUserEnvironment()
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
	return dirs.StateDir()
}

func defaultRuntimeDir() string {
	return dirs.RuntimeDir()
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
