#!/usr/bin/env bash
#
# claude.sh - minimal Claude API client using swash
#
# All messages are stored as Swash events. No files are needed, and the
# conversation works with either the systemd or POSIX backend.
#
# Usage:
#   claude.sh "Your prompt here"
#   claude.sh --resume SESSION_ID "Continue the conversation"
#   claude.sh --list
#
set -euo pipefail

API_URL="https://api.anthropic.com/v1/messages"
MODEL="${CLAUDE_MODEL:-claude-opus-5}"
MESSAGE_EVENT="claude-message"

die() { echo "error: $*" >&2; exit 1; }

command -v jq >/dev/null || die "jq is required"
command -v swash >/dev/null || die "swash is required"

new_session_id() {
    LC_ALL=C od -An -N12 -tx1 /dev/urandom | tr -d ' \n'
}

# Store one conversation turn in Swash's backend-independent event log.
emit_message() {
    local session="$1" role="$2" content="$3"
    swash emit "$session" \
        --event "$MESSAGE_EVENT" \
        --message "$content" \
        --field "CLAUDE_ROLE=$role" >/dev/null
}

# Build the Anthropic messages array from the conversation's Swash events.
build_messages() {
    local session="$1"
    swash events --session "$session" --event "$MESSAGE_EVENT" --json | \
        jq -s '[.[] | {role: .fields.CLAUDE_ROLE, content: .message}]'
}

# List sessions in creation order, with the newest 20 at the bottom.
list_sessions() {
    echo "Recent sessions:"
    swash events --event "$MESSAGE_EVENT" --field CLAUDE_ROLE=user --json | \
        jq -r '.fields.SWASH_SESSION' | \
        awk '!seen[$0]++' | tail -20
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
            [[ $# -ge 2 ]] || die "$1 requires a session ID"
            RESUME="$2"
            shift 2
            ;;
        --help|-h)
            echo 'Usage: claude.sh [--resume ID] "prompt"'
            echo '       claude.sh --list'
            exit 0
            ;;
        --)
            shift
            [[ $# -eq 1 ]] || die "Expected one prompt after --"
            PROMPT="$1"
            shift
            ;;
        -*)
            die "Unknown option: $1"
            ;;
        *)
            [[ -z "$PROMPT" ]] || die "Expected one prompt"
            PROMPT="$1"
            shift
            ;;
    esac
done

[[ -n "$PROMPT" ]] || die "No prompt provided. Usage: claude.sh [--resume ID] \"prompt\""
command -v curl >/dev/null || die "curl is required"
[[ -n "${ANTHROPIC_API_KEY:-}" ]] || die "ANTHROPIC_API_KEY not set"

if [[ -n "$RESUME" ]]; then
    SESSION_ID="$RESUME"
    # Verify that the ID belongs to a conversation, not merely a Swash task.
    if ! swash events --session "$SESSION_ID" --event "$MESSAGE_EVENT" --json | \
        jq -e -s 'length > 0' >/dev/null; then
        die "Session not found: $SESSION_ID"
    fi
else
    SESSION_ID=$(new_session_id)
fi

# Store the user message before making the request so failed turns can be resumed.
emit_message "$SESSION_ID" "user" "$PROMPT"

# Build messages array from Swash events.
messages=$(build_messages "$SESSION_ID")

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

# Start the request detached; swash follow below owns streaming the response.
if ! swash_output=$(swash start --tag "CLAUDE_SESSION=$SESSION_ID" --protocol sse -- \
        curl --silent --show-error --no-buffer --fail-with-body --max-time 600 "$API_URL" \
        -H "Authorization: Bearer $ANTHROPIC_API_KEY" \
        -H "anthropic-version: 2023-06-01" \
        -H "content-type: application/json" \
        -d "$body" 2>&1); then
    die "Failed to start Swash session: $swash_output"
fi
read -r swash_session start_status <<<"$swash_output"

if [[ -z "$swash_session" || "$start_status" != "started" ]]; then
    die "Failed to start swash session: $swash_output"
fi

# Follow and parse SSE events
response=""
while IFS= read -r json; do
    [[ -z "$json" ]] && continue

    event_type=$(echo "$json" | jq -r '.type // empty' 2>/dev/null) || continue

    case "$event_type" in
        content_block_delta)
            # The marker prevents command substitution from stripping newlines
            # that are part of the streamed text itself.
            text=$(jq -jr '(.delta.text // empty), "__CLAUDE_SH_END__"' <<<"$json" 2>/dev/null) || continue
            text=${text%__CLAUDE_SH_END__}
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

# Store the completed assistant turn as another Swash event.
if [[ -n "$response" ]]; then
    emit_message "$SESSION_ID" "assistant" "$response"
fi

echo "Session: $SESSION_ID" >&2
