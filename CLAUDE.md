# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

busdap exposes lldb-dap (LLDB's Debug Adapter Protocol implementation) via D-Bus, enabling debugger access from any process without managing socket connections. Debug sessions run as systemd user services with unique D-Bus names.

## Development Commands

```bash
# Run busdap
nix run . -- [command]

# Development shell (for hacking on busdap.py)
nix develop
```

## Architecture

The main tool is `busdap.py` - a unified CLI that functions as both server and client:

- **Server mode** (`--serve`): Launched internally via `systemd-run` when user runs `busdap start`. Registers D-Bus service `org.claude.Debugger.{session_id}` and spawns lldb-dap as a subprocess.

- **Client mode** (all other commands): Connects to D-Bus service and proxies commands to lldb-dap.

Key design decisions:
- Each session gets a unique 6-char ID (e.g., `KXO284`) and runs in a systemd transient service (`busdap-{id}.service`)
- Sessions can run under `nix develop` (`--develop`) or inherit current environment (`--inherit`)
- The server refuses to run on TTY - must be launched via systemd to ensure clean process lifecycle
- D-Bus interface uses async sdbus with JSON-serialized DAP responses

## D-Bus Interface

- Name: `org.claude.Debugger.{session_id}`
- Path: `/org/claude/Debugger`
- Methods mirror DAP commands: `initialize`, `launch`, `attach`, `set_breakpoint`, `continue_execution`, `step_over`, `stack_trace`, etc.
- All methods return JSON strings containing DAP responses plus any events received
