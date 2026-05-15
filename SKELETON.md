# SKELETON — for the rewrite of the LFX 2026 Term 2 urunc proposal

> **You write every paragraph.** This file is a structural guide and a list of
> facts to weave in. The current `cover-letter.pdf` is heavily my drafting and
> needs replacement — cmainas's policy in [urunc#112](https://github.com/urunc-dev/urunc/issues/112#issuecomment-2849xxxxx)
> is explicit: *"Feel free to use LLMs to refine the proposal, but do not use
> LLMs to write the entire proposal. We are interested about the mentee's
> ideas, not for a generated project plan."*
>
> Rewrite in your voice. Drop sentences that sound polished but generic.
> Keep the file/line/PR citations and the concrete numbers; those are facts.
> Drop my flourishes.
>
> Suggested home for the rewrite: replace the prose inside `cover-letter.html`,
> re-render to `cover-letter.pdf` with `weasyprint cover-letter.html cover-letter.pdf`.

---

## Header / framing

- Title: **Project Proposal — LFX Mentorship 2026 Term 2 — Improve lifecycle management of sandbox monitors (urunc#112)**
- Your name, contact, GitHub: `unchangedraman@gmail.com`, `github.com/unchangedraman`, IIITM Gwalior, IST.
- To: `@cmainas` & `@ananos`, Nubificus.
- Link to the proposal repo: `github.com/unchangedraman/proposal` — make the link to `prototypes/` very prominent.
- Date.

---

## §1 · About me

**One paragraph. Your voice.**

Facts to use, in roughly this order:
- B.Tech Information Technology, ABV-IIITM Gwalior, class of 2026.
- Working at Juspay — India's largest payments orchestration platform.
- Most recent landed work: led the CEPH → Lightbits NVMe/TCP block-storage migration. 48K TPS vs 37K TPS. 4 nodes vs 40. Active-active Barbican HA on a shared backend.
- Current Juspay work: `wipe-disk` command on `namma-cli`, our Rust CLI that drives bare-metal hosts in our datacenter through MAAS. Secure-erase via NVMe Sanitize / hdparm.
- One sentence on *why* this LFX project specifically — the kernel-from-scratch thing is what got you into systems, urunc is the userspace side of the same world. Use your own framing.

**Things to avoid:**
- "I am passionate about..."
- "I am excited to apply for..."
- Three-bullet lists (rule of three is an LLM tic). Just sentences.

**TODO:** Write 4-6 sentences in your voice.

---

## §2 · Pre-application exercise summary

**This is the section that proves we did the work cmainas asked for.** It is the
most important new section vs the prior draft. Subsections, in order:

### §2.1 · Reading urunc's source to understand the actual lifecycle

What I read and what I now understand. Bullet list of file:line cites — these
are *facts*, do not change them:

- `cmd/urunc/main.go` — the binary's entrypoint, dispatches subcommands. The `_ "github.com/opencontainers/runc/libcontainer/nsenter"` import on line 33 is what gives urunc the runc-style nsenter handshake.
- `cmd/urunc/create.go:71-280` — the `urunc create` parent process: creates state dir, init socket pair (`newSockPair("init")`), log pipe, forks reexec via `createReexecCmd` (line 282), reads `{stage1_pid, stage2_pid}` JSON from the init pipe, runs `CreateRuntime` OCI hooks, sends `AckReexec` to reexec, **then exits**. There is no persistent urunc process on the host.
- `cmd/urunc/create.go:347-465` — the **reexec process**, running inside the container's namespaces. It waits for `AckReexec`, runs `CreateContainer` hooks, creates a listener on `urunc.sock` (the `FromReexec` socket), **blocks on `AwaitMsg(StartExecve)`** (line 410). This is the gap the slide deck wants to fill with parallel VMM init.
- `cmd/urunc/start.go:38-115` — `urunc start` (separate transient invocation). Sends `StartExecve` to reexec (line 83), waits for `StartSuccess` (line 102), sets running state, runs `Poststart` hooks, exits.
- `pkg/unikontainers/ipc.go:29-44` — the IPC message constants: `ReexecStarted`, `AckReexec`, `StartExecve`, `StartSuccess`, `StartErr`. Two Unix sockets — `urunc.sock` (reexec → urunc start) and `reexec.sock` (urunc start → reexec).
- `pkg/unikontainers/unikontainers.go:205-533` — the `Exec` function called inside reexec once `StartExecve` arrives. Key sequence: `vmm.BuildExecCmd` (507) → `SendMessage(StartSuccess)` (519) → `vmm.PreExec` (525) → **`syscall.Exec(vmm.Path(), execCmd, env)` (533)**. The reexec process is *replaced* by QEMU/Firecracker via execve, same PID.

**Crucial fact to call out:** the PID stored in `state.json` as `containerPid` is the reexec PID, which becomes the monitor's PID after execve. Any architectural change has to respect this OCI semantic.

**TODO:** Write 2-3 paragraphs in your voice. Don't list every file path — pick the most surprising thing you learned and lead with it. Suggested lead: "Until I read create.go I had assumed urunc had a long-running process; it doesn't. Both `urunc create` and `urunc start` are transient. The container's init process is QEMU/Firecracker itself, arrived at via a single `syscall.Exec` inside reexec at `unikontainers.go:533`." That's the kind of sentence that proves you read the code.

### §2.2 · QMP probe end-to-end

What you built. Hard facts:

- **`prototypes/qmp-probe/`** in the repo. A 130-line Go client that drives a real `qemu-system-x86_64 8.2.2` instance through its QMP Unix socket — greeting, `qmp_capabilities`, `query-status` before+after `cont`, `query-cpus-fast`, `stop`, `quit`.
- Full transcript at `prototypes/qmp-probe/run.transcript.txt`. Wall time: **~2 ms** for the whole exchange on a local Unix socket.
- Six lessons documented in `prototypes/qmp-probe/README.md`:
  1. The greeting frame is mandatory to consume.
  2. `qmp_capabilities` handshake gate.
  3. **Async events interleave with command replies on the same socket** — my naive "one read per command" first version got shifted by one frame on `cont`'s `RESUME` event. Caught it, fixed with type-dispatch. **This is the find worth leading with.**
  4. Reply payload schema varies per command (`query-status` returns an object; `query-cpus-fast` returns an array; `quit`/`cont` return `{}`). Use `json.RawMessage` for Return; parse per-command at the caller.
  5. `-qmp unix:<path>,server,nowait` is the right flag combo.
  6. Round-trip latencies are sub-millisecond — the cost is in architecture and failure modes, not latency.

**TODO:** Write 2-3 paragraphs. Lead with the event/reply interleave find — it's the most evidently learned-by-doing detail and the kind of thing the mentors will believe came from a real run. Make sure to say *"I ran this; here is what happened; here is what changed in my client"* — not *"QMP can be tricky."* Mention the prototype lives in `prototypes/qmp-probe/` and invite them to run it.

### §2.3 · Firecracker probe end-to-end

What you built. Hard facts:

- **`prototypes/firecracker-probe/`** in the repo. A 130-line Go client that drives Firecracker v1.13.1 over its HTTP-over-Unix-socket API. `GET /` → `PUT /machine-config` → `PUT /boot-source` → `PUT /drives/rootfs` → `PUT /actions {InstanceStart}` → `GET /` (state == Running) → `PATCH /vm Paused/Resumed` → `PUT /actions {SendCtrlAltDel}`.
- **Booted a real Ubuntu 22.04 microVM** end-to-end (1 vCPU, 128 MiB). Guest reached systemd, then honored Ctrl-Alt-Del, then firecracker exited cleanly (`exit_code=0`).
- Total wall time: ~820 ms (most of it the guest's own boot + shutdown). The single most expensive API call was `PUT /actions {InstanceStart}` at **16 ms** — that's the actual KVM boot. Every config call was 0.3-0.5 ms.
- Six lessons documented in `prototypes/firecracker-probe/README.md`:
  1. **Firecracker has no command-line equivalent for guest config.** `--api-sock` is the only way in. urunc's current FC path must already be using `--config-file` or the API; the LFX project formalises this.
  2. All state-changing endpoints return `204 No Content`. State has to be read back via `GET /`.
  3. **No async events on the API socket.** Unlike QMP. Detect guest exit via polling, process exit, or stderr log.
  4. Pause/Resume is `PATCH /vm`, not `/actions`. Different shape from QMP's `cont/stop`. Same outcome, different REST modelling.
  5. **No `device_add` equivalent.** FC supports `PATCH /drives/{id}` to swap an existing drive's backing path, but not adding a new virtio device. Validates the capability-flag design.
  6. The 16 ms `InstanceStart` is the parallel-init target — `PUT` everything else during the create/start gap, then `PUT InstanceStart` when urunc start arrives.

**TODO:** Write 2 paragraphs. The biggest contrast-with-QMP findings are: (a) FC is API-only by design, (b) no async events, (c) no hot-add. State these as concrete observations from the prototype. Mention the prototype path; if you want to be playful, mention that booting a real Ubuntu took less wall time than re-rendering this PDF.

---

## §3 · Background — why I am a fit

**Existing material from the current cover-letter — keep, but rewrite in your voice.**
Subsections same as current:

### §3.1 · Namma Yatri — open source, shipped, production
- 3 merged PRs in `github.com/nammayatri`.
- Headliner: `nammayatri/nammayatri#13442` — distributed event-driven payout scheduler, ~10K rides/day, T+2 hour latency vs prior weekly batch.
- Scheduler shape: Redis sorted-set keyed on ride-id with fire-time as score, per-ride distributed lock at fire time, idempotency token persisted before payout call. Exactly-once under broker/app restart. O(1) on the booking hot path.
- Follow-ups: `#13452` (state exposed to driver app), `shared-kernel#1108` (PaytmEDC payment integration in Haskell with several review rounds before merge).
- The connect to LFX: closest thing you have to "shipped upstream code in a large unfamiliar codebase with maintainer review." This is the muscle the LFX project needs.

**TODO:** Write 2-3 paragraphs. The scheduler design is technically the most interesting part — call out the exactly-once / hot-path tradeoff specifically.

### §3.2 · Juspay — Lightbits, identity service, namma-cli wipe-disk
- Lightbits NVMe/TCP migration (already in §1, brief here).
- Identity service on OpenStack + OpenBao: identity derived from network position (IMDS-attested), Shamir secret-sharing for root key (m-of-n shards, no single broker holds full root), memory-only mlock'd keys, Raft HA across AZs, Python `osvault` SDK on VM side.
- The error-domain modeling on the identity service is the same shape as what urunc's monitor-control plane needs around socket-lifecycle edge cases.
- namma-cli wipe-disk work: the SSH-exec `Err(_)` arm that treated *all* SSH errors as expected poweroff, silently masking auth-failure and connection-refused on a destructive operation. Caught on local end-to-end test. Proposed a discrimination heuristic — if remote command produced no stdout before disconnect, treat as error.
- The wipe-disk find directly parallels what urunc's `MonitorControl` will need around "QMP socket closed because we asked for `quit`" vs "QMP socket closed because the monitor died." Same shape of error-domain discipline.

**TODO:** Write 2 paragraphs. Lead with whichever of the three pieces (Lightbits / identity / namma-cli) you find most interesting. Don't list all three equally — pick one as the lead and weave the others.

### §3.3 · Other relevant background (brief)
- BREAK OS — i386 kernel from scratch (1 paragraph). The connect-the-dots: the 16550 UART at `0x3F8` is the same hardware register from BREAK OS that I later traced in urunc#389.
- PostgreSQL on Kubernetes / OpenStack — 5-layer nested-virt stack, CAPO + CloudNativePG, 7-second failover, RPO=0 (1 paragraph, brief).

**TODO:** Write 2 short paragraphs. Don't pad — these are supporting, not lead.

### §3.4 · urunc work so far — issue #389
- I picked up #389 after a prior contributor (codesmith25103) chased the wrong root cause (`bufio.Scanner` in `cmd/urunc/log_forward.go`). My own profiling showed that scanner only handles the parent-child JSON pipe during `urunc create`; once `syscall.Exec` runs, the VMM manages stdio directly. The real cost is one layer deeper.
- I built `bench-console.sh` (in `urunc-work/bench/`) — KVM-exit + throughput sampler. ananos has indicated they want to fold it into urunc CI as a regression check (issue thread, comment 17).
- Trace: per-byte PIO traps on the 16550 UART at port `0x3F8`. `-serial stdio` baseline costs ~1.16 M PIO exits per 5-second window; QEMU's `address_space_write` dominates the host flame graph. A simple `virtio-console` swap drops PIO exits to ~0 and lifts throughput from 2,016 → 73,378 lines/sec (~36×). Closes the runc-vs-urunc gap on this workload from ~407× to ~11×.
- Already through one design-discussion round with cmainas. First proposal was capability-intersection (borrowed from urunc's rootfs path); constructively rejected because Linux kernels can be built without `CONFIG_VIRTIO_CONSOLE`. The feedback shape from that round is what I expect from this project's design-doc review.
- Other thread artefacts: roadmap-level awareness of the `com.urunc.unikernel.cmdline` → `kernelArgs` annotation pivot mentioned in comment 13; blog-post invitation from ananos.

**TODO:** Write 1-2 paragraphs. This is where you have the strongest direct evidence with the mentors. The "I picked up where the previous contributor left off and proved the prior diagnosis was wrong" is the single most credibility-loaded fact — lead with it.

---

## §4 · The project — what I would actually do

This is where the proposal currently is weakest because we did not have evidence
before. Now we do. Rewrite based on the verified architecture.

### §4.1 · The problem in container-runtime terms

**cmainas in #112 comment 10 explicitly said to frame this in container-runtime terms, not generic VMM terms.** Lead accordingly.

Facts:
- urunc today: `urunc create` → spawn reexec → reexec waits → `urunc start` unblocks reexec → reexec execves into QEMU/FC.
- After execve, there is no socket-based control. `urunc kill` and `urunc delete` operate on the PID via signals.
- This is *fine* for "start a guest, let it run, kill it on container delete." It is insufficient for: graceful pause/resume, hotplug, querying state, detecting guest exit without polling, attaching a console after the fact.
- The slide deck for #112 (https://docs.google.com/presentation/d/1ZB1oZm9rhRmdkVAt7cHKZXkRHLM-nPvjwsiT7Hg-mjU/) names the parallel-init opportunity: spawn the VMM in background mode during the gap where reexec blocks on `AwaitMsg(StartExecve)`. The slides quote "30-40 ms faster boot" as the target.

**TODO:** Write 2 paragraphs. Don't list QMP/FC features — instead, explain what *container-runtime operations* urunc cannot do today. The audience is a container-runtime maintainer, not a VMM maintainer.

### §4.2 · The architectural question (and my position)

**cmainas explicitly asked applicants to take a position on this in comment 8.** This is the section that distinguishes serious proposals from generic ones.

The question (from `@AyushSriv06` in comment 7, paraphrased):

> Approach A — preserve the current execve model, store the monitor's API socket path, let later urunc commands reconnect to it as needed.
> Approach B — a long-running supervisor / monitor-manager process that starts the monitor and manages it through its API.

My recommended position (you can disagree — but if you do, document why):

- **Approach A.** Preserve `containerPid` = monitor PID for OCI compatibility; do not introduce a supervisor between the runtime and the container process.
- *Where* in the flow the monitor gets spawned matters more than *who* holds the socket. Spawn the monitor in background mode (FC `--api-sock`, QEMU `-S -qmp ...`) during the create-start gap — concretely, after `CreateContainer` hooks in reexec but before `AwaitMsg(StartExecve)`. The monitor is now its own process, the socket is open, urunc has not lost any current invariant.
- When `StartExecve` arrives, urunc connects to the socket and sends "start guest" (`cont` for QEMU, `PUT /actions {InstanceStart}` for FC). This is the moment that replaces the current `syscall.Exec`.
- `containerPid` in `state.json` is updated to the monitor's PID (the FC/QEMU process), not reexec's. Reexec exits after sending `StartSuccess`.
- For lifecycle ops *after* start: `urunc kill` / `urunc delete` / a new `urunc monitor query-status` open a fresh connection to the persisted socket, send the command, close. Each lifecycle op is one round-trip on a Unix socket — cheap.

**Why Approach A wins on container-runtime grounds:**
- Preserves the OCI semantic that the container's init process is the monitor itself, not a supervisor.
- Containerd/the shim already tracks the monitor PID; nothing changes there.
- Failure modes are simpler: if the monitor dies, the PID is gone, the socket is dead, urunc knows. No "supervisor is alive but monitor is dead" zombie state.
- Cleaner than B for snapshot/restore and migration if those ever land (the supervisor would have to be re-attached or recreated; the monitor's state is the only state that matters).

**Why Approach B might win (be honest):**
- Persistent socket connection (no reconnect cost per op). True, but Unix-socket reconnect is sub-millisecond — see the qmp-probe transcript.
- Async event subscription stays alive across urunc invocations. True, but a separate "event sink" process (or the shim) can do this without becoming the container's init.

**TODO:** Write 2-3 paragraphs taking a clear position. If your position differs from mine, write yours with reasoning — that is strictly better. What is *not* OK is to say "I'll decide later." cmainas pushed back on that in comment 10.

### §4.3 · The execution: what changes in code

Concrete changes, file by file. These are *facts* about the codebase — keep them:

1. **`pkg/unikontainers/hypervisors/qemu.go`** — `BuildExecCmd` adds `-qmp unix:<sock>,server,nowait` and `-S` (start paused, don't auto-run CPU). The socket path is per-container: e.g. `/run/urunc/<id>/qmp.sock`. After execve, QEMU listens; whoever connects can drive the lifecycle.
2. **`pkg/unikontainers/hypervisors/firecracker.go`** — Already API-only. Existing FC integration must already use `--api-sock` or `--config-file`. The change: stop relying on config-file-then-boot pattern; instead, do incremental `PUT` calls and only `PUT /actions {InstanceStart}` when `StartExecve` arrives.
3. **`pkg/unikontainers/hypervisors/types.go`** — extend the `VMM` interface (or add a sibling `MonitorControl` interface — Decision 2 from the design doc) with the post-spawn operations: `QueryStatus`, `Pause`, `Resume`, `Shutdown`, `HotplugNet`, `HotplugBlock`, `AttachConsole`. The implementations live in the per-monitor files.
4. **`pkg/unikontainers/unikontainers.go`** — `Exec` (currently the function that ends in `syscall.Exec`) is split into: spawn-monitor-in-background (called by reexec during the gap), wait-for-StartExecve, send-start-via-socket (replaces the syscall.Exec), send-StartSuccess. The reexec process exits after the "send start" step; it does *not* execve into the monitor anymore.
5. **`pkg/unikontainers/ipc.go`** — small additions for the new state transitions, if any. The existing message types (`StartExecve`, `StartSuccess`) may be sufficient.
6. **`cmd/urunc/kill.go`, `delete.go`** — update to attempt `MonitorControl.Shutdown()` first, fall back to signalling the PID. The fallback matters: monitor-control sockets can fail (broken, deleted, monitor-dead-already).

**TODO:** This subsection can stay close to bullet form. Rewrite the prose around it.

### §4.4 · What I will explicitly not do (scope)

(Keep from current proposal. Wording in your voice.)

- Not rewriting the seccomp handling (`PreExec` for HVT) in this scope.
- Not adding a new monitor (cloud-hypervisor, Dragonball, etc.) — the interface should make adding one a separate first-PR for someone else.
- Not introducing a heavy QMP/FC library unless the design doc justifies it. Both prototypes show the wire protocols are small enough to hand-roll the urunc-side types.
- Not redesigning urunc's IPC (`reexec.sock` / `urunc.sock`) — work *with* it, not around it.

### §4.5 · What I will prove out before each merge

- The QEMU+QMP path can spawn paused, query state, gracefully shut down, and hotplug a virtio-net device end-to-end. Existing nginx-on-unikraft integration tests stay green.
- The Firecracker+API path can do the same subset that FC supports.
- The CLI-only fallback still works for any monitor that does not expose a socket (HVT).
- An extended `bench-console.sh` measures the per-round-trip cost on QMP and on FC's API. Goes into urunc CI as a regression check.

---

## §5 · Plan — 12 weeks (8 Jun – 31 Aug 2026)

Suggested table (you can keep my existing one in cover-letter.html or redo).
The phases that *changed* compared to my prior draft, based on the verification:

- **Wk 1-2 onboarding** — re-read everything I already read, this time inside the project's slack/comms channel. Open the design doc as a draft PR from week 1.
- **Wk 3 design doc sign-off** — resolve Decisions 1-4 (QMP wire types library? interface placement? socket location? opt-in vs default-on?) with cmainas/ananos.
- **Wk 4-5 QEMU QMP** — extend the qmp-probe code into a real urunc integration. MVP first (`query-status` + `quit` + `cont`), then full lifecycle (`stop`/`cont`/`device_add`/`device_del`).
- **Wk 6-7 Firecracker API** — same urunc-internal interface, FC-backed. Wire the reexec flow change so the monitor is spawned in background mode during the create-start gap.
- **Wk 8-9 tests + CI + hotplug demo** — integration tests; extend `bench-console.sh` to measure monitor-control round-trip cost; live demo of `device_add` on a unikraft-nginx sandbox.
- **Wk 10 docs + bug-fix** — user docs (how to talk to a urunc sandbox's QMP socket, how to opt in), review-comment cleanup.
- **Wk 11-12 buffer + blog post** — Nubificus/CNCF blog post on the redesign and the numbers.

**TODO:** Decide whether to keep this as a 7-row table or expand. The verification reduces risk on weeks 4-7, so the buffer at the end could be tighter.

---

## §6 · Risk register

(Keep from current cover-letter.html. Already grounded; just rephrase in your voice.)

The four risks I called out: design doc slip, QMP socket-lifecycle edge cases, FC SDK churn, CI test-suite blow-up. Each has a mitigation.

The *new* risk to add, based on the verification:
- **The IPC sequencing change (reexec exiting instead of execve'ing) is more invasive than the rest of the work.** It touches both reexec and `urunc start` simultaneously. Mitigation: feature-flag it from week 4; the legacy execve path stays the default until the new path has passed integration tests in week 8.

---

## §7 · Stretch goals

(Keep from current cover-letter.html, but reorder.)

Promote to top: **wire `bench-console.sh` into CI** — the prototype already exists, this is the smallest follow-up that has the most ongoing value.

Others (in order):
- `chardev-add` console attach (closes #389's noisy-stdio cost permanently).
- QMP event subscription as a long-running goroutine, surfacing into urunc's state machine.
- A third backend (cloud-hypervisor) only as a sanity check that the interface generalises.

---

## §8 · Closing

**Don't write a paragraph.** End with one or two lines, in your voice, signing off. Devesh's accepted proposal just stopped. Doing the same is fine.

The repo link: `github.com/unchangedraman/proposal` — and specifically the
`prototypes/` directory and this skeleton.

---

## Things to deliberately delete from the current cover-letter.pdf

- The Q1/Q2/Q3/Q4 cover-letter framing (drop entirely — the 4 LFX questions go in the LFX form text field as short answers; this PDF is the proposal/exercise summary).
- Anywhere I used "leverage", "robust", "seamless", "comprehensive", "passion", "thrilled", "excited" — find/replace check.
- The current Decision Points formatting (the four shaded boxes) — keep the *content* (Decisions 1-4) but lift them into prose inside §4.2 so the structure isn't visually screaming "AI-generated rubric."
- The "Pre-application work — in flight" section — the prototypes exist now, so the language changes from "I am landing" to "I built; transcript at …".

## Things to keep verbatim (these are facts)

- All file:line citations in §2.1
- The numeric findings from #389 (1.16 M PIO exits, 0x3F8, 2,016 → 73,378 lines/sec, 36×, 407× → 11×)
- The QMP probe timings (~2 ms total)
- The FC probe timings (16 ms InstanceStart, 820 ms total)
- All PR numbers (#13442, #13452, #1108)
- The pre-application required tasks cmainas listed in comment 2 of #112

## A note on LLM-disclosure honesty

cmainas's note in #112: *"Feel free to use LLMs to refine the proposal, but do not
use LLMs to write the entire proposal."*

Refine = OK. Generate = not OK.

This skeleton was produced by an LLM (me, in collaboration with you). The
prototypes and the source-reading findings are real and reproducible. The
prose should be yours. If you want to include a one-liner in the proposal
acknowledging tooling use, that's defensible and Nubificus's culture would
respect it.
