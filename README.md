# swash

Run commands as sessions you can leave and come back to.

swash keeps a command running independently of the terminal that started it.
You can follow its output, reconnect to an interactive program, send input, or
stop it later. Completed sessions remain available in history with their output
and exit status.

This is useful for builds, test suites, development servers, remote work, and
any command that may outlive the shell—or the tool—that launched it.

```console
$ swash start -- make test
KXO284 started

$ swash
# KXO284 is listed while it runs

$ swash follow KXO284
# streams saved output and waits for the command to exit

$ swash history
# KXO284 remains available after it finishes
```

## Install

There are no packaged releases yet. Build swash from source with Go 1.25+, a C
compiler, and GNU Make:

```bash
git clone https://github.com/lessrest/swash.git
cd swash
make install
```

The systemd headers and terminal-emulation sources needed by the build are
included in the repository.

## Use

### Run a command

```bash
swash run -- make test
```

`run` shows output and returns the command's exit status if it finishes within
three seconds. If it is still running, swash detaches and prints a session ID so
you can come back to it. Change the wait with `--detach-after`:

```bash
swash --detach-after 30s run -- ./slow-script
swash --detach-after 0 run -- ./server       # detach immediately
swash start -- ./server                      # shorthand for the above
```

swash also detaches after 1920 bytes of output by default, so a noisy command
does not monopolize its caller. Set `--detach-after-output 0` to disable that
limit.

When swash stops waiting for a command that is still running, it exits with
status 124 (like `timeout`), so callers can tell "still running" from a
command that failed. `follow` waits indefinitely by default, but accepts the
same `--detach-after` and `--detach-after-output` limits.

### Environment

By default a session gets the caller's environment, so `swash run -- $x`
behaves like `$x`. With `--login` (`-l`) it gets a fresh login session
instead: the user service manager's environment, run through your login
shell, keeping only the working directory from the caller.

```bash
swash -l run -- nixos-rebuild switch
```

This is useful when the caller is itself sandboxed or carries a lot of
private state, such as an agent harness running inside a bubblewrap
container: the session runs outside that container, as a sibling of your
other user services.

### Inspect and control sessions

```bash
swash                         # list running sessions
swash poll KXO284             # print output collected so far
swash follow KXO284           # stream output until the session exits
swash send KXO284 "yes"       # write to the command's standard input
swash stop KXO284             # request a graceful stop
swash kill KXO284             # terminate immediately
swash history                 # list completed sessions
```

Session output is stored independently of the client. Closing the terminal or
interrupting `follow` does not discard it.

### Watch resource usage

Each session host samples its session once a second: CPU, memory, swap,
processes, disk I/O, TCP traffic, and pressure stalls. That makes it easy to
tell a quiet download or a busy compile from a hung process:

```bash
swash stats                   # one line per running session
swash stats KXO284            # live usage, or totals once it has exited
swash run --stats 30s -d 10m -- nix flake update
swash follow --stats 10s KXO284
```

```
swash: [4s] cpu 3% · mem 46M/23G · disk w 22M +5.4M/s · net ↓34M +8.4M/s ↑1.7K (1 conn) · 1 proc
swash: [6s] cpu 151% · mem 7.3M/23G · 6 procs (sh 38%, sh 38%, sh 37%) · stalled on cpu 17%
swash: exited 0 · cpu 221ms · mem peak 51M · disk w 38M · net ↓38M ↑1.7K
```

Totals are also recorded on each session's `exited` event (`SWASH_CPU_USEC`,
`SWASH_MEM_PEAK`, `SWASH_DISK_WRITE`, `SWASH_NET_RX`, `SWASH_OOM_KILLS`, ...).
Network counters cover TCP only.

### Resource limits

With the systemd backend, every session gets limits that keep one runaway
command from taking over the machine while leaving ordinary heavy work alone:

- all CPU cores but two, at half priority under contention
- memory throttled at 50% of RAM and OOM-killed at 75%, with at most half the
  swap space on top
- 4096 processes and threads

All sessions together are also capped at all cores but one and 90% of RAM.
Adjust per session, or set defaults with `SWASH_LIMITS`:

```bash
swash run --cpus 4 -- make -j32
swash run --mem 8G -- ./leaky     # a hard cap: no swap unless swap= is set
swash run --unlimited -- ./bench
export SWASH_LIMITS="cpus=8,mem=16G,swap=2G,tasks=1024"   # or "off"
```

When the OOM killer hits a session, the host survives to record it: the
session exits with status 137 and its totals show `OOM-KILLED`.

### Provenance

The `started` event records where a session came from: `SWASH_CWD`,
`SWASH_PARENT` (the enclosing swash session, if any), `SWASH_CALLER_CHAIN`
(e.g. `bash < claude < claude-desktop < bwrap < systemd`), `SWASH_CALLER_EXE`,
`SWASH_CALLER_CGROUP`, `SWASH_LOGIN`, and `SWASH_ORIGIN`, which comes from the
`SWASH_ORIGIN` variable or, inside Claude Code, the Claude session ID:

```bash
swash events --event started --field SWASH_ORIGIN=claude-code:1d81f141-... --json
```

### Run interactive programs

Use TTY mode for programs such as shells, editors, and process monitors:

```bash
swash --tty run -- htop
```

Press `Ctrl+\` to detach without stopping the program. Reconnect or inspect its
current screen later:

```bash
swash attach KXO284
swash screen KXO284
```

TTY sessions preserve terminal state between attachments. Multiple clients may
attach to the same session.

### Add structured metadata

Tags attach application-specific fields to a session:

```bash
swash --tag PROJECT=myapp --tag ENV=staging run -- ./deploy
```

A process or another client can append semantic events to a session, then query
them through the same backend-independent event log:

```bash
swash emit KXO284 --event ready --field PORT=8080
swash events --session KXO284 --event ready --json
swash events --field PROJECT=myapp --follow
```

Event field names use the uppercase `KEY=VALUE` journal convention. Unfiltered
queries require the explicit `swash events --all` form.

## Backends

swash provides the same CLI through two execution backends:

- **POSIX** runs sessions as independent process groups, controls them over Unix
  sockets, and stores events in a SQLite WAL database. It works without systemd
  and is also the default integration-test backend.
- **systemd** runs sessions as transient user units, controls them over D-Bus,
  and stores output in the systemd journal. This adds cgroup-based lifecycle
  management and compatibility with standard systemd tools.

swash selects systemd when a user D-Bus and systemd user manager are available,
and POSIX otherwise. Override detection with either form:

```bash
swash --backend posix start -- ./server
SWASH_BACKEND=systemd swash start -- ./server
```

On the systemd backend, the structured output is also directly queryable:

```bash
journalctl --user SWASH_SESSION=KXO284
journalctl --user SWASH_SESSION=KXO284 -o cat
```

## How it works

Each session has a host process that owns the command's input, output, and
lifecycle. Clients talk to the host rather than holding the command's pipes
open themselves. The host continues recording output when no client is
connected, which is what makes detach, follow, and reattach reliable.

Pipe sessions record stdout and stderr as structured lines. TTY sessions use
libvterm to preserve the screen and terminal state for later attachments.

## Development

```bash
make build              # build bin/swash
make test-unit          # unit tests
make test-integration   # isolated POSIX integration tests
make test               # both
```

Building through Make sets the include path for the vendored systemd headers.
Set `SWASH_TEST_MODE=real` to run integration tests against the current user's
real systemd instance.
