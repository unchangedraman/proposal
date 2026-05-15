#!/usr/bin/env bash
# Drive a real QEMU instance through its QMP socket end-to-end.
# Pre-application exercise for urunc issue #112 (LFX 2026 Term 2).
#
# Boots qemu-system-x86_64 paused (-S), with QMP exposed on a Unix socket.
# Runs the qmp-probe Go client against it, then prints the resulting
# transcript. QEMU shuts itself down when the probe sends "quit".

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOCK="/tmp/lfx-qmp-probe.sock"
PIDFILE="/tmp/lfx-qmp-probe.qemu.pid"
LOG="/tmp/lfx-qmp-probe.qemu.log"

cleanup() {
    if [[ -f "$PIDFILE" ]]; then
        local pid
        pid=$(cat "$PIDFILE" 2>/dev/null || true)
        [[ -n "$pid" ]] && kill -9 "$pid" 2>/dev/null || true
        rm -f "$PIDFILE"
    fi
    rm -f "$SOCK"
}
trap cleanup EXIT

cd "$HERE"

echo "==> building qmp-probe..."
go build -o ./qmp-probe ./main.go

# Clean state from any prior run.
cleanup

echo "==> starting qemu (paused, no guest disk) with -qmp on $SOCK ..."
# -S means start with the CPU stopped; we'll send 'cont' from the probe.
# -nodefaults removes all the implicit devices we don't need for the protocol test.
# -no-shutdown means QEMU doesn't auto-quit on guest panic; we control termination via QMP.
qemu-system-x86_64 \
    -machine type=q35,accel=tcg \
    -m 64M \
    -smp 2 \
    -nographic \
    -nodefaults \
    -no-shutdown \
    -S \
    -qmp "unix:${SOCK},server,nowait" \
    -daemonize \
    -pidfile "$PIDFILE" \
    -serial "file:${LOG}"

# Wait for QEMU to start listening.
for _ in {1..50}; do
    [[ -S "$SOCK" ]] && break
    sleep 0.05
done

if [[ ! -S "$SOCK" ]]; then
    echo "ERROR: QEMU did not create the QMP socket at $SOCK" >&2
    exit 1
fi

echo "==> running qmp-probe against $SOCK ..."
echo
./qmp-probe "$SOCK"
echo
echo "==> done. QEMU has been told to quit via QMP."
