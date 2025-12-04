#!/usr/bin/env python3
"""
busker - Interactive process sessions over D-Bus

Usage:
    busker                              Show session status
    busker run <command>                Run shell command
    busker debug [--program <path>]     Start debug session
    busker stop                         Stop session
    busker mcp                          Run as MCP server (stdio)

    busker poll                         Get recent output + gist
    busker wait [--timeout <secs>]      Wait for events
    busker scroll [offset] [limit]      Read scrollback
    busker send <input>                 Send input to process
    busker kill                         Kill process

Options:
    -s, --session ID      Session ID (auto-detected when only one running)
    -j, --json            Output raw JSON
"""

import argparse
import asyncio
import json
import os
import random
import string
import subprocess
import sys
import time
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable

import anyio
from anyio.streams.buffered import BufferedByteReceiveStream

from rich.console import Console
from rich.panel import Panel
from rich import box

from sdbus import (
    DbusInterfaceCommonAsync,
    dbus_method_async,
    dbus_property_async,
    request_default_bus_name_async,
)

# ============================================================================
# Constants
# ============================================================================

DBUS_NAME_PREFIX = "org.claude.Busker"
DBUS_PATH = "/org/claude/Busker"
SLICE = "busker.slice"
DEFAULT_TAIL = 24  # TTY vibe: ~terminal height

console = Console()


def error(msg: str):
    """Print error and exit."""
    console.print(f"[red]error:[/red] {msg}")
    sys.exit(1)


def gen_session_id() -> str:
    """Generate a short random session ID like 'KXO284'."""
    letters = ''.join(random.choices(string.ascii_uppercase, k=3))
    digits = ''.join(random.choices(string.digits, k=3))
    return letters + digits


def log(tag: str, msg: dict):
    """Log to stderr (goes to journald via systemd)."""
    compact = json.dumps(msg, separators=(",", ":"))
    print(f"[{tag}] {compact}", file=sys.stderr, flush=True)


# ============================================================================
# EventLog - Append-only event log with cursor-based streaming
# ============================================================================

@dataclass
class Event:
    """A single event in the log."""
    seq: int
    kind: str
    data: Any
    timestamp: float = field(default_factory=time.time)

    def to_dict(self) -> dict:
        return {
            "seq": self.seq,
            "kind": self.kind,
            "data": self.data,
            "timestamp": self.timestamp,
        }


class EventLog:
    """Append-only event log with cursor-based access and async waiting."""

    def __init__(self):
        self._events: list[Event] = []
        self._cursor = 0
        self._lock = anyio.Lock()
        self._condition = anyio.Condition(self._lock)
        self._gist: dict = {}

    @property
    def cursor(self) -> int:
        """Current end-of-log position."""
        return self._cursor

    @property
    def gist(self) -> dict:
        """Current state summary."""
        return self._gist.copy()

    def set_gist(self, gist: dict):
        """Update the gist."""
        self._gist = gist

    def update_gist(self, **kwargs):
        """Merge updates into gist."""
        self._gist.update(kwargs)

    async def append(self, kind: str, data: Any) -> int:
        """Append event and notify waiters. Returns event sequence number."""
        async with self._condition:
            event = Event(seq=self._cursor, kind=kind, data=data)
            self._events.append(event)
            self._cursor += 1
            self._condition.notify_all()
            return event.seq

    async def poll(self, since: int = 0) -> tuple[list[Event], int]:
        """Get events since cursor. Returns (events, new_cursor)."""
        async with self._lock:
            events = self._events[since:] if since < len(self._events) else []
            return events, self._cursor

    async def wait(self, since: int, timeout: float) -> tuple[list[Event], int, bool]:
        """Wait for events after cursor. Returns (events, new_cursor, timed_out)."""
        try:
            with anyio.fail_after(timeout):
                async with self._condition:
                    while self._cursor <= since:
                        await self._condition.wait()
                    events = self._events[since:]
                    return events, self._cursor, False
        except TimeoutError:
            async with self._lock:
                events = self._events[since:] if since < len(self._events) else []
                return events, self._cursor, True

    async def gather(self, since: int, gather_secs: float, timeout: float) -> tuple[list[Event], int, bool]:
        """Wait for first event, then gather for duration. Returns (events, new_cursor, timed_out)."""
        deadline = time.time() + timeout
        gather_until = time.time() + gather_secs

        try:
            # Wait for at least one event
            with anyio.fail_after(timeout):
                async with self._condition:
                    while self._cursor <= since:
                        await self._condition.wait()

            # Gather for the gather period
            remaining = min(gather_until - time.time(), deadline - time.time())
            if remaining > 0:
                await anyio.sleep(remaining)

            async with self._lock:
                events = self._events[since:]
                return events, self._cursor, False

        except TimeoutError:
            async with self._lock:
                events = self._events[since:] if since < len(self._events) else []
                return events, self._cursor, True

    def tail(self, n: int = DEFAULT_TAIL) -> list[Event]:
        """Get last n events (non-async, for formatting)."""
        return self._events[-n:] if self._events else []

    def scrollback(self, offset: int = 0, limit: int = 100) -> list[Event]:
        """Get events from offset with limit."""
        return self._events[offset:offset + limit]

    def clear(self):
        """Clear all events (use when restarting process)."""
        self._events.clear()
        self._cursor = 0


# ============================================================================
# BaseSession - Abstract session with event log
# ============================================================================

class BaseSession(ABC):
    """Base class for interactive process sessions."""

    def __init__(self, session_id: str, task_group: anyio.abc.TaskGroup):
        self.session_id = session_id
        self._tg = task_group
        self.events = EventLog()
        self._process: anyio.abc.Process | None = None

    @property
    def running(self) -> bool:
        return self._process is not None and self._process.returncode is None

    @property
    def exit_code(self) -> int | None:
        if self._process is None:
            return None
        return self._process.returncode

    @property
    def gist(self) -> dict:
        """Current session gist."""
        base = {
            "running": self.running,
            "exit_code": self.exit_code,
        }
        base.update(self.events.gist)
        return base

    @abstractmethod
    async def start(self, **kwargs):
        """Start the session process."""
        pass

    async def kill(self):
        """Kill the process."""
        if self._process is not None:
            self._process.kill()
            await self.events.append("state", {"event": "killed"})

    async def send_input(self, data: str):
        """Send input to process stdin."""
        if self._process is not None and self._process.stdin is not None:
            await self._process.stdin.send(data.encode())


# ============================================================================
# ShellSession - Basic subprocess with stdout/stderr streaming
# ============================================================================

class ShellSession(BaseSession):
    """Interactive shell command session."""

    async def start(self, command: str | list[str], cwd: str | None = None):
        """Start shell command."""
        if isinstance(command, str):
            cmd = ["sh", "-c", command]
        else:
            cmd = command

        self._process = await anyio.open_process(
            cmd,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=cwd,
        )

        self.events.update_gist(command=command if isinstance(command, str) else " ".join(command))
        await self.events.append("state", {"event": "started", "pid": self._process.pid})

        # Start reader tasks
        self._tg.start_soon(self._read_stream, self._process.stdout, "stdout")
        self._tg.start_soon(self._read_stream, self._process.stderr, "stderr")
        self._tg.start_soon(self._wait_exit)

    async def _read_stream(self, stream: anyio.abc.ByteReceiveStream, name: str):
        """Read lines from stream and append as events."""
        try:
            buffer = b""
            async for chunk in stream:
                buffer += chunk
                while b"\n" in buffer:
                    line, buffer = buffer.split(b"\n", 1)
                    text = line.decode("utf-8", errors="replace")
                    await self.events.append("output", {"stream": name, "text": text})
            # Handle remaining buffer
            if buffer:
                text = buffer.decode("utf-8", errors="replace")
                await self.events.append("output", {"stream": name, "text": text})
        except anyio.ClosedResourceError:
            pass
        except Exception as e:
            log("ERR", {"error": str(e), "stream": name})

    async def _wait_exit(self):
        """Wait for process to exit."""
        code = await self._process.wait()
        self.events.update_gist(exit_code=code)
        await self.events.append("state", {"event": "exited", "exit_code": code})


# ============================================================================
# D-Bus Service - Exposes session over D-Bus
# ============================================================================

class BuskerService(DbusInterfaceCommonAsync, interface_name=DBUS_NAME_PREFIX):
    """D-Bus interface for busker sessions."""

    def __init__(self, session: BaseSession):
        super().__init__()
        self.session = session

    # -------------------------------------------------------------------------
    # Properties
    # -------------------------------------------------------------------------

    @dbus_property_async(property_signature="s")
    def gist(self) -> str:
        return json.dumps(self.session.gist)

    # -------------------------------------------------------------------------
    # Methods - Process Control
    # -------------------------------------------------------------------------

    @dbus_method_async(input_signature="s", result_signature="s")
    async def send_input(self, data: str) -> str:
        await self.session.send_input(data)
        return json.dumps({"sent": len(data)})

    @dbus_method_async(result_signature="s")
    async def kill(self) -> str:
        await self.session.kill()
        return json.dumps({"killed": True})

    # -------------------------------------------------------------------------
    # Methods - Event Access
    # -------------------------------------------------------------------------

    @dbus_method_async(input_signature="i", result_signature="s")
    async def poll_events(self, since: int) -> str:
        events, cursor = await self.session.events.poll(since)
        return json.dumps({
            "events": [e.to_dict() for e in events],
            "cursor": cursor,
            "gist": self.session.gist,
        })

    @dbus_method_async(input_signature="id", result_signature="s")
    async def wait_events(self, since: int, timeout: float) -> str:
        events, cursor, timed_out = await self.session.events.wait(since, timeout)
        return json.dumps({
            "events": [e.to_dict() for e in events],
            "cursor": cursor,
            "gist": self.session.gist,
            "timed_out": timed_out,
        })

    @dbus_method_async(input_signature="idd", result_signature="s")
    async def gather_events(self, since: int, gather: float, timeout: float) -> str:
        events, cursor, timed_out = await self.session.events.gather(since, gather, timeout)
        return json.dumps({
            "events": [e.to_dict() for e in events],
            "cursor": cursor,
            "gist": self.session.gist,
            "timed_out": timed_out,
        })

    @dbus_method_async(input_signature="ii", result_signature="s")
    async def scrollback(self, offset: int, limit: int) -> str:
        events = self.session.events.scrollback(offset, limit)
        return json.dumps({
            "events": [e.to_dict() for e in events],
            "total": self.session.events.cursor,
        })

    @dbus_method_async(input_signature="i", result_signature="s")
    async def tail(self, n: int) -> str:
        events = self.session.events.tail(n)
        return json.dumps({
            "events": [e.to_dict() for e in events],
            "gist": self.session.gist,
        })


# ============================================================================
# Server Entry Point
# ============================================================================

async def run_shell_server(session_id: str, command: str, cwd: str):
    """Run shell session server."""
    if sys.stdin.isatty():
        sys.exit("ERROR: must be launched via systemd-run")

    dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"

    async with anyio.create_task_group() as tg:
        session = ShellSession(session_id, tg)
        service = BuskerService(session)

        await request_default_bus_name_async(dbus_name)
        service.export_to_dbus(DBUS_PATH)

        print("=" * 60, file=sys.stderr)
        print("BUSKER SESSION STARTED", file=sys.stderr)
        print(f"  Session: {session_id}", file=sys.stderr)
        print(f"  D-Bus:   {dbus_name}", file=sys.stderr)
        print(f"  Command: {command}", file=sys.stderr)
        print("=" * 60, file=sys.stderr, flush=True)

        await session.start(command, cwd)
        await anyio.sleep_forever()


# ============================================================================
# Session Management (CLI helpers)
# ============================================================================

def list_sessions() -> list[dict]:
    """List all running busker sessions."""
    result = subprocess.run(
        ["systemctl", "--user", "list-units", "busker-*.service",
         "--no-legend", "--plain"],
        capture_output=True, text=True
    )
    sessions = []
    for line in result.stdout.strip().splitlines():
        if not line:
            continue
        parts = line.split()
        unit = parts[0]
        session_id = unit.replace("busker-", "").replace(".service", "")

        prop_result = subprocess.run(
            ["systemctl", "--user", "show", unit,
             "--property=ActiveEnterTimestamp,MainPID,WorkingDirectory"],
            capture_output=True, text=True
        )
        props = {}
        for prop_line in prop_result.stdout.strip().splitlines():
            if "=" in prop_line:
                k, v = prop_line.split("=", 1)
                props[k] = v

        sessions.append({
            "id": session_id,
            "unit": unit,
            "pid": props.get("MainPID", "?"),
            "cwd": props.get("WorkingDirectory", "?"),
            "started": props.get("ActiveEnterTimestamp", "?"),
        })
    return sessions


def unit_name(session_id: str) -> str:
    return f"busker-{session_id}.service"


def start_shell_session(command: str, cwd: str | None = None, inherit_env: bool = True) -> tuple[str, dict]:
    """Start a new shell session. Returns (session_id, context)."""
    session_id = gen_session_id()
    unit = unit_name(session_id)
    self_path = Path(__file__).resolve()
    working_dir = cwd or os.getcwd()

    dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"
    cmd = [
        "systemd-run", "--user",
        "--collect",
        f"--unit={unit}",
        f"--slice={SLICE}",
        "--service-type=dbus",
        f"--property=BusName={dbus_name}",
        "--property=StandardOutput=journal",
        "--property=StandardError=journal",
        f"--working-directory={working_dir}",
    ]

    # Pass environment
    if inherit_env:
        for var, val in os.environ.items():
            if val and not var.startswith("_"):
                cmd.append(f"--setenv={var}={val}")

    cmd.extend([
        "--",
        sys.executable, str(self_path),
        "--serve", "shell",
        "--session", session_id,
        "--shell-command", command,
    ])

    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(f"Failed to start: {result.stderr}")

    return session_id, {
        "systemd_unit": unit,
        "dbus_name": dbus_name,
        "dbus_path": DBUS_PATH,
        "logs_command": f"journalctl --user -u {unit} -f",
        "working_directory": working_dir,
    }


def stop_session(session_id: str):
    """Stop a session."""
    subprocess.run(["systemctl", "--user", "stop", unit_name(session_id)], check=True)


def get_service_proxy(session_id: str) -> BuskerService:
    """Get D-Bus proxy for session."""
    dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"
    return BuskerService.new_proxy(dbus_name, DBUS_PATH)


# ============================================================================
# MCP Server
# ============================================================================

def run_mcp():
    """Run as MCP server."""
    from mcp.server.fastmcp import FastMCP

    mcp = FastMCP("busker")
    _cursors: dict[str, int] = {}

    def get_cursor(session_id: str) -> int:
        return _cursors.get(session_id, 0)

    def set_cursor(session_id: str, cursor: int):
        _cursors[session_id] = cursor

    def get_session() -> tuple[BuskerService, str]:
        sessions = list_sessions()
        if not sessions:
            raise RuntimeError("No session running. Use run() to start one.")
        s = sessions[0]
        return get_service_proxy(s["id"]), s["id"]

    def format_events(data: dict, session_id: str) -> dict:
        """Format event response and update cursor."""
        events = data.get("events", [])
        cursor = data.get("cursor", 0)
        gist = data.get("gist", {})
        timed_out = data.get("timed_out", False)

        set_cursor(session_id, cursor)

        # Separate output from other events
        output = [e for e in events if e.get("kind") == "output"]
        state_events = [e for e in events if e.get("kind") == "state"]

        result = {"gist": gist, "timed_out": timed_out}

        if state_events:
            result["state_changes"] = [e["data"] for e in state_events]

        if output:
            lines = [e["data"] for e in output]
            if len(lines) > DEFAULT_TAIL:
                result["output"] = {
                    "lines": lines[-DEFAULT_TAIL:],
                    "total": len(lines),
                    "truncated": True,
                }
            else:
                result["output"] = {"lines": lines, "total": len(lines)}

        return result

    @mcp.tool()
    def run(command: str, cwd: str = "") -> dict:
        """Run a shell command in a new session."""
        sessions = list_sessions()
        if sessions:
            return {"error": "session already running", "session_id": sessions[0]["id"]}

        try:
            session_id, context = start_shell_session(command, cwd or None)
            return {"session_id": session_id, **context}
        except RuntimeError as e:
            return {"error": str(e)}

    @mcp.tool()
    def stop() -> dict:
        """Stop the current session."""
        sessions = list_sessions()
        if not sessions:
            return {"error": "no session running"}
        s = sessions[0]
        stop_session(s["id"])
        return {"stopped": s["id"]}

    @mcp.tool()
    async def poll() -> dict:
        """Get recent output and gist."""
        proxy, session_id = get_session()
        cursor = get_cursor(session_id)
        data = json.loads(await proxy.poll_events(cursor))
        return format_events(data, session_id)

    @mcp.tool()
    async def wait(timeout: float = 30, gather: float | None = None) -> dict:
        """Wait for events."""
        proxy, session_id = get_session()
        cursor = get_cursor(session_id)
        if gather:
            data = json.loads(await proxy.gather_events(cursor, gather, timeout))
        else:
            data = json.loads(await proxy.wait_events(cursor, timeout))
        return format_events(data, session_id)

    @mcp.tool()
    async def send(input: str) -> dict:
        """Send input to the process."""
        proxy, _ = get_session()
        return json.loads(await proxy.send_input(input))

    @mcp.tool()
    async def scroll(offset: int = 0, limit: int = 100) -> dict:
        """Read scrollback buffer."""
        proxy, _ = get_session()
        data = json.loads(await proxy.scrollback(offset, limit))
        events = data.get("events", [])
        return {
            "lines": [e["data"] for e in events if e.get("kind") == "output"],
            "total": data.get("total", 0),
        }

    mcp.run(transport="stdio")


# ============================================================================
# CLI
# ============================================================================

async def cmd_status():
    """Show session status."""
    sessions = list_sessions()
    if not sessions:
        console.print("[dim]no sessions[/dim]")
        console.print("[dim]busker run <command>[/dim]")
        return

    for s in sessions:
        try:
            proxy = get_service_proxy(s["id"])
            gist = json.loads(await proxy.gist)
            status = "[green]running[/green]" if gist.get("running") else "[red]exited[/red]"
            cmd = gist.get("command", "?")
            console.print(f"[bold cyan]{s['id']}[/bold cyan] {status} {cmd}")
        except Exception as e:
            console.print(f"[bold cyan]{s['id']}[/bold cyan] [red]unreachable[/red]")


async def cmd_poll(session_id: str):
    """Poll for events."""
    proxy = get_service_proxy(session_id)
    data = json.loads(await proxy.poll_events(0))
    gist = data.get("gist", {})
    events = data.get("events", [])

    # Show gist
    status = "[green]running[/green]" if gist.get("running") else f"[red]exited {gist.get('exit_code')}[/red]"
    console.print(f"[bold]{gist.get('command', '?')}[/bold] {status}")
    console.print()

    # Show last N lines of output
    output = [e for e in events if e.get("kind") == "output"]
    for e in output[-DEFAULT_TAIL:]:
        d = e["data"]
        text = d.get("text", "")
        if d.get("stream") == "stderr":
            console.print(f"[red]{text}[/red]")
        else:
            console.print(text)


def main():
    parser = argparse.ArgumentParser(description="Interactive process sessions over D-Bus")
    parser.add_argument("-s", "--session", help="Session ID")
    parser.add_argument("-j", "--json", action="store_true", help="JSON output")
    parser.add_argument("--serve", help=argparse.SUPPRESS)
    parser.add_argument("--shell-command", dest="shell_command", help=argparse.SUPPRESS)
    parser.add_argument("command", nargs="?", help="Command")
    parser.add_argument("args", nargs="*", help="Arguments")

    args = parser.parse_args()

    # Server mode
    if args.serve == "shell":
        asyncio.run(run_shell_server(args.session, args.shell_command, os.getcwd()))
        return

    # MCP mode
    if args.command == "mcp":
        run_mcp()
        return

    # CLI commands
    if not args.command:
        asyncio.run(cmd_status())
        return

    cmd = args.command.lower()

    if cmd == "run":
        if not args.args:
            error("run <command>")
        command = " ".join(args.args)
        try:
            session_id, ctx = start_shell_session(command)
            console.print(f"[bold cyan]{session_id}[/bold cyan] [green]started[/green]")
            console.print(f"[dim]{command}[/dim]")
        except RuntimeError as e:
            error(str(e))

    elif cmd == "stop":
        sessions = list_sessions()
        if not sessions:
            error("no sessions")
        s = sessions[0]
        stop_session(s["id"])
        console.print(f"[bold cyan]{s['id']}[/bold cyan] [red]stopped[/red]")

    elif cmd == "poll":
        sessions = list_sessions()
        if not sessions:
            error("no sessions")
        asyncio.run(cmd_poll(sessions[0]["id"]))

    else:
        error(f"unknown command: {cmd}")


if __name__ == "__main__":
    main()
