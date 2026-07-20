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
that single verdict and stops.

**Precedence.** The terminating tiers are evaluated in this order, which is *not* the same
as rung number:

1. **Daemon down** (rung 0) — nothing below can even be collected.
2. **Host hardware** (rung 7) — the two-guest discriminator and the load canary. These are
   live, present-tense observations of the machine miscomputing, and they deliberately
   **outrank rung 1**: rung 1 is inferred from a log, and telling an operator to rebuild
   umbra when the CPU is failing under load sends them down a dead end. When both host
   signals are present they merge into one verdict carrying both evidence strings.
3. **Netstack death** (rung 1) — host-wide, but log-derived, so it yields to tier 2.

Rungs 2–7 are scoped to a guest or a repo, and doctor reports
**one verdict per affected subject**, sorted by rung. A host with a healthy guest and one
stale runner registration therefore prints exactly one rung-5 finding, not a global failure.

The exit code reflects the most severe rung found (0 = all healthy or `unknown`-only,
non-zero otherwise) so the watchdog can consume it.

| # | Rung | Signature | Next action |
|---|---|---|---|
| 0 | Daemon down | Ping fails | `umbra daemon install` |
| 1 | Netstack death | ≥2 guests that are each *currently running, currently unreachable, and named by a `guest link <MAC> … cannot receive packets` line in this daemon lifetime* | `make build && make install` |
| 2 | Guest no-IP | state `running` and **no configured address in the registry record** | recreate |
| 3 | Guest has IP, ssh won't accept | readiness/ssh timeout | `stop`/`start`, wait for `cloud-init status` = done |
| 4 | Runner service inactive | systemd unit not `active` **and no other active runner unit for the same repo scope anywhere on the fleet** | restart the unit |
| 5 | Service active, GitHub reports offline | stale registration | `umbra runner add` + delete the stale registration |
| 6 | Billing lockout | jobs fail in ~3s with `runner_name:""` and `steps:[]` | **Ahmad** — org Billing & plans |
| 7 | Idle-healthy but faults under load | `--deep` canary hits SIGILL/SIGSEGV | **power-cycle + Apple Diagnostics** |

Rung 4 is in-guest (systemd, over ssh). Rungs 5–6 are per-repo and depend on `gh`.

### Two addresses, and why rung 2 depends on which one is missing

`MachineView` carries **two** addresses, and conflating them was the worst bug this design
shipped:

- `MachineView.IP` — the **readiness-confirmed runtime address**. `umbrad` publishes it via
  `mgr.SetIP` only *after* its readiness probe succeeds, up to `vm.DefaultReadyTimeout`
  (90s) after the machine enters `running`, and clears it on stop. `lc.Start` flips the
  state and returns immediately; readiness blocks afterwards.
- `registry.Machine.IP` (embedded, so already on `MachineView`) — the **static configured
  address** assigned at create time, present from creation to deletion.

So *every healthy guest is `running` with an empty `MachineView.IP` for its entire boot
window*, and permanently if readiness times out. Rung 2 therefore convicts **only when the
configured address is absent** — a damaged registry record, which waiting cannot fix.
"Configured but not readiness-confirmed" is an **Unknown** with a re-check action, never a
recreate instruction.

### Runner units: stale registration vs. outage (rung 4)

Registering a runner leaves the previous systemd unit behind, enabled but dead, so a repo
routinely carries a stale unit beside the live one serving its CI. Rung 4 groups units by
repo scope **across the whole fleet**: an inactive unit is a `fail` only when *no* runner
for that scope is active anywhere. Otherwise it is an Unknown with a
`systemctl disable --now` action. Without this, `umbra doctor` exited 1 on every run of a
verifiably healthy host.

### `--deep`

`--deep` opts into the one probe that costs real time. It does **not** start, stop, or
otherwise mutate any machine.

- **Native-binary load canary** (rung 7): a bounded ~60s stress **per running guest**,
  run sequentially — so a two-guest host costs ~2 minutes, not ~60s. It is deliberately
  not parallelised: running the canary on every guest at once would put the host under a
  combined load the single-guest signature was calibrated against, and a `--deep` run
  already only happens when someone is sitting in front of it. The canary is
  `curl`/`openssl` loops plus a parallel CPU burst — watching for SIGILL/SIGSEGV. Each
  canary is additionally bounded by a hard 3-minute timeout, so a wedged guest cannot hang
  the run. Stock arm64 binaries taking
  CPU-level signals means the guest is miscomputing, which is a host-level fault that no
  amount of RAM/CPU tuning will fix.

**Doctor does NOT boot the spare guest.** An earlier draft had `--deep` start the stopped
spare, probe it, and stop it again. That was deliberately not built: it would make `--deep`
mutate machine state, and the ops watchdog (`ci-runner-guard.sh`) treats the spare being
stopped as a steady-state invariant, so doctor starting it would trip the very alarm doctor
exists to explain. (A `GuestEvidence.Spare` field survived that cut but was never populated
by any collector, so it claimed "not the spare" for every guest on the fleet. It has been
removed.)

### The two-guest discriminator (rung 7, default run — **requires corroboration**)

The discriminator runs on the **default read-only run**, not under `--deep`, and it is a
rung-7 host-hardware question — not the rung-2 refinement an earlier draft of this document
described.

It does **not** convict on its own. The original rule was "two running guests with an empty
IP ⇒ the host's hardware is bad ⇒ power-cycle and run Apple Diagnostics", and every term in
that inference was wrong. Per the two-addresses section above, two guests are in that state
throughout any ordinary boot — which is the steady state of a host reboot with two autostart
guests, and is reachable through doctor's own `umbra stop X && umbra start X` advice.

That verdict is the most expensive wrong answer this tool can give: it sends the operator to
Apple, and `host-hardware` is in the watchdog's `UNHEALABLE_RUNGS`, so the watchdog stands
down during a routine boot. **It was produced against a real host**: a human operator
concluded "host-level hardware fault, book Apple service" and had to retract it — the real
cause was memory overcommit.

So a host-hardware conviction now requires a **load-canary fault**. Correlated netstack log
lines were considered as an alternative corroborator and rejected: they point at umbra's
user-mode network stack, whose remedy is `make build && make install`, not at the CPU, and
since tier 2 runs before tier 3 using them here would answer a netstack fault with a
power-cycle. Uncorroborated, the two-guest fact becomes an explicit **Unknown** that
discloses what doctor cannot distinguish — including host memory overcommit, for which the
ladder has no rung at all.

## The stale-log trap

**This is the most likely way doctor gives a confidently wrong answer, so it is called
out explicitly.**

`umbrad.err.log` is append-only across daemon restarts and reboots. At the time of
writing it still contains netstack-death lines from 22:25, on a host that is now healthy.
A naive grep would match those lines and report rung 1 forever.

Three mitigations, all required:

1. The log scanner considers only lines **at or after the current daemon's start
   timestamp**. If the start marker's own timestamp will not parse, the scanner fails
   closed and returns an error rather than silently falling back to an older marker.
2. Rung 1 additionally requires a **live reachability failure**. Log evidence alone never
   convicts.
3. **Correlation, not counting.** `guest link <MAC> closed: cannot receive packets` is
   `level=INFO` and is emitted on **every ordinary guest shutdown** — measured against the
   real log, 7 of 13 daemon lifetimes already carried ≥2 distinct MACs with no fault
   present. So counting MACs is not evidence of anything. A MAC counts only when it belongs
   to a guest that is *right now* running and unreachable, and ≥2 such guests are required.
   Without this, rung 1 degenerates into "any unreachable guest ⇒ rebuild umbra" and makes
   rungs 2 and 3 unreachable in production.

   This correlation needs `GuestEvidence.MAC`, populated from the machine registry
   (`registry.Machine.MAC`, reachable as `MachineView.MAC`). If **no** guest carries a MAC,
   doctor cannot perform the correlation and emits an explicit `unknown` verdict on the
   `unknown` rung saying so — it never convicts on log evidence alone, and never stays
   silent, which would render an unpopulated field as health.

Each of these gets a dedicated test.

## Error handling

When `gh` is missing, unauthenticated, or rate-limited, the per-repo rungs report `unknown`
— never `pass`. Silence must not be reportable as health; an absent probe and a passing
probe are different states and are rendered differently.

An `unknown` verdict carries the dedicated **`unknown` rung**, not the rung it failed to
evaluate. "We could not tell" is not the same finding as "the runner is offline", and a
consumer keying remediation off `rung` must not act on a fault that was never diagnosed.
An `unknown` verdict degrades that one rung and lets the rest of the diagnosis continue —
it never terminates the ladder.

Any single probe failing degrades that rung to `unknown` and lets classification continue.
One unreachable guest must not abort the whole diagnosis. This includes the machine list
itself: if `umbrad` answers its ping but `ListMachines` fails, that is one `unknown`
verdict, not an aborted run.

### Silence is not health (the collection boundary)

The classifier can only honour "unprobed is never pass" for facts it is *told about*. A
collector that stays silent when a probe cannot run reintroduces the bug one layer down —
and it did: with no `ssh` on `PATH`, no forwarded ssh port, or an unreadable
`umbrad.err.log`, doctor printed `healthy: no faults detected` and exited 0.

`Evidence.Unprobed` is the fix. Every probe the collector could not **run at all** is
recorded there and becomes an explicit `unknown` verdict. The recorded cases are: the log
missing / unparseable / carrying no daemon-start marker; the machine list unavailable; a
running guest with no forwarded ssh port; `ssh` absent from `PATH`; and a repo whose `gh`
probe failed. A **stopped** guest is deliberately *not* recorded — the ladder skips it by
design, so reporting it would be noise rather than honesty.

Unprobed verdicts are appended to **every** return path, including the terminating tiers.
A terminating tier answers "what is the fault"; an unprobed record answers "what did we
fail to look at", and suppressing the second because the first fired would recreate the
same bug at a third layer.

## Deriving the repos to probe

The per-repo rungs need a repo list, and hardcoding one guarantees it stops matching the
day a runner is added. Instead the list is derived from the systemd unit names already
read out of each guest: `actions.runner.<owner>-<repo>.<instance>.service`.

The separator is `-`, which is also legal inside both an owner and a repo name —
`ForceAI-KW/umbra` and `Force/my-repo` produce indistinguishable scopes. So the scope is
not split by string surgery: every possible split is offered as a candidate to
`gh api repos/<owner>/<repo>`, and GitHub itself decides which is real. If none resolves
— including because `gh` is missing, unauthenticated, or rate-limited — the repo is
recorded `Probed:false` and reported `unknown`. A healthy reading is never fabricated.

**Billing-lockout signature.** Only the newest failed workflow run is inspected: a lockout
blocks *every* run, so if the newest failure does not carry the signature, the org is not
locked out right now. Every failed job in that run must have an empty `runner_name`, zero
`steps`, and a duration under 10s. One job that reached a runner means this is an ordinary
CI failure — sending Ahmad to the org billing page for a broken test is exactly the
misdiagnosis this tool exists to prevent. Runs older than 7 days are ignored, for the same
stale-evidence reason as the log cutoff.

## `--json` is a watchdog contract

`~/.claude/scripts/ci-runner-guard.sh` consumes this output. The field names `rung`,
`health` and `next_action`, and the rung slug strings, are **frozen**; changes may only be
additive. Two additive convenience fields exist so consumers need not reimplement the
exit-code rule:

- `healthy` — true only when nothing at all was found: no fault *and* no unprobed probe.
- `unknown_only` — findings exist but none is a fault. Not a clean bill of health, but
  nothing was diagnosed either.

`verdicts` is always an array, never `null`. The exit code is driven by `fail` alone;
`unknown` never sets it, or every host without `gh` would look broken to the watchdog.

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
