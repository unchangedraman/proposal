# qmp-probe — pre-application exercise for urunc #112

A small Go client that drives a real QEMU instance through its QEMU Machine
Protocol (QMP) Unix socket end-to-end. Written for the LFX 2026 Term 2
project *"Improve lifecycle management of sandbox monitors"* (upstream issue
[urunc-dev/urunc#112](https://github.com/urunc-dev/urunc/issues/112)).

The point of the exercise — per cmainas's [pre-application requirement
in #112](https://github.com/urunc-dev/urunc/issues/112#issuecomment-2849xxxxx) —
was *"choose one of the supported monitors in urunc and create a VM configuring
it through the monitor's API."* This is QEMU/QMP. The Firecracker counterpart
lives in `../firecracker-probe/`.

## How to run

```bash
./run.sh
```

That script:
1. Builds `qmp-probe` from `main.go`.
2. Starts `qemu-system-x86_64` paused (`-S`), with QMP on
   `/tmp/lfx-qmp-probe.sock`, daemonized, no real guest needed (`-nodefaults`).
3. Runs the probe against the socket.
4. QEMU shuts itself down when the probe sends `quit`.

The captured transcript lives in `run.transcript.txt`. Sample run, on a
QEMU 8.2.2 / Linux x86_64 box:

```
[t+  0.14ms] connected to /tmp/lfx-qmp-probe.sock
[t+  0.53ms] <- greeting        {QMP: {capabilities: [oob], version: 8.2.2}}
[t+  0.56ms] -> qmp_capabilities
[t+  0.94ms] <- qmp_capabilities {return: {}}
[t+  0.95ms] -> query-status
[t+  1.15ms] <- query-status     {status: prelaunch, running: false}
[t+  1.15ms] -> cont
[t+  1.32ms] <- (async) RESUME
[t+  1.33ms] <- cont             {return: {}}
[t+  1.33ms] -> query-status
[t+  1.48ms] <- query-status     {status: running,   running: true}
[t+  1.48ms] -> query-cpus-fast
[t+  1.71ms] <- query-cpus-fast  [{thread-id: ..., cpu-index: 0, ...}, {...}]
[t+  1.71ms] -> stop
[t+  1.87ms] <- (async) STOP
[t+  1.88ms] <- stop             {return: {}}
[t+  1.88ms] -> quit
[t+  2.02ms] <- (async) SHUTDOWN
[t+  2.03ms] <- quit             {return: {}}
[t+  2.04ms] probe complete.
```

Total wall time for greeting + handshake + 6 commands + their async events,
on a Unix socket on the same host: **~2 ms**.

## Lessons that this exercise actually taught me

Six things I had not internalized before running real bytes through real QEMU:

### 1. The greeting frame is mandatory to consume

QEMU sends one JSON frame on connect, before any client command, containing
its version and the QMP capabilities it advertises:

```json
{"QMP": {"capabilities": ["oob"], "version": {...}}}
```

A client that writes a command before reading the greeting wedges; QEMU
buffers the greeting and the client buffers its command, and the first read
from either side gets the wrong frame.

### 2. `qmp_capabilities` handshake gate

QEMU rejects every command except `qmp_capabilities` until it has been sent.
The handshake reply itself is `{"return": {}}` — there is no payload to
parse, just an acknowledgement.

### 3. Async events interleave with command replies on the same socket

**This was the actual rough edge.** State-changing commands cause QEMU to
emit async events (`RESUME`, `STOP`, `SHUTDOWN`, `BLOCK_IO_ERROR`, etc.) on
the same socket as command replies, in arrival order. My first version of
this probe was naive: one read per command. It worked through the greeting,
the handshake, and `query-status` — and then `cont` triggered a `RESUME`
event that landed *before* the cont reply, every read after that got
off-by-one, and `query-cpus-fast`'s reply turned up where I was expecting
`stop`'s reply (transcript of the broken run is in git history).

The fix is to type-dispatch every incoming frame:

| Frame shape | Meaning |
| --- | --- |
| `{"QMP": ...}` | one-shot greeting on connect |
| `{"return": <anything>}` | reply to the most recent in-flight command |
| `{"error": {"class": ..., "desc": ...}}` | command failed |
| `{"event": "<NAME>", "timestamp": {...}, "data": {...}}` | async event — *not* a reply |

A correct client reads in a loop and only treats `return`/`error` frames as
replies to the outstanding command; events are dispatched to a separate
subscriber. For urunc this matters specifically because graceful shutdown
relies on `quit` being observed correctly:

```
client: quit
server: {"event": "SHUTDOWN", "timestamp": ...}  ← arrives first
server: {"return": {}}                            ← arrives second
```

If we trust the first frame as the reply, we close the socket too early and
miss real errors on some other paths.

### 4. Reply payload schema varies per command

A single Go struct cannot statically type the Return field:

| Command | Return shape |
| --- | --- |
| `qmp_capabilities` | `{}` |
| `query-status` | object: `{status, running, singlestep}` |
| `query-cpus-fast` | array: `[{thread-id, props, ...}, ...]` |
| `cont`, `stop`, `quit` | `{}` |
| `device_add`, `device_del` | `{}` on success, structured error on failure |

The probe uses `json.RawMessage` for `Return` and lets each caller parse
per-command. urunc's `MonitorControl` interface needs to do the same — a
sibling type per QMP command, parsed by the implementation, exposed at the
interface as urunc's native types.

### 5. `-qmp unix:<path>,server,nowait` is the right flag combo

- `unix:<path>` — Unix-domain socket, the natural choice for a single-host
  container runtime. (TCP is also supported but exposing a hypervisor
  control plane over the network is exactly what cmainas's #10 comment
  cautioned against.)
- `server` — QEMU acts as the listener; the client (urunc) connects.
- `nowait` — QEMU does not block on `accept()` before starting. Important:
  it means QEMU is fully booted and accepting client connections in
  parallel with the rest of its own initialization.

This is the flag combo that makes the slides' "parallel VMM init during the
create-start gap" idea realizable: QEMU starts and runs setup while urunc
is still doing namespace work, and only blocks on `cont` when urunc tells
it to start the guest CPU.

### 6. Timings are not the bottleneck

End-to-end, this entire transcript runs in ~2 ms. The cost of a
socket-based control plane is in the architecture and the failure modes,
not in latency. The relevant overheads for urunc are:

- One-time socket-open per container (sub-millisecond on Unix sockets).
- Per-command round-trip (~0.2–0.4 ms on the same host).
- The async-event subscriber goroutine (one per container, sleeping on a
  socket read most of the time).

None of these are concerning at urunc's expected fleet density. A regression
test that asserts "monitor control round-trip is under 5 ms" would catch
real degradation without false alarms.

## What this exercise does not yet cover

These are explicit gaps; the actual LFX project would close them.

- **Hotplug end-to-end.** `device_add` / `device_del` for virtio-net and
  virtio-blk. Requires booting a real guest kernel to verify the hotplug
  is visible inside.
- **Console attach via `chardev-add`.** Closes the gap from
  [urunc#389](https://github.com/urunc-dev/urunc/issues/389) — instead of
  always-on stdio, the console is attached on demand.
- **The reexec / `urunc start` sequencing.** The QMP socket has to be
  ready *before* the reexec process tries to talk to it, which is the
  IPC-ordering question @namansh70747 raised in
  [#112 comment 5](https://github.com/urunc-dev/urunc/issues/112#issuecomment-2860706488).
- **Error-domain modelling.** `quit` reply vs `SHUTDOWN` event vs the socket
  closing on monitor death — three signals, three reasons, three meanings.
  Distinguishing them is the same engineering-discipline shape as the
  namma-cli wipe-disk SSH-error-handling find described in the main proposal.
