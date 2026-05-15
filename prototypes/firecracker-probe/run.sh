#!/usr/bin/env bash
# Drive a real Firecracker microVM through its API socket end-to-end.
# Pre-application exercise for urunc issue #112 (LFX 2026 Term 2).
#
# Starts firecracker with --api-sock, runs the fc-probe Go client against
# the socket through the full lifecycle (configure -> boot -> pause/resume
# -> Ctrl-Alt-Del), then cleans up.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOCK="/tmp/lfx-fc-probe.sock"
FC_BIN="${FC_BIN:-/tmp/firecracker}"
KERNEL="${KERNEL:-/tmp/vmlinux.bin}"
ROOTFS="${ROOTFS:-/tmp/rootfs.ext4}"
LOG="/tmp/lfx-fc-probe.fc.log"
FC_PID=""

cleanup() {
    if [[ -n "${FC_PID}" ]]; then
        kill -9 "${FC_PID}" 2>/dev/null || true
    fi
    rm -f "$SOCK"
}
trap cleanup EXIT

cd "$HERE"

for f in "$FC_BIN" "$KERNEL" "$ROOTFS"; do
    if [[ ! -f "$f" ]]; then
        echo "ERROR: required file not found: $f" >&2
        exit 1
    fi
done

echo "==> building fc-probe..."
go build -o ./fc-probe ./main.go

# Clean any prior state.
rm -f "$SOCK"

echo "==> starting firecracker (api-sock=${SOCK})..."
"$FC_BIN" --api-sock "$SOCK" --id "lfx-probe" > "$LOG" 2>&1 &
FC_PID=$!

# Wait for the API socket to appear.
for _ in {1..40}; do
    [[ -S "$SOCK" ]] && break
    sleep 0.05
done

if [[ ! -S "$SOCK" ]]; then
    echo "ERROR: firecracker did not create $SOCK" >&2
    cat "$LOG" >&2
    exit 1
fi

echo "==> running fc-probe..."
echo
./fc-probe "$SOCK" "$KERNEL" "$ROOTFS"

# Give the guest a moment to honor the Ctrl-Alt-Del before we tear down.
sleep 1

echo
echo "==> done. firecracker process state:"
if kill -0 "$FC_PID" 2>/dev/null; then
    echo "    still running (guest may not have honored Ctrl-Alt-Del — that's a config-of-the-guest concern, not the API)"
else
    echo "    exited cleanly"
fi

echo
echo "==> firecracker log (tail):"
tail -25 "$LOG" 2>/dev/null || true
