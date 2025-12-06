#!/bin/bash
# Integration tests for swash with mini-systemd
#
# Prerequisites: binaries built via `make bin/swash bin/mini-systemd`

set -e

cd "$(dirname "$0")/.."
export PATH="$PWD/bin:$PATH"

# Check binaries exist
for bin in swash mini-systemd; do
    if ! command -v $bin &>/dev/null; then
        echo "ERROR: $bin not found in PATH. Run 'make' first." >&2
        exit 1
    fi
done

# Setup temp directory and cleanup trap
TMPDIR=$(mktemp -d)
cleanup() {
    [ -n "$MS_PID" ] && kill $MS_PID 2>/dev/null || true
    [ -n "$DBUS_PID" ] && kill $DBUS_PID 2>/dev/null || true
    rm -rf "$TMPDIR"
}
trap cleanup EXIT

JOURNAL_DIR="$TMPDIR/journal"
mkdir -p "$JOURNAL_DIR"

# Start dbus-daemon
export DBUS_SESSION_BUS_ADDRESS="unix:path=$TMPDIR/bus.sock"
dbus-daemon --session --nofork --address="$DBUS_SESSION_BUS_ADDRESS" &
DBUS_PID=$!

# Wait for socket
for i in $(seq 1 50); do
    [ -S "$TMPDIR/bus.sock" ] && break
    sleep 0.01
done

# Start mini-systemd
mini-systemd --journal-dir="$JOURNAL_DIR" &
MS_PID=$!
sleep 0.5  # wait for registration

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
    "$@"
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
    # Write via D-Bus
    dbus-send --print-reply \
        --dest=org.freedesktop.systemd1 \
        /org/freedesktop/systemd1 \
        sh.swa.MiniSystemd.Journal.Send \
        "string:Test message from shell" \
        "dict:string:string:TEST_KEY,test_value" >/dev/null

    # Read with journalctl
    local out
    out=$(journalctl --file="$JOURNAL_DIR"/*.journal -o short 2>&1) || true
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
    # This test verifies task output appears in mini-systemd's journal
    # Known issue: swash host uses journal.Send() which writes to real journald socket
    local out session_id
    out=$(swash run echo "UNIQUE_OUTPUT_12345" 2>&1)
    session_id=$(echo "$out" | awk '{print $1}')

    sleep 1  # wait for output capture

    local journal_out
    journal_out=$(dbus-send --print-reply \
        --dest=org.freedesktop.systemd1 \
        /org/freedesktop/systemd1 \
        sh.swa.MiniSystemd.Journal.GetEntries 2>&1) || true

    if echo "$journal_out" | grep -q "UNIQUE_OUTPUT_12345"; then
        pass "task output captured in journal"
    else
        fail "task output not captured (known issue: swash uses real journald socket)"
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

    swash run "$script" >/dev/null 2>&1
    sleep 1

    local journal_out
    journal_out=$(dbus-send --print-reply \
        --dest=org.freedesktop.systemd1 \
        /org/freedesktop/systemd1 \
        sh.swa.MiniSystemd.Journal.GetEntries 2>&1) || true

    local found=0
    for line in LINE_ONE LINE_TWO LINE_THREE; do
        if echo "$journal_out" | grep -q "$line"; then
            ((found++))
        fi
    done

    if [ "$found" -eq 3 ]; then
        pass "newline splitting works"
    else
        fail "newline splitting: found $found/3 lines (known issue: output capture)"
    fi
}

# Run tests
echo "=== Integration Tests ==="
echo

run_test test_dbus_registered
run_test test_journal_write
run_test test_swash_run
run_test test_task_output_capture
run_test test_newline_splitting

echo
echo "=== Results: $TESTS_PASSED/$TESTS_RUN passed ==="

[ "$TESTS_PASSED" -eq "$TESTS_RUN" ] || exit 1
