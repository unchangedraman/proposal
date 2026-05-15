#!/usr/bin/env bash
# Run Approach D end-to-end against a REAL guest:
#   QEMU starts paused with the cloud image attached and QMP socket exposed.
#   qmp-boot-test connects, sends cont, lets the guest start booting, then
#   exercises pause / resume / introspection / hotplug / quit on the live guest.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOCK="/tmp/lfx-d-test.sock"
PIDFILE="/tmp/lfx-d-test.qemu.pid"
GUEST_LOG="/tmp/lfx-d-test.guest.log"
DISK="${DISK:-/tmp/lfx-d-test.qcow2}"

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

if [[ ! -f "$DISK" ]]; then
    echo "ERROR: cloud disk not found at $DISK" >&2
    echo "       (expected the jammy cloud image copy)" >&2
    exit 1
fi

echo "==> building qmp-boot-test..."
go build -o ./qmp-boot-test ./main.go

cleanup
> "$GUEST_LOG"

echo "==> starting QEMU paused with cloud image attached + QMP socket on $SOCK"
qemu-system-x86_64 \
    -machine type=q35,accel=kvm \
    -cpu host \
    -m 1024 \
    -smp 1 \
    -drive "file=${DISK},if=virtio,format=qcow2" \
    -netdev user,id=net0 \
    -device virtio-net-pci,netdev=net0 \
    -device pcie-root-port,id=rp1,chassis=1,slot=1 \
    -nographic \
    -nodefaults \
    -serial "file:${GUEST_LOG}" \
    -no-shutdown \
    -S \
    -qmp "unix:${SOCK},server,nowait" \
    -daemonize \
    -pidfile "$PIDFILE"

for _ in {1..50}; do
    [[ -S "$SOCK" ]] && break
    sleep 0.05
done

if [[ ! -S "$SOCK" ]]; then
    echo "ERROR: QEMU did not create $SOCK" >&2
    exit 1
fi

echo "==> driving Approach D wire flow against the live guest"
echo
./qmp-boot-test "$SOCK"

echo
echo "==> guest serial log (first 25 lines after boot):"
head -25 "$GUEST_LOG" 2>/dev/null || true
