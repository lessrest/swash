# Contexts

A context is a lightweight namespace for grouping related sessions. It provides
a working directory and an identity without the overhead of a named project or
container.

## Integration with Backends

Contexts are part of the `Backend` interface. Each backend implementation
manages context state using its own event log:

- **systemd backend**: Context events go to the systemd journal
- **posix backend**: Context events go to the file-based event log

Context directories live under the backend's `StateDir`:

```
$XDG_STATE_HOME/swash/contexts/<ID>/
```

This means contexts are scoped to a backend. If you switch backends (e.g.,
moving from a systemd machine to one without), contexts don't automatically
migrate. This is acceptable—backend choice is typically environment-specific,
not something users flip between.

## Backend Interface Additions

```go
type Backend interface {
    // ... existing methods ...

    // Context management
    CreateContext(ctx context.Context) (contextID string, dir string, err error)
    ListContexts(ctx context.Context) ([]Context, error)
    GetContextDir(ctx context.Context, contextID string) (string, error)
}

type Context struct {
    ID      string
    Dir     string
    Created time.Time
}
```

## Conceptual Model

Contexts and sessions are related through facts stored in the journal:

- A session **belongs to** a context
- A context **belongs to** another context (future)

This is relational rather than hierarchical—relations are first-class data, not
baked-in tree structure. A session-context binding is a single journal event:

```
SWASH_EVENT=session-context
SWASH_SESSION=ABC123
SWASH_CONTEXT=XYZ789
```

To find all output from a context, query in two steps:
1. Find sessions belonging to the context
2. Query journal entries for those sessions

The swash CLI hides this complexity.

## Minimal Implementation

### Creating a context

```bash
swash context new
# XYZ789 created
# /home/user/.local/state/swash/contexts/XYZ789
```

This:
- Generates an ID
- Creates a working directory at `$XDG_STATE_HOME/swash/contexts/<ID>/`
- Emits `SWASH_EVENT=context-created SWASH_CONTEXT=<ID>` to the journal
- Prints the ID and path

### Listing contexts

```bash
swash context list
# XYZ789  2024-01-15 10:30  /home/user/.local/state/swash/contexts/XYZ789
# ABC456  2024-01-14 09:00  /home/user/.local/state/swash/contexts/ABC456
```

Queries the journal for `SWASH_EVENT=context-created` events.

### Entering a context

Option 1: Spawn a subshell
```bash
swash context shell XYZ789
# Now in context XYZ789
# Working directory: /home/user/.local/state/swash/contexts/XYZ789
$ swash run ./my-script.py   # automatically associated with XYZ789
$ exit
```

Option 2: Manual
```bash
export SWASH_CONTEXT=XYZ789
cd "$(swash context dir XYZ789)"
swash run ./my-script.py
```

Both are supported.

### Session-context binding

When `SWASH_CONTEXT` is set in the environment, `swash run` and `swash start`
emit a relation event at session start:

```
SWASH_EVENT=session-context
SWASH_SESSION=<session-id>
SWASH_CONTEXT=<context-id>
```

### Filtered history

```bash
swash history                    # all sessions
SWASH_CONTEXT=XYZ789 swash history  # only sessions in XYZ789
swash context shell XYZ789
$ swash history                  # only sessions in XYZ789
```

## Deferred

These are explicitly out of scope for the initial implementation:

- **Separate state vs working directory**: For now, one directory serves both.
- **Git initialization**: Contexts start as empty directories.
- **Archiving/closing**: Contexts persist indefinitely.
- **Retroactive tagging**: May work naturally but no explicit support.
- **Context-to-context relations**: Flat structure initially.
- **Default entrypoints**: No command inheritance.
- **Auto-detection from cwd**: Only env var selection for now.

## Journal Fields

| Field | Description |
|-------|-------------|
| `SWASH_CONTEXT` | Context ID |
| `SWASH_EVENT=context-created` | Context creation event |
| `SWASH_EVENT=session-context` | Session-to-context relation |

## Example Workflow

```bash
# Create a context for Gmail automation
swash context new
# GML001 created
# /home/user/.local/state/swash/contexts/GML001

# Enter the context
swash context shell GML001

# Write some code
cat > fetch.py << 'EOF'
#!/usr/bin/env python3
print("fetching gmail...")
EOF
chmod +x fetch.py

# Run it (automatically tracked under GML001)
swash run ./fetch.py

# Later, see what ran in this context
swash history
# GML001  ABC123  2024-01-15 10:35  ./fetch.py  exited 0

# Exit the context shell
exit
```
