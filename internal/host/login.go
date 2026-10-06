package host

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ForwardedEnvironment converts the caller's environment into the map a
// session host is started with. Underscore-prefixed shell bookkeeping is
// dropped, as are entries whose names are not valid variable names
// (e.g. "SENTRY-TRACE"), which systemd rejects outright.
func ForwardedEnvironment(environ []string) map[string]string {
	env := make(map[string]string)
	for _, e := range environ {
		name, value, ok := strings.Cut(e, "=")
		if !ok || strings.HasPrefix(name, "_") || !validEnvName(name) {
			continue
		}
		env[name] = value
	}
	return env
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// LoginCommand wraps command so it runs under the user's login shell,
// which sets up the environment the way a fresh login would.
func LoginCommand(command []string) []string {
	return append([]string{loginShell(), "-l", "-c", `exec "$@"`, "swash"}, command...)
}

// loginShell returns the current user's shell from the passwd database,
// falling back to $SHELL and then /bin/sh.
func loginShell() string {
	if shell := passwdShell("/etc/passwd", os.Getuid()); shell != "" {
		if _, err := os.Stat(shell); err == nil {
			return shell
		}
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

func passwdShell(path string, uid int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	want := strconv.Itoa(uid)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) >= 7 && fields[2] == want {
			return fields[6]
		}
	}
	return ""
}

// resolveCommand checks that command[0] can be executed from the host's
// own PATH and filesystem, so a missing program fails the session at once
// with a shell-style message instead of a host crash.
func resolveCommand(command []string) error {
	if len(command) == 0 {
		return exec.ErrNotFound
	}
	_, err := exec.LookPath(command[0])
	return err
}
