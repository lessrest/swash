#!/usr/bin/env bash
#
# claude.sh - minimal Claude API client using swash
#
# Conversation messages and tool state are stored as Swash events, so the
# script works with either the systemd or POSIX backend and needs no files.
#
# Usage:
#   claude.sh new "Your prompt here"
#   claude.sh prompt SESSION_ID "Add another prompt"
#   claude.sh run-tool SESSION_ID
#   claude.sh continue SESSION_ID
#   claude.sh list
#
set -euo pipefail

MODEL="${CLAUDE_MODEL:-claude-opus-5}"
MESSAGE_EVENT="claude-message"

die() { echo "error: $*" >&2; exit 1; }

usage() {
    cat <<'EOF'
Usage: claude.sh new PROMPT
       claude.sh prompt SESSION_ID PROMPT
       claude.sh run-tool SESSION_ID
       claude.sh continue SESSION_ID
       claude.sh list

Claude may request one shell command per response. The command is only shown,
not executed. run-tool executes the latest pending command through Swash and
records its result; continue sends that result to Claude in a new API request.
EOF
}

command -v jq >/dev/null || die "jq is required"
command -v swash >/dev/null || die "swash is required"

new_session_id() {
    LC_ALL=C od -An -N12 -tx1 /dev/urandom | tr -d ' \n'
}

# Structured API content preserves tool calls, results, and thinking signatures.
emit_json_message() {
    local session="$1" role="$2" content="$3"
    shift 3

    local args=(
        emit "$session"
        --event "$MESSAGE_EVENT"
        --message "$content"
        --field "CLAUDE_ROLE=$role"
        --field CLAUDE_CONTENT=json
    )
    local field
    for field in "$@"; do
        args+=(--field "$field")
    done
    swash "${args[@]}" >/dev/null
}

# Build Anthropic messages from the structured content stored in Swash events.
build_messages() {
    local session="$1"
    swash events --session "$session" --event "$MESSAGE_EVENT" --json | \
        jq -s '[.[] | {
            role: .fields.CLAUDE_ROLE,
            content: (.message | fromjson)
        }]'
}

session_exists() {
    local session="$1"
    swash events --session "$session" --event "$MESSAGE_EVENT" --json | \
        jq -e -s 'length > 0' >/dev/null
}

# Return the newest shell call that does not yet have a matching tool_result.
latest_pending_tool() {
    local messages="$1"
    jq -c '
        [ .[] | select(.role == "assistant") | .content
          | if type == "array" then .[] else empty end
          | select(.type == "tool_use" and .name == "shell") ] as $calls
        | [ .[] | select(.role == "user") | .content
            | if type == "array" then .[] else empty end
            | select(.type == "tool_result") | .tool_use_id ] as $results
        | [ $calls[] | select(.id as $id | ($results | index($id) | not)) ]
        | last // empty
    ' <<<"$messages"
}

latest_message_is_tool_result() {
    local messages="$1"
    jq -e '
        (last // {}) as $message
        | $message.role == "user"
          and ($message.content | type) == "array"
          and any($message.content[]; .type == "tool_result")
    ' <<<"$messages" >/dev/null
}

# List sessions in creation order, with the newest 20 at the bottom.
list_sessions() {
    echo "Recent sessions:"
    swash events --event "$MESSAGE_EVENT" --field CLAUDE_ROLE=user --json | \
        jq -r '.fields.SWASH_SESSION' | \
        awk '!seen[$0]++' | tail -20
}

run_tool() {
    local session="$1" messages tool command tool_id
    session_exists "$session" || die "Session not found: $session"
    messages=$(build_messages "$session")
    tool=$(latest_pending_tool "$messages")
    [[ -n "$tool" ]] || die "No pending shell tool call in session: $session"

    tool_id=$(jq -r '.id' <<<"$tool")
    command=$(jq -r '.input.command // empty' <<<"$tool")
    [[ -n "$command" ]] || die "Pending shell tool call has no command"

    echo "+ $command"

    local start_output tool_session start_status
    if ! start_output=$(swash start \
            --tag "CLAUDE_SESSION=$session" \
            --tag "CLAUDE_TOOL_ID=$tool_id" \
            -- bash -lc "$command" 2>&1); then
        die "Failed to start tool through Swash: $start_output"
    fi
    read -r tool_session start_status <<<"$start_output"
    if [[ -z "$tool_session" || "$start_status" != "started" ]]; then
        die "Failed to start tool through Swash: $start_output"
    fi

    local result_file output exit_code
    result_file=$(mktemp)
    set +e
    swash follow "$tool_session" | tee "$result_file"
    exit_code=${PIPESTATUS[0]}
    set -e
    output=$(cat "$result_file")
    rm -f "$result_file"

    if [[ -z "$output" ]]; then
        if [[ "$exit_code" -eq 0 ]]; then
            output="Command completed successfully with no output."
        else
            output="Command produced no output."
        fi
    fi
    if [[ "$exit_code" -ne 0 ]]; then
        output+=$'\n\nCommand exited with status '
        output+="$exit_code"
        output+='.'
    fi

    local is_error=false result
    [[ "$exit_code" -ne 0 ]] && is_error=true
    result=$(jq -cn \
        --arg tool_id "$tool_id" \
        --arg output "$output" \
        --argjson is_error "$is_error" \
        '[{type: "tool_result", tool_use_id: $tool_id,
           content: $output, is_error: $is_error}]')
    emit_json_message "$session" user "$result" \
        "CLAUDE_TOOL_ID=$tool_id" \
        "CLAUDE_TOOL_SESSION=$tool_session" \
        "CLAUDE_TOOL_EXIT_CODE=$exit_code"

    echo "Tool result recorded. Continue with: claude.sh continue $session" >&2
}

# Set by stream_response for persistence after the stream completes.
ASSISTANT_CONTENT='[]'

stream_response() {
    local swash_session="$1"
    local block_json='' tool_input='' event_type delta_type text fragment command
    local printed_text=false follow_fd follow_pid
    ASSISTANT_CONTENT='[]'

    coproc SWASH_FOLLOW { swash follow "$swash_session"; }
    follow_fd=${SWASH_FOLLOW[0]}
    follow_pid=$SWASH_FOLLOW_PID
    while IFS= read -r json; do
        [[ -z "$json" ]] && continue
        event_type=$(jq -r '.type // empty' <<<"$json" 2>/dev/null) || continue

        case "$event_type" in
            content_block_start)
                block_json=$(jq -c '.content_block' <<<"$json")
                tool_input=''
                ;;
            content_block_delta)
                [[ -n "$block_json" ]] || continue
                delta_type=$(jq -r '.delta.type // empty' <<<"$json")
                case "$delta_type" in
                    text_delta)
                        text=$(jq -jr '(.delta.text // empty), "__CLAUDE_SH_END__"' <<<"$json")
                        text=${text%__CLAUDE_SH_END__}
                        printf '%s' "$text"
                        [[ -n "$text" ]] && printed_text=true
                        block_json=$(jq -c --arg text "$text" \
                            '.text = ((.text // "") + $text)' <<<"$block_json")
                        ;;
                    input_json_delta)
                        fragment=$(jq -jr '(.delta.partial_json // empty), "__CLAUDE_SH_END__"' <<<"$json")
                        fragment=${fragment%__CLAUDE_SH_END__}
                        tool_input+="$fragment"
                        ;;
                    thinking_delta)
                        fragment=$(jq -jr '(.delta.thinking // empty), "__CLAUDE_SH_END__"' <<<"$json")
                        fragment=${fragment%__CLAUDE_SH_END__}
                        block_json=$(jq -c --arg fragment "$fragment" \
                            '.thinking = ((.thinking // "") + $fragment)' <<<"$block_json")
                        ;;
                    signature_delta)
                        fragment=$(jq -jr '(.delta.signature // empty), "__CLAUDE_SH_END__"' <<<"$json")
                        fragment=${fragment%__CLAUDE_SH_END__}
                        block_json=$(jq -c --arg fragment "$fragment" \
                            '.signature = ((.signature // "") + $fragment)' <<<"$block_json")
                        ;;
                esac
                ;;
            content_block_stop)
                [[ -n "$block_json" ]] || continue
                if [[ $(jq -r '.type' <<<"$block_json") == tool_use ]]; then
                    [[ -n "$tool_input" ]] || tool_input='{}'
                    block_json=$(jq -c --arg input "$tool_input" \
                        '.input = ($input | fromjson)' <<<"$block_json") \
                        || die "Invalid streamed tool input"
                    command=$(jq -r '.input.command // empty' <<<"$block_json")
                    printf '\n\nShell tool requested:\n  %s\n' "$command" >&2
                    printf 'Run it with: claude.sh run-tool %s\n' "$SESSION_ID" >&2
                fi
                ASSISTANT_CONTENT=$(jq -cn \
                    --argjson content "$ASSISTANT_CONTENT" \
                    --argjson block "$block_json" \
                    '$content + [$block]')
                block_json=''
                tool_input=''
                ;;
            message_stop)
                [[ "$printed_text" == true ]] && echo
                ;;
            error)
                die "API error: $(jq -r '.error.message // "Unknown error"' <<<"$json")"
                ;;
        esac
    done <&"$follow_fd"
    if ! wait "$follow_pid"; then
        die "Anthropic request failed"
    fi
}

call_claude() {
    local session="$1" messages="$2" body swash_output swash_session start_status
    command -v curl >/dev/null || die "curl is required"
    [[ -n "${ANTHROPIC_API_KEY:-}" ]] || die "ANTHROPIC_API_KEY not set"

    body=$(jq -n \
        --arg model "$MODEL" \
        --argjson messages "$messages" \
        '{
            model: $model,
            max_tokens: 4096,
            stream: true,
            tools: [{
                name: "shell",
                description: "Propose one non-interactive shell command when running a command would help answer the user. The command will not run automatically: it is shown to the user for review and only runs if they invoke a separate command. It runs through bash -lc in the current working directory, with stdout and stderr returned afterward. Do not use this tool for explanations or when a command is unnecessary.",
                strict: true,
                input_schema: {
                    type: "object",
                    properties: {
                        command: {
                            type: "string",
                            description: "The complete shell command to execute with bash -lc."
                        }
                    },
                    required: ["command"],
                    additionalProperties: false
                }
            }],
            tool_choice: {type: "auto", disable_parallel_tool_use: true},
            messages: $messages
        }')

    # Keep the API key and conversation body out of SWASH_COMMAND. The child
    # inherits both variables, while Swash only records this static launcher.
    export CLAUDE_REQUEST_BODY="$body"
    # Expansions in the single-quoted script deliberately happen in the child.
    # shellcheck disable=SC2016
    if ! swash_output=$(swash start --tag "CLAUDE_SESSION=$session" --protocol sse -- \
            bash -c 'curl --silent --show-error --no-buffer --fail-with-body --max-time 600 \
                "https://api.anthropic.com/v1/messages" \
                -H "Authorization: Bearer $ANTHROPIC_API_KEY" \
                -H "anthropic-version: 2023-06-01" \
                -H "content-type: application/json" \
                --data-binary "$CLAUDE_REQUEST_BODY"
                status=$?
                if ((status != 0)); then
                    printf '\''\n\ndata: {"type":"error","error":{"message":"Anthropic request failed (curl status %s)"}}\n\n'\'' "$status"
                fi
                exit "$status"' 2>&1); then
        unset CLAUDE_REQUEST_BODY
        die "Failed to start Swash session: $swash_output"
    fi
    unset CLAUDE_REQUEST_BODY
    read -r swash_session start_status <<<"$swash_output"
    if [[ -z "$swash_session" || "$start_status" != "started" ]]; then
        die "Failed to start Swash session: $swash_output"
    fi

    stream_response "$swash_session"
    if [[ $(jq 'length' <<<"$ASSISTANT_CONTENT") -gt 0 ]]; then
        emit_json_message "$session" assistant "$ASSISTANT_CONTENT"
    fi
    echo "Session: $session" >&2
}

case "${1:-}" in
    list)
        [[ $# -eq 1 ]] || die "list accepts no arguments"
        list_sessions
        exit 0
        ;;
    run-tool)
        [[ $# -eq 2 ]] || die "Usage: claude.sh run-tool SESSION_ID"
        run_tool "$2"
        exit 0
        ;;
    continue)
        [[ $# -eq 2 ]] || die "Usage: claude.sh continue SESSION_ID"
        SESSION_ID="$2"
        session_exists "$SESSION_ID" || die "Session not found: $SESSION_ID"
        messages=$(build_messages "$SESSION_ID")
        latest_message_is_tool_result "$messages" \
            || die "No new tool result to send in session: $SESSION_ID"
        call_claude "$SESSION_ID" "$messages"
        exit 0
        ;;
    new)
        [[ $# -eq 2 ]] || die "Usage: claude.sh new PROMPT"
        SESSION_ID=$(new_session_id)
        prompt_content=$(jq -cn --arg prompt "$2" '$prompt')
        emit_json_message "$SESSION_ID" user "$prompt_content"
        messages=$(build_messages "$SESSION_ID")
        call_claude "$SESSION_ID" "$messages"
        exit 0
        ;;
    prompt)
        [[ $# -eq 3 ]] || die "Usage: claude.sh prompt SESSION_ID PROMPT"
        SESSION_ID="$2"
        session_exists "$SESSION_ID" || die "Session not found: $SESSION_ID"
        messages=$(build_messages "$SESSION_ID")
        [[ -z $(latest_pending_tool "$messages") ]] \
            || die "Run the pending tool first: claude.sh run-tool $SESSION_ID"
        prompt_content=$(jq -cn --arg prompt "$3" '$prompt')
        emit_json_message "$SESSION_ID" user "$prompt_content"
        messages=$(build_messages "$SESSION_ID")
        call_claude "$SESSION_ID" "$messages"
        exit 0
        ;;
    help|-h|--help)
        usage
        exit 0
        ;;
    *)
        usage >&2
        exit 2
        ;;
esac
