# CI cutover runbook — retiring OrbStack `fwb-ci` for an Umbra `ci-runner` machine

Human procedure for standing up `fwb-ci2` (an Umbra-managed GitHub Actions
self-hosted runner) in **parallel** with the existing OrbStack `fwb-ci`
runners, verifying it green, and only then — on Ahmad's explicit go-ahead —
cutting real workflows over and retiring OrbStack.

Grounded in `docs/research/launchd-and-ci-cutover.md` §4–§6. Read that first
for the full reasoning (token lifetime, label AND-semantics, docker-inside-
the-runner-VM rationale, disk sizing).

**Nothing in Steps 1–3 touches the live org's real CI.** New runners carry a
distinguishing label (`umbra-ci`) that no existing FWB/WBS/CRM workflow
requests, so they sit idle-but-registered until a human explicitly flips a
`runs-on:`. The cutover itself (Step 4 onward) is a **human gate** — do not
automate it.

---

## Prereqs

- `gh auth refresh -h github.com -s admin:org` — registering/removing
  self-hosted runners needs `admin:org` scope; the browser must be signed in
  as `voidengineer-911` (org owner of `ForceAI-KW`, id 287515696).
- Host `docker` CLI (`brew install docker`) if you want to test forwards/
  `docker` commands from the Mac side against `fwb-ci2` directly — not
  required for the runner install itself (the runner VM gets its own
  dockerd via cloud-init, see `internal/cloudinit/seed.go`'s
  `ciRunnerRuncmdLines`).
- `umbrad` running (`make run-daemon`, or installed via `umbra daemon
  install` — see README's M4 section).

## INPUT NEEDED — size `fwb-ci2` before creating it

Umbra can't discover OrbStack's config on its own. Before Step 1, get from
Ahmad (or run `orb info fwb-ci` / `orb list -v` yourself if OrbStack is
installed on this Mac):

- **CPUs / RAM / disk** allocated to the OrbStack `fwb-ci` VM — size
  `fwb-ci2` to match or exceed it (`--cpus`, `--memory-gib`, `--disk-gib`).
  Give disk extra headroom over whatever `fwb-ci` has proven sufficient for
  (P22 — CI cache/build-artifact growth over weeks of churn).
- **Runner-instance count** — how many `actions-runner-N` processes
  `fwb-ci` currently runs (parallel CI across FWB/WBS/CRM). Match it with
  `RUNNER_COUNT` in Step 2.

Do not guess these numbers or hardcode a default — confirm with Ahmad first.

---

## Step 1 — create + boot `fwb-ci2`

```sh
umbra create fwb-ci2 --role ci-runner --cpus <N> --memory-gib <M> --disk-gib <D>
umbra start fwb-ci2
```

`--role ci-runner` provisions plain docker via cloud-init (no `tcp://0.0.0.0:2375`
exposure — a CI runner executes untrusted PR code and its dockerd must stay
local-socket-only, unlike the shared `docker` role VM). Confirm it booted:

```sh
umbra list                 # fwb-ci2 should show state=running (ci-runner machines ARE listed — see Task 7 note below)
umbra shell fwb-ci2 -- uname -m   # arm64
umbra shell fwb-ci2 -- docker version
```

**Task 7 fix (this milestone):** unlike the reserved `docker` role, a
`ci-runner` machine is a normal, user-visible machine — `umbra list` and
`GET /v1/machines` show it. Only the single reserved `docker` VM is hidden.

## Step 2 — register the runner(s)

Fetch a **fresh** org registration token immediately before running the
install script — it expires in **1 hour** (P20):

```sh
REG_TOKEN=$(gh api --method POST -H "Accept: application/vnd.github+json" \
  /orgs/ForceAI-KW/actions/runners/registration-token | jq -r .token)
```

Push and run `scripts/install-runner.sh` inside the guest over the existing
`umbra shell` SSH channel:

```sh
umbra shell fwb-ci2 -- \
  REG_TOKEN="$REG_TOKEN" RUNNER_NAME=fwb-ci2 RUNNER_COUNT=<count-from-INPUT-NEEDED> \
  bash -s < scripts/install-runner.sh
```

The script is idempotent (`config.sh --replace`) — safe to re-run if it was
interrupted partway (e.g. the token expired mid-batch on a large
`RUNNER_COUNT`; fetch a new `REG_TOKEN` and re-run).

Confirm registration:

```sh
gh api /orgs/ForceAI-KW/actions/runners --jq \
  '.runners[] | select(.labels[].name=="umbra-ci") | {name, status, busy}'
```

Expect `status: "online"`, `busy: false` for each instance.

## Step 3 — verify

Copy `.github/workflow-templates/umbra-ci-verify.yml` into a scratch repo's
`.github/workflows/` (or a scratch branch of an existing repo — do **not**
add it to a repo's default branch as a permanent workflow, it's a one-off
verification tool):

```sh
gh workflow run umbra-ci-verify.yml --repo ForceAI-KW/<scratch-repo>
gh run list --repo ForceAI-KW/<scratch-repo> --workflow umbra-ci-verify.yml --limit 1
gh run watch --repo ForceAI-KW/<scratch-repo>
```

`runs-on: [self-hosted, umbra-ci]` uses GitHub's label AND-semantics — the
job can only land on `fwb-ci2`'s runners (the only ones carrying
`umbra-ci`), never on `fwb-ci`'s. Confirm the run is green and actually
executed on `fwb-ci2` (checkout succeeds, `docker run --rm hello-world`
succeeds, `docker version` + `git --version` print sane output).

**Sleep/wake test (P21)** — put the host Mac to sleep, wake it, then
re-trigger the verify workflow. Confirm it still completes (the runner's own
HTTPS long-poll to GitHub's Actions backend should auto-reconnect via its
systemd unit; this is not automatically exercised by M2's network
supervisor, which only covers the SSH/netstack path — verify it for real
here rather than assuming).

Run the verify workflow several times, across at least one host sleep/wake
cycle, before treating `fwb-ci2` as trustworthy. A single green run is not
enough.

---

## === HUMAN GATE — Ahmad's hands only, do NOT run unattended ===

Everything below is a **manual, human-approved** cutover. Do not script or
automate this section end-to-end — each sub-step is a deliberate go/no-go
decision, and (c) is irreversible.

### (a) Point real workflows at the new runners — still reversible

Change `runs-on: self-hosted` → `runs-on: [self-hosted, umbra-ci]` in the
target repos' real workflows, one repo/PR at a time. Both runner pools stay
registered during this step — a bad run on `fwb-ci2` is just a red CI job,
trivially reverted by reverting the `runs-on` change.

Recommended before flipping: give `fwb-ci`'s existing runners their own
distinguishing label too (e.g. `fwb-ci`), so rollback doesn't depend on
`fwb-ci2` being manually turned off to avoid load-balancing between the two
pools again.

### (b) Deregister the OrbStack `fwb-ci` runners

Only after real workflows have run healthy on `fwb-ci2` for a real
observation window (several days across normal PR volume — not one green
run):

```sh
# Per runner instance, from inside the fwb-ci OrbStack VM:
cd ~/actions-runner-1 && sudo ./svc.sh stop && sudo ./svc.sh uninstall
REMOVE_TOKEN=$(gh api --method POST -H "Accept: application/vnd.github+json" \
  /orgs/ForceAI-KW/actions/runners/remove-token | jq -r .token)
./config.sh remove --token "$REMOVE_TOKEN"
# repeat for actions-runner-2, -3, ... on the same VM

# Or, faster, force-remove from the API side without touching the VM at all:
gh api /orgs/ForceAI-KW/actions/runners --jq '.runners[] | select(.labels[].name=="fwb-ci") | .id' | \
  xargs -I{} gh api --method DELETE /orgs/ForceAI-KW/actions/runners/{}
```

### (c) Stop/delete the OrbStack `fwb-ci` machine, uninstall OrbStack — IRREVERSIBLE

```sh
orb stop fwb-ci
orb delete fwb-ci        # irreversible — confirm the 26GB disk has nothing else worth keeping first
# then, only once nothing else in the household depends on OrbStack:
brew uninstall --cask orbstack
```

### Rollback (only valid before step (c) runs)

As long as (c) hasn't run, rollback is just reversing (a): flip `runs-on`
back to bare `self-hosted` (or `[self-hosted, fwb-ci]` if you labeled it per
the (a) recommendation above). Once (c) has run, rollback means
re-provisioning OrbStack + `fwb-ci` from scratch — this is exactly why (c)
must be last, gated on real confidence, and why (a)/(b)/(c) are sequential
human-approved gates, never a single script.

---

## Watchdog note

`umbra status --json` already folds `docker` health alongside `daemon` +
`machines`. `fwb-ci2` shows up in `machines` like any other machine (per the
Task 7 list-visibility fix) — no separate probe path needed for the
self-healing OS watchdog to notice it's down.
