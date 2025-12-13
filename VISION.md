# Vision: Agent-Native Process Execution

## What This Is

Swash is groundwork for building better LLM coding agents. The immediate goal is
a solid foundation for tool calls - the part where an agent runs shell commands,
edits files, and interacts with the system. The longer-term goal is a full agent
harness (like Claude Code, OpenCode, Codex CLI) built on these primitives.

## The Problem with Current Agent Tool Calls

When an LLM agent needs to run a shell command, current approaches have issues:

**Blocking**: The agent waits for the command to finish. Long-running tasks
(builds, tests, deployments) block the entire conversation.

**Fragile**: If the agent process crashes or times out, running work is orphaned.
There's no way to reconnect to it or see what happened.

**Opaque**: Process output is captured as a blob of text. No structure, no way
to query specific parts, no separation of stdout/stderr with timestamps.

**No handoff**: Work is tied to a specific agent session. You can't start
something with Claude and hand it to Gemini, or let a human take over.

**Synchronous model**: The whole paradigm assumes "send command, wait for output,
continue." But real work is often asynchronous - start a build, do other things,
come back when it's done.

## The Vision

### Tasks as Persistent Objects

Instead of ephemeral processes, tasks are first-class objects with:

- **Stable identity**: A name that persists across restarts
- **Provenance**: Who started it, why, from what conversation context
- **Observable state**: Running, completed, failed, awaiting reception
- **Structured output**: Logs in the systemd journal with metadata

### Completion as a State, Not an Event

Traditional Unix: a process exits, the exit code is returned, done. If you
weren't waiting, you missed it.

Swash: completed tasks sit in an inbox "awaiting reception." Someone - the
original agent, a different agent, a human - must explicitly receive and
acknowledge the completion. Nothing falls through the cracks.

### Natural Handoff

Start a long build with one agent, come back hours later, have a different
agent (or yourself) pick up where it left off. The completed task has full
context - the conversation that led to it, what the agent planned to do next.

### Async-First

Starting a task and waiting for it are separate operations:

```bash
swash run -- cargo build      # start and wait (current)
swash create build -- cargo build  # create task (future)
swash start build             # begin execution
swash receive build           # handle completion when ready
```

Agents can start work, continue with other tasks, and handle completions
when they're ready - or let completions queue up for later.

## Why systemd?

systemd already solves the hard problems:

- **Process lifecycle**: Start, stop, restart, track state
- **Resource isolation**: Cgroups, slices, limits
- **Logging**: Journal with structured fields, rotation, queries
- **Dependencies**: Units can depend on other units
- **Persistence**: Survives the client process exiting
- **Credentials**: Secure passing of secrets to processes

We're not reimplementing process management. We're building a higher-level
interface that exposes systemd's capabilities for agent use cases.

## Why D-Bus?

Each swash session runs a small D-Bus service (the "host") that:

- Owns a well-known bus name for reliable addressing
- Exposes methods: SendInput, Kill, GetScreenText, etc.
- Survives the original client disconnecting
- Allows any client to connect and interact

This gives us a stable control plane that doesn't depend on keeping a
specific process alive.

## Why the Journal?

The systemd journal gives us:

- **Structured logging**: Fields like SWASH_SESSION, FD, EXIT_CODE
- **Efficient queries**: Find all output from session ABC123
- **Persistence**: Logs survive process exit, system restart
- **Streaming**: Tail logs as they arrive (swash follow)
- **No extra infrastructure**: Already there on Linux systems

Output goes to the journal, not to pipes held by the client. The client can
disconnect and reconnect; the output is still there.

## Current State

What's implemented:

- `swash run` - Start a command as a systemd transient unit
- `swash follow` - Stream output from the journal until exit
- `swash send` - Send input to a running session
- `swash screen` - View current terminal state (for TTY sessions)
- `swash kill/stop` - Terminate sessions
- TTY mode with libvterm for interactive programs
- posix backend for testing without real systemd

What's not yet implemented:

- `swash create` / `swash start` separation
- Task inbox and reception model
- Provenance tracking (conversation context)
- Receiver chains and escalation
- Composition (task dependencies, pipelines)
- MCP integration

## Relation to Agent Harnesses

Tools like Claude Code, OpenCode, and Codex CLI are "agent harnesses" - they
wrap an LLM and give it tools to interact with a codebase. The quality of those
tools matters a lot for agent effectiveness.

Swash is infrastructure for building better tools:

- **Shell command tool**: Instead of subprocess.run(), use swash for
  observability, persistence, async execution
- **Background task tool**: Start long-running work without blocking
- **Task status tool**: Check on previously started work
- **Task handoff**: Transfer work between agent sessions

Eventually, the goal is to build a full harness on these primitives - one
where agents can work more autonomously, handle long-running tasks gracefully,
and collaborate with humans on complex workflows.

## Design Principles

**CLI/protocol-first**: Everything expressible as commands and data, not Go
closures. Pipelines should be inspectable, serializable, resumable.

**systemd-native**: Use systemd's primitives (units, slices, journal) rather
than reimplementing them. We're a layer on top, not a replacement.

**Agent-friendly**: Designed for LLM tool calls - structured output, clear
error handling, async-capable.

**Human-in-the-loop**: Agents are unreliable. Humans are the backstop.

## Exploratory Ideas

GitHub issues #6 and #7 contain speculative ideation about where this could go:

- Issue #6: Unified task model (goroutines as the fundamental abstraction)
- Issue #7: Structured concurrency ideas inspired by C++ stdexec

These explore concepts like pipeline composition, receiver chains, task inboxes,
and acknowledgment semantics. Some of this might be overcomplicating things -
it's brainstorming, not a roadmap. The core value is probably simpler: just
making tool calls more observable and persistent. Read those issues as "thinking
out loud" rather than a spec.
