#!/usr/bin/env python3
"""
claude.py - Claude LLM API demo with tool calling, journald + git storage

Messages are stored as RDF/Turtle in git commits: ~/.local/share/claude/messages.git
Each session is a ref: refs/sessions/<SESSION_ID>

Usage:
    nix run .#claude -- "Your prompt here"
    nix run .#claude -- --resume SESSION_ID "Continue the conversation"
    nix run .#claude -- --list                 # List available sessions

View conversation history:
    git -C ~/.local/share/claude/messages.git log refs/sessions/<SESSION_ID>
    git -C ~/.local/share/claude/messages.git show HEAD:msg/<id>.ttl
"""

import argparse
import asyncio
import json
import os
import sys
import time
import uuid
from abc import abstractmethod
from datetime import datetime
from pathlib import Path
from typing import Protocol, runtime_checkable

import pygit2
from anthropic import AsyncAnthropic, beta_async_tool
from rdflib import Graph, Literal, Namespace
from rdflib.namespace import RDF, XSD
from rich.console import Console
from rich.panel import Panel
from rich.table import Table
from systemd import journal

console = Console()

# =============================================================================
# RDF Namespaces
# =============================================================================

AS = Namespace("https://www.w3.org/ns/activitystreams#")
CLAUDE = Namespace("https://anthropic.com/ns/claude#")


# =============================================================================
# SessionGraph - RDF graph as the single source of truth
# =============================================================================


class SessionGraph:
    """
    RDF graph as the primary data structure for a conversation session.

    All operations add facts (triples) to the graph. The nested dict/list
    representation for the Anthropic API is built on-demand by querying the graph.
    """

    def __init__(self, session_id: str):
        self.session_id = session_id
        self.graph = Graph()
        self.graph.bind("as", AS)
        self.graph.bind("claude", CLAUDE)
        self._last_message_id: str | None = None

    @property
    def last_message_id(self) -> str | None:
        return self._last_message_id

    # -------------------------------------------------------------------------
    # Graph primitives - thin wrappers for common operations
    # -------------------------------------------------------------------------

    def _add(self, *triples):
        """Add multiple triples to the graph."""
        for triple in triples:
            self.graph.add(triple)

    def _str(self, subject, predicate) -> str:
        """Get a string value from the graph, defaulting to empty string."""
        return str(self.graph.value(subject, predicate) or "")

    def _has_type(self, subject, rdf_type) -> bool:
        """Check if subject has the given RDF type."""
        return (subject, RDF.type, rdf_type) in self.graph

    def _of_type(self, rdf_type):
        """Iterate subjects that have the given RDF type."""
        return self.graph.subjects(RDF.type, rdf_type)

    # URI builders - consistent namespace URIs
    def _msg(self, msg_id: str): return CLAUDE[f"message/{msg_id}"]
    def _actor(self, name: str): return CLAUDE[f"actor/{name}"]
    def _session(self): return CLAUDE[f"session/{self.session_id}"]
    def _tool(self, tool_id: str): return CLAUDE[f"tool_use/{tool_id}"]
    def _result(self, tool_id: str): return CLAUDE[f"tool_result/{tool_id}"]

    # -------------------------------------------------------------------------
    # Fact-adding methods - these mutate the graph
    # -------------------------------------------------------------------------

    def _add_message(self, message_id: str, actor: str, content: str | None = None) -> None:
        """Add a message node with common properties."""
        msg = self._msg(message_id)
        self._add(
            (msg, RDF.type, AS.Note),
            (msg, AS.id, Literal(message_id)),
            (msg, AS.published, Literal(datetime.now().isoformat(), datatype=XSD.dateTime)),
            (msg, AS.attributedTo, self._actor(actor)),
            (msg, CLAUDE.session, self._session()),
        )
        if content:
            self._add((msg, AS.content, Literal(content)))
        if self._last_message_id:
            self._add((msg, AS.inReplyTo, self._msg(self._last_message_id)))
        self._last_message_id = message_id

    def add_user_message(self, message_id: str, content: str) -> None:
        """Add a user message to the graph."""
        self._add_message(message_id, "user", content)

    def add_assistant_message(self, message_id: str) -> None:
        """Add an assistant message node (text content added separately)."""
        self._add_message(message_id, "claude")

    def set_message_content(self, message_id: str, content: str) -> None:
        """Set the text content of a message."""
        self._add((self._msg(message_id), AS.content, Literal(content)))

    def add_tool_use(self, message_id: str, tool_use_id: str, tool_name: str, input_data: dict) -> None:
        """Add a tool use attached to an assistant message."""
        msg, tool = self._msg(message_id), self._tool(tool_use_id)
        self._add(
            (tool, RDF.type, CLAUDE.ToolUse),
            (tool, AS.id, Literal(tool_use_id)),
            (tool, CLAUDE.tool, Literal(tool_name)),
            (tool, CLAUDE.input, Literal(json.dumps(input_data))),
            (msg, AS.attachment, tool),
            (tool, AS.context, msg),
        )

    def add_tool_result(self, tool_use_id: str, content: str) -> None:
        """Add a tool result linked to its tool use."""
        tool, result = self._tool(tool_use_id), self._result(tool_use_id)
        self._add(
            (result, RDF.type, CLAUDE.ToolResult),
            (result, AS.content, Literal(content)),
            (result, AS.inReplyTo, tool),
        )

    # -------------------------------------------------------------------------
    # Graph query methods - each returns a value from the graph
    # -------------------------------------------------------------------------

    def _get_message_role(self, msg_uri) -> str:
        """Returns 'user' or 'assistant' based on attributedTo."""
        return "user" if "actor/user" in self._str(msg_uri, AS.attributedTo) else "assistant"

    def _get_session_messages(self) -> list[tuple]:
        """Returns [(msg_uri, published, role), ...] sorted by timestamp."""
        messages = [
            (msg, self._str(msg, AS.published), self._get_message_role(msg))
            for msg in self._of_type(AS.Note)
            if self.graph.value(msg, CLAUDE.session) == self._session()
        ]
        return sorted(messages, key=lambda x: x[1])

    def _get_message_content(self, msg_uri) -> str:
        """Returns the text content of a message."""
        return self._str(msg_uri, AS.content)

    def _parse_json(self, s: str, default=None):
        """Parse JSON string, returning default on failure."""
        try:
            return json.loads(s)
        except json.JSONDecodeError:
            return default if default is not None else {}

    def _get_tool_use_block(self, tool_uri) -> dict:
        """Returns an API tool_use block from a tool URI."""
        return {
            "type": "tool_use",
            "id": self._str(tool_uri, AS.id),
            "name": self._str(tool_uri, CLAUDE.tool),
            "input": self._parse_json(self._str(tool_uri, CLAUDE.input) or "{}"),
        }

    def _get_tool_result_block(self, tool_uri, tool_id: str) -> dict | None:
        """Returns an API tool_result block if a result exists for this tool use."""
        result = next(
            (uri for uri in self.graph.subjects(AS.inReplyTo, tool_uri)
             if self._has_type(uri, CLAUDE.ToolResult)),
            None
        )
        return {"type": "tool_result", "tool_use_id": tool_id, "content": self._str(result, AS.content)} if result else None

    def _get_message_tool_uses(self, msg_uri) -> list:
        """Returns all tool_use URIs attached to a message."""
        return [
            tool for tool in self.graph.objects(msg_uri, AS.attachment)
            if self._has_type(tool, CLAUDE.ToolUse)
        ]

    # -------------------------------------------------------------------------
    # Block generators - yield content blocks within messages
    # -------------------------------------------------------------------------

    def _iter_text_block(self, content: str):
        """Yields a text block if content is non-empty."""
        if content:
            yield {"type": "text", "text": content}

    def _iter_tool_use_blocks(self, msg_uri):
        """Yields tool_use blocks for all tools attached to a message."""
        for tool_uri in self._get_message_tool_uses(msg_uri):
            yield self._get_tool_use_block(tool_uri)

    def _iter_tool_result_blocks(self, msg_uri):
        """Yields tool_result blocks for all tool results in a message."""
        for tool_uri in self._get_message_tool_uses(msg_uri):
            if result := self._get_tool_result_block(tool_uri, self._str(tool_uri, AS.id)):
                yield result

    # -------------------------------------------------------------------------
    # Message generators - yield complete API messages
    # -------------------------------------------------------------------------

    def _iter_user_message(self, msg_uri):
        """Yields a user API message if content exists."""
        if content := self._get_message_content(msg_uri):
            yield {"role": "user", "content": content}

    def _iter_assistant_messages(self, msg_uri):
        """Yields API messages for an assistant turn (1-2 messages)."""
        content = self._get_message_content(msg_uri)
        tool_use_blocks = list(self._iter_tool_use_blocks(msg_uri))

        content_blocks = list(self._iter_text_block(content)) + tool_use_blocks
        yield {"role": "assistant", "content": content_blocks}

        if tool_result_blocks := list(self._iter_tool_result_blocks(msg_uri)):
            yield {"role": "user", "content": tool_result_blocks}

    def _iter_message(self, msg_uri, role: str):
        """Yields API messages for a single graph message."""
        if role == "user":
            yield from self._iter_user_message(msg_uri)
        else:
            yield from self._iter_assistant_messages(msg_uri)

    def iter_api_messages(self):
        """Yields API messages by querying the graph."""
        for msg_uri, _, role in self._get_session_messages():
            yield from self._iter_message(msg_uri, role)

    def build_api_messages(self) -> list[dict]:
        """Query the graph and build the messages list for the Anthropic API."""
        return list(self.iter_api_messages())

    # -------------------------------------------------------------------------
    # Serialization
    # -------------------------------------------------------------------------

    def serialize(self, format: str = "turtle") -> str:
        """Serialize the graph to a string."""
        return self.graph.serialize(format=format)

    def parse(self, data: str, format: str = "turtle") -> None:
        """Parse serialized data into the graph."""
        self.graph.parse(data=data, format=format)
        self._restore_last_message_id()

    def _restore_last_message_id(self) -> None:
        """Find the most recent message ID from the graph."""
        if messages := self._get_session_messages():
            self._last_message_id = self._str(messages[-1][0], AS.id)


@runtime_checkable
class EventLogger(Protocol):
    """Protocol for event logging backends."""

    @abstractmethod
    def log(
        self,
        event_type: str,
        data: dict,
        message_id: str | None = None,
        message: str | None = None,
    ) -> None:
        """Log an event with optional message context."""
        ...


# =============================================================================
# Git Storage - Simple persistence for SessionGraph
# =============================================================================

MESSAGES_REPO_PATH = Path.home() / ".local/share/claude/messages.git"


def get_messages_repo() -> pygit2.Repository:
    """Get or create the messages git repository."""
    if not MESSAGES_REPO_PATH.exists():
        MESSAGES_REPO_PATH.parent.mkdir(parents=True, exist_ok=True)
        return pygit2.init_repository(str(MESSAGES_REPO_PATH), bare=True)
    return pygit2.Repository(str(MESSAGES_REPO_PATH))


class GitSessionStore:
    """
    Simple git-backed persistence for SessionGraph.

    The entire session graph is stored as a single turtle file.
    Each save creates a new commit; sessions are refs/sessions/<id>.
    """

    def __init__(self):
        self.repo = get_messages_repo()

    def list_sessions(self) -> list[tuple[str, str, str]]:
        """List all sessions: (session_id, timestamp, summary)."""
        sessions = []
        for ref in self.repo.references:
            if ref.startswith("refs/sessions/"):
                session_id = ref.removeprefix("refs/sessions/")
                try:
                    commit = self.repo.revparse_single(ref)
                    summary = commit.message.split("\n")[0] if commit.message else ""
                    timestamp = datetime.fromtimestamp(commit.commit_time).strftime("%Y-%m-%d %H:%M")
                    sessions.append((session_id, timestamp, summary))
                except Exception:
                    sessions.append((session_id, "?", "?"))
        return sorted(sessions, key=lambda x: x[1], reverse=True)

    def load(self, session_id: str) -> SessionGraph | None:
        """Load a SessionGraph from git, or None if not found."""
        ref_name = f"refs/sessions/{session_id}"
        try:
            commit = self.repo.revparse_single(ref_name)
        except KeyError:
            return None

        tree = commit.tree
        if "session.ttl" not in tree:
            # Try legacy format with msg/ directory
            return self._load_legacy(session_id, tree)

        blob = self.repo.get(tree["session.ttl"].id)
        graph = SessionGraph(session_id)
        graph.parse(blob.data.decode(), format="turtle")
        return graph

    def save(self, graph: SessionGraph, commit_message: str) -> pygit2.Oid:
        """Save the SessionGraph to git, returning the commit ID."""
        session_id = graph.session_id
        ref_name = f"refs/sessions/{session_id}"

        # Serialize graph to turtle
        ttl = graph.serialize(format="turtle")
        blob_id = self.repo.create_blob(ttl.encode())

        # Build tree with single session.ttl file
        tree_builder = self.repo.TreeBuilder()
        tree_builder.insert("session.ttl", blob_id, pygit2.GIT_FILEMODE_BLOB)
        tree_id = tree_builder.write()

        # Get parent commit if exists
        parents = []
        try:
            parent = self.repo.revparse_single(ref_name)
            parents = [parent.id]
        except KeyError:
            pass

        # Create commit
        sig = pygit2.Signature("Claude Session", "session@claude.local")
        summary = commit_message[:72] + "..." if len(commit_message) > 72 else commit_message
        full_message = f"{summary}\n\nSession: {session_id}"

        commit_id = self.repo.create_commit(None, sig, sig, full_message, tree_id, parents)

        # Update session ref
        try:
            self.repo.references.create(ref_name, commit_id, force=True)
        except Exception:
            self.repo.references.get(ref_name).set_target(commit_id)

        return commit_id


# =============================================================================
# Logging
# =============================================================================


class JournaldLogger:
    """
    Journald-backed event logger implementing the EventLogger protocol.

    Logs events to systemd journal with structured fields for querying.
    """

    def __init__(self, session_id: str):
        """
        Initialize the logger for a session.

        Args:
            session_id: Session identifier included in all log entries
        """
        self.session_id = session_id

    def log(
        self,
        event_type: str,
        data: dict,
        message_id: str | None = None,
        message: str | None = None,
    ) -> None:
        """Log an event to journald with structured fields."""
        fields = {
            "CLAUDE_SESSION": self.session_id,
            "CLAUDE_EVENT": event_type,
            "CLAUDE_TIMESTAMP": str(time.time()),
            "CLAUDE_DATA": json.dumps(data),
        }
        if message_id:
            fields["CLAUDE_MESSAGE_ID"] = message_id
        if message is None:
            message = f"[{event_type}] {json.dumps(data, ensure_ascii=False)[:200]}"
        journal.send(message, **fields)


def make_user_message_id() -> str:
    """Generate a unique message ID for user messages."""
    return f"msg_user_{uuid.uuid4().hex[:24]}"


@beta_async_tool
async def calculator(expression: str) -> str:
    """Evaluate a mathematical expression.

    Args:
        expression (str): The mathematical expression to evaluate, e.g. '2 + 2' or '(10 * 5) / 2'. Supports basic arithmetic (+, -, *, /), exponentiation (**), and parentheses.

    Returns:
        str: The result of the calculation.
    """
    allowed = set("0123456789+-*/(). ")
    if not all(c in allowed for c in expression):
        return "Error: Invalid characters in expression"
    try:
        result = eval(expression, {"__builtins__": {}}, {})
        return str(result)
    except Exception as e:
        return f"Error: {e}"


@beta_async_tool
async def get_current_time() -> str:
    """Get the current date and time.

    Returns:
        str: The current date and time in YYYY-MM-DD HH:MM:SS format.
    """
    return datetime.now().strftime("%Y-%m-%d %H:%M:%S")


def error(msg: str):
    console.print(f"[red]error:[/red] {msg}")
    sys.exit(1)


async def main():
    parser = argparse.ArgumentParser(description="Claude CLI with git-backed message history")
    parser.add_argument("prompt", nargs="*", help="The prompt to send")
    parser.add_argument("--resume", "-r", metavar="SESSION", help="Resume from a previous session")
    parser.add_argument("--list", "-l", action="store_true", help="List available sessions")
    args = parser.parse_args()

    # Git storage for persistence
    git_store = GitSessionStore()

    # Handle --list command
    if args.list:
        sessions = git_store.list_sessions()
        if not sessions:
            console.print("[dim]No sessions found[/dim]")
            return

        table = Table(title="Sessions")
        table.add_column("Session ID", style="cyan")
        table.add_column("Last Updated", style="green")
        table.add_column("Last Message", style="white")
        for sid, ts, summary in sessions[:20]:
            table.add_row(sid, ts, summary[:60])
        console.print(table)
        return

    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if not api_key:
        error("ANTHROPIC_API_KEY environment variable not set")

    prompt = " ".join(args.prompt) if args.prompt else "What is 123 * 456? Also, what time is it?"

    # Load or create SessionGraph - the single source of truth
    if args.resume:
        session_id = args.resume
        graph = git_store.load(session_id)
        if graph:
            console.print(f"[dim]Resuming session:[/dim] {session_id}")
            msg_count = len(graph._get_session_messages())
            console.print(f"[dim]Loaded {msg_count} messages from history[/dim]")
        else:
            console.print(f"[yellow]Warning: No messages found in session {args.resume}[/yellow]")
            session_id = uuid.uuid4().hex[:12].upper()
            graph = SessionGraph(session_id)
    else:
        session_id = uuid.uuid4().hex[:12].upper()
        graph = SessionGraph(session_id)

    # Create logger for this session
    logger = JournaldLogger(session_id)

    console.print(f"[dim]Session:[/dim] {session_id}")
    console.print(f"[dim]Repo:[/dim] {MESSAGES_REPO_PATH}")
    console.print(Panel(prompt, title="[bold]Prompt[/bold]", border_style="dim"))

    # Add user message to graph (facts, not dicts!)
    user_message_id = make_user_message_id()
    graph.add_user_message(user_message_id, prompt)

    logger.log("session_start", {"prompt": prompt, "user_message_id": user_message_id, "resumed_from": args.resume})
    logger.log("message", {"role": "user", "content": prompt}, message_id=user_message_id)

    # Save graph after user message
    commit_id = git_store.save(graph, f"[user] {prompt}")
    console.print(f"[dim]Git:[/dim] {str(commit_id)[:8]}")

    # Build API messages by querying the graph - this is the ONLY place we make dicts
    messages = graph.build_api_messages()

    client = AsyncAnthropic(api_key=api_key)
    runner = client.beta.messages.tool_runner(
        model="claude-opus-4-5-20251101",
        max_tokens=1024,
        tools=[calculator, get_current_time],
        messages=messages,
    )

    current_message_id: str | None = None

    async for message in runner:
        msg_id = message.id

        # New assistant message - add to graph
        if msg_id != current_message_id:
            if current_message_id:
                # Save intermediate state
                git_store.save(graph, "[assistant] intermediate")
            graph.add_assistant_message(msg_id)
            current_message_id = msg_id

        # Collect tool uses and add them to the graph
        tool_uses = []
        for block in message.content:
            if block.type == "tool_use":
                console.print(f"\n[yellow]Tool call:[/yellow] {block.name}")
                console.print(f"[dim]Input:[/dim] {block.input}")
                logger.log("tool_call", {"name": block.name, "input": block.input}, message_id=msg_id)

                # Add fact to graph
                graph.add_tool_use(msg_id, block.id, block.name, block.input)
                tool_uses.append(block)

        # Process tool results
        if tool_uses:
            if response := await runner.generate_tool_call_response():
                # Extract results and add them to graph
                tool_results_by_id = {
                    item["tool_use_id"]: item["content"]
                    for item in response.get("content", [])
                    if item.get("type") == "tool_result"
                }
                for tool_use in tool_uses:
                    result_content = tool_results_by_id.get(tool_use.id, "No result")
                    console.print(f"[dim]Result ({tool_use.name}):[/dim] {result_content}")
                    logger.log("tool_result", {"name": tool_use.name, "content": result_content}, message_id=msg_id)

                    # Add fact to graph
                    graph.add_tool_result(tool_use.id, str(result_content))

        # Handle end of turn
        if message.stop_reason == "end_turn":
            text_content = next((b.text for b in message.content if hasattr(b, "text")), None)
            if text_content:
                console.print()
                console.print(Panel(text_content, title="[bold]Response[/bold]", border_style="green"))
                logger.log("message", {"role": "assistant", "content": text_content}, message_id=msg_id)
                graph.set_message_content(msg_id, text_content)

            # Save final state
            summary = text_content[:50] + "..." if text_content and len(text_content) > 50 else text_content or "tool interaction"
            commit_id = git_store.save(graph, f"[assistant] {summary}")
            console.print(f"[dim]Git:[/dim] {str(commit_id)[:8]}")
            current_message_id = None

    logger.log("session_end", {"session_id": session_id})
    console.print("\n[dim]Resume this session:[/dim]")
    console.print(f"  nix run .#claude -- --resume {session_id} \"Your follow-up\"")


if __name__ == "__main__":
    asyncio.run(main())
