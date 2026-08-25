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
