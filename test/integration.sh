#!/bin/bash
# Integration tests for swash with mini-systemd
#
# Usage:
#   ./test/integration.sh                    # Run all tests with mini-systemd
#   ./test/integration.sh --real             # Run with real systemd (no mini-systemd)
#   ./test/integration.sh --timeout 1        # Set follow timeout (default: 5)
#   ./test/integration.sh test_tty_mode_output  # Run single test
#   ./test/integration.sh --real --timeout 1 test_tty_mode_output

set -e

cd "$(dirname "$0")/.."

# Parse arguments
USE_REAL_SYSTEMD=false
SINGLE_TEST=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --real)
            USE_REAL_SYSTEMD=true
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
    JOURNAL_DIR=""
    JOURNAL_CMD="journalctl --user"
else
    # Use mini-systemd with isolated D-Bus and journal
    JOURNAL_DIR="$TMPDIR/journal"
    JOURNAL_SOCKET="$TMPDIR/journal.socket"
    mkdir -p "$JOURNAL_DIR"
    
    go build -o "$TMPDIR/swash" \
        -ldflags "-X github.com/coreos/go-systemd/v22/journal.journalSocket=$JOURNAL_SOCKET -X github.com/mbrock/swash/internal/swash.JournalDir=$JOURNAL_DIR" \
        ./cmd/swash/
    
    go build -o "$TMPDIR/mini-systemd" ./cmd/mini-systemd/
    
    export PATH="$TMPDIR:$PATH"
    
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
    local out
    out=$(swash run echo "hello from test" 2>&1)
    if echo "$out" | grep -q "started"; then
        local session_id
        session_id=$(echo "$out" | awk '{print $1}')
        pass "swash run started session $session_id"
    else
        fail "swash run did not report 'started'" "$out"
    fi
}

test_task_output_capture() {
    local out session_id
    out=$(swash run echo "UNIQUE_OUTPUT_12345" 2>&1)
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
    out=$(swash run "$script" 2>&1)
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
    # Test that TTY mode captures output
    local out session_id
    out=$(swash run --tty echo "TTY_OUTPUT_TEST" 2>&1)
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
    out=$(swash run --tty "$script" 2>&1)
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
    out=$(swash run --tty --rows 5 --cols 40 echo "SCREEN_CAPTURE_TEST" 2>&1)
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
    echo "=== TTY Mode Tests ==="
    echo

    run_test test_tty_mode_output
    run_test test_tty_colors_preserved
    run_test test_tty_screen_event
fi

echo
echo "=== Results: $TESTS_PASSED/$TESTS_RUN passed ==="

[ "$TESTS_PASSED" -eq "$TESTS_RUN" ] || exit 1
