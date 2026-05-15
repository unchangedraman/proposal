# fc-probe — pre-application exercise for urunc #112 (Firecracker side)

Companion to `../qmp-probe/`. Where that one drove QEMU through QMP, this one
drives **Firecracker** through its HTTP-over-Unix-socket API end-to-end —
configuration, boot, pause/resume, graceful shutdown — using only the
`--api-sock`, no CLI arguments for the guest at all.

Booted: Ubuntu 22.04 rootfs on Linux 5.10.223, 1 vCPU, 128 MiB.
Firecracker version: v1.13.1.

## How to run

```bash
# expects /tmp/firecracker, /tmp/vmlinux.bin, /tmp/rootfs.ext4
./run.sh
```

The transcript is captured in `run.transcript.txt`. Key bytes:

```
[t+  0.00ms] -> GET /
[t+  1.09ms] <- 200 OK   {state: Not started, vmm_version: 1.13.1}
[t+  1.21ms] -> PUT /machine-config {vcpu_count: 1, mem_size_mib: 128}
[t+  1.65ms] <- 204 No Content
[t+  1.68ms] -> PUT /boot-source {kernel_image_path: ..., boot_args: ...}
[t+  1.99ms] <- 204 No Content
[t+  2.02ms] -> PUT /drives/rootfs {drive_id: rootfs, path_on_host: ..., is_root_device: true, is_read_only: true}
[t+  2.38ms] <- 204 No Content
[t+  2.41ms] -> PUT /actions {action_type: InstanceStart}
[t+ 18.43ms] <- 204 No Content                         ← 16 ms boot
[t+819.48ms] -> GET /
[t+819.92ms] <- 200 OK   {state: Running}
[t+819.93ms] -> PATCH /vm {state: Paused}
[t+820.12ms] <- 204 No Content
[t+820.12ms] -> PATCH /vm {state: Resumed}
[t+820.44ms] <- 204 No Content
[t+820.46ms] -> PUT /actions {action_type: SendCtrlAltDel}
[t+820.55ms] <- 204 No Content

[firecracker log] OK Reached target System Reboot.
[firecracker log] reboot: Restarting system
[firecracker log] Vmm is stopping.
[firecracker log] Firecracker exiting successfully. exit_code=0
```

## Lessons that this exercise actually taught me

### 1. Firecracker has no command-line equivalent for guest config

Unlike QEMU (which urunc currently invokes with `-kernel ... -drive ... -netdev ...`),
Firecracker v1 has no `--kernel`, no `--drive`, no `--netdev`. The flags Firecracker
accepts on the command line are:

- `--api-sock` — the control socket (this whole thing's reason for existing)
- `--id`       — VM ID for logging
- `--config-file` — a JSON file equivalent to a sequence of API calls
- `--no-api`   — uses `--config-file` only, then runs

That's the API surface. **"Background mode" (slide deck term) is the only mode
Firecracker actually supports** — there is no exec-with-everything-on-the-cmdline
path. urunc's current Firecracker integration must already be using `--config-file`
or making API calls; the LFX project formalises this by giving urunc its own
in-process client.

This is a sharp contrast with QEMU, which supports both styles and uses exec mode
by default in urunc today. Whatever abstraction the LFX project lands has to
accommodate both extremes.

### 2. All state-changing endpoints return 204 No Content

| Endpoint | Method | Reply |
| --- | --- | --- |
| `/` | GET | 200 + JSON `{id, state, vmm_version, app_name}` |
| `/machine-config` | PUT | 204 |
| `/boot-source` | PUT | 204 |
| `/drives/{id}` | PUT | 204 |
| `/drives/{id}` | PATCH | 204 |
| `/vm` | PATCH | 204 |
| `/actions` | PUT | 204 |
| `/snapshot/create` | PUT | 204 |

The Go client cannot expect a response body for any mutation. State has to be
read back via `GET /`. This means urunc's FC-backed `MonitorControl` must
follow `Pause()` / `Resume()` / etc. with a `QueryStatus()` if the caller
needs confirmation — or trust the 204.

### 3. No async events on the API socket

QMP pushes events (`RESUME`, `STOP`, `SHUTDOWN`, `BLOCK_IO_ERROR`) on the
same socket as command replies. Firecracker does not push anything. To
detect a guest exit, you either:

- Poll `GET /` and watch the state field, or
- Observe the firecracker process exiting (it does, cleanly, after the
  guest halts — see exit_code=0 in the transcript), or
- Read stderr / stdout / log file (this is where the kernel messages and
  systemd output landed in our run).

For urunc this means the `MonitorControl` abstraction cannot assume
events. The interface should either expose an `Events() <-chan Event` that
QMP fills and FC leaves empty, or hide the asymmetry behind capability
flags (the `SupportsSharedfs` shape urunc already uses).

### 4. Pause/Resume lives on `/vm` (PATCH), not `/actions`

FC's REST modelling is slightly more REST-ful than QMP's flat command list:
- "things you do to the VM" → `PUT /actions` with an enum (`InstanceStart`,
  `SendCtrlAltDel`, `FlushMetrics`, ...)
- "state transitions of the VM" → `PATCH /vm` with `{state: Paused | Resumed}`
- "configuration of resources" → `PUT /drives/{id}`, `/network-interfaces/{id}`, etc.

The QMP equivalents are flat: `cont`, `stop`, `device_add`, `device_del`,
all are `execute` commands. urunc's `MonitorControl` abstracts over both
shapes; the per-backend implementations translate.

### 5. No `device_add` equivalent for net/block

After `InstanceStart`, FC supports updating existing drives
(`PATCH /drives/{id}` with a new `path_on_host`) and updating network rate
limiters, but **does not let you add a brand-new virtio device that wasn't
configured before boot.** Hot *plug*, not hot *add*. QMP supports both
(via `device_add` plus `chardev-add` etc.).

This is exactly why urunc's interface needs capability flags. The proposal's
§3.2 (Firecracker SDK call mapping) calls this out; the prototype confirms
it empirically.

### 6. The 16 ms boot — and what it means for parallel init

The single most expensive call is `PUT /actions {InstanceStart}`: 16 ms vs.
0.3–0.4 ms for each prior PUT. That is the actual KVM boot, kernel start,
init's first runqueue.

The slide deck for #112 wants to use the create/start gap to overlap this
16 ms with urunc's own namespace setup. The fact that FC's API surface is
inherently background-mode makes this trivial on the FC side: urunc can
`PUT` everything except `InstanceStart` during the gap, then `PUT InstanceStart`
when `urunc start` arrives. On the QEMU side, the equivalent is `-S` plus
QMP `cont`, demonstrated in the QMP probe.

## What this exercise does not yet cover

- **Snapshots.** FC's `PUT /snapshot/create` and `/snapshot/load` are how
  microVMs go faster than 16 ms boot. Out of scope for #112 directly but
  the interface should not preclude them.
- **Network configuration end-to-end.** I configured drives and boot source
  but not a tap device, because the local box's libvirt was using the
  default bridge and I did not want to plumb a tap just for the probe. urunc
  already has tap-device plumbing — the FC integration in this LFX project
  inherits it.
- **MMDS (the FC microvm metadata service).** Useful for urunit-style
  bootstrapping, but tangential to the monitor lifecycle question.
- **Error handling.** I check 4xx status and bail; a real client needs to
  parse FC's `fault_message` JSON for diagnostics. The Go SDK
  (`firecracker-go-sdk`) handles this.
