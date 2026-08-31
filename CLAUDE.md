# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**swash** is a Go CLI that runs commands as systemd transient units with D-Bus control and journal-based output logging. Each session gets a host service exposing D-Bus methods (SendInput, Kill, GetScreen) while output goes to the systemd journal with structured fields for querying.

## Build Commands

```bash
make build              # Build bin/swash
make test               # Run all tests (unit + integration)
make test-unit          # Run unit tests only: go test ./pkg/... ./internal/... ./vterm/...
make test-integration   # Run integration tests: go test ./integration/... -v -timeout 120s
make clean              # Remove bin/
```

**Important**: Building requires CGO with vendored systemd headers. Use `make` or `./build.sh` which set `CGO_CFLAGS=-I$(pwd)/cvendor`. Direct `go build` without this flag will fail.

Single test: `go test ./vterm -run TestVTerm -v`

## Architecture

### Session Process Model

When you run `swash run echo hello`:
1. CLI asks systemd to start `swash-host-<ID>.service` (the host)
2. Host owns D-Bus name `sh.swa.Swash.<ID>` for remote control
3. Host starts the command as a child in the same service cgroup
4. All session services live in the shared `swash.slice`
5. Output flows: task → host → systemd journal (with `SWASH_SESSION=<ID>`)

### Backend Abstraction (`internal/backend/`)

The `Backend` interface abstracts over two complete implementations:
- **systemd** (`internal/backend/systemd/`): D-Bus + transient units + journald
- **posix** (`internal/backend/posix/`): Unix sockets + a shared SQLite WAL database

Both backends support TTY mode, session control, history, and structured events. The posix backend writes structured events directly to SQLite without a journal daemon.

Backend selection: `SWASH_BACKEND` env var, or instant auto-detection from the systemd user-manager endpoints under `$XDG_RUNTIME_DIR` or `/run/user/$UID`. When login-session environment variables are absent, swash derives and exports them from those endpoints.

### Key Internal Packages

- `internal/host/` - Pipe-based session host (D-Bus server for non-TTY sessions)
- `internal/tty/` - TTYHost using PTY + vterm module for interactive programs
- `internal/session/` - Client-side session management and TTY attach logic
- `internal/eventlog/` - Journal abstraction (journald, file-based backends)
- `internal/process/` - Process backend abstraction (systemd, exec-based)
- `internal/platform/systemd/` - Systemd-specific process and journal implementations
- `internal/journald/` - Minimal journald daemon for posix backend

### Workspace Modules

This is a Go multi-module workspace (`go.work`):
- `.` - Main swash module (`swa.sh`)
- `vterm/` - Independent terminal emulation module (`swa.sh/vterm`)

### Public Packages (`pkg/`)

- `pkg/journalfile/` - Native systemd journal file writer (used by posix backend)

### Testing

Integration tests run in two modes:
- **posix** (default): Isolated test environment using the SQLite-backed posix backend
- **real**: Tests against real systemd (creates transient units in user systemd)

Use `SWASH_TEST_MODE=real` to test with real systemd, or leave unset for isolated posix testing.

## Session Modes

- **Pipe mode** (default): Lines captured as journal entries, stdin/stdout via pipes
- **TTY mode** (`--tty`): Full terminal emulation via libvterm, supports attach/detach, screen snapshots

## Journal Fields

Sessions write structured fields: `SWASH_SESSION`, `SWASH_EVENT` (started/exited/screen), `FD` (1=stdout, 2=stderr). Query with: `journalctl --user SWASH_SESSION=<ID>`.
