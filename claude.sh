#!/usr/bin/env bash
#
# claude.sh - minimal Claude API client using curl
#
# Usage:
#   claude.sh "Your prompt here"
#   claude.sh --resume SESSION_ID "Continue the conversation"
#   claude.sh --list
#
set -euo pipefail

# XDG base directory
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/claude"
SESSIONS_DIR="$DATA_DIR/sessions"

# API config
API_URL="https://api.anthropic.com/v1/messages"
MODEL="${CLAUDE_MODEL:-claude-sonnet-4-20250514}"

die() { echo "error: $*" >&2; exit 1; }

# Ensure jq is available
command -v jq >/dev/null || die "jq is required"
command -v curl >/dev/null || die "curl is required"

# Check API key
[[ -n "${ANTHROPIC_API_KEY:-}" ]] || die "ANTHROPIC_API_KEY not set"

mkdir -p "$SESSIONS_DIR"

# Generate session ID (8 hex chars)
new_session_id() {
    head -c4 /dev/urandom | xxd -p
}

# List sessions
list_sessions() {
    if [[ ! -d "$SESSIONS_DIR" ]] || [[ -z "$(ls -A "$SESSIONS_DIR" 2>/dev/null)" ]]; then
        echo "No sessions found"
        exit 0
    fi
    echo "Sessions:"
    for dir in "$SESSIONS_DIR"/*/; do
        [[ -d "$dir" ]] || continue
        id=$(basename "$dir")
        msgs="$dir/messages.jsonl"
        if [[ -f "$msgs" ]]; then
            count=$(wc -l < "$msgs")
            last=$(tail -1 "$msgs" | jq -r '.content[:50]' 2>/dev/null || echo "?")
            printf "  %s  (%d msgs)  %s...\n" "$id" "$count" "$last"
        else
            printf "  %s  (empty)\n" "$id"
        fi
    done
}

# Build messages array from jsonl file
build_messages() {
    local file="$1"
    if [[ -f "$file" ]]; then
        jq -s '.' "$file"
    else
        echo "[]"
    fi
}

# Main
SESSION_ID=""
RESUME=""
PROMPT=""

# Parse args
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

# Set up session
if [[ -n "$RESUME" ]]; then
    SESSION_ID="$RESUME"
    SESSION_DIR="$SESSIONS_DIR/$SESSION_ID"
    [[ -d "$SESSION_DIR" ]] || die "Session not found: $SESSION_ID"
    echo "Resuming session: $SESSION_ID" >&2
else
    SESSION_ID=$(new_session_id)
    SESSION_DIR="$SESSIONS_DIR/$SESSION_ID"
    mkdir -p "$SESSION_DIR"
    echo "New session: $SESSION_ID" >&2
fi

MESSAGES_FILE="$SESSION_DIR/messages.jsonl"

# Add user message
echo "{\"role\":\"user\",\"content\":$(jq -Rs '.' <<< "$PROMPT")}" >> "$MESSAGES_FILE"

# Build full messages array
messages=$(build_messages "$MESSAGES_FILE")

# Make API call and capture response
echo "---" >&2
response=""

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

# Stream and capture
while IFS= read -r line; do
    if [[ "$line" == data:* ]]; then
        json="${line#data: }"
        [[ "$json" == "[DONE]" ]] && continue
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
    fi
done < <(curl -sN "$API_URL" \
    -H "Content-Type: application/json" \
    -H "x-api-key: $ANTHROPIC_API_KEY" \
    -H "anthropic-version: 2023-06-01" \
    -d "$body" 2>/dev/null)

# Save assistant response
if [[ -n "$response" ]]; then
    echo "{\"role\":\"assistant\",\"content\":$(jq -Rs '.' <<< "$response")}" >> "$MESSAGES_FILE"
fi

echo "---" >&2
echo "Session: $SESSION_ID" >&2
echo "Resume: claude.sh --resume $SESSION_ID \"your follow-up\"" >&2


