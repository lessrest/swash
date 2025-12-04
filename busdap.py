#!/usr/bin/env python3
"""
busdap - Debug Adapter Protocol over D-Bus

Usage:
    busdap                          Show session status
    busdap start --develop          Start under 'nix develop'
    busdap start --inherit          Start with current environment
    busdap stop                     Stop debug session
    busdap mcp                      Run as MCP server (stdio)

    busdap launch <program> [args]  Launch program for debugging
    busdap attach <pid>             Attach to running process
    busdap break <file> <line>      Set breakpoint at file:line
    busdap fbreak <function>        Set breakpoint at function
    busdap continue                 Continue execution
    busdap step                     Step over
    busdap stepin                   Step into
    busdap stepout                  Step out
    busdap bt                       Get backtrace
    busdap threads                  List threads
    busdap scopes <frame_id>        Get scopes for frame
    busdap vars <ref>               Get variables
    busdap eval <expr> [frame_id]   Evaluate expression
    busdap disconnect               Disconnect from debuggee

Options:
    -s, --session ID      Session ID (auto-detected when only one running)
    -j, --json            Output raw JSON
    --new                 Force start new session even if one exists
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
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import anyio
from anyio.streams.buffered import BufferedByteReceiveStream

from rich.console import Console
from rich.table import Table
from rich.text import Text
from rich import box

from sdbus import (
    DbusInterfaceCommonAsync,
    dbus_method_async,
    dbus_property_async,
    request_default_bus_name_async,
)

DBUS_NAME_PREFIX = "org.claude.Debugger"
DBUS_PATH = "/org/claude/Debugger"
SLICE = "busdap.slice"

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


def list_sessions() -> list[dict]:
    """List all running busdap sessions with their info."""
    result = subprocess.run(
        ["systemctl", "--user", "list-units", "busdap-*.service",
         "--no-legend", "--plain"],
        capture_output=True, text=True
    )
    sessions = []
    for line in result.stdout.strip().splitlines():
        if not line:
            continue
        parts = line.split()
        unit = parts[0]
        # Extract session ID from unit name like "busdap-KXO284.service"
        session_id = unit.replace("busdap-", "").replace(".service", "")

        # Get unit properties
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


def get_unique_session(sessions: list[dict], specified: str = None) -> str:
    """Get session ID, requiring exactly one if not specified."""
    if specified:
        return specified
    if len(sessions) == 0:
        console.print("[red]no sessions[/red]")
        console.print("[dim]busdap start --develop[/dim]")
        sys.exit(1)
    if len(sessions) == 1:
        return sessions[0]["id"]
    ids = [f"[cyan]{s['id']}[/cyan]" for s in sessions]
    console.print(f"[yellow]multiple sessions:[/yellow] {', '.join(ids)}")
    console.print("[dim]--session <id>[/dim]")
    sys.exit(1)


def log(direction: str, msg: dict):
    """Log DAP message to stderr (goes to journald via systemd service)."""
    compact = json.dumps(msg, separators=(",", ":"))
    print(f"[DAP {direction}] {compact}", file=sys.stderr, flush=True)


# ============================================================================
# Server (runs when 'busdap start' launches us with --serve)
# ============================================================================

@dataclass
class Event:
    """A single event in the append-only event log."""
    seq: int
    kind: str  # "state" | "output" | "breakpoint" | "response"
    data: dict
    timestamp: float = field(default_factory=time.time)


class DebuggerService(DbusInterfaceCommonAsync, interface_name=DBUS_NAME_PREFIX):
    """D-Bus interface for lldb-dap debugging with structured concurrency."""

    def __init__(self, session_id: str, server_tg: anyio.abc.TaskGroup):
        super().__init__()
        self.session_id = session_id
        self._server_tg = server_tg

        # DAP state
        self._seq = 1
        self._initialized = False
        self._program: str | None = None
        self._debug_state = "idle"  # idle, launching, stopped, running, exited
        self.thread_id: int | None = None

        # Subprocess management
        self._process: anyio.abc.Process | None = None
        self._subprocess_tg: anyio.abc.TaskGroup | None = None
        self._stdin: anyio.abc.ByteSendStream | None = None
        self._stdout: BufferedByteReceiveStream | None = None

        # Response routing: seq -> Event to signal when response arrives
        self._pending_requests: dict[int, anyio.Event] = {}
        self._responses: dict[int, dict] = {}

        # Append-only event log with cursor-based access
        self._events: list[Event] = []
        self._event_seq = 0
        self._lock = anyio.Lock()
        self._event_arrived = anyio.Condition(self._lock)

        # Current state snapshot (convenience)
        self._current_state: dict | None = None

    def _next_seq(self) -> int:
        """Get next DAP sequence number."""
        seq = self._seq
        self._seq += 1
        return seq

    async def _append_event(self, kind: str, data: dict):
        """Append an event to the log and notify waiters."""
        async with self._event_arrived:
            event = Event(seq=self._event_seq, kind=kind, data=data)
            self._events.append(event)
            self._event_seq += 1

            # Update current state snapshot for state events
            if kind == "state":
                self._current_state = data
                if data.get("event") == "stopped":
                    self.thread_id = data.get("threadId")
                    self._debug_state = "stopped"
                elif data.get("event") == "exited":
                    self._debug_state = "exited"
                elif data.get("event") == "terminated":
                    self._debug_state = "terminated"

            # Wake up any waiters
            self._event_arrived.notify_all()

    async def _ensure_lldb(self):
        """Ensure lldb-dap subprocess is running."""
        if self._process is not None and self._process.returncode is None:
            return  # Already running

        # Start subprocess in a new task group
        self._process = await anyio.open_process(
            ["lldb-dap"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        self._stdin = self._process.stdin
        self._stdout = BufferedByteReceiveStream(self._process.stdout)

        # Reset state
        self._seq = 1
        self._initialized = False
        self._events.clear()
        self._event_seq = 0

        # Start reader task
        self._server_tg.start_soon(self._reader_task)

    async def _reader_task(self):
        """Read messages from lldb-dap until it closes."""
        try:
            while True:
                msg = await self._read_message()
                if msg is None:
                    break
                await self._handle_message(msg)
        except anyio.ClosedResourceError:
            log("INF", {"message": "lldb-dap stream closed"})
        except Exception as e:
            log("ERR", {"error": str(e), "context": "reader_task"})
        finally:
            # Record that subprocess ended
            await self._append_event("state", {"event": "terminated", "reason": "subprocess_exit"})

    async def _read_message(self) -> dict | None:
        """Read a single DAP message from lldb-dap stdout."""
        try:
            # Read headers until \r\n\r\n
            headers = b""
            while b"\r\n\r\n" not in headers:
                chunk = await self._stdout.receive(1)
                if not chunk:
                    return None
                headers += chunk

            # Parse Content-Length
            header_str = headers.decode()
            content_length = None
            for line in header_str.split("\r\n"):
                if line.startswith("Content-Length:"):
                    content_length = int(line.split(":")[1].strip())
                    break

            if content_length is None:
                log("ERR", {"error": f"No Content-Length in: {header_str}"})
                return None

            # Read body
            body = await self._stdout.receive_exactly(content_length)
            msg = json.loads(body.decode())
            log("<<<", msg)
            return msg

        except anyio.EndOfStream:
            return None
        except Exception as e:
            log("ERR", {"error": str(e), "context": "read_message"})
            return None

    async def _handle_message(self, msg: dict):
        """Route incoming DAP message."""
        msg_type = msg.get("type")

        if msg_type == "response":
            request_seq = msg.get("request_seq")
            async with self._lock:
                if request_seq in self._pending_requests:
                    self._responses[request_seq] = msg
                    self._pending_requests[request_seq].set()

        elif msg_type == "event":
            await self._handle_event(msg)

    async def _handle_event(self, event: dict):
        """Process and store a DAP event."""
        etype = event.get("event", "")
        body = event.get("body", {})

        if etype in ("stopped", "exited", "terminated"):
            await self._append_event("state", {
                "event": etype,
                "reason": body.get("reason"),
                "description": body.get("description"),
                "threadId": body.get("threadId"),
                "exitCode": body.get("exitCode"),
            })
        elif etype == "output":
            text = body.get("output", "").rstrip()
            if text:
                await self._append_event("output", {
                    "category": body.get("category", "stdout"),
                    "text": text,
                })
        elif etype == "breakpoint":
            await self._append_event("breakpoint", body)
        else:
            # Store other events too
            await self._append_event(etype, body)

    async def _send_request(self, command: str, arguments: dict | None = None) -> dict:
        """Send DAP request and wait for response."""
        if self._process is None or self._stdin is None:
            raise RuntimeError("lldb-dap not running")

        seq = self._next_seq()
        request = {"seq": seq, "type": "request", "command": command}
        if arguments:
            request["arguments"] = arguments

        # Register pending request
        response_event = anyio.Event()
        async with self._lock:
            self._pending_requests[seq] = response_event

        try:
            # Send request
            log(">>>", request)
            body = json.dumps(request)
            header = f"Content-Length: {len(body)}\r\n\r\n"
            await self._stdin.send((header + body).encode())

            # Wait for response with timeout
            with anyio.fail_after(30):
                await response_event.wait()

            # Get response
            async with self._lock:
                response = self._responses.pop(seq, {})
                del self._pending_requests[seq]

            return {"response": response, "cursor": self._event_seq}

        except TimeoutError:
            async with self._lock:
                self._pending_requests.pop(seq, None)
            raise TimeoutError(f"No response for {command} (seq={seq})")

    # -------------------------------------------------------------------------
    # D-Bus Properties
    # -------------------------------------------------------------------------

    @dbus_property_async(property_signature="b")
    def is_running(self) -> bool:
        return self._process is not None and self._process.returncode is None

    @dbus_property_async(property_signature="b")
    def is_initialized(self) -> bool:
        return self._initialized

    # -------------------------------------------------------------------------
    # D-Bus Methods - Lifecycle
    # -------------------------------------------------------------------------

    @dbus_method_async(result_signature="s")
    async def initialize(self) -> str:
        await self._ensure_lldb()
        result = await self._send_request("initialize", {
            "clientID": "busdap",
            "clientName": "busdap",
            "adapterID": "lldb",
            "pathFormat": "path",
            "linesStartAt1": True,
            "columnsStartAt1": True,
        })
        self._initialized = True
        return json.dumps(result)

    @dbus_method_async(input_signature="sass", result_signature="s")
    async def launch(self, program: str, args: list, cwd: str) -> str:
        if not self._initialized:
            await self.initialize()

        self._program = os.path.abspath(program)
        self._debug_state = "launching"

        result = await self._send_request("launch", {
            "program": self._program,
            "args": args,
            "cwd": cwd or os.getcwd(),
            "stopOnEntry": True,
        })

        # Send configurationDone to actually start
        await self._send_request("configurationDone", {})

        return json.dumps(result)

    @dbus_method_async(input_signature="i", result_signature="s")
    async def attach(self, pid: int) -> str:
        if not self._initialized:
            await self.initialize()
        return json.dumps(await self._send_request("attach", {"pid": pid}))

    @dbus_method_async(result_signature="s")
    async def disconnect(self) -> str:
        result = await self._send_request("disconnect", {"terminateDebuggee": True})
        return json.dumps(result)

    # -------------------------------------------------------------------------
    # D-Bus Methods - Breakpoints
    # -------------------------------------------------------------------------

    @dbus_method_async(input_signature="si", result_signature="s")
    async def set_breakpoint(self, file_path: str, line: int) -> str:
        return json.dumps(await self._send_request("setBreakpoints", {
            "source": {"path": os.path.abspath(file_path)},
            "breakpoints": [{"line": line}],
        }))

    @dbus_method_async(input_signature="s", result_signature="s")
    async def set_function_breakpoint(self, function_name: str) -> str:
        return json.dumps(await self._send_request("setFunctionBreakpoints", {
            "breakpoints": [{"name": function_name}],
        }))

    # -------------------------------------------------------------------------
    # D-Bus Methods - Execution Control
    # -------------------------------------------------------------------------

    @dbus_method_async(result_signature="s")
    async def continue_execution(self) -> str:
        self._debug_state = "running"
        return json.dumps(await self._send_request("continue", {"threadId": self.thread_id or 1}))

    @dbus_method_async(result_signature="s")
    async def pause(self) -> str:
        return json.dumps(await self._send_request("pause", {"threadId": self.thread_id or 1}))

    @dbus_method_async(result_signature="s")
    async def step_over(self) -> str:
        return json.dumps(await self._send_request("next", {"threadId": self.thread_id or 1}))

    @dbus_method_async(result_signature="s")
    async def step_into(self) -> str:
        return json.dumps(await self._send_request("stepIn", {"threadId": self.thread_id or 1}))

    @dbus_method_async(result_signature="s")
    async def step_out(self) -> str:
        return json.dumps(await self._send_request("stepOut", {"threadId": self.thread_id or 1}))

    # -------------------------------------------------------------------------
    # D-Bus Methods - Inspection
    # -------------------------------------------------------------------------

    @dbus_method_async(result_signature="s")
    async def threads(self) -> str:
        return json.dumps(await self._send_request("threads", {}))

    @dbus_method_async(result_signature="s")
    async def stack_trace(self) -> str:
        return json.dumps(await self._send_request("stackTrace", {
            "threadId": self.thread_id or 1,
            "levels": 50,
        }))

    @dbus_method_async(input_signature="i", result_signature="s")
    async def scopes(self, frame_id: int) -> str:
        return json.dumps(await self._send_request("scopes", {"frameId": frame_id}))

    @dbus_method_async(input_signature="i", result_signature="s")
    async def variables(self, variables_reference: int) -> str:
        return json.dumps(await self._send_request("variables", {
            "variablesReference": variables_reference,
        }))

    @dbus_method_async(input_signature="si", result_signature="s")
    async def evaluate(self, expression: str, frame_id: int) -> str:
        return json.dumps(await self._send_request("evaluate", {
            "expression": expression,
            "frameId": frame_id,
            "context": "repl",
        }))

    # -------------------------------------------------------------------------
    # D-Bus Methods - Event Access
    # -------------------------------------------------------------------------

    @dbus_method_async(result_signature="s")
    async def status(self) -> str:
        """Get current debug status."""
        async with self._lock:
            return json.dumps({
                "initialized": self._initialized,
                "program": self._program,
                "debug_state": self._debug_state,
                "thread_id": self.thread_id,
                "current_state": self._current_state,
                "event_cursor": self._event_seq,
            })

    @dbus_method_async(input_signature="i", result_signature="s")
    async def poll_events(self, since: int) -> str:
        """Get events since cursor position. Returns events and new cursor."""
        async with self._lock:
            events_slice = self._events[since:] if since < len(self._events) else []
            return json.dumps({
                "events": [
                    {"seq": e.seq, "kind": e.kind, "data": e.data, "timestamp": e.timestamp}
                    for e in events_slice
                ],
                "cursor": self._event_seq,
                "state": self._current_state,
            })

    def _events_since(self, since: int) -> list[dict]:
        """Get events since cursor (must hold lock)."""
        events_slice = self._events[since:] if since < len(self._events) else []
        return [
            {"seq": e.seq, "kind": e.kind, "data": e.data, "timestamp": e.timestamp}
            for e in events_slice
        ]

    @dbus_method_async(input_signature="id", result_signature="s")
    async def wait_event(self, since: int, timeout_secs: float) -> str:
        """Wait for next event after cursor. Returns immediately when event arrives or on timeout."""
        try:
            with anyio.fail_after(timeout_secs):
                async with self._event_arrived:
                    # Check if events already available
                    while self._event_seq <= since:
                        await self._event_arrived.wait()

                    return json.dumps({
                        "events": self._events_since(since),
                        "cursor": self._event_seq,
                        "state": self._current_state,
                        "timed_out": False,
                    })
        except TimeoutError:
            async with self._lock:
                return json.dumps({
                    "events": self._events_since(since),
                    "cursor": self._event_seq,
                    "state": self._current_state,
                    "timed_out": True,
                })

    @dbus_method_async(input_signature="idd", result_signature="s")
    async def gather_events(self, since: int, gather_secs: float, timeout_secs: float) -> str:
        """Gather events for a duration. Buffers for gather_secs before returning."""
        deadline = time.time() + timeout_secs
        gather_until = time.time() + gather_secs

        try:
            # Wait for at least one event or timeout
            with anyio.fail_after(timeout_secs):
                async with self._event_arrived:
                    while self._event_seq <= since:
                        await self._event_arrived.wait()

            # Now gather for the gather period (or until timeout)
            remaining_gather = gather_until - time.time()
            if remaining_gather > 0:
                remaining_timeout = deadline - time.time()
                wait_time = min(remaining_gather, remaining_timeout)
                if wait_time > 0:
                    await anyio.sleep(wait_time)

            async with self._lock:
                return json.dumps({
                    "events": self._events_since(since),
                    "cursor": self._event_seq,
                    "state": self._current_state,
                    "timed_out": False,
                })
        except TimeoutError:
            async with self._lock:
                return json.dumps({
                    "events": self._events_since(since),
                    "cursor": self._event_seq,
                    "state": self._current_state,
                    "timed_out": True,
                })


async def run_server(session_id: str):
    """Run the D-Bus server with structured concurrency."""
    # Refuse to run on a TTY (must be launched via systemd-run)
    if sys.stdin.isatty():
        sys.exit("ERROR: busdap --serve must not be run on a TTY. Use 'busdap start' instead.")

    dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"

    async with anyio.create_task_group() as tg:
        # Create service with access to task group for spawning reader tasks
        service = DebuggerService(session_id, tg)

        # Register D-Bus name and export service
        await request_default_bus_name_async(dbus_name)
        service.export_to_dbus(DBUS_PATH)

        # Log startup
        print("=" * 60, file=sys.stderr)
        print("BUSDAP SERVER STARTED", file=sys.stderr)
        print(f"  Session: {session_id}", file=sys.stderr)
        print(f"  D-Bus:   {dbus_name}", file=sys.stderr)
        print(f"  PID:     {os.getpid()}", file=sys.stderr)
        print("=" * 60, file=sys.stderr, flush=True)

        # Keep alive - task group keeps running until cancelled
        await anyio.sleep_forever()


# ============================================================================
# Output formatting
# ============================================================================

def print_response(cmd: str, data: dict):
    """Print DAP response with rich formatting."""
    response = data.get("response", {})
    body = response.get("body", {})
    events = data.get("events", [])

    if cmd == "bt":
        frames = body.get("stackFrames", [])
        if frames:
            table = Table(box=box.SIMPLE, show_header=False, padding=(0, 1))
            table.add_column("id", style="dim", width=4, justify="right")
            table.add_column("name", style="cyan")
            table.add_column("location", style="dim")
            for frame in frames:
                name = frame.get("name", "??")
                source = frame.get("source", {})
                path = source.get("path") or source.get("name", "??")
                # Shorten path
                if "/" in path:
                    path = ".../" + "/".join(path.split("/")[-2:])
                loc = f"{path}:{frame.get('line', '?')}"
                table.add_row(str(frame.get("id", "?")), name, loc)
            console.print(table)
        else:
            console.print("[dim](no stack frames)[/dim]")

    elif cmd in ("break", "fbreak"):
        for bp in body.get("breakpoints", []):
            source = bp.get("source", {})
            path = source.get("path") or source.get("name", "??")
            if "/" in path:
                path = ".../" + "/".join(path.split("/")[-2:])
            verified = bp.get("verified")
            status = Text("verified", style="green") if verified else Text("pending", style="yellow")
            console.print(f"[bold]bp {bp.get('id', '?')}[/bold] {path}:[cyan]{bp.get('line', '?')}[/cyan] ", end="")
            console.print(status)

    elif cmd == "vars":
        table = Table(box=box.SIMPLE, show_header=False, padding=(0, 1))
        table.add_column("name", style="cyan")
        table.add_column("value")
        table.add_column("type", style="dim")
        table.add_column("ref", style="dim")
        for var in body.get("variables", []):
            ref = var.get("variablesReference", 0)
            ref_str = f"[{ref}]" if ref > 0 else ""
            table.add_row(
                var.get("name", "?"),
                var.get("value", "?"),
                var.get("type", ""),
                ref_str
            )
        console.print(table)

    elif cmd == "scopes":
        for scope in body.get("scopes", []):
            console.print(f"[bold]{scope.get('name', '?')}[/bold] [dim]ref={scope.get('variablesReference', 0)}[/dim]")

    elif cmd == "threads":
        for thread in body.get("threads", []):
            console.print(f"[cyan]{thread.get('id', '?')}[/cyan] {thread.get('name', '?')}")

    elif cmd == "eval":
        result = body.get("result", "")
        vtype = body.get("type", "")
        console.print(f"[green]{result}[/green]" + (f" [dim]({vtype})[/dim]" if vtype else ""))

    elif cmd == "init":
        console.print(f"[green]initialized[/green] [dim]{body.get('$__lldb_version', '')}[/dim]")

    elif cmd == "launch":
        if response.get("success"):
            console.print("[green]launched[/green]")
        else:
            console.print(f"[red]failed:[/red] {response.get('message', '?')}")

    # Events
    for event in events:
        etype = event.get("event", "")
        ebody = event.get("body", {})
        if etype == "stopped":
            reason = ebody.get("reason", "unknown")
            desc = ebody.get("description", "")
            style = "yellow" if reason == "breakpoint" else "magenta"
            console.print(f"[{style}]stopped[/{style}] {reason}" + (f" [dim]{desc}[/dim]" if desc else ""))
        elif etype == "output":
            output = ebody.get("output", "").rstrip()
            if output:
                cat = ebody.get("category", "")
                if cat == "stderr":
                    console.print(f"[red]{output}[/red]")
                elif cat and cat != "stdout":
                    console.print(f"[dim][{cat}][/dim] {output}")
                else:
                    console.print(output)
        elif etype == "exited":
            code = ebody.get("exitCode", "?")
            style = "green" if code == 0 else "red"
            console.print(f"[{style}]exited[/{style}] {code}")
        elif etype == "terminated":
            console.print("[yellow]terminated[/yellow]")


# ============================================================================
# CLI
# ============================================================================

def unit_name(session: str) -> str:
    """Get systemd unit name for a session."""
    return f"busdap-{session}.service"


def format_elapsed(timestamp_str: str) -> str:
    """Format elapsed time from systemd timestamp."""
    if not timestamp_str or timestamp_str == "?":
        return "?"
    try:
        from datetime import datetime
        # Parse systemd timestamp like "Thu 2024-01-01 12:00:00 UTC"
        parts = timestamp_str.split()
        if len(parts) >= 4:
            dt_str = f"{parts[1]} {parts[2]}"
            dt = datetime.strptime(dt_str, "%Y-%m-%d %H:%M:%S")
            elapsed = datetime.now() - dt
            mins = int(elapsed.total_seconds() / 60)
            if mins < 60:
                return f"{mins}m"
            hours = mins // 60
            return f"{hours}h{mins % 60}m"
    except:
        pass
    return "?"


def do_start_session(develop: bool, inherit: bool, cwd: str | None = None) -> tuple[str, str]:
    """
    Start a new debug session. Returns (session_id, env_mode).
    Raises RuntimeError on failure.
    """
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

    # Collect env vars to pass
    env_vars = {}
    if inherit:
        for var, val in os.environ.items():
            if val and not var.startswith("_"):
                env_vars[var] = val
        env_mode = "inherit"
    else:
        for var in ["PATH", "HOME", "USER"]:
            val = os.environ.get(var)
            if val:
                env_vars[var] = val
        env_mode = "nix develop"

    for var, val in env_vars.items():
        cmd.append(f"--setenv={var}={val}")

    cmd.append("--")

    if develop:
        cmd.extend(["nix", "develop", "--command"])

    cmd.extend([sys.executable, str(self_path), "--serve", "--session", session_id])

    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(f"Failed to start session: {result.stderr}")

    return session_id, env_mode


def do_stop_session(session_id: str):
    """Stop a debug session."""
    unit = unit_name(session_id)
    subprocess.run(["systemctl", "--user", "stop", unit], check=True)


def cmd_start(args):
    """Start a new debug session (CLI entry point)."""
    sessions = list_sessions()

    # Check for existing sessions
    if sessions and not args.new:
        s = sessions[0]
        elapsed = format_elapsed(s["started"])
        console.print(f"[yellow]already running:[/yellow] [bold cyan]{s['id']}[/bold cyan] [dim]({elapsed})[/dim]")
        console.print(f"  [dim]{s['cwd']}[/dim]")
        console.print("  use [bold]--new[/bold] to start another")
        sys.exit(1)

    # Require explicit environment mode
    if not args.develop and not args.inherit:
        console.print("[red]error:[/red] no environment specified")
        console.print("  [bold]--develop[/bold]  run under 'nix develop'")
        console.print("  [bold]--inherit[/bold]  inherit current environment")
        sys.exit(1)

    # Generate new session ID
    session_id = gen_session_id()
    unit = unit_name(session_id)
    self_path = Path(__file__).resolve()
    cwd = os.getcwd()

    dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"
    cmd = [
        "systemd-run", "--user",
        "--collect",
        f"--unit={unit}",
        f"--slice={SLICE}",
        "--service-type=dbus",  # Wait for D-Bus name acquisition
        f"--property=BusName={dbus_name}",
        "--property=StandardOutput=journal",
        "--property=StandardError=journal",
        f"--working-directory={cwd}",
    ]

    # Collect env vars to pass
    env_vars = {}
    if args.inherit:
        for var, val in os.environ.items():
            if val and not var.startswith("_"):
                env_vars[var] = val
        env_mode = "inherit"
    else:
        # --develop: minimal env to find nix
        for var in ["PATH", "HOME", "USER"]:
            val = os.environ.get(var)
            if val:
                env_vars[var] = val
        env_mode = "nix develop"

    # Add env vars to command
    for var, val in env_vars.items():
        cmd.append(f"--setenv={var}={val}")

    cmd.append("--")

    if args.develop:
        cmd.extend(["nix", "develop", "--command"])

    cmd.extend([sys.executable, str(self_path), "--serve", "--session", session_id])

    # Start with status display
    console.print(f"[bold cyan]{session_id}[/bold cyan] [dim]{env_mode}[/dim]")

    with console.status("[dim]starting...[/dim]", spinner="dots"):
        result = subprocess.run(cmd, capture_output=True, text=True)

    if result.returncode != 0:
        console.print("[red]failed[/red]")
        console.print(f"[dim]{result.stderr}[/dim]", style="red")
        console.print(f"[dim]journalctl --user -u {unit}[/dim]")
        sys.exit(1)

    console.print("[green]ready[/green]")


def cmd_stop(args):
    """Stop a debug session."""
    sessions = list_sessions()
    session_id = get_unique_session(sessions, args.session)
    unit = unit_name(session_id)
    subprocess.run(["systemctl", "--user", "stop", unit], check=True)
    console.print(f"[red]stopped[/red] [bold cyan]{session_id}[/bold cyan]")


async def cmd_status(args):
    """Show status of all sessions (default command)."""
    sessions = list_sessions()
    if not sessions:
        console.print("[dim]no sessions[/dim]")
        console.print("[dim]busdap start --develop[/dim]")
        return

    for s in sessions:
        elapsed = format_elapsed(s["started"])
        console.print(f"[bold cyan]{s['id']}[/bold cyan] [dim]{elapsed}[/dim]  [dim]{s['cwd']}[/dim]")

        # Try to get debug status from the service
        try:
            dbus_name = f"{DBUS_NAME_PREFIX}.{s['id']}"
            debugger = DebuggerService.new_proxy(dbus_name, DBUS_PATH)
            status_json = await debugger.status()
            status = json.loads(status_json)
            program = status.get("program")
            state = status.get("state", "idle")
            if program:
                prog_name = os.path.basename(program)
                state_style = "yellow" if state == "stopped" else "green" if state == "running" else "dim"
                console.print(f"  [dim]└─[/dim] {prog_name} [{state_style}]{state}[/{state_style}]")
            else:
                console.print("  [dim]└─ (no program)[/dim]")
        except Exception:
            console.print("  [dim]└─ (unreachable)[/dim]")


async def cmd_client(args, cmd: str, cmd_args: list):
    """Run a client command."""
    sessions = list_sessions()
    session_id = get_unique_session(sessions, args.session)
    dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"
    debugger = DebuggerService.new_proxy(dbus_name, DBUS_PATH)

    def output(result: str):
        if args.json:
            console.print_json(result)
        else:
            print_response(cmd, json.loads(result))

    if cmd == "status":
        running = await debugger.is_running
        initialized = await debugger.is_initialized
        console.print(f"[bold cyan]{session_id}[/bold cyan]")
        console.print(f"  running: [{'green' if running else 'red'}]{running}[/]")
        console.print(f"  initialized: [{'green' if initialized else 'dim'}]{initialized}[/]")

    elif cmd == "init":
        output(await debugger.initialize())

    elif cmd == "launch":
        if not cmd_args:
            error("launch <program> [args]")
        output(await debugger.launch(cmd_args[0], cmd_args[1:], os.getcwd()))

    elif cmd == "attach":
        if not cmd_args:
            error("attach <pid>")
        output(await debugger.attach(int(cmd_args[0])))

    elif cmd == "break":
        if len(cmd_args) < 2:
            error("break <file> <line>")
        output(await debugger.set_breakpoint(cmd_args[0], int(cmd_args[1])))

    elif cmd == "fbreak":
        if not cmd_args:
            error("fbreak <function>")
        output(await debugger.set_function_breakpoint(cmd_args[0]))

    elif cmd == "continue":
        output(await debugger.continue_execution())

    elif cmd == "step":
        output(await debugger.step_over())

    elif cmd == "stepin":
        output(await debugger.step_into())

    elif cmd == "stepout":
        output(await debugger.step_out())

    elif cmd == "bt":
        output(await debugger.stack_trace())

    elif cmd == "threads":
        output(await debugger.threads())

    elif cmd == "scopes":
        if not cmd_args:
            error("scopes <frame_id>")
        output(await debugger.scopes(int(cmd_args[0])))

    elif cmd == "vars":
        if not cmd_args:
            error("vars <ref>")
        output(await debugger.variables(int(cmd_args[0])))

    elif cmd == "eval":
        if not cmd_args:
            error("eval <expr> [frame_id]")
        frame_id = int(cmd_args[1]) if len(cmd_args) > 1 else 0
        output(await debugger.evaluate(cmd_args[0], frame_id))

    elif cmd == "disconnect":
        output(await debugger.disconnect())

    else:
        error(f"unknown command: {cmd}")


# ============================================================================
# MCP Server
# ============================================================================

def run_mcp():
    """Run as MCP server."""
    from mcp.server.fastmcp import FastMCP

    mcp = FastMCP("busdap")

    def clean_dap(raw: str, session_id: str | None = None) -> dict:
        """Clean up DAP response for readability. Tracks cursor if session_id provided."""
        data = json.loads(raw)
        result = {}

        # Extract response body
        resp = data.get("response", {})
        if not resp.get("success", True):
            result["error"] = resp.get("message", "unknown error")
        body = resp.get("body", {})
        if body:
            result.update(body)

        # Track cursor internally (don't expose to client)
        if "cursor" in data and session_id:
            set_cursor(session_id, data["cursor"])

        return result

    # Track event cursor per session (MCP server state)
    _cursors: dict[str, int] = {}

    def get_debugger():
        """Get debugger proxy for existing session."""
        sessions = list_sessions()
        if not sessions:
            raise RuntimeError("No session running. Use start_session first.")
        session_id = sessions[0]["id"]
        dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"
        return DebuggerService.new_proxy(dbus_name, DBUS_PATH), session_id

    def get_cursor(session_id: str) -> int:
        """Get current cursor for session."""
        return _cursors.get(session_id, 0)

    def set_cursor(session_id: str, cursor: int):
        """Update cursor for session."""
        _cursors[session_id] = cursor

    def get_cli_path() -> str:
        """Get path to the busdap CLI wrapper."""
        return str(Path(__file__).parent / "busdap")

    def session_context(session_id: str, working_dir: str) -> dict:
        """Build context info for a session."""
        return {
            "systemd_unit": f"busdap-{session_id}.service",
            "dbus_name": f"{DBUS_NAME_PREFIX}.{session_id}",
            "dbus_path": DBUS_PATH,
            "cli_tool": get_cli_path(),
            "logs_command": f"journalctl --user -u busdap-{session_id}.service -f",
            "stop_command": f"systemctl --user stop busdap-{session_id}.service",
            "working_directory": working_dir,
        }

    @mcp.tool()
    def start_session(env: str = "develop", cwd: str = "") -> dict:
        """Start a debug session. env: 'develop' (nix develop) or 'inherit' (current env). cwd: working directory."""
        sessions = list_sessions()
        if sessions:
            s = sessions[0]
            return {
                "error": "session already running",
                "session_id": s["id"],
                **session_context(s["id"], s["cwd"]),
            }

        if env not in ("develop", "inherit"):
            return {"error": "env must be 'develop' or 'inherit'"}

        # Resolve working directory
        working_dir = None
        if cwd:
            working_dir = os.path.expanduser(cwd)
            if not os.path.isdir(working_dir):
                return {"error": f"directory not found: {cwd}"}

        try:
            session_id, env_mode = do_start_session(
                develop=(env == "develop"),
                inherit=(env == "inherit"),
                cwd=working_dir,
            )
            return {
                "session_id": session_id,
                "env": env_mode,
                **session_context(session_id, working_dir or os.getcwd()),
            }
        except RuntimeError as e:
            return {"error": str(e)}

    @mcp.tool()
    def stop_session() -> dict:
        """Stop the debug session."""
        sessions = list_sessions()
        if not sessions:
            return {"error": "no session running", "cli_tool": get_cli_path()}

        s = sessions[0]
        context = session_context(s["id"], s["cwd"])

        try:
            do_stop_session(s["id"])
            return {"stopped": s["id"], "was": context}
        except Exception as e:
            return {"error": str(e), "session_id": s["id"]}

    @mcp.tool()
    async def status() -> dict:
        """Get debug session status."""
        sessions = list_sessions()
        if not sessions:
            return {
                "sessions": [],
                "cli_tool": get_cli_path(),
                "hint": "Use start_session to begin a debug session",
            }
        result = []
        for s in sessions:
            try:
                dbus_name = f"{DBUS_NAME_PREFIX}.{s['id']}"
                debugger = DebuggerService.new_proxy(dbus_name, DBUS_PATH)
                status_json = await debugger.status()
                info = json.loads(status_json)
                info["session_id"] = s["id"]
                info.update(session_context(s["id"], s["cwd"]))
                result.append(info)
            except Exception as e:
                result.append({
                    "session_id": s["id"],
                    "error": str(e),
                    **session_context(s["id"], s["cwd"]),
                })
        return {"sessions": result}

    @mcp.tool()
    async def launch(program: str, args: list[str] = [], cwd: str = "") -> dict:
        """Launch a program for debugging. Stops on entry."""
        debugger, session_id = get_debugger()
        return clean_dap(await debugger.launch(program, args, cwd or os.getcwd()), session_id)

    @mcp.tool()
    async def attach(pid: int) -> dict:
        """Attach to a running process by PID."""
        debugger, session_id = get_debugger()
        return clean_dap(await debugger.attach(pid), session_id)

    @mcp.tool()
    async def breakpoint(file: str, line: int) -> dict:
        """Set a breakpoint at file:line."""
        debugger, session_id = get_debugger()
        return clean_dap(await debugger.set_breakpoint(file, line), session_id)

    @mcp.tool()
    async def function_breakpoint(function: str) -> dict:
        """Set a breakpoint at a function by name."""
        debugger, session_id = get_debugger()
        return clean_dap(await debugger.set_function_breakpoint(function), session_id)

    async def _maybe_wait(debugger, session_id: str, gather: float | None, timeout: float | None) -> dict | None:
        """Optionally wait for events after a command."""
        if timeout is None:
            return None
        cursor = get_cursor(session_id)
        if gather is not None and gather > 0:
            data = json.loads(await debugger.gather_events(cursor, gather, timeout))
        else:
            data = json.loads(await debugger.wait_event(cursor, timeout))
        return _format_events_result(data, session_id)

    @mcp.tool()
    async def continue_execution(gather: float | None = None, timeout: float | None = None) -> dict:
        """Continue execution. Optional: gather=secs to buffer events, timeout=secs to wait."""
        debugger, session_id = get_debugger()
        result = clean_dap(await debugger.continue_execution(), session_id)
        waited = await _maybe_wait(debugger, session_id, gather, timeout)
        if waited:
            result["waited"] = waited
        return result

    @mcp.tool()
    async def step_over(gather: float | None = None, timeout: float | None = None) -> dict:
        """Step over current line. Optional: gather=secs to buffer, timeout=secs to wait for stop."""
        debugger, session_id = get_debugger()
        result = clean_dap(await debugger.step_over(), session_id)
        waited = await _maybe_wait(debugger, session_id, gather, timeout)
        if waited:
            result["waited"] = waited
        return result

    @mcp.tool()
    async def step_into(gather: float | None = None, timeout: float | None = None) -> dict:
        """Step into function. Optional: gather=secs to buffer, timeout=secs to wait for stop."""
        debugger, session_id = get_debugger()
        result = clean_dap(await debugger.step_into(), session_id)
        waited = await _maybe_wait(debugger, session_id, gather, timeout)
        if waited:
            result["waited"] = waited
        return result

    @mcp.tool()
    async def step_out(gather: float | None = None, timeout: float | None = None) -> dict:
        """Step out of function. Optional: gather=secs to buffer, timeout=secs to wait for stop."""
        debugger, session_id = get_debugger()
        result = clean_dap(await debugger.step_out(), session_id)
        waited = await _maybe_wait(debugger, session_id, gather, timeout)
        if waited:
            result["waited"] = waited
        return result

    @mcp.tool()
    async def backtrace() -> dict:
        """Get stack trace (backtrace) of current thread."""
        debugger, session_id = get_debugger()
        return clean_dap(await debugger.stack_trace(), session_id)

    @mcp.tool()
    async def threads() -> dict:
        """List all threads."""
        debugger, session_id = get_debugger()
        return clean_dap(await debugger.threads(), session_id)

    @mcp.tool()
    async def scopes(frame_id: int) -> dict:
        """Get variable scopes for a stack frame."""
        debugger, session_id = get_debugger()
        return clean_dap(await debugger.scopes(frame_id), session_id)

    @mcp.tool()
    async def variables(reference: int) -> dict:
        """Get variables by reference ID (from scopes or nested variables)."""
        debugger, session_id = get_debugger()
        return clean_dap(await debugger.variables(reference), session_id)

    @mcp.tool()
    async def evaluate(expression: str, frame_id: int = 0) -> dict:
        """Evaluate an expression in the given stack frame context."""
        debugger, session_id = get_debugger()
        return clean_dap(await debugger.evaluate(expression, frame_id), session_id)

    @mcp.tool()
    async def disconnect() -> dict:
        """Disconnect from debuggee and terminate it."""
        debugger, session_id = get_debugger()
        return clean_dap(await debugger.disconnect(), session_id)

    @mcp.tool()
    async def poll_events(since: int = 0) -> dict:
        """Poll for events since cursor position. Returns events and new cursor for next poll."""
        debugger, session_id = get_debugger()
        # Use tracked cursor if since not explicitly provided (default 0 means use tracked)
        cursor_to_use = since if since > 0 else get_cursor(session_id)
        data = json.loads(await debugger.poll_events(cursor_to_use))
        return _format_events_result(data, session_id)

    def _format_events_result(data: dict, session_id: str, update_cursor: bool = True) -> dict:
        """Format event response for MCP and optionally update tracked cursor."""
        events = data.get("events", [])
        cursor = data.get("cursor", 0)
        state = data.get("state")
        timed_out = data.get("timed_out", False)

        # Update tracked cursor
        if update_cursor:
            set_cursor(session_id, cursor)

        output_events = [e for e in events if e["kind"] == "output"]
        state_events = [e for e in events if e["kind"] == "state"]
        other_events = [e for e in events if e["kind"] not in ("output", "state")]

        result = {
            "current_state": state,
            "timed_out": timed_out,
        }

        if state_events:
            result["state_changes"] = [e["data"] for e in state_events]

        if output_events:
            lines = [e["data"] for e in output_events]
            result["output"] = {
                "count": len(lines),
                "lines": lines[-20:] if len(lines) > 20 else lines,
                "truncated": len(lines) > 20,
            }

        if other_events:
            result["other_events"] = [{"kind": e["kind"], "data": e["data"]} for e in other_events]

        if not events and not timed_out:
            result["message"] = "no new events"

        return result

    @mcp.tool()
    async def wait(gather: float | None = None, timeout: float = 3.0) -> dict:
        """Wait for events. gather=secs buffers events, timeout=secs max wait. Returns on first event if no gather."""
        debugger, session_id = get_debugger()
        cursor = get_cursor(session_id)
        if gather is not None and gather > 0:
            data = json.loads(await debugger.gather_events(cursor, gather, timeout))
        else:
            data = json.loads(await debugger.wait_event(cursor, timeout))
        return _format_events_result(data, session_id)

    mcp.run(transport="stdio")


def main():
    parser = argparse.ArgumentParser(
        description="Debug Adapter Protocol over D-Bus",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("-s", "--session", help="Session ID (usually auto-detected)")
    parser.add_argument("-j", "--json", action="store_true", help="Output raw JSON")
    parser.add_argument("--new", action="store_true", help="Force start new session")
    parser.add_argument("--develop", action="store_true", help="Run under 'nix develop'")
    parser.add_argument("--inherit", action="store_true", help="Inherit current environment")
    parser.add_argument("--serve", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("command", nargs="?", help="Command")
    parser.add_argument("args", nargs="*", help="Command arguments")

    args = parser.parse_args()

    # Server mode (internal)
    if args.serve:
        asyncio.run(run_server(args.session))
        return

    # No command or 'ls' → show status
    if not args.command or args.command.lower() == "ls":
        asyncio.run(cmd_status(args))
        return

    cmd = args.command.lower()

    # Lifecycle commands
    if cmd == "mcp":
        run_mcp()
    elif cmd == "start":
        cmd_start(args)
    elif cmd == "stop":
        cmd_stop(args)
    else:
        # Client commands (async)
        try:
            asyncio.run(cmd_client(args, cmd, args.args))
        except Exception as e:
            error(str(e))


if __name__ == "__main__":
    main()
