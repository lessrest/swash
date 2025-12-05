#!/usr/bin/env bash
#
# claude.sh - minimal Claude API client using swash + journal
#
# All messages are stored in the systemd journal with CLAUDE_SESSION field.
# No files needed - just query the journal to resume.
#
# Usage:
#   claude.sh "Your prompt here"
#   claude.sh --resume SESSION_ID "Continue the conversation"
#   claude.sh --list
#
set -euo pipefail

API_URL="https://api.anthropic.com/v1/messages"
MODEL="${CLAUDE_MODEL:-claude-opus-4-5-20251101}"

die() { echo "error: $*" >&2; exit 1; }

command -v jq >/dev/null || die "jq is required"
command -v curl >/dev/null || die "curl is required"
command -v swash >/dev/null || die "swash is required"

[[ -n "${ANTHROPIC_API_KEY:-}" ]] || die "ANTHROPIC_API_KEY not set"

new_session_id() { systemd-id128 new; }

# Write a message to the journal with CLAUDE_SESSION tag
journal_message() {
    local session="$1" role="$2" content="$3"
    logger --journald <<EOF
MESSAGE=$content
CLAUDE_SESSION=$session
CLAUDE_ROLE=$role
EOF
}

# Build messages array from journal entries for a session
build_messages_from_journal() {
    local session="$1"
    # Query journal for entries with this session, extract role and content
    journalctl --user CLAUDE_SESSION="$session" -o json --no-pager 2>/dev/null | \
        jq -c 'select(.CLAUDE_ROLE) | {role: .CLAUDE_ROLE, content: .MESSAGE}' | \
        jq -s '.'
}

# List sessions from journal
list_sessions() {
    echo "Recent sessions (from journal):"
    journalctl --user -o json --no-pager 2>/dev/null | \
        jq -r 'select(.CLAUDE_SESSION) | .CLAUDE_SESSION' | \
        sort -u | tail -20
}

SESSION_ID=""
RESUME=""
PROMPT=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --list|-l)
            list_sessions
            exit 0
            ;;
        --resume|-r)
            RESUME="$2"
            shift 2
            ;;
        *)
            PROMPT="$1"
            shift
            ;;
    esac
done

[[ -n "$PROMPT" ]] || die "No prompt provided. Usage: claude.sh [--resume ID] \"prompt\""

if [[ -n "$RESUME" ]]; then
    SESSION_ID="$RESUME"
    # Verify session exists in journal
    count=$(journalctl --user CLAUDE_SESSION="$SESSION_ID" -o json --no-pager 2>/dev/null | wc -l)
    [[ "$count" -gt 0 ]] || die "Session not found in journal: $SESSION_ID"
else
    SESSION_ID=$(new_session_id)
fi

# Write user message to journal
journal_message "$SESSION_ID" "user" "$PROMPT"

# Build messages array from journal
messages=$(build_messages_from_journal "$SESSION_ID")

# Build request body
body=$(jq -n \
    --arg model "$MODEL" \
    --argjson messages "$messages" \
    '{
        model: $model,
        max_tokens: 4096,
        stream: true,
        messages: $messages
    }')

# Start API call via swash with session tag
swash_output=$(swash run --tag "CLAUDE_SESSION=$SESSION_ID" --protocol sse -- \
    curl -sN "$API_URL" \
    -H "Content-Type: application/json" \
    -H "x-api-key: $ANTHROPIC_API_KEY" \
    -H "anthropic-version: 2023-06-01" \
    -d "$body" 2>&1) || die "failed to start swash"

swash_session=$(echo "$swash_output" | grep -oE '[A-Z0-9]{6}' | head -1)

if [[ -z "$swash_session" ]]; then
    die "Failed to start swash session: $swash_output"
fi

# Follow and parse SSE events
response=""
while IFS= read -r json; do
    [[ -z "$json" ]] && continue

    event_type=$(echo "$json" | jq -r '.type // empty' 2>/dev/null) || continue

    case "$event_type" in
        content_block_delta)
            text=$(echo "$json" | jq -r '.delta.text // empty' 2>/dev/null) || continue
            if [[ -n "$text" ]]; then
                printf '%s' "$text"
                response+="$text"
            fi
            ;;
        message_stop)
            echo
            ;;
        error)
            err=$(echo "$json" | jq -r '.error.message // "Unknown error"' 2>/dev/null)
            die "API error: $err"
            ;;
    esac
done < <(swash follow "$swash_session")

# Write assistant response to journal
if [[ -n "$response" ]]; then
    journal_message "$SESSION_ID" "assistant" "$response"
fi

echo "Session: $SESSION_ID" >&2
