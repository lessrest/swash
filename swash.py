#!/usr/bin/env python3
"""
swash - Interactive process sessions over D-Bus

Usage:
    swash                              Show session status
    swash run <command>                Run shell command
    swash debug [--program <path>]     Start debug session
    swash stop                         Stop session
    swash mcp                          Run as MCP server (stdio)

    swash poll                         Get recent output + gist
    swash wait [--timeout <secs>]      Wait for events
    swash scroll [offset] [limit]      Read scrollback
    swash send <input>                 Send input to process
    swash kill                         Kill process

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
import base64

import anyio

# Import identity module
import swash_identity as identity
import swash_journal as sjournal
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

DBUS_NAME_PREFIX = "sh.swa.Swash"
DBUS_PATH = "/sh/swa/Swash"
SLICE = "swash.slice"
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
# Protocol - Codec between raw I/O and typed events
# ============================================================================

class Protocol(ABC):
    """Protocol for interpreting process I/O as typed events."""

    @abstractmethod
    async def on_stdout(self, data: bytes, emit: Callable) -> None:
        """Handle stdout bytes. Call emit(kind, data) to produce events."""
        pass

    @abstractmethod
    async def on_stderr(self, data: bytes, emit: Callable) -> None:
        """Handle stderr bytes. Call emit(kind, data) to produce events."""
        pass

    @abstractmethod
    async def on_exit(self, code: int, emit: Callable) -> None:
        """Handle process exit. Call emit(kind, data) to produce events."""
        pass

    @abstractmethod
    def format_command(self, name: str, args: dict) -> bytes:
        """Format a command for sending to process stdin."""
        pass

    @abstractmethod
    def update_gist(self, gist: dict, event_kind: str, event_data: Any) -> dict:
        """Update gist based on new event. Returns updated gist."""
        pass


class ShellProtocol(Protocol):
    """Line-oriented shell protocol (default)."""

    def __init__(self):
        self._stdout_buffer = b""
        self._stderr_buffer = b""

    async def on_stdout(self, data: bytes, emit: Callable) -> None:
        self._stdout_buffer += data
        while b"\n" in self._stdout_buffer:
            line, self._stdout_buffer = self._stdout_buffer.split(b"\n", 1)
            text = line.decode("utf-8", errors="replace")
            await emit("output", {"stream": "stdout", "text": text})

    async def on_stderr(self, data: bytes, emit: Callable) -> None:
        self._stderr_buffer += data
        while b"\n" in self._stderr_buffer:
            line, self._stderr_buffer = self._stderr_buffer.split(b"\n", 1)
            text = line.decode("utf-8", errors="replace")
            await emit("output", {"stream": "stderr", "text": text})

    async def on_exit(self, code: int, emit: Callable) -> None:
        # Flush remaining buffers
        if self._stdout_buffer:
            text = self._stdout_buffer.decode("utf-8", errors="replace")
            await emit("output", {"stream": "stdout", "text": text})
            self._stdout_buffer = b""
        if self._stderr_buffer:
            text = self._stderr_buffer.decode("utf-8", errors="replace")
            await emit("output", {"stream": "stderr", "text": text})
            self._stderr_buffer = b""
        await emit("state", {"event": "exited", "exit_code": code})

    def format_command(self, name: str, args: dict) -> bytes:
        # Shell protocol: just send raw text
        text = args.get("input", "")
        return text.encode()

    def update_gist(self, gist: dict, event_kind: str, event_data: Any) -> dict:
        if event_kind == "state":
            event = event_data.get("event")
            if event == "exited":
                gist["exit_code"] = event_data.get("exit_code")
        return gist


class DAPProtocol(Protocol):
    """Debug Adapter Protocol (JSON with Content-Length framing)."""

    def __init__(self):
        self._buffer = b""
        self._seq = 1

    async def on_stdout(self, data: bytes, emit: Callable) -> None:
        self._buffer += data
        while True:
            msg = self._try_parse_message()
            if msg is None:
                break
            await self._handle_message(msg, emit)

    def _try_parse_message(self) -> dict | None:
        """Try to parse a complete DAP message from buffer."""
        header_end = self._buffer.find(b"\r\n\r\n")
        if header_end == -1:
            return None

        header = self._buffer[:header_end].decode()
        content_length = None
        for line in header.split("\r\n"):
            if line.startswith("Content-Length:"):
                content_length = int(line.split(":")[1].strip())
                break

        if content_length is None:
            return None

        body_start = header_end + 4
        body_end = body_start + content_length
        if len(self._buffer) < body_end:
            return None

        body = self._buffer[body_start:body_end]
        self._buffer = self._buffer[body_end:]
        return json.loads(body.decode())

    async def _handle_message(self, msg: dict, emit: Callable) -> None:
        """Handle a parsed DAP message."""
        msg_type = msg.get("type")
        if msg_type == "event":
            await self._handle_event(msg, emit)
        elif msg_type == "response":
            # Responses are handled by the command mechanism
            await emit("response", msg)

    async def _handle_event(self, event: dict, emit: Callable) -> None:
        """Convert DAP event to typed event."""
        etype = event.get("event", "")
        body = event.get("body", {})

        if etype in ("stopped", "exited", "terminated"):
            await emit("state", {
                "event": etype,
                "reason": body.get("reason"),
                "description": body.get("description"),
                "threadId": body.get("threadId"),
                "exitCode": body.get("exitCode"),
            })
        elif etype == "output":
            text = body.get("output", "").rstrip()
            if text:
                await emit("output", {
                    "stream": body.get("category", "stdout"),
                    "text": text,
                })
        else:
            await emit(etype, body)

    async def on_stderr(self, data: bytes, emit: Callable) -> None:
        # DAP stderr is usually debug logs, emit as output
        text = data.decode("utf-8", errors="replace").rstrip()
        if text:
            await emit("output", {"stream": "stderr", "text": text})

    async def on_exit(self, code: int, emit: Callable) -> None:
        await emit("state", {"event": "terminated", "exit_code": code})

    def format_command(self, name: str, args: dict) -> bytes:
        """Format DAP request."""
        request = {
            "seq": self._seq,
            "type": "request",
            "command": name,
        }
        if args:
            request["arguments"] = args
        self._seq += 1

        body = json.dumps(request)
        header = f"Content-Length: {len(body)}\r\n\r\n"
        return (header + body).encode()

    def update_gist(self, gist: dict, event_kind: str, event_data: Any) -> dict:
        if event_kind == "state":
            event = event_data.get("event")
            gist["debug_state"] = event
            if event == "stopped":
                gist["thread_id"] = event_data.get("threadId")
                gist["stop_reason"] = event_data.get("reason")
            elif event == "exited":
                gist["exit_code"] = event_data.get("exitCode")
        return gist


# Protocol registry
PROTOCOLS = {
    "shell": ShellProtocol,
    "dap": DAPProtocol,
}


# ============================================================================
# Session - Process + Protocol + Journal Events
# ============================================================================

class Session:
    """Process session that writes events directly to systemd journal."""

    def __init__(self, session_id: str, protocol: Protocol, task_group: anyio.abc.TaskGroup):
        self.session_id = session_id
        self.protocol = protocol
        self._tg = task_group
        self._process: anyio.abc.Process | None = None
        self._gist: dict = {}

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
        base.update(self._gist)
        return base

    async def _emit(self, kind: str, data: Any):
        """Emit event to journal and update gist."""
        sjournal.journal_send(self.session_id, kind, data)
        self._gist = self.protocol.update_gist(self._gist, kind, data)

    async def start(self, command: list[str], cwd: str | None = None):
        """Start the process."""
        self._process = await anyio.open_process(
            command,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=cwd,
        )

        self._gist["command"] = " ".join(command)
        await self._emit("state", {"event": "started", "pid": self._process.pid, "command": " ".join(command)})

        # Start I/O tasks
        self._tg.start_soon(self._read_stdout)
        self._tg.start_soon(self._read_stderr)
        self._tg.start_soon(self._wait_exit)

    async def _read_stdout(self):
        """Read stdout and pass to protocol."""
        try:
            async for chunk in self._process.stdout:
                await self.protocol.on_stdout(chunk, self._emit)
        except anyio.ClosedResourceError:
            pass
        except Exception as e:
            log("ERR", {"error": str(e), "context": "stdout"})

    async def _read_stderr(self):
        """Read stderr and pass to protocol."""
        try:
            async for chunk in self._process.stderr:
                await self.protocol.on_stderr(chunk, self._emit)
        except anyio.ClosedResourceError:
            pass
        except Exception as e:
            log("ERR", {"error": str(e), "context": "stderr"})

    async def _wait_exit(self):
        """Wait for process exit."""
        code = await self._process.wait()
        await self.protocol.on_exit(code, self._emit)

    async def kill(self):
        """Kill the process."""
        if self._process is not None:
            self._process.kill()
            await self._emit("state", {"event": "killed"})

    async def send_input(self, data: str):
        """Send raw input to process stdin."""
        if self._process is not None and self._process.stdin is not None:
            await self._process.stdin.send(data.encode())

    async def send_command(self, name: str, args: dict = None):
        """Send protocol-formatted command."""
        if self._process is not None and self._process.stdin is not None:
            data = self.protocol.format_command(name, args or {})
            await self._process.stdin.send(data)


# ============================================================================
# D-Bus Service - Minimal interface for process control
# ============================================================================

class SwashService(DbusInterfaceCommonAsync, interface_name=DBUS_NAME_PREFIX):
    """
    D-Bus interface for swash sessions.

    Events are accessed via systemd journal, not D-Bus methods.
    This service only exposes process control and identity properties.
    """

    def __init__(self, session: Session, service_identity: identity.ServiceIdentity | None = None):
        super().__init__()
        self.session = session
        self.service_identity = service_identity

    # -------------------------------------------------------------------------
    # Properties
    # -------------------------------------------------------------------------

    @dbus_property_async(property_signature="s")
    def gist(self) -> str:
        return json.dumps(self.session.gist)

    @dbus_property_async(property_signature="s")
    def did(self) -> str:
        """DID (Decentralized Identifier) for this session."""
        if self.service_identity:
            return self.service_identity.did()
        return ""

    @dbus_property_async(property_signature="s")
    def uri(self) -> str:
        """URI for this session."""
        if self.service_identity:
            return self.service_identity.uri()
        return f"urn:swash:{self.session.session_id}"

    @dbus_property_async(property_signature="s")
    def lineage(self) -> str:
        """Verifiable Presentation of attestation chain."""
        if self.service_identity:
            return json.dumps(self.service_identity.present_lineage())
        return "{}"

    @dbus_property_async(property_signature="s")
    def session_id(self) -> str:
        """Session ID for journal filtering."""
        return self.session.session_id

    # -------------------------------------------------------------------------
    # Methods - Process Control Only
    # -------------------------------------------------------------------------

    @dbus_method_async(input_signature="s", result_signature="s")
    async def send_input(self, data: str) -> str:
        await self.session.send_input(data)
        return json.dumps({"sent": len(data)})

    @dbus_method_async(input_signature="ss", result_signature="s")
    async def send_command(self, name: str, args_json: str) -> str:
        """Send protocol-formatted command."""
        args = json.loads(args_json) if args_json else {}
        await self.session.send_command(name, args)
        return json.dumps({"sent": name})

    @dbus_method_async(result_signature="s")
    async def kill(self) -> str:
        await self.session.kill()
        return json.dumps({"killed": True})


# ============================================================================
# Server Entry Point
# ============================================================================

async def run_server(session_id: str, protocol_name: str, command: list[str], cwd: str):
    """Run session server with specified protocol.

    Args:
        session_id: Unique session identifier
        protocol_name: Protocol to use (shell, dap)
        command: Command to run
        cwd: Working directory
    """
    if sys.stdin.isatty():
        sys.exit("ERROR: must be launched via systemd-run")

    dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"
    protocol_cls = PROTOCOLS.get(protocol_name, ShellProtocol)

    # Load identity from credentials (if available)
    service_identity = identity.ServiceIdentity.load_from_credentials()

    async with anyio.create_task_group() as tg:
        protocol = protocol_cls()
        session = Session(session_id, protocol, tg)
        service = SwashService(session, service_identity)

        await request_default_bus_name_async(dbus_name)
        service.export_to_dbus(DBUS_PATH)

        print("=" * 60, file=sys.stderr)
        print("SWASH SESSION STARTED", file=sys.stderr)
        print(f"  Session:  {session_id}", file=sys.stderr)
        print(f"  Protocol: {protocol_name}", file=sys.stderr)
        print(f"  D-Bus:    {dbus_name}", file=sys.stderr)
        print(f"  Command:  {' '.join(command)}", file=sys.stderr)
        if service_identity:
            print(f"  DID:      {service_identity.did()}", file=sys.stderr)
            print(f"  URI:      {service_identity.uri()}", file=sys.stderr)
        print(f"  Events:   journalctl --user -u swash-{session_id}.service", file=sys.stderr)
        print("=" * 60, file=sys.stderr, flush=True)

        await session.start(command, cwd)
        await anyio.sleep_forever()


# ============================================================================
# Session Management (CLI helpers)
# ============================================================================

def list_sessions() -> list[dict]:
    """List all running swash sessions."""
    result = subprocess.run(
        ["systemctl", "--user", "list-units", "swash-*.service",
         "--no-legend", "--plain"],
        capture_output=True, text=True
    )
    sessions = []
    for line in result.stdout.strip().splitlines():
        if not line:
            continue
        parts = line.split()
        unit = parts[0]
        session_id = unit.replace("swash-", "").replace(".service", "")

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
    return f"swash-{session_id}.service"


def start_session(
    command: str | list[str],
    protocol: str = "shell",
    cwd: str | None = None,
    inherit_env: bool = True,
) -> tuple[str, dict]:
    """Start a new session. Returns (session_id, context)."""
    session_id = gen_session_id()
    unit = unit_name(session_id)
    self_path = Path(__file__).resolve()
    working_dir = cwd or os.getcwd()

    # Convert command to string for passing through
    if isinstance(command, list):
        command_str = " ".join(command)
    else:
        command_str = command

    # Create identity and credentials for the new service
    service_identity, credentials = identity.ServiceIdentity.create_for_service(
        command=command_str,
        unit_name=unit,
    )

    dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"
    systemd_cmd = [
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

    # Pass credentials securely via systemd credentials mechanism
    for cred_name, cred_value in credentials.items():
        # Base64 encode the credential value for safe transport
        encoded = base64.b64encode(cred_value.encode()).decode()
        systemd_cmd.append(f"--property=SetCredential={cred_name}:{encoded}")

    # Pass environment
    if inherit_env:
        for var, val in os.environ.items():
            if val and not var.startswith("_"):
                systemd_cmd.append(f"--setenv={var}={val}")

    systemd_cmd.extend([
        "--",
        sys.executable, str(self_path),
        "--serve",
        "--protocol", protocol,
        "--session", session_id,
        "--command", command_str,
    ])

    result = subprocess.run(systemd_cmd, capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(f"Failed to start: {result.stderr}")

    return session_id, {
        "protocol": protocol,
        "systemd_unit": unit,
        "dbus_name": dbus_name,
        "dbus_path": DBUS_PATH,
        "logs_command": f"journalctl --user -u {unit} -f",
        "working_directory": working_dir,
        "did": service_identity.did(),
        "uri": service_identity.uri(),
    }


def stop_session(session_id: str):
    """Stop a session."""
    subprocess.run(["systemctl", "--user", "stop", unit_name(session_id)], check=True)


def get_service_proxy(session_id: str) -> SwashService:
    """Get D-Bus proxy for session."""
    dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"
    return SwashService.new_proxy(dbus_name, DBUS_PATH)


# ============================================================================
# MCP Server (Journal-Native)
# ============================================================================

def run_mcp():
    """Run as MCP server with journal-native event reading."""
    from mcp.server.fastmcp import FastMCP

    mcp = FastMCP("swash")

    # Journal cursors (strings, not ints)
    _cursors: dict[str, str | None] = {}
    _readers: dict[str, sjournal.JournalReader] = {}

    def get_cursor(session_id: str) -> str | None:
        return _cursors.get(session_id)

    def set_cursor(session_id: str, cursor: str | None):
        _cursors[session_id] = cursor

    def get_reader(session_id: str) -> sjournal.JournalReader:
        if session_id not in _readers:
            _readers[session_id] = sjournal.JournalReader(session_id)
        return _readers[session_id]

    def get_session_info() -> dict:
        """Get info about the current session."""
        sessions = list_sessions()
        if not sessions:
            raise RuntimeError("No session running. Use run() to start one.")
        return sessions[0]

    def get_proxy(session_id: str) -> SwashService:
        """Get D-Bus proxy for session (for send_input/kill only)."""
        dbus_name = f"{DBUS_NAME_PREFIX}.{session_id}"
        return SwashService.new_proxy(dbus_name, DBUS_PATH)

    def format_events(events: list[sjournal.JournalEvent], cursor: str | None, session_id: str, timed_out: bool = False) -> dict:
        """Format journal events for MCP response."""
        set_cursor(session_id, cursor)

        # Compute gist from events
        gist = sjournal.compute_gist(events)

        # Separate output from other events
        output = [e for e in events if e.kind == "output"]
        state_events = [e for e in events if e.kind == "state"]

        result = {"gist": gist, "timed_out": timed_out}

        if state_events:
            result["state_changes"] = [e.data for e in state_events]

        if output:
            lines = [e.data for e in output]
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
    def run(command: str, protocol: str = "shell", cwd: str = "") -> dict:
        """Run a command in a new session. protocol: 'shell' (default) or 'dap'."""
        sessions = list_sessions()
        if sessions:
            return {"error": "session already running", "session_id": sessions[0]["id"]}

        try:
            session_id, context = start_session(command, protocol=protocol, cwd=cwd or None)
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
        # Clean up reader
        if s["id"] in _readers:
            _readers[s["id"]].close()
            del _readers[s["id"]]
        return {"stopped": s["id"]}

    @mcp.tool()
    def poll() -> dict:
        """Get recent output and gist from journal."""
        info = get_session_info()
        session_id = info["id"]
        reader = get_reader(session_id)
        cursor = get_cursor(session_id)

        events, new_cursor = reader.poll(cursor)
        return format_events(events, new_cursor, session_id)

    @mcp.tool()
    def wait(timeout: float = 30, gather: float | None = None) -> dict:
        """Wait for events from journal."""
        info = get_session_info()
        session_id = info["id"]
        reader = get_reader(session_id)
        cursor = get_cursor(session_id)

        if gather:
            events, new_cursor, timed_out = reader.gather(cursor, gather, timeout)
        else:
            events, new_cursor, timed_out = reader.wait(cursor, timeout)

        return format_events(events, new_cursor, session_id, timed_out)

    @mcp.tool()
    async def send(input: str) -> dict:
        """Send input to the process via D-Bus."""
        info = get_session_info()
        proxy = get_proxy(info["id"])
        return json.loads(await proxy.send_input(input))

    @mcp.tool()
    def scroll(limit: int = 100) -> dict:
        """Read recent output from journal (tail)."""
        info = get_session_info()
        session_id = info["id"]
        reader = get_reader(session_id)

        events = reader.tail(limit)
        output = [e.data for e in events if e.kind == "output"]

        return {
            "lines": output,
            "total": len(output),
        }

    @mcp.tool()
    def status() -> dict:
        """Get session status."""
        sessions = list_sessions()
        if not sessions:
            return {"running": False, "sessions": []}
        return {
            "running": True,
            "sessions": sessions,
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
        console.print("[dim]swash run <command>[/dim]")
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
    parser.add_argument("-p", "--protocol", default="shell", help="Protocol: shell, dap")
    parser.add_argument("--serve", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--command", dest="serve_command", help=argparse.SUPPRESS)
    parser.add_argument("command", nargs="?", help="Command")
    parser.add_argument("args", nargs="*", help="Arguments")

    args = parser.parse_args()

    # Server mode (internal)
    if args.serve:
        if not args.serve_command:
            sys.exit("ERROR: no command specified")
        # Build process command based on protocol
        if args.protocol == "shell":
            process_cmd = ["sh", "-c", args.serve_command]
        else:
            process_cmd = args.serve_command.split()
        asyncio.run(run_server(args.session, args.protocol, process_cmd, os.getcwd()))
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
            session_id, ctx = start_session(command, protocol=args.protocol)
            console.print(f"[bold cyan]{session_id}[/bold cyan] [green]started[/green] [dim]({args.protocol})[/dim]")
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

