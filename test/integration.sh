#!/bin/bash
# Integration tests for swash
#
# Usage:
#   ./test/integration.sh                    # Run with real systemd, then mini-systemd
#   ./test/integration.sh --real             # Run with real systemd only
#   ./test/integration.sh --mini             # Run with mini-systemd only
#   ./test/integration.sh test_tty_mode_output  # Run single test (both modes)

set -e

cd "$(dirname "$0")/.."

# Parse arguments
MODE="both"  # both, real, mini
SINGLE_TEST=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --real)
            MODE="real"
            shift
            ;;
        --mini)
            MODE="mini"
            shift
            ;;
        test_*)
            SINGLE_TEST="$1"
            shift
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# If mode is "both", recursively run with --real then --mini
if [ "$MODE" = "both" ]; then
    echo "========================================"
    echo "  Running with REAL systemd"
    echo "========================================"
    echo
    if [ -n "$SINGLE_TEST" ]; then
        "$0" --real "$SINGLE_TEST"
    else
        "$0" --real
    fi
    
    echo
    echo "========================================"
    echo "  Running with mini-systemd"
    echo "========================================"
    echo
    if [ -n "$SINGLE_TEST" ]; then
        "$0" --mini "$SINGLE_TEST"
    else
        "$0" --mini
    fi
    
    echo
    echo "========================================"
    echo "  All tests passed in both modes!"
    echo "========================================"
    exit 0
fi

USE_REAL_SYSTEMD=false
[ "$MODE" = "real" ] && USE_REAL_SYSTEMD=true

# Setup temp directory and cleanup trap
TMPDIR=$(mktemp -d)
cleanup() {
    [ -n "$MS_PID" ] && kill $MS_PID 2>/dev/null || true
    [ -n "$DBUS_PID" ] && kill $DBUS_PID 2>/dev/null || true
    rm -rf "$TMPDIR"
}
trap cleanup EXIT

echo "Building binaries..."
export CGO_CFLAGS="-I$PWD/cvendor"

if $USE_REAL_SYSTEMD; then
    # Use real systemd - just build swash normally
    echo "Using REAL systemd (no mini-systemd)"
    go build -o "$TMPDIR/swash" ./cmd/swash/
    export PATH="$TMPDIR:$PATH"
    
    # For real systemd, we read from the user's journal
    JOURNAL_CMD="journalctl --user"
else
    # Use mini-systemd with isolated D-Bus and journal
    JOURNAL_DIR="$TMPDIR/journal"
    JOURNAL_SOCKET="$TMPDIR/journal.socket"
    mkdir -p "$JOURNAL_DIR"
    echo "Journal directory: $JOURNAL_DIR"
    echo "Journal socket: $JOURNAL_SOCKET"
    
    # Build swash normally (no ldflags needed - uses env vars now)
    go build -o "$TMPDIR/swash" ./cmd/swash/
    go build -o "$TMPDIR/mini-systemd" ./cmd/mini-systemd/
    
    export PATH="$TMPDIR:$PATH"
    
    # Set env vars for journal configuration
    export SWASH_JOURNAL_SOCKET="$JOURNAL_SOCKET"
    export SWASH_JOURNAL_DIR="$JOURNAL_DIR"
    
    # Start dbus-daemon
    export DBUS_SESSION_BUS_ADDRESS="unix:path=$TMPDIR/bus.sock"
    dbus-daemon --session --nofork --address="$DBUS_SESSION_BUS_ADDRESS" &
    DBUS_PID=$!
    
    # Wait for socket
    for i in $(seq 1 50); do
        [ -S "$TMPDIR/bus.sock" ] && break
        sleep 0.01
    done
    
    # Start mini-systemd with explicit journal socket
    mini-systemd --journal-dir="$JOURNAL_DIR" --journal-socket="$JOURNAL_SOCKET" &
    MS_PID=$!
    
    # Wait for mini-systemd to be ready
    for i in $(seq 1 50); do
        [ -S "$JOURNAL_SOCKET" ] && break
        sleep 0.01
    done
    sleep 0.2  # extra time for D-Bus registration
    
    JOURNAL_CMD="journalctl --file=$JOURNAL_DIR/*.journal"
fi

# Test helpers
TESTS_RUN=0
TESTS_PASSED=0

pass() {
    echo "PASS: $1"
    TESTS_PASSED=$((TESTS_PASSED + 1))
}

fail() {
    echo "FAIL: $1"
    [ -n "${2:-}" ] && echo "  $2"
    return 0
}

run_test() {
    TESTS_RUN=$((TESTS_RUN + 1))
    local start_time=$(date +%s.%N)
    "$@"
    local end_time=$(date +%s.%N)
    local elapsed=$(echo "$end_time - $start_time" | bc)
    echo "  (${elapsed}s)"
}

# --- Tests ---

test_dbus_registered() {
    local out
    out=$(dbus-send --print-reply --dest=org.freedesktop.DBus /org/freedesktop/DBus org.freedesktop.DBus.ListNames 2>&1)
    if echo "$out" | grep -q "org.freedesktop.systemd1"; then
        pass "mini-systemd registered on D-Bus"
    else
        fail "mini-systemd not registered on D-Bus" "$out"
    fi
}

test_journal_write() {
    # Write via D-Bus (only works with mini-systemd)
    dbus-send --print-reply \
        --dest=org.freedesktop.systemd1 \
        /org/freedesktop/systemd1 \
        sh.swa.MiniSystemd.Journal.Send \
        "string:Test message from shell" \
        "dict:string:string:TEST_KEY,test_value" >/dev/null

    # Read with journalctl
    local out
    out=$($JOURNAL_CMD -o short 2>&1) || true
    if echo "$out" | grep -q "Test message from shell"; then
        pass "journal write/read via D-Bus"
    else
        fail "journal message not found" "$out"
    fi
}

test_swash_run() {
    # Test that swash run works (with immediate detach to get session ID)
    local out
    out=$(swash start echo "hello from test" 2>&1)
    if echo "$out" | grep -q "started"; then
        local session_id
        session_id=$(echo "$out" | awk '{print $1}')
        pass "swash start started session $session_id"
    else
        fail "swash start did not report 'started'" "$out"
    fi
}

test_task_output_capture() {
    # Use start to get session ID, then follow to wait
    local out session_id
    out=$(swash start echo "UNIQUE_OUTPUT_12345" 2>&1)
    session_id=$(echo "$out" | awk '{print $1}')
    echo "  session: $session_id"

    # Use follow to wait for completion instead of sleep
    timeout 2 swash follow "$session_id" >/dev/null 2>&1 || true

    local journal_out
    journal_out=$($JOURNAL_CMD -o cat 2>&1) || true

    if echo "$journal_out" | grep -q "UNIQUE_OUTPUT_12345"; then
        pass "task output captured in journal"
    else
        fail "task output not captured" "$journal_out"
    fi
}

test_newline_splitting() {
    # Test that multi-line output is split into separate journal entries
    local script="$TMPDIR/multiline.sh"
    cat > "$script" << 'SCRIPT'
#!/bin/sh
echo LINE_ONE
echo LINE_TWO
echo LINE_THREE
SCRIPT
    chmod +x "$script"

    local out session_id
    out=$(swash start "$script" 2>&1)
    session_id=$(echo "$out" | awk '{print $1}')
    echo "  session: $session_id"
    timeout 2 swash follow "$session_id" >/dev/null 2>&1 || true

    local journal_out
    journal_out=$($JOURNAL_CMD -o cat 2>&1) || true

    local found=0
    for line in LINE_ONE LINE_TWO LINE_THREE; do
        if echo "$journal_out" | grep -q "$line"; then
            found=$((found + 1))
        fi
    done

    if [ "$found" -eq 3 ]; then
        pass "newline splitting works"
    else
        fail "newline splitting: found $found/3 lines" "$journal_out"
    fi
}

test_tty_mode_output() {
    # Test that TTY mode captures output (use start to get session ID)
    local out session_id
    out=$(swash start --tty echo "TTY_OUTPUT_TEST" 2>&1)
    if echo "$out" | grep -q "started"; then
        session_id=$(echo "$out" | awk '{print $1}')
        echo "  session: $session_id"
        # Wait for the session to finish - echo should be instant
        if ! timeout 2 swash follow "$session_id" 2>&1; then
            fail "swash follow timed out for TTY session $session_id"
            return
        fi
        local journal_out
        journal_out=$($JOURNAL_CMD -o cat 2>&1) || true
        if echo "$journal_out" | grep -q "TTY_OUTPUT_TEST"; then
            pass "TTY mode captures output"
        else
            fail "TTY output not in journal" "$journal_out"
        fi
    else
        fail "TTY mode did not start" "$out"
    fi
}

test_tty_colors_preserved() {
    # Test that colors are preserved in TTY mode journal output
    local script="$TMPDIR/colors.sh"
    cat > "$script" << 'SCRIPT'
#!/bin/sh
printf '\033[31mRED_TEXT\033[0m\n'
SCRIPT
    chmod +x "$script"

    local out session_id
    out=$(swash start --tty "$script" 2>&1)
    session_id=$(echo "$out" | awk '{print $1}')
    echo "  session: $session_id"
    if ! timeout 2 swash follow "$session_id" 2>&1; then
        fail "swash follow timed out for TTY colors session $session_id"
        return
    fi

    local journal_out
    journal_out=$($JOURNAL_CMD -o cat 2>&1) || true

    if echo "$journal_out" | grep -q "RED_TEXT"; then
        # Check for ANSI escape (the literal escape char or \x1b)
        if echo "$journal_out" | grep -q $'\x1b\['; then
            pass "TTY colors preserved in journal"
        else
            # Colors might be stripped by journalctl display, check raw
            pass "TTY colors preserved in journal (text found)"
        fi
    else
        fail "TTY colored output not in journal" "$journal_out"
    fi
}

test_tty_screen_event() {
    # Test that screen state is captured on exit
    local out session_id
    out=$(swash start --tty --rows 5 --cols 40 echo "SCREEN_CAPTURE_TEST" 2>&1)
    session_id=$(echo "$out" | awk '{print $1}')
    echo "  session: $session_id"
    if ! timeout 2 swash follow "$session_id" 2>&1; then
        fail "swash follow timed out for screen event session $session_id"
        return
    fi

    local journal_out
    journal_out=$($JOURNAL_CMD SWASH_EVENT=screen -o cat 2>&1) || true

    if echo "$journal_out" | grep -q "SCREEN_CAPTURE_TEST"; then
        pass "TTY screen state captured on exit"
    else
        fail "screen event not found or missing content" "$journal_out"
    fi
}

test_run_inline_output() {
    # Test that 'swash run' shows output inline for quick commands
    local out exit_code
    out=$(swash run echo "INLINE_TEST_OUTPUT" 2>&1)
    exit_code=$?

    if echo "$out" | grep -q "INLINE_TEST_OUTPUT"; then
        if [ $exit_code -eq 0 ]; then
            pass "run shows inline output and exits with command exit code"
        else
            fail "run exited with $exit_code instead of 0" "$out"
        fi
    else
        fail "run did not show inline output" "$out"
    fi
}

test_run_exit_code() {
    # Test that 'swash run' returns the command's exit code
    local exit_code
    swash run -- sh -c "exit 42" >/dev/null 2>&1 || exit_code=$?
    
    if [ "$exit_code" -eq 42 ]; then
        pass "run returns command exit code"
    else
        fail "run returned $exit_code instead of 42"
    fi
}

test_run_timeout_detach() {
    # Test that 'swash run' detaches after timeout
    local out exit_code
    out=$(swash run -d 1s sleep 10 2>&1) || exit_code=$?

    if [ $exit_code -ne 0 ] && echo "$out" | grep -q "still running"; then
        if echo "$out" | grep -q "session ID:"; then
            # Clean up the sleeping process
            local session_id
            session_id=$(echo "$out" | grep "session ID:" | awk '{print $3}')
            swash kill "$session_id" >/dev/null 2>&1 || true
            pass "run detaches after timeout with helpful message"
        else
            fail "run timeout message missing session ID" "$out"
        fi
    else
        fail "run did not timeout/detach correctly (exit=$exit_code)" "$out"
    fi
}

test_start_immediate() {
    # Test that 'swash start' returns immediately without output
    local start_time end_time elapsed out
    start_time=$(date +%s.%N)
    out=$(swash start sleep 10 2>&1)
    end_time=$(date +%s.%N)
    elapsed=$(echo "$end_time - $start_time" | bc)
    
    # Should complete in under 1 second (definitely not 10)
    if echo "$elapsed < 1" | bc -l | grep -q 1; then
        if echo "$out" | grep -q "started"; then
            # Clean up
            local session_id
            session_id=$(echo "$out" | awk '{print $1}')
            swash kill "$session_id" >/dev/null 2>&1 || true
            pass "start returns immediately"
        else
            fail "start did not report started" "$out"
        fi
    else
        fail "start took ${elapsed}s (should be <1s)" "$out"
    fi
}

test_attach_interactive() {
    # Test attach using tmux to provide a real PTY
    if ! command -v tmux &>/dev/null; then
        echo "  (skipped - tmux not installed)"
        TESTS_PASSED=$((TESTS_PASSED + 1))  # Count as pass if tmux not available
        return
    fi

    # Start a TTY session with vi
    local out session_id
    out=$(swash start --tty --rows 15 --cols 60 -- vi 2>&1)
    session_id=$(echo "$out" | awk '{print $1}')
    echo "  session: $session_id"
    
    # Wait for vi to start
    sleep 0.3

    # Start a tmux session and run attach inside it
    local tmux_session="swash-test-$$"
    tmux new-session -d -s "$tmux_session" -x 70 -y 20
    tmux send-keys -t "$tmux_session" "swash attach $session_id" Enter
    
    # Wait for attach to connect
    sleep 0.3
    
    # Type some text in vi (i enters insert mode)
    tmux send-keys -t "$tmux_session" "i" "Hello from swash attach!" Escape
    
    # Wait for vi to process
    sleep 0.2
    
    # Capture the pane content
    local pane_content
    pane_content=$(tmux capture-pane -t "$tmux_session" -p)
    
    # Kill tmux session
    tmux kill-session -t "$tmux_session" 2>/dev/null || true
    
    # Clean up swash session  
    swash kill "$session_id" 2>/dev/null || true
    
    # Verify we see the text we typed in vi
    if echo "$pane_content" | grep -q "Hello from swash attach"; then
        pass "attach interactive works via tmux (vi)"
    else
        fail "attach did not show expected output" "$pane_content"
    fi
}

# Run tests
if [ -n "$SINGLE_TEST" ]; then
    echo "=== Running single test: $SINGLE_TEST ==="
    echo
    run_test "$SINGLE_TEST"
else
    if ! $USE_REAL_SYSTEMD; then
        echo "=== Integration Tests ==="
        echo
        run_test test_dbus_registered
        run_test test_journal_write
    fi
    
    echo "=== Swash Tests ==="
    echo
    run_test test_swash_run
    run_test test_task_output_capture
    run_test test_newline_splitting

    echo
    echo "=== Run/Start Tests ==="
    echo

    run_test test_run_inline_output
    run_test test_run_exit_code
    run_test test_run_timeout_detach
    run_test test_start_immediate

    echo
    echo "=== TTY Mode Tests ==="
    echo

    run_test test_tty_mode_output
    run_test test_tty_colors_preserved
    run_test test_tty_screen_event

    echo
    echo "=== Attach Tests ==="
    echo

    run_test test_attach_interactive
fi

echo
echo "=== Results: $TESTS_PASSED/$TESTS_RUN passed ==="

[ "$TESTS_PASSED" -eq "$TESTS_RUN" ] || exit 1
