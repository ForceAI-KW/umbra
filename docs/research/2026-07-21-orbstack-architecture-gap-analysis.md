# OrbStack architecture — what it actually does, and what umbra should adopt

_2026-07-21. Sources: multi-agent web research (adversarially verified) + direct inspection of
`github.com/Code-Hex/vz/v3 v3.7.1` in this module cache. Every API claim below was checked
against that source, not taken from the research._

## The headline

**OrbStack runs on Apple's Virtualization.framework — the same layer umbra uses.** Its author
(Danny Lin / kdrag0n) states this publicly and *against interest*: a custom VMM would be
"faster, simpler, more reliable" but Apple gates Rosetta behind VZ (`VZLinuxRosettaDirectoryShare`
has no Hypervisor.framework equivalent), so "I still use Virtualization.framework, but only
because I have no choice."

So there is **no hypervisor advantage to close**. Every OrbStack advantage lives *above* VZ, and
three of the four are reachable from umbra's existing library today.

## What OrbStack actually is (documented)

| | |
|---|---|
| Hypervisor | Apple Virtualization.framework (author-attested, not on their architecture page) |
| Topology | **ONE** lightweight VM, shared kernel, WSL2-style. "Strictly speaking, OrbStack machines are not independent VMs." Machines are namespaces inside it, not VMs. |
| Memory | No fixed per-guest allocation. A **global pool**; per-machine limits are opt-in *ceilings*. Dynamic memory shipped in v1.7.0 (Aug 2024). |
| Networking | A **from-scratch custom** stack: NAT for v4/v6 + a custom DNS forwarder. Explicitly *not* gvisor-tap-vsock, slirp, or VPNKit. |
| File sharing | **Stock VirtioFS** plus proprietary caching. Same base protocol as umbra — the gap is caching policy, not protocol. |

**What is NOT documented, anywhere:** the *mechanism* of OrbStack's dynamic memory. Not in their
docs, architecture page, engineering blog, or published kernel repo. Any account of "how" is
inference. We design from VZ's documented surface instead.

## Verified against `vz@v3.7.1` — available to umbra, unused today

This is the actionable part. All four confirmed by grepping the module source:

| Capability | vz API (exists) | umbra today |
|---|---|---|
| **Memory ballooning** | `NewVirtioTraditionalMemoryBalloonDeviceConfiguration` + `SetTargetVirtualMachineMemorySize` | **not used** — fixed allocation |
| **VM state supervision** | `StateChangedNotify()` → channel of transitions; `CanStart/CanStop/CanRequestStop/Pause/RequestStop` | **not used** — only one polled `vm.State()` call |
| **Native NAT networking** | `NewNATNetworkDeviceAttachment` (also Bridged, FileHandle) | **not used** — gvisor-tap-vsock userspace netstack |
| **Owned console handle** | `NewFileHandleSerialPortAttachment(read, write *os.File)` | uses `NewFileSerialPortAttachment(path, false)` |

**The research's verifier marked several of these "refuted". The source says otherwise.** Trust the
module, not the report — and note this is the second time on this project that a confident
secondary claim lost to a one-command check.

## Recommendations, ranked by the failures they'd have prevented

### 1. Supervise VM state — `StateChangedNotify()`  ← highest value

umbra never observes VM lifecycle; it *infers* liveness. That is the root of the whole zombie
class: `umbra list` reported `running` for a guest whose VM was at 0% CPU and, later, for one
whose process had exited entirely.

On 2026-07-20 a VM process leaked after an unclean stop and held the guest's `disk.img`,
`efi-vars.fd`, `seed.iso` **and `console.log`** for 1h39m — silently blocking every subsequent
boot with zero diagnostics. A supervisor consuming `StateChangedNotify()` would have marked the
machine stopped the moment VZ said so, and could reap on transition rather than hoping `lsof`
catches it later.

Corroborating: Virtualization.framework is documented across multiple trackers to crash and leak
VM state on unclean stops — including a VM-slot counter not decremented. **Treat VZ-level failure
as an expected mode, not an exception.**

### 2. Fix the console — and stop it being stealable

`console.log` has been **zero bytes through every boot**, which is why the 2026-07-20 incident
took hours: we were diagnosing a guest we could not see.

Two independent problems:

- **Ownership.** `NewFileSerialPortAttachment(path, ...)` hands VZ the path; the leaked VM held
  that fd and every new boot truncated the file to nothing. `NewFileHandleSerialPortAttachment`
  takes `*os.File` handles umbra opens and owns — a dead VM cannot steal them.
- **Delivery.** Even with a clean handle, output only lands if the guest writes to that device.
  Confirm the guest console is actually `hvc0` for these EFI-booted Ubuntu images; if it is not,
  no attachment change will help. **This must be verified empirically before building on it.**

Console capture is not hygiene. It is the difference between a 20-minute diagnosis and a
six-hour one.

### 3. Fail CLOSED on orphan reaping

`internal/vm/orphan.go` already has `reapOrphanHolders`, but `diskHolders` shells to
`lsof -t -- <disk>` and its own comment says *"treat any error as 'no holders'"*. Any `lsof`
failure that is not "nothing matched" ⇒ umbra concludes the disk is free and launches anyway.

That is a gate that fails open, silently — the exact pattern rule 0 warns about. Distinguish
lsof exit 1 (genuinely no match) from any other failure, and **refuse to launch** on the latter.

### 4. Memory: add a balloon, but keep a hard cap

The 10 GiB-guest-on-16 GB-host swap thrash was **a project choice, not a VZ limitation** —
`SetTargetVirtualMachineMemorySize` has been available all along.

But do not expect OrbStack-grade behaviour from it. VZ's balloon is a **one-way, host-driven
lever**: get/set target size, nothing else. No statistics, no free-page reporting, no callback
when the guest returns pages. Attaching the device alone reclaims nothing — umbra must write the
host-side policy loop. Lima's experience confirms this empirically.

Free page reporting (the guest-side half of real dynamic memory) exists in Linux but is
unreachable here: its only in-tree consumer is virtio-balloon negotiating
`VIRTIO_BALLOON_F_REPORTING`, and Apple ships no such device and no way to add a custom one.

And reclaim is seconds-scale even where it works, so it can **never** resolve an acute spike like
a CI build. **A hard guest-size cap relative to host RAM remains mandatory** — that is what
actually fixed the incident (10 GiB → 6 GiB), and it should be enforced at `create`/`set` time
rather than left to judgement.

### 5. Networking: evaluate native NAT, but do not assume it is better

gvisor-tap-vsock is the same architectural class as libslirp/VPNKit — the class OrbStack
explicitly replaced. `NewNATNetworkDeviceAttachment` is a VZ-native alternative already in the
binding, and Lima wired it up without any sudoers/root setup.

Worth a spike given the repeated `netstack: guest link closed: cannot receive packets` errors.
**But this is a real migration** — port forwarding, `*.umbra.local` DNS and the docker socket
bridge all sit on gvisor-tap-vsock today. Do not start it during an incident.

## What is NOT worth chasing

- **A custom hypervisor.** OrbStack's own author would not choose VZ if he had an alternative;
  neither should umbra spend effort here.
- **Replacing VirtioFS.** OrbStack uses stock VirtioFS too. Any gap is caching policy.
- **The single-shared-kernel VM topology.** It is the elegant part of OrbStack's design, but it is
  a rewrite of umbra's entire machine model, and it would *not* have prevented the memory
  incident — OrbStack held host RAM in one VM too until v1.7.0 shipped dynamic memory separately.

## Ordering

1 → 3 → 2 (verify the console device first) → 4 → spike 5.

Supervision and fail-closed reaping are small, self-contained, and directly prevent the class of
incident that has now cost two long sessions. Memory and networking are larger and can follow.
