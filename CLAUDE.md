# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**busker** provides interactive process sessions over D-Bus, enabling tool access from any process (including MCP clients) without managing connections directly. Sessions run as systemd user services with unique D-Bus names.

The core pattern: **Process + Events + Commands + Gist**
- A process spews events (stdout, state changes)
- Commands go in (send input, kill)
- Events stream out with cursor-based access (poll, wait, gather)
- Gist summarizes current state

## Architecture

```
┌────────────────────────────────────────┐
│  MCP Server (cursor tracking, thin)    │
├────────────────────────────────────────┤
│  CLI Client (rich output, scripting)   │
├────────────────────────────────────────┤
│  D-Bus Interface (the real API)        │
├────────────────────────────────────────┤
│  Session Service (systemd transient)   │
│  └─ EventLog + Gist + Process          │
├────────────────────────────────────────┤
│  systemd (lifecycle, journald, cgroups)│
└────────────────────────────────────────┘
```

### Key Components

- **EventLog**: Append-only event log with cursor-based streaming, async wait/gather
- **BaseSession**: Abstract session with event log and process management
- **ShellSession**: Basic subprocess with stdout/stderr streaming
- **BuskerService**: D-Bus interface exposing session over the bus

### Files

- `busker.py` - Main implementation (EventLog, sessions, D-Bus, CLI, MCP)
- `busdap.py` - Legacy DAP-specific implementation (to be refactored)
- `flake.nix` - Nix packaging

## Development Commands

```bash
# Run busker
nix run .                           # default (busker)
nix run .#busdap                    # legacy DAP version

# Development shell
nix develop

# CLI usage
busker run "make -j8"               # start shell session
busker poll                         # see output + gist
busker wait                         # block for events
busker stop                         # terminate
```

## Event System

Events flow through an append-only log with cursor-based access:

- **poll(since)**: Get events since cursor, non-blocking
- **wait(since, timeout)**: Block until events arrive
- **gather(since, gather, timeout)**: Wait for first event, then buffer for duration

Default tail is 24 lines (terminal height vibes). Scrollback available for full history.

## D-Bus Interface

- Name: `org.claude.Busker.{session_id}`
- Path: `/org/claude/Busker`
- Methods: `poll_events`, `wait_events`, `gather_events`, `scrollback`, `tail`, `send_input`, `kill`
- Properties: `gist` (JSON)

## MCP Tools

- `run(command, cwd)` - Start shell session
- `stop()` - Stop session
- `poll()` - Get recent output + gist
- `wait(timeout, gather)` - Block for events
- `send(input)` - Send stdin
- `scroll(offset, limit)` - Read scrollback
