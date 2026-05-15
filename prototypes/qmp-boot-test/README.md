# qmp-boot-test — Approach D end-to-end on a real Linux guest

Third prototype in the series. Where `qmp-probe/` validated the QMP wire on a
no-guest paused QEMU, this one validates the **full Approach D flow** on a
real Ubuntu guest: QEMU starts paused with `-S -qmp ...`, the client sends
`cont` to begin guest execution (the moment that replaces urunc's current
`syscall.Exec` → immediate-run), the guest actually boots, and then we
exercise pause / resume / introspection / hotplug / quit on a *running*
guest. End-to-end, on the wire urunc would use, against a kernel that
actually runs.

## How to run

Requires a copy of the Ubuntu 22.04 cloud image at `/tmp/lfx-d-test.qcow2`.
The script reads `DISK` env var if you want to point it elsewhere.

```bash
./run.sh
```

Sample transcript (excerpted; full run.transcript.txt is checked in):

```
[t+   0.07ms] connected to /tmp/lfx-d-test.sock
[t+   0.22ms] <- greeting: QEMU 8.2.2, capabilities [oob]
[t+   0.40ms] <- qmp_capabilities         {return: {}}
[t+   0.52ms] <- query-status (pre-cont)  {status: prelaunch, running: false}
[t+   0.53ms] -> cont   ← THE moment that replaces urunc's syscall.Exec → immediate run
[t+   0.62ms] <- (event) RESUME
[t+   0.64ms] <- cont                     {return: {}}
... 5 second wait while Ubuntu boots ...
[t+5006.11ms] <- query-status (post-cont) {status: running, running: true}
[t+5005.41ms] <- stop                     {return: {}}    ← pause works on live guest
[t+5005.60ms] <- query-status (paused)    {status: paused}
[t+5006.03ms] <- cont (resume)            {return: {}}    ← resume works on live guest
[t+5006.40ms] <- query-block              [{virtio0, /tmp/lfx-d-test.qcow2, 2.2 GiB}]
[t+5006.55ms] <- query-cpus-fast          [{thread-id: 950468, cpu-index: 0}]
[t+5007.91ms] <- netdev_add               {return: {}}
[t+5018.68ms] <- device_add (on rp1)      {return: {}}    ← 11ms — actual PCI enumeration
[t+5018.97ms] <- query-pci                [..., {qdev_id: "hotnet0-dev", bus: 1, slot: 0}]
[t+5019.22ms] <- device_del               {return: {}}
[t+5019.33ms] <- (event) SHUTDOWN
[t+5019.66ms] <- quit                     {return: {}}    ← graceful shutdown
```

Guest serial log confirms a real boot:

```
SeaBIOS (version 1.16.3-debian-1.16.3-2)
iPXE (https://ipxe.org) 00:01.0 C000 PCI2.10 PnP PMM+3EFCABA0+3EF0ABA0 C000
Booting from Hard Disk...
[    0.000000] Linux version 5.15.0-171-generic ... (Ubuntu 5.15.0-171.181)
[    0.000000] Command line: BOOT_IMAGE=/boot/vmlinuz-5.15.0-171-generic root=LABEL=cloudimg-rootfs ro console=tty1 console=ttyS0
```

## Two findings that matter for the proposal

### 1. q35's default `pcie.0` does not support hotplug

The first run of this test had `device_add` failing with:

```
GenericError: Bus 'pcie.0' does not support hotplugging
```

QEMU's q35 machine type — the default on modern QEMU for x86_64-system —
exposes the root PCIe bus as `pcie.0`, which is *not* hotplug-capable. To
hotplug PCIe devices, the QEMU command line has to pre-allocate one or
more `pcie-root-port` devices at startup:

```
-device pcie-root-port,id=rp1,chassis=1,slot=1
```

Then `device_add` targets that root port: `device_add ... bus=rp1`.

urunc's current `pkg/unikontainers/hypervisors/qemu.go:BuildExecCmd` does
not specify a machine type and does not allocate any root ports. The naive
Approach D — just add `-S -qmp` — therefore *cannot* hotplug anything. The
fix is a urunc-side change: `BuildExecCmd` adds a configurable number of
root ports (one or two for the common case; more if a future urunc
annotation requests them). This needs to land in the same change set as
the QMP integration.

### 2. Real PCI hotplug costs ~11 ms

Once the root port is in place, `device_add` for a virtio-net-pci behind
`rp1` takes about 11 ms. That is real hardware enumeration work — the guest
kernel's PCI subsystem receives the hotplug event, probes the new device,
attaches the virtio-net driver, brings up the network interface. It is the
same order of magnitude as Firecracker's 16 ms `PUT /actions {InstanceStart}`
boot cost (see `../firecracker-probe/`): the per-operation wire round-trip
is sub-millisecond, but the *underlying work* is in the milliseconds.

The implication for urunc: a `MonitorControl.HotplugNet()` method's
latency budget should be tens of milliseconds, not single-digit. Useful
for the bench-console.sh-style CI assertions the proposal mentions.

## What this prototype does NOT cover

- **Guest-side verification of the hotplug.** I confirmed the device appears
  in QEMU's PCI topology (`query-pci`) but did not log into the guest to
  confirm Linux's `lspci` sees the new NIC and `ip link` brings it up.
  That would need cloud-init password injection or SSH key plumbing; out
  of scope for a wire-protocol verification.
- **Firecracker side of Approach D.** Already covered by
  `../firecracker-probe/` — that one is API-only by design and the timing
  inversion is implicit (FC has no exec mode).
- **The urunc-side flow change.** This prototype validates the wire
  behaviour. The flow change (moving the `execve` earlier in reexec,
  removing the urunc IPC handshake, teaching `urunc start` to talk to the
  monitor socket) is a code change in urunc itself, which would land in
  the LFX project's weeks 4–5.
