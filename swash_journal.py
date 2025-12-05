"""
Swash Journal: Journal-native event streaming.

Uses systemd journal as the canonical event store instead of in-memory EventLog.
Events are written with structured fields and read using cursor-based streaming.

Event Fields:
    SWASH_SESSION=<session_id>     - Session identifier
    SWASH_EVENT=<event_type>       - Event type (output, state, etc.)
    SWASH_STREAM=<stdout|stderr>   - Stream for output events
    SWASH_DATA=<json>              - Event data as JSON
    MESSAGE=<text>                 - Human-readable message
"""

from __future__ import annotations

import asyncio
import json
import select
import time
from dataclasses import dataclass
from typing import Any

from systemd import journal


# ============================================================================
# Journal Writing
# ============================================================================

def journal_send(
    session_id: str,
    event_type: str,
    data: Any = None,
    stream: str | None = None,
    message: str | None = None,
    extra_fields: dict | None = None,
):
    """Send a structured event to the journal."""
    fields = {
        "SWASH_SESSION": session_id,
        "SWASH_EVENT": event_type,
        "SWASH_TIMESTAMP": str(time.time()),
    }

    # Add any extra fields (like CLAUDE_SESSION)
    if extra_fields:
        fields.update(extra_fields)

    if stream:
        fields["SWASH_STREAM"] = stream

    if data is not None:
        # For SSE events, data is already a JSON string - don't double-encode
        if event_type == "sse" and isinstance(data, str):
            fields["SWASH_DATA"] = data
        else:
            fields["SWASH_DATA"] = json.dumps(data)

    if message is None:
        if event_type == "output" and isinstance(data, dict):
            message = data.get("text", "")
        elif event_type == "sse" and isinstance(data, str):
            # For SSE, the message IS the raw JSON data
            message = data
        else:
            message = f"[{event_type}] {json.dumps(data)}"

    journal.send(message, **fields)


def journal_output(session_id: str, stream: str, text: str):
    """Log output (stdout/stderr) to journal."""
    journal_send(
        session_id,
        event_type="output",
        data={"stream": stream, "text": text},
        stream=stream,
        message=text,
    )


def journal_state(session_id: str, event: str, **data):
    """Log state change to journal."""
    journal_send(
        session_id,
        event_type="state",
        data={"event": event, **data},
        message=f"[state] {event}",
    )


# ============================================================================
# Journal Reading
# ============================================================================

@dataclass
class JournalEvent:
    """Event read from journal."""
    cursor: str
    timestamp: float
    kind: str
    data: Any

    def to_dict(self) -> dict:
        return {
            "cursor": self.cursor,
            "timestamp": self.timestamp,
            "kind": self.kind,
            "data": self.data,
        }


class JournalReader:
    """Read swash events from the journal."""

    def __init__(self, session_id: str, unit: str | None = None):
        """
        Create a journal reader for a session.

        Args:
            session_id: The swash session ID
            unit: Optional systemd unit name to filter by (e.g., "swash-ABC123.service")
        """
        self.session_id = session_id
        self.unit = unit or f"swash-{session_id}.service"
        self._reader: journal.Reader | None = None

    def _ensure_reader(self) -> journal.Reader:
        """Get or create the journal reader."""
        if self._reader is None:
            self._reader = journal.Reader()
            # Filter by unit
            self._reader.add_match(_SYSTEMD_USER_UNIT=self.unit)
            # Also match by session ID in case we log without unit context
            self._reader.add_disjunction()
            self._reader.add_match(SWASH_SESSION=self.session_id)
            # Seek to beginning
            self._reader.seek_head()
        return self._reader

    def close(self):
        """Close the journal reader."""
        if self._reader is not None:
            self._reader.close()
            self._reader = None

    def _parse_entry(self, entry: dict) -> JournalEvent | None:
        """Parse a journal entry into a JournalEvent."""
        cursor = entry.get("__CURSOR", "")

        # Get timestamp
        ts = entry.get("__REALTIME_TIMESTAMP")
        if ts:
            timestamp = ts.timestamp() if hasattr(ts, 'timestamp') else float(ts) / 1e6
        else:
            timestamp = time.time()

        # Check if it's a swash event
        event_type = entry.get("SWASH_EVENT")
        if event_type:
            # Structured swash event
            data_str = entry.get("SWASH_DATA")
            if data_str:
                try:
                    data = json.loads(data_str)
                except json.JSONDecodeError:
                    data = {"raw": data_str}
            else:
                data = {}
            return JournalEvent(cursor, timestamp, event_type, data)

        # Plain message (stdout/stderr captured by systemd)
        message = entry.get("MESSAGE", "")
        if message:
            # Determine stream from priority
            priority = entry.get("PRIORITY", 6)
            stream = "stderr" if priority <= 3 else "stdout"
            return JournalEvent(
                cursor, timestamp, "output",
                {"stream": stream, "text": message}
            )

        return None

    def poll(self, cursor: str | None = None, limit: int = 1000) -> tuple[list[JournalEvent], str | None]:
        """
        Poll for events since cursor.

        Args:
            cursor: Resume from this cursor (None = from beginning)
            limit: Maximum events to return

        Returns:
            (events, new_cursor) - new_cursor is cursor of last event, or None if no events
        """
        reader = self._ensure_reader()

        if cursor:
            try:
                reader.seek_cursor(cursor)
                reader.get_next()  # Skip the cursor entry itself
            except Exception:
                reader.seek_head()
        else:
            reader.seek_head()

        events = []
        last_cursor = cursor

        for _ in range(limit):
            entry = reader.get_next()
            if not entry:
                break

            event = self._parse_entry(entry)
            if event:
                events.append(event)
                last_cursor = event.cursor

        return events, last_cursor

    def tail(self, n: int = 24) -> list[JournalEvent]:
        """Get last n events."""
        reader = self._ensure_reader()
        reader.seek_tail()

        # Read backwards
        entries = []
        for _ in range(n + 10):  # Read extra in case some don't parse
            entry = reader.get_previous()
            if not entry:
                break
            entries.append(entry)

        # Parse in reverse order (oldest first)
        events = []
        for entry in reversed(entries):
            event = self._parse_entry(entry)
            if event:
                events.append(event)
                if len(events) >= n:
                    break

        return events[-n:]

    def wait(self, cursor: str | None, timeout: float) -> tuple[list[JournalEvent], str | None, bool]:
        """
        Wait for events after cursor.

        Args:
            cursor: Resume from this cursor
            timeout: Max seconds to wait

        Returns:
            (events, new_cursor, timed_out)
        """
        reader = self._ensure_reader()

        if cursor:
            try:
                reader.seek_cursor(cursor)
                reader.get_next()
            except Exception:
                reader.seek_head()
        else:
            reader.seek_head()

        deadline = time.time() + timeout
        events = []
        last_cursor = cursor

        while time.time() < deadline:
            entry = reader.get_next()
            if entry:
                event = self._parse_entry(entry)
                if event:
                    events.append(event)
                    last_cursor = event.cursor
                continue

            # No entry, wait for journal changes
            remaining = deadline - time.time()
            if remaining <= 0:
                break

            # Wait on journal fd
            fd = reader.fileno()
            readable, _, _ = select.select([fd], [], [], min(remaining, 1.0))
            if readable:
                reader.process()

        return events, last_cursor, len(events) == 0

    def gather(
        self, cursor: str | None, gather_secs: float, timeout: float
    ) -> tuple[list[JournalEvent], str | None, bool]:
        """
        Wait for first event, then gather for duration.

        Args:
            cursor: Resume from this cursor
            gather_secs: How long to gather after first event
            timeout: Max seconds to wait for first event

        Returns:
            (events, new_cursor, timed_out)
        """
        reader = self._ensure_reader()

        if cursor:
            try:
                reader.seek_cursor(cursor)
                reader.get_next()
            except Exception:
                reader.seek_head()
        else:
            reader.seek_head()

        deadline = time.time() + timeout
        events = []
        last_cursor = cursor
        gather_until = None

        while True:
            now = time.time()

            # Check deadlines
            if gather_until and now >= gather_until:
                break
            if now >= deadline:
                break

            entry = reader.get_next()
            if entry:
                event = self._parse_entry(entry)
                if event:
                    events.append(event)
                    last_cursor = event.cursor
                    # Start gather timer on first event
                    if gather_until is None:
                        gather_until = now + gather_secs
                        deadline = min(deadline, gather_until)
                continue

            # No entry, wait
            remaining = min(
                deadline - now,
                (gather_until - now) if gather_until else float('inf')
            )
            if remaining <= 0:
                break

            fd = reader.fileno()
            readable, _, _ = select.select([fd], [], [], min(remaining, 0.5))
            if readable:
                reader.process()

        return events, last_cursor, len(events) == 0

    def follow(self, raw: bool = False, debug: bool = False):
        """
        Follow journal events until the process exits.

        Yields events as they arrive. Stops when it sees a state event
        with "exited" or "terminated".

        Args:
            raw: If True, yield the raw MESSAGE string instead of JournalEvent
            debug: If True, print debug info to stderr

        Yields:
            JournalEvent objects (or raw MESSAGE strings if raw=True)
        """
        import sys
        def dbg(msg):
            if debug:
                print(f"[follow] {msg}", file=sys.stderr, flush=True)

        reader = self._ensure_reader()
        reader.seek_head()
        dbg(f"started, unit={self.unit}")

        while True:
            entry = reader.get_next()
            if entry:
                dbg(f"got entry: SWASH_EVENT={entry.get('SWASH_EVENT')}")

                if raw:
                    message = entry.get("MESSAGE", "")
                    if message:
                        yield message
                else:
                    event = self._parse_entry(entry)
                    if event:
                        yield event

                # Check if process exited
                event_type = entry.get("SWASH_EVENT")
                if event_type == "state":
                    data_str = entry.get("SWASH_DATA", "{}")
                    try:
                        data = json.loads(data_str)
                        if data.get("event") in ("exited", "terminated"):
                            dbg("saw exit event, returning")
                            return
                    except json.JSONDecodeError:
                        pass
                continue

            # No entry, wait for journal changes
            dbg("no entry, waiting on select...")
            fd = reader.fileno()
            readable, _, _ = select.select([fd], [], [], 1.0)
            dbg(f"select returned: readable={bool(readable)}")
            if readable:
                reader.process()


# ============================================================================
# Async Wrappers
# ============================================================================

class AsyncJournalReader:
    """Async wrapper for JournalReader using anyio."""

    def __init__(self, session_id: str, unit: str | None = None):
        self.session_id = session_id
        self.unit = unit
        self._sync_reader = JournalReader(session_id, unit)

    def close(self):
        self._sync_reader.close()

    async def poll(self, cursor: str | None = None, limit: int = 1000) -> tuple[list[JournalEvent], str | None]:
        """Async poll for events."""
        loop = asyncio.get_event_loop()
        return await loop.run_in_executor(None, lambda: self._sync_reader.poll(cursor, limit))

    async def tail(self, n: int = 24) -> list[JournalEvent]:
        """Async tail."""
        loop = asyncio.get_event_loop()
        return await loop.run_in_executor(None, lambda: self._sync_reader.tail(n))

    async def wait(self, cursor: str | None, timeout: float) -> tuple[list[JournalEvent], str | None, bool]:
        """Async wait for events."""
        loop = asyncio.get_event_loop()
        return await loop.run_in_executor(None, lambda: self._sync_reader.wait(cursor, timeout))

    async def gather(
        self, cursor: str | None, gather_secs: float, timeout: float
    ) -> tuple[list[JournalEvent], str | None, bool]:
        """Async gather events."""
        loop = asyncio.get_event_loop()
        return await loop.run_in_executor(
            None,
            lambda: self._sync_reader.gather(cursor, gather_secs, timeout)
        )


# ============================================================================
# Gist Computation
# ============================================================================

def compute_gist(events: list[JournalEvent]) -> dict:
    """Compute gist from events."""
    gist = {
        "running": True,
        "exit_code": None,
    }

    for event in events:
        if event.kind == "state":
            data = event.data
            state_event = data.get("event")
            if state_event == "started":
                gist["running"] = True
                gist["pid"] = data.get("pid")
                gist["command"] = data.get("command")
            elif state_event == "exited":
                gist["running"] = False
                gist["exit_code"] = data.get("exit_code")
            elif state_event == "killed":
                gist["running"] = False
            elif state_event == "stopped":
                gist["debug_state"] = "stopped"
                gist["thread_id"] = data.get("threadId")
                gist["stop_reason"] = data.get("reason")

    return gist


# ============================================================================
# Demo
# ============================================================================

if __name__ == "__main__":
    import sys

    if len(sys.argv) > 1:
        session_id = sys.argv[1]
        print(f"Reading journal for session: {session_id}")

        reader = JournalReader(session_id)
        events, cursor = reader.poll()

        print(f"Found {len(events)} events")
        for e in events[-10:]:
            print(f"  [{e.kind}] {e.data}")

        print(f"\nGist: {compute_gist(events)}")
        reader.close()
    else:
        # Demo writing
        test_id = "TEST" + str(int(time.time()) % 10000)
        print(f"Writing test events for session: {test_id}")

        journal_state(test_id, "started", pid=12345, command="echo hello")
        journal_output(test_id, "stdout", "Hello, world!")
        journal_output(test_id, "stderr", "Warning: something")
        journal_state(test_id, "exited", exit_code=0)

        print("Done. Read with:")
        print(f"  python swash_journal.py {test_id}")
        print("  journalctl --user -t python --output=verbose | grep SWASH")

