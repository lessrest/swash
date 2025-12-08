# Vision: A Shell for the Age of Agents

## The Problem with Current Agent Interfaces

Every AI coding tool has built a terminal UI: Claude Code, OpenCode, Codex CLI,
Gemini CLI. They all share the same problems:

- **Hijacked terminal**: Your scrollback buffer doesn't work. The TUI captures
  scroll events and implements its own scrolling. There are settings for scroll
  speed. Why?

- **Modal interfaces**: Pop-up dialogs in the middle of your terminal. Special
  "chat mode" separate from "command mode." None of this is necessary.

- **Reinventing badly**: These TUIs reimplement what terminals already do well -
  scrolling, copy/paste, search, resize handling - and do it worse.

- **Separate from the shell**: You run the agent harness, then you're in a
  different world. Your normal shell, your aliases, your workflow - suspended.

The fundamental mistake: treating agent interaction as something that requires
a special interface, when it's really just another thing you do in a shell.

## The Insight

What do you actually need from an agent interface?

- Type a prompt
- See response stream in
- Commands run, output shows
- Scroll back works normally

That's it. The shell already does this. GNU readline already handles input.
stdout already handles output. Your terminal already handles scrollback.

The "agent harness" could be:

```bash
$ claude "fix the build errors"
Looking at the errors in main.rs...
$ cargo build
   Compiling foo v0.1.0
$ claude "still failing on line 42"
Ah, you need to clone that value...
```

Just normal shell usage, with `claude` as another command. The journal keeps
conversation context across invocations. No special mode, no TUI.

## But Then: The Shell Could Be Better

Once you realize you don't need a special TUI, the next thought is: the shell
itself could be improved. Most developers run bash inside tmux to get:

- Multiple panes/windows
- Session persistence
- A status bar (usually useless, bright green by default)

So you're running bash + tmux + maybe the agent TUI, three layers deep.

What if instead there was one thing that combined:

- **Shell**: Command input, history, job control
- **Multiplexer**: Splits, sessions, detach/reattach
- **Task monitor**: Background jobs with live resource usage
- **Agent integration**: LLM as a natural part of the workflow

## Ambient Awareness

Bash has job control - Ctrl+Z to background, `jobs` to list, `fg` to resume.
But it's manual. You have to remember to check. If something hangs, you might
not notice.

The status bar should show what's actually happening:

```
┌─────────────────────────────────────────────────────────────┐
│ $ vim src/main.rs                                           │
│                                                             │
│                                                             │
├─────────────────────────────────────────────────────────────┤
│ [KXO284 cargo build ██░░ 45% 2.1G] [NFL381 npm test ✓ 0]    │
└─────────────────────────────────────────────────────────────┘
```

Each background task shows:

- Session ID
- Command (truncated)
- Progress indicator or spinner
- CPU/memory usage (from cgroups - accurate, not estimated)
- Latest output line scrolling by
- Exit status when done

You can:

- Tab through to select a task
- Enter to attach or split-view
- See immediately when something finishes or hangs

This is what tmux's status bar should have been - actual useful information
about your work, not the date and hostname.

## Pipelines

Bash pipelines have the same opacity problem as background jobs. When you run:

```bash
cat giant.log | grep ERROR | sort | uniq -c | sort -rn | head
```

And it hangs - which stage is stuck? Is `grep` still processing? Is `sort`
waiting for input? People use `pv` to add visibility, but it's manual.

The shell should show pipeline stages like it shows background tasks:

```
[cat giant.log ████████ 100%] → [grep ████░░░░ 52%] → [sort ░░░░ waiting] → ...
```

Each stage as a mini progress indicator. Click/select to see that stage's
current buffer. Obvious where the bottleneck is.

## The Recursive Realization

Here's where it gets interesting. An agent is just a process that:

- Takes input (prompt)
- Does work (thinks, makes API calls)
- Produces output (response, tool calls)

A shell command is just a process that:

- Takes input (arguments, stdin)
- Does work (computation, I/O)
- Produces output (stdout, exit code)

They're the same thing. An LLM API call is just a long-running command that
happens to talk to a remote server. A subagent is just a background job that
happens to be another agent instance.

If the shell's task model is good enough, agents fit naturally:

```bash
$ claude "analyze this codebase" &
[1] ABC123 claude "analyze this codebase"

$ cargo build
   Compiling...

$ fg 1
... I've identified three main modules...
```

The agent runs in the background like any other job. Its "output" streams to
the journal. You can foreground it, check its status, kill it. No special API -
just the same primitives used for every other task.

## Structured Concurrency

systemd already provides structured concurrency for processes:

- **Slices**: Hierarchical grouping of processes
- **Cgroups**: Resource limits that apply to entire subtrees
- **Freeze/thaw**: Atomically pause an entire slice
- **Clean termination**: Kill a slice, all children die

The shell should expose this naturally. A subshell or background job group
becomes a slice. You can:

```bash
$ freeze %1          # Pause that job and all its children
$ thaw %1            # Resume
$ limit %1 cpu=50%   # Cap resource usage
$ kill %1            # Clean termination of entire tree
```

This is especially powerful for agents. An agent spawns subagents, runs builds,
starts test suites - all as children in its slice. If you need to stop
everything the agent is doing: one command, clean termination, no orphans.

## What This Replaces

| Current    | This Shell                        |
| ---------- | --------------------------------- |
| bash       | Better job control, agent-aware   |
| tmux       | Splits and sessions, task-native  |
| htop       | Resource monitoring in status bar |
| Agent TUIs | Just another command              |
| pv         | Pipeline visibility built-in      |

Not five tools awkwardly composed, but one coherent thing.

## Semantic Context

Tasks should carry meaning, not just process IDs:

```bash
$ cargo build --intent="fixing auth bug"
[KXO284] cargo build (fixing auth bug)
```

The intent flows through to:

- Journal entries (queryable later)
- Status bar display
- Agent context (ambient awareness of what's happening)
- History (why did I run this?)

When an agent starts a task, its reasoning is attached. When you resume a
session tomorrow, you can see not just what ran but why.

## Implementation Notes

This would be built on:

- **swash**: Task execution with systemd integration (exists)
- **systemd journal**: Structured logging and queries (exists)
- **D-Bus**: Control plane for task interaction (exists)
- **libvterm**: Terminal emulation for TTY tasks (exists)
- **New shell frontend**: readline-style input, task-aware status bar

The backend (swash + systemd) is largely done. The shell frontend is the new
work - but it's a frontend to proven infrastructure, not a from-scratch
reimplementation of process management.

## Non-Goals

- **Bash compatibility**: Not trying to run existing bash scripts. Different
  language, different model. Use bash for bash scripts.

- **Full terminal emulator**: This runs inside your existing terminal. It's a
  shell, not xterm.

- **Fancy graphics**: No images in terminal, no web views, no electron. Text
  and ANSI colors.

## Open Questions

- What's the right input language? Bash syntax? Something cleaner?
- How much tmux-style window management vs. relying on terminal splits?
- How do pipelines compose with the task model?
- What's the minimal viable version that's already useful?

The core bet: a shell designed around observable, persistent tasks - where
agents and commands are the same kind of thing - is more useful than the
current stack of bash + tmux + agent TUI.
