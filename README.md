# swash

Process sessions over D-Bus with systemd integration.

swash runs commands as systemd transient units and captures their output to the
systemd journal. Each session gets a dedicated D-Bus service for control (send
input, kill process) and uses the journal for output streaming.

## Architecture

```
┌─────────────┐     D-Bus      ┌──────────────────┐
│  swash CLI  │───────────────▶│  systemd (user)  │
└─────────────┘                └──────────────────┘
       │                              │
       │ D-Bus                        │ StartTransient
       ▼                              ▼
┌─────────────────────────────────────────────────┐
│  swash-host-ABC123.service (D-Bus activated)    │
│  ┌───────────────────────────────────────────┐  │
│  │  Host / TTYHost                           │  │
│  │  - Exposes SendInput, Kill, Gist methods  │  │
│  │  - Starts task unit                       │  │
│  │  - Captures output to journal             │  │
│  └───────────────────────────────────────────┘  │
│                      │                          │
│                      │ StartTransient           │
│                      ▼                          │
│  ┌───────────────────────────────────────────┐  │
│  │  swash-task-ABC123.service                │  │
│  │  (the actual command)                     │  │
│  └───────────────────────────────────────────┘  │
└─────────────────────────────────────────────────┘
                       │
                       │ Write
                       ▼
              ┌─────────────────┐
              │  systemd journal │
              │  SWASH_SESSION=  │
              │  ABC123          │
              └─────────────────┘
```

For each session:

1. **swash-host-ABC123.service** - A D-Bus activated service that:
   - Owns bus name `sh.swa.Swash.ABC123`
   - Exposes methods: `SendInput`, `Kill`, `Gist`, `SessionID`
   - Starts the actual task as a nested transient unit
   - Captures stdout/stderr and writes to journal with `SWASH_SESSION=ABC123`

2. **swash-task-ABC123.service** - The actual command, running inside
   `swash-ABC123.slice` for resource grouping.

## Usage

```bash
# Run a command in a new session
swash run echo "hello world"
# ABC123 started

# List running sessions
swash
# ID       STATUS   AGE      PID      COMMAND
# ABC123   running  2s       12345    echo hello world

# Follow output until exit
swash follow ABC123

# Send input to stdin
swash send ABC123 "some input"

# Poll recent output
swash poll ABC123

# Stop gracefully
swash stop ABC123

# Kill immediately
swash kill ABC123

# Show session history (from journal)
swash history
```

### TTY Mode

TTY mode allocates a pseudo-terminal and runs the command with full terminal
emulation via libvterm. This enables proper handling of interactive programs,
colors, cursor movement, and alternate screen mode.

```bash
# Run with PTY
swash run --tty -- vim file.txt

# Specify terminal size
swash run --tty --rows 40 --cols 120 -- htop

# View current screen content (with colors)
swash screen ABC123
```

Example session with htop:

```
$ swash run --tty --rows 10 --cols 70 -- htop
XYZ789 started

$ swash screen XYZ789
    0[|||  4.6%]   4[||   2.0%]   8[||   3.3%]  12[||   2.0%]
    1[||   2.0%]   5[||   2.6%]   9[||   2.6%]  13[||   1.3%]
  Mem[|||||||||||||||||||||||||||||||||12.5G/62.6G]
  Swp[|                               520M/32.0G]

    PID USER       PRI  NI  VIRT   RES S  CPU% Command
1521271 mbrock      20   0 76.1G 4383M S  39.5 opencode
3814634 mbrock      20   0  855M  110M S   3.3 emacs
F1Help F2Setup F3Search F4Filter F5Tree F6SortBy F9Kill F10Quit
```

In TTY mode:
- Output is processed through libvterm before logging to journal
- Lines pushed to scrollback are logged as they scroll
- Final screen state is captured on exit (`SWASH_EVENT=screen`)
- ANSI color codes are preserved in journal entries

### Tags

Add custom journal fields to session output:

```bash
swash run -t PROJECT=myapp -t ENV=staging -- ./deploy.sh
```

### Protocols

The `--protocol` flag controls how stdout is parsed:

- `shell` (default): Line-oriented, each line becomes a journal entry
- `sse`: Server-Sent Events format, parses `data:` lines

## Components

### cmd/swash

The CLI tool. Connects to user systemd via D-Bus to manage sessions.

### internal/swash

Core library:

- `Runtime` - High-level API for session management
- `Host` - Pipe-based session host (non-TTY mode)
- `TTYHost` - PTY-based session host with vterm integration
- `Systemd` interface - systemd D-Bus operations
- `Journal` interface - journal read/write operations

### pkg/vterm

Go bindings to libvterm for terminal emulation. Provides:

- Screen state tracking
- Scrollback callbacks
- ANSI escape sequence rendering
- Color and attribute support

### pkg/journalfile

Native Go implementation of systemd journal file format. Used by mini-systemd
for testing. Supports:

- Journal file creation with proper headers
- Data deduplication via hash tables
- Field indexing for efficient queries
- Entry arrays for enumeration

### cmd/mini-systemd

A minimal systemd substitute for integration testing. Implements:

- `org.freedesktop.systemd1.Manager` D-Bus interface
- `StartTransientUnit` with file descriptor passing
- Journal socket listener (native journal protocol)
- Journal file writer

This enables running the full test suite without root or a real systemd
instance.

## Building

```bash
# Build swash
go build ./cmd/swash/

# Build mini-systemd (for testing)
go build ./cmd/mini-systemd/

# Run tests
./test/integration.sh

# Run tests against real systemd
./test/integration.sh --real
```

Requires:
- Go 1.23+
- C compiler (for libvterm via cgo)
- systemd headers in cvendor/

## Journal Fields

swash writes these fields to journal entries:

| Field | Description |
|-------|-------------|
| `SWASH_SESSION` | Session ID (e.g., ABC123) |
| `SWASH_EVENT` | Lifecycle event: `started`, `exited`, `screen` |
| `SWASH_COMMAND` | The command that was run |
| `SWASH_EXIT_CODE` | Exit code (on `exited` event) |
| `FD` | File descriptor: 1 (stdout) or 2 (stderr) |
| `MESSAGE` | The actual output text |

Query session output with journalctl:

```bash
# All output from a session
journalctl --user SWASH_SESSION=ABC123

# Just exit events
journalctl --user SWASH_EVENT=exited

# Output as plain text
journalctl --user SWASH_SESSION=ABC123 -o cat
```

## Design Notes

**Why systemd transient units?**

- Process lifecycle management (start, stop, kill)
- Resource isolation via slices
- Automatic cleanup on exit
- Integration with existing systemd tooling

**Why D-Bus for the host service?**

- Bus activation ensures the host starts when needed
- Named bus ownership prevents duplicate hosts
- Method calls for input/control are straightforward
- Works with the session bus (no root required)

**Why journal for output?**

- Structured logging with metadata
- Built-in rotation and retention
- Efficient queries by field
- Survives process restarts
- Standard tooling (journalctl)

**Why libvterm?**

- Accurate terminal emulation (state machine, not regex)
- Handles all escape sequences correctly
- Provides scrollback callbacks for logging
- Separates alternate screen from main screen
