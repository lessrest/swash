#!/usr/bin/env python3
"""
Swash web dashboard - minimal ASGI + SSE + htmx + tagflow

Run with: uvicorn swash-web:app --reload
Or: python swash-web.py (uses uvicorn programmatically)
"""

import asyncio
import anyio
from typing import AsyncIterator
from tagflow import tag, text, document
from sdbus import DbusInterfaceCommonAsync, dbus_signal_async, dbus_method_async, sd_bus_open_user

# Import swash interface definitions
from swash import SwashService, DBUS_NAME_PREFIX as SWASH_PREFIX, DBUS_PATH as SWASH_PATH


# D-Bus interface for VTerm (C++ service, separate from swash)
class VTermInterface(DbusInterfaceCommonAsync, interface_name="org.claude.VTerm"):
    @dbus_signal_async("ii")
    def damage(self) -> tuple[int, int]: ...

    @dbus_signal_async("ii")
    def cursor_moved(self) -> tuple[int, int]: ...

    @dbus_signal_async("")
    def bell(self) -> None: ...

    @dbus_signal_async("i")
    def exited(self) -> int: ...

    @dbus_signal_async("s")
    def scroll_line(self) -> str: ...

    @dbus_signal_async("s")
    def title_changed(self) -> str: ...

    @dbus_signal_async("b")
    def screen_mode(self) -> bool: ...

    @dbus_method_async("", "ay", method_name="RenderPNG")
    async def render_png(self) -> bytes: ...

    @dbus_method_async("", "s", method_name="GetScreenText")
    async def get_screen_text(self) -> str: ...

    @dbus_method_async("", "s", method_name="GetScreenHtml")
    async def get_screen_html(self) -> str: ...

    @dbus_method_async("", "s", method_name="GetScreenData")
    async def get_screen_data(self) -> str: ...


class FreedesktopDBus(DbusInterfaceCommonAsync, interface_name="org.freedesktop.DBus"):
    @dbus_method_async("", "as")
    async def list_names(self) -> list[str]: ...


async def discover_sessions() -> dict:
    """Discover active swash sessions on D-Bus"""
    sessions = {}
    try:
        bus = sd_bus_open_user()
        dbus = FreedesktopDBus.new_proxy("org.freedesktop.DBus", "/org/freedesktop/DBus", bus)
        names = await dbus.list_names()

        for name in names:
            if name.startswith("org.claude.VTerm."):
                session_id = name.replace("org.claude.VTerm.", "")
                sessions[f"vterm-{session_id}"] = {
                    "type": "vterm",
                    "bus_name": name,
                    "dbus_path": "/org/claude/VTerm",
                    "title": session_id,
                    "status": "running"
                }
            elif name.startswith("org.claude.Debugger."):
                session_id = name.replace("org.claude.Debugger.", "")
                sessions[f"debug-{session_id}"] = {
                    "type": "debug",
                    "bus_name": name,
                    "title": session_id,
                    "status": "running"
                }
            elif name.startswith(f"{SWASH_PREFIX}."):
                session_id = name.replace(f"{SWASH_PREFIX}.", "")
                sessions[f"swash-{session_id}"] = {
                    "type": "swash",
                    "bus_name": name,
                    "dbus_path": SWASH_PATH,
                    "title": session_id,
                    "status": "running"
                }
    except Exception as e:
        print(f"D-Bus discovery error: {e}")
        import traceback
        traceback.print_exc()

    return sessions


# Cache sessions, refresh periodically
SESSIONS: dict = {}
SESSIONS_LOCK = anyio.Lock()


async def send_response(send, status: int, headers: list[tuple[bytes, bytes]], body: bytes):
    await send({
        "type": "http.response.start",
        "status": status,
        "headers": headers,
    })
    await send({
        "type": "http.response.body",
        "body": body,
    })


async def send_sse_start(send):
    await send({
        "type": "http.response.start",
        "status": 200,
        "headers": [
            (b"content-type", b"text/event-stream"),
            (b"cache-control", b"no-cache"),
            (b"connection", b"keep-alive"),
        ],
    })


async def send_sse_event(send, event: str, data: str):
    payload = f"event: {event}\ndata: {data}\n\n".encode()
    await send({
        "type": "http.response.body",
        "body": payload,
        "more_body": True,
    })


async def get_sessions() -> dict:
    """Get current sessions, refreshing from D-Bus"""
    global SESSIONS
    async with SESSIONS_LOCK:
        SESSIONS = await discover_sessions()
    return SESSIONS


def render_index(sessions: dict) -> str:
    with document() as doc:
        with tag.html(lang="en"):
            with tag.head():
                with tag.title():
                    text("Swash Dashboard")
                with tag.script(src="https://unpkg.com/htmx.org@2.0.4"):
                    pass
                with tag.script():
                    text("htmx.config.globalViewTransitions = true;")
                with tag.script(src="https://unpkg.com/htmx-ext-sse@2.2.2/sse.js"):
                    pass
                with tag.meta(name="viewport", content="width=device-width, initial-scale=1"):
                    pass
                with tag.style():
                    text("""
                        * { box-sizing: border-box; margin: 0; padding: 0; }
                        body { background: #222; padding: 8px; }
                        .sessions { display: flex; flex-direction: column; gap: 8px; }
                        .session { border: 1px solid #444; border-radius: 4px; overflow: hidden; }
                        .session-header, .status-running, h1 { display: none; }
                        .terminal-screen { position: relative; }
                        .terminal-screen canvas { display: block; }
                        .terminal-screen pre {
                            position: absolute; top: 0; left: 0;
                            margin: 0 !important; padding: 0 !important;
                            font-family: monospace !important;
                            font-size: 14px;
                            line-height: 1.2 !important;
                            white-space: pre !important;
                            color: transparent;
                            pointer-events: none;
                        }
                    """)
                with tag.script():
                    text("""
const TermRenderer = {
    fontSize: 14,
    charW: 0, charH: 0, ascent: 0, descent: 0,

    init() {
        const c = document.createElement('canvas').getContext('2d');
        c.font = this.fontSize + 'px monospace';
        this.charW = c.measureText('M').width;
        const m = c.measureText('Mgy|');
        this.ascent = m.actualBoundingBoxAscent;
        this.descent = m.actualBoundingBoxDescent;
        this.charH = Math.ceil(this.ascent + this.descent + 2);
    },

    decode(b64) {
        const bin = atob(b64);
        const arr = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
        return arr;
    },

    render(container) {
        const pre = container.querySelector('pre');
        if (!pre || !pre.dataset.fg) return;

        const rows = +pre.dataset.rows, cols = +pre.dataset.cols;
        const fg = this.decode(pre.dataset.fg);
        const bg = this.decode(pre.dataset.bg);
        const text = pre.textContent;
        const lines = text.split('\\n');

        const w = Math.ceil(cols * this.charW);
        const h = rows * this.charH;

        let canvas = container.querySelector('canvas');
        if (!canvas) {
            canvas = document.createElement('canvas');
            container.insertBefore(canvas, pre);
        }
        canvas.width = w; canvas.height = h;
        const ctx = canvas.getContext('2d');

        for (let row = 0; row < rows; row++) {
            const line = lines[row] || '';
            const y = row * this.charH;
            const textY = y + this.ascent + 1;
            for (let col = 0; col < cols; col++) {
                const i = (row * cols + col) * 3;
                const x = col * this.charW;

                ctx.fillStyle = `rgb(${bg[i]},${bg[i+1]},${bg[i+2]})`;
                ctx.fillRect(Math.floor(x), y, Math.ceil(this.charW) + 1, this.charH);

                const bold = fg[i] & 0x80;
                const r = fg[i] & 0x7f;
                ctx.fillStyle = `rgb(${r},${fg[i+1]},${fg[i+2]})`;
                ctx.font = (bold ? 'bold ' : '') + this.fontSize + 'px monospace';
                const ch = line[col] || ' ';
                ctx.fillText(ch, x, textY);
            }
        }

        const scale = Math.min(1, container.clientWidth / w);
        canvas.style.transform = `scale(${scale})`;
        canvas.style.transformOrigin = 'top left';
        container.style.height = (h * scale) + 'px';
    }
};

TermRenderer.init();
htmx.on('htmx:afterSwap', e => {
    if (e.target.classList.contains('terminal-screen')) TermRenderer.render(e.target);
});
window.onresize = () => document.querySelectorAll('.terminal-screen').forEach(c => TermRenderer.render(c));
                    """)
            with tag.body():
                with tag.h1():
                    text("Swash Sessions")
                with tag.div(classes="sessions"):
                    if not sessions:
                        with tag.div(classes="no-sessions"):
                            text("No active sessions. Start a vterm-service or swash session.")
                    for sid, session in sessions.items():
                        with tag.div(classes="session", id=f"session-{sid}"):
                            with tag.div(classes="session-header"):
                                with tag.span(classes="session-title"):
                                    text(session["title"])
                                with tag.span(classes=f"session-type"):
                                    text(session["type"])
                            with tag.div(classes=f"status-{session['status']}"):
                                text(session["status"])
                            if session["type"] == "vterm":
                                with tag.div(
                                    classes="terminal-screen",
                                    hx_get=f"/sessions/{sid}/screen.html",
                                    hx_trigger="every 1s",
                                    hx_swap="innerHTML"
                                ):
                                    pass
    return doc.to_html()


def render_event(session_id: str, event_type: str, message: str) -> str:
    import time
    with document() as doc:
        with tag.div(classes="event"):
            with tag.span(classes="event-time"):
                text(time.strftime("%H:%M:%S"))
            with tag.span(classes=f"event-{event_type}"):
                text(f"[{event_type}] {message}")
    return doc.to_html()


async def subscribe_vterm_events(session_id: str, bus_name: str) -> AsyncIterator[tuple[str, str]]:
    """Subscribe to real D-Bus signals from a VTerm session"""
    from sdbus import sd_bus_open_user

    try:
        bus = sd_bus_open_user()
        vterm = VTermInterface.new_proxy(bus_name, "/org/claude/VTerm", bus)

        # Create channels for each signal type
        async def watch_signals():
            tasks = []

            async def watch_damage():
                async for start_row, end_row in vterm.damage:
                    yield ("damage", f"rows {start_row}-{end_row}")

            async def watch_scroll():
                async for line in vterm.scroll_line:
                    yield ("scroll", line[:50] + "..." if len(line) > 50 else line)

            async def watch_title():
                async for title in vterm.title_changed:
                    yield ("title", title)

            async def watch_bell():
                async for _ in vterm.bell:
                    yield ("bell", "ding!")

            async def watch_exit():
                async for code in vterm.exited:
                    yield ("exited", f"code {code}")

            # Merge all signal streams
            async with anyio.create_task_group() as tg:
                send, recv = anyio.create_memory_object_stream()

                async def forward(gen):
                    async for event in gen:
                        await send.send(event)

                tg.start_soon(forward, watch_damage())
                tg.start_soon(forward, watch_scroll())
                tg.start_soon(forward, watch_title())
                tg.start_soon(forward, watch_bell())
                tg.start_soon(forward, watch_exit())

                async for event in recv:
                    yield event

        async for event in watch_signals():
            yield event

    except Exception as e:
        yield ("error", str(e))


async def subscribe_swash_events(session_id: str, bus_name: str) -> AsyncIterator[tuple[str, str]]:
    """Subscribe to swash session events via poll_events"""
    import json
    try:
        proxy = SwashService.new_proxy(bus_name, SWASH_PATH)
        cursor = 0

        while True:
            # Wait for events with 30s timeout
            result = json.loads(await proxy.wait_events(cursor, 30.0))
            events = result.get("events", [])
            cursor = result.get("cursor", cursor)

            for event in events:
                kind = event.get("kind", "unknown")
                data = event.get("data", {})

                if kind == "output":
                    text = data.get("text", "")
                    stream = data.get("stream", "stdout")
                    yield (stream, text[:80] + "..." if len(text) > 80 else text)
                elif kind == "state":
                    evt = data.get("event", "")
                    yield ("state", evt)
                else:
                    yield (kind, str(data)[:50])

    except Exception as e:
        yield ("error", str(e))


async def generate_events(session_id: str) -> AsyncIterator[tuple[str, str]]:
    """Generate events for a session - real D-Bus or fallback"""
    global SESSIONS

    session = SESSIONS.get(session_id)
    if not session:
        yield ("error", "session not found")
        return

    if session["type"] == "vterm" and "bus_name" in session:
        async for event in subscribe_vterm_events(session_id, session["bus_name"]):
            yield event
    elif session["type"] == "swash" and "bus_name" in session:
        async for event in subscribe_swash_events(session_id, session["bus_name"]):
            yield event
    else:
        # Fallback mock for debug sessions or missing bus
        import random
        while True:
            await anyio.sleep(2)
            yield ("info", f"mock event for {session_id}")


async def app(scope, receive, send):
    """Raw ASGI application"""
    if scope["type"] != "http":
        return

    path = scope["path"]
    method = scope["method"]

    # GET / - dashboard
    if path == "/" and method == "GET":
        sessions = await get_sessions()
        body = render_index(sessions).encode()
        await send_response(send, 200, [
            (b"content-type", b"text/html; charset=utf-8"),
        ], body)
        return

    # GET /sessions/{id}/events - SSE stream
    if path.startswith("/sessions/") and path.endswith("/events"):
        session_id = path.split("/")[2]

        # Refresh sessions to get bus_name
        await get_sessions()

        if session_id not in SESSIONS:
            await send_response(send, 404, [], b"Session not found")
            return

        await send_sse_start(send)

        try:
            async for event_type, message in generate_events(session_id):
                html = render_event(session_id, event_type, message)
                # SSE data must be single line, escape newlines
                data = html.replace("\n", "")
                await send_sse_event(send, "message", data)
        except (asyncio.CancelledError, anyio.get_cancelled_exc_class()):
            pass
        except Exception as e:
            html = render_event(session_id, "error", str(e))
            await send_sse_event(send, "message", html.replace("\n", ""))

        await send({"type": "http.response.body", "body": b"", "more_body": False})
        return

    # GET /sessions/{id}/screen.html - render terminal as HTML
    if path.startswith("/sessions/") and path.endswith("/screen.html"):
        session_id = path.split("/")[2]

        await get_sessions()
        session = SESSIONS.get(session_id)

        if not session or session["type"] != "vterm":
            await send_response(send, 404, [], b"VTerm session not found")
            return

        try:
            bus = sd_bus_open_user()
            vterm = VTermInterface.new_proxy(session["bus_name"], "/org/claude/VTerm", bus)
            html = await vterm.get_screen_data()
            await send_response(send, 200, [
                (b"content-type", b"text/html; charset=utf-8"),
                (b"cache-control", b"no-cache"),
            ], html.encode())
        except Exception as e:
            await send_response(send, 500, [], f"Error: {e}".encode())
        return

    # GET /sessions/{id}/screen.png - render terminal as PNG
    if path.startswith("/sessions/") and path.endswith("/screen.png"):
        session_id = path.split("/")[2]

        await get_sessions()
        session = SESSIONS.get(session_id)

        if not session or session["type"] != "vterm":
            await send_response(send, 404, [], b"VTerm session not found")
            return

        try:
            bus = sd_bus_open_user()
            vterm = VTermInterface.new_proxy(session["bus_name"], "/org/claude/VTerm", bus)
            png_data = bytes(await vterm.render_png())
            await send_response(send, 200, [
                (b"content-type", b"image/png"),
                (b"cache-control", b"no-cache"),
            ], png_data)
        except Exception as e:
            await send_response(send, 500, [], f"Error: {e}".encode())
        return

    # 404 for everything else
    await send_response(send, 404, [], b"Not found")


if __name__ == "__main__":
    import uvicorn
    import sys

    host = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
    port = int(sys.argv[2]) if len(sys.argv) > 2 else 8000

    # Run with short graceful shutdown timeout (SSE streams can block)
    uvicorn.run(app, host=host, port=port, timeout_graceful_shutdown=2)

