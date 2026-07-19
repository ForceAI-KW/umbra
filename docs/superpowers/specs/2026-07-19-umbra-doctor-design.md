# `umbra doctor` — design

_2026-07-19. Status: approved, ready for implementation planning._

## Why

On 2026-07-19 a single host fault cost most of a working day. It presented three
different ways as it degraded — under-load native crashes, then guests unreachable
while still reporting `running`, then guests failing to obtain an IP at all — and at
each stage the documented fix for the *previous* stage was retried and failed. Two
unrelated CI failure modes (a GitHub org billing lockout, stale runner registrations)
looked similar enough from the outside to confuse the diagnosis further.

The knowledge to triage this exists, but only as prose in a memory file. `umbra doctor`
encodes it as an executable ladder so recurrence costs minutes instead of hours.

The command is **diagnose-only**. It never mutates state in its default mode. This is
deliberate: the expensive lesson of 2026-07-19 was that applying a fix at the wrong rung
is worse than doing nothing, and the fix at the bottom rung (a hardware power-cycle) is
not something software can perform.

## Architecture

Three units, split so the diagnostic logic is testable without a broken host.

### `cmd/umbra/doctor.go` — evidence collection

All I/O lives in the CLI layer, not in `internal/doctor`. This follows the repo's
existing pattern (`stats.go` and `runner.go` both collect over ssh from `cmd/umbra`) and
keeps `internal/doctor` free of cobra and client plumbing — that purity is what makes
`Classify` testable, so it is worth protecting.

Collected:

- daemon ping (via the existing API client)
- machine list (name, state, IP)
- `~/.umbra/log/umbrad.err.log` scan
- per-guest ssh reachability probe
- per-guest runner systemd unit state
- GitHub runner registration + recent job outcomes, via `gh`

Produces an `Evidence` struct. No interpretation happens here. Every probe failure
degrades that one field rather than aborting — one unreachable guest must not blind the
diagnosis to the rest of the host.

### `internal/doctor` — `Classify(Evidence) Verdict`

A **pure function**. No I/O, no clock, no filesystem. Evidence in, verdict out. This is
where the ladder lives, and being pure is what makes every rung unit-testable with a
literal struct.

`Verdict` carries: matched rung, human-readable reasoning, the supporting evidence, and
the exact next-action command.

### `cmd/umbra/doctor.go`

Thin CLI, mirroring the existing `status.go` shape: `--json` for watchdog probes,
`--deep` for the mutating/expensive probes.

## The ladder

Ordered by blast radius: host-wide faults first, then per-guest, then per-repo.

**Verdict shape.** Rungs 0 and 1 are host-wide and terminate the ladder — if the daemon is
down or netstack is dead, nothing below it can be diagnosed meaningfully, so doctor reports
that single verdict and stops. Rungs 2–7 are scoped to a guest or a repo, and doctor reports
**one verdict per affected subject**, sorted by rung. A host with a healthy guest and one
stale runner registration therefore prints exactly one rung-5 finding, not a global failure.

The exit code reflects the most severe rung found (0 = all healthy or `unknown`-only,
non-zero otherwise) so the watchdog can consume it.

| # | Rung | Signature | Next action |
|---|---|---|---|
| 0 | Daemon down | Ping fails | `umbra daemon install` |
| 1 | Netstack death | `guest link … cannot receive packets` across ≥2 distinct MACs, and/or `no route to host`, **plus** a live reachability failure | `make build && make install` |
| 2 | Guest no-IP | state `running`, IP empty | recreate — unless the discriminator says host-level |
| 3 | Guest has IP, ssh won't accept | readiness/ssh timeout | `stop`/`start`, wait for `cloud-init status` = done |
| 4 | Runner service inactive | systemd unit not `active` | restart the unit |
| 5 | Service active, GitHub reports offline | stale registration | `umbra runner add` + delete the stale registration |
| 6 | Billing lockout | jobs fail in ~3s with `runner_name:""` and `steps:[]` | **Ahmad** — org Billing & plans |
| 7 | Idle-healthy but faults under load | `--deep` canary hits SIGILL/SIGSEGV | **power-cycle + Apple Diagnostics** |

Rungs 4–6 are per-repo and depend on `gh`.

### `--deep`

Default mode is strictly read-only. `--deep` opts into two probes that cost time or
mutate state:

- **Two-guest discriminator** (rung 2): boot the spare guest and probe it. If it fails
  identically, the fault is host-level, not guest-image — which rules out a ~20-minute
  recreate in about 2 minutes. Doctor stops the spare again afterward. If the spare boots
  clean, the primary's image really is damaged and recreate is the correct action.
- **Native-binary load canary** (rung 7): a bounded ~60s stress — `curl`/`openssl` loops
  plus a parallel CPU burst — watching for SIGILL/SIGSEGV. Stock arm64 binaries taking
  CPU-level signals means the guest is miscomputing, which is a host-level fault that no
  amount of RAM/CPU tuning will fix.

## The stale-log trap

**This is the most likely way doctor gives a confidently wrong answer, so it is called
out explicitly.**

`umbrad.err.log` is append-only across daemon restarts and reboots. At the time of
writing it still contains netstack-death lines from 22:25, on a host that is now healthy.
A naive grep would match those lines and report rung 1 forever.

Two mitigations, both required:

1. The log scanner considers only lines **at or after the current daemon's start
   timestamp**.
2. Rung 1 additionally requires a **live reachability failure**. Log evidence alone never
   convicts.

This gets a dedicated test.

## Error handling

When `gh` is missing, unauthenticated, or rate-limited, rungs 4–6 report `unknown` — never
`pass`. Silence must not be reportable as health; an absent probe and a passing probe are
different states and are rendered differently.

Any single probe failing degrades that rung to `unknown` and lets classification continue.
One unreachable guest must not abort the whole diagnosis.

## Testing

- **Table-driven tests over `Classify`** — one case per rung, driven by literal `Evidence`
  structs. This is the bulk of the value and needs no host.
- **The stale-log case** — evidence containing pre-restart netstack lines plus healthy live
  probes must classify as healthy, not rung 1.
- **The discriminator case** — two guests with no IP must classify as host-level, not as
  two independent damaged images.
- **`unknown` propagation** — absent `gh` must not produce a `pass`.
- **Parser tests** over canned `umbrad.err.log` excerpts.
- **CLI smoke test** for flag wiring and `--json` shape.

## Out of scope (YAGNI)

No auto-fix or remediation. No trending or history. No menu-bar surfacing. No
remote-daemon mode. Each was considered and deliberately excluded.

## Related

- `feedback-umbra-ci-runner-recovery-and-gotchas` — the prose triage knowledge this encodes
- `docs/superpowers/plans/2026-07-15-umbra-v2-core-upgrades.md` — the v2 command set
