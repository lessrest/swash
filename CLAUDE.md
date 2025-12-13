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
make generate           # Run go generate for templ files
make clean              # Remove bin/
```

**Important**: Building requires CGO with vendored systemd headers. Use `make` or `./build.sh` which set `CGO_CFLAGS=-I$(pwd)/cvendor`. Direct `go build` without this flag will fail.

Single test: `go test ./vterm -run TestVTerm -v`

## Architecture

### Two-Process Model

When you run `swash run echo hello`:
1. CLI asks systemd to start `swash-host-<ID>.service` (the host)
2. Host owns D-Bus name `sh.swa.Swash.<ID>` for remote control
3. Host starts `swash-task-<ID>.service` (the actual command)
4. Both live in `swash-<ID>.slice` for resource grouping
5. Output flows: task → host → systemd journal (with `SWASH_SESSION=<ID>`)

### Backend Abstraction (`internal/backend/`)

The `Backend` interface abstracts over two complete implementations:
- **systemd** (`internal/backend/systemd/`): D-Bus + transient units + journald
- **posix** (`internal/backend/posix/`): Unix sockets + per-session journal files

Both backends support all features (TTY mode, contexts, follow, etc.). The posix backend writes native systemd journal format files (via `pkg/journalfile`) that `journalctl --file=...` can read.

Backend selection: `SWASH_BACKEND` env var, or auto-detect (probes D-Bus for `org.freedesktop.systemd1`).

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
- `.` - Main swash module
- `vterm/` - Independent terminal emulation module (WASM-based libvterm bindings)

### Public Packages (`pkg/`)

- `pkg/journalfile/` - Native systemd journal file writer (used by posix backend)

### Testing

Integration tests run in two modes:
- **posix** (default): Isolated test environment using the posix backend with `swash minijournald`
- **real**: Tests against real systemd (creates transient units in user systemd)

Use `SWASH_TEST_MODE=real` to test with real systemd, or leave unset for isolated posix testing.

## Session Modes

- **Pipe mode** (default): Lines captured as journal entries, stdin/stdout via pipes
- **TTY mode** (`--tty`): Full terminal emulation via libvterm, supports attach/detach, screen snapshots

## Journal Fields

Sessions write structured fields: `SWASH_SESSION`, `SWASH_EVENT` (started/exited/screen), `FD` (1=stdout, 2=stderr). Query with: `journalctl --user SWASH_SESSION=<ID>`.

## Context System

Contexts group sessions with shared working directories (`~/.local/state/swash/contexts/<ID>/`). The `swash context shell <ID>` command enters a bash shell with `SWASH_CONTEXT` set. All sessions started within inherit this env var, allowing `swash` and `swash history` to filter by context (use `-a` to see all).

## HTTP API

`swash http` runs a web server with session listing, output viewing, and WebSocket TTY attach. Can be installed as a systemd socket-activated service via `swash http install`.
