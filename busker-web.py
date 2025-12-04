#!/usr/bin/env python3
"""
Busker web dashboard - minimal ASGI + SSE + htmx + tagflow

Run with: uvicorn busker-web:app --reload
Or: python busker-web.py (uses uvicorn programmatically)
"""

import asyncio
from typing import AsyncIterator
from tagflow import tag, text, document

# Mock busker data for now
SESSIONS = {
    "vterm-001": {"type": "vterm", "title": "bash", "status": "running"},
    "debug-002": {"type": "debug", "title": "test.py", "status": "paused"},
    "agent-003": {"type": "agent", "title": "refactor task", "status": "running"},
}


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


def render_index() -> str:
    with document() as doc:
        with tag.html(lang="en"):
            with tag.head():
                with tag.title():
                    text("Busker Dashboard")
                with tag.script(src="https://unpkg.com/htmx.org@2.0.4"):
                    pass
                with tag.script(src="https://unpkg.com/htmx-ext-sse@2.2.2/sse.js"):
                    pass
                with tag.style():
                    text("""
                        body { font-family: system-ui; background: #1a1a2e; color: #eee; padding: 2rem; }
                        .sessions { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); }
                        .session { background: #16213e; border-radius: 8px; padding: 1rem; }
                        .session-header { display: flex; justify-content: space-between; margin-bottom: 0.5rem; }
                        .session-type { background: #0f3460; padding: 2px 8px; border-radius: 4px; font-size: 0.8rem; }
                        .session-title { font-weight: bold; }
                        .events { max-height: 200px; overflow-y: auto; font-family: monospace; font-size: 0.85rem; }
                        .event { padding: 2px 0; border-bottom: 1px solid #0f3460; }
                        .event-time { color: #888; margin-right: 0.5rem; }
                        .status-running { color: #4ade80; }
                        .status-paused { color: #fbbf24; }
                    """)
            with tag.body():
                with tag.h1():
                    text("Busker Sessions")
                with tag.div(classes="sessions"):
                    for sid, session in SESSIONS.items():
                        with tag.div(classes="session", id=f"session-{sid}"):
                            with tag.div(classes="session-header"):
                                with tag.span(classes="session-title"):
                                    text(session["title"])
                                with tag.span(classes=f"session-type"):
                                    text(session["type"])
                            with tag.div(classes=f"status-{session['status']}"):
                                text(session["status"])
                            with tag.div(
                                classes="events",
                                hx_ext="sse",
                                sse_connect=f"/sessions/{sid}/events",
                                sse_swap="message",
                                hx_swap="beforeend"
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


async def generate_mock_events(session_id: str) -> AsyncIterator[tuple[str, str]]:
    """Mock event generator - replace with real D-Bus subscription"""
    import random
    events = [
        ("damage", "screen updated"),
        ("output", "$ echo hello"),
        ("cursor", "moved to 5,10"),
        ("bell", "ding!"),
        ("title", "changed to 'vim'"),
    ]
    while True:
        await asyncio.sleep(random.uniform(1, 3))
        event_type, message = random.choice(events)
        yield event_type, f"{session_id}: {message}"


async def app(scope, receive, send):
    """Raw ASGI application"""
    if scope["type"] != "http":
        return

    path = scope["path"]
    method = scope["method"]

    # GET / - dashboard
    if path == "/" and method == "GET":
        body = render_index().encode()
        await send_response(send, 200, [
            (b"content-type", b"text/html; charset=utf-8"),
        ], body)
        return

    # GET /sessions/{id}/events - SSE stream
    if path.startswith("/sessions/") and path.endswith("/events"):
        session_id = path.split("/")[2]
        if session_id not in SESSIONS:
            await send_response(send, 404, [], b"Session not found")
            return

        await send_sse_start(send)

        try:
            async for event_type, message in generate_mock_events(session_id):
                html = render_event(session_id, event_type, message)
                # SSE data must be single line, escape newlines
                data = html.replace("\n", "")
                await send_sse_event(send, "message", data)
        except asyncio.CancelledError:
            pass

        await send({"type": "http.response.body", "body": b"", "more_body": False})
        return

    # 404 for everything else
    await send_response(send, 404, [], b"Not found")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run("busker-web:app", host="127.0.0.1", port=8000, reload=True)
