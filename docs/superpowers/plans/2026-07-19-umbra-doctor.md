# `umbra doctor` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **STATUS — read before using this as a reference.** This plan is a **point-in-time artifact**: its task breakdown, code snippets and self-review record how the work was planned on 2026-07-19, and are NOT maintained against the shipped implementation. Six review waves changed rung semantics substantially. For current behaviour read, in order: `internal/doctor/classify.go` (the rungs and their reasoning), `internal/doctor/types.go` (the evidence contract), and the design spec `docs/superpowers/specs/2026-07-19-umbra-doctor-design.md`.
>
> **Two exceptions are corrected in place rather than left stale**, because they are binding constraints rather than history, and a worker resuming from this plan would act on them: the `--deep` spare-guest boot (Global Constraints, Task 6 note) and the uncorroborated two-guest discriminator (Self-Review). Superseded text is marked where it appears.

**Goal:** Ship `umbra doctor`, a diagnose-only command that classifies an umbra/CI fault into one rung of a documented triage ladder and prints the exact next action.

**Architecture:** A new `internal/doctor` package splits I/O (`Collect`) from interpretation (`Classify`). `Classify` is a pure function — `Evidence` in, `[]Verdict` out — so every rung is unit-testable with a literal struct and no live host. `cmd/umbra/doctor.go` is a thin CLI mirroring the existing `status.go` shape.

**Tech Stack:** Go 1.x, cobra, stdlib only (`regexp`, `time`, `encoding/json`, `os/exec`). No new dependencies.

Spec: `docs/superpowers/specs/2026-07-19-umbra-doctor-design.md`

## Global Constraints

- **Diagnose-only.** NO mode mutates host or guest state. `--deep` adds the load canary and nothing else — it must **never** boot the spare guest. (Superseded the original wording, which permitted `--deep` to boot the spare; the ops watchdog treats the spare being stopped as a steady-state invariant, so booting it is both a mutation and a false alarm. The design spec was corrected first; this line follows it.)
- **No new dependencies.** Stdlib + cobra only, matching the rest of the repo.
- **`unknown` is never `pass`.** A probe that could not run reports `unknown`. Silence must not be reportable as health.
- **Log evidence alone never convicts rung 1.** It must be paired with a live reachability failure. See Task 2.
- **Exit code:** `0` when all rungs pass or are `unknown`-only; `1` when any rung fails.
- **vz/Virtualization framework code is NOT touched** by any task here — no entitlement re-sign risk.
- **Work in the worktree** `/Users/ahmadsharaf/Desktop/projects/_worktrees/umbra__feat-umbra-doctor` on branch `feat/umbra-doctor`. Never commit in `~/Desktop/projects/umbra` (stays on `main`).
- Guest shell access goes through the existing shared `sshArgs(mv *client.MachineView, remoteCmd []string) []string` helper in `cmd/umbra/shell.go:28`. Do not build ssh argv by hand.

---

### Task 1: Core types

**Files:**
- Create: `internal/doctor/types.go`
- Test: (none — pure declarations, exercised by Task 3)

**Interfaces:**
- Consumes: nothing.
- Produces: `Health`, `Rung`, `Evidence`, `GuestEvidence`, `RunnerEvidence`, `RepoEvidence`, `CanaryResult`, `LogLine`, `Verdict`. Every later task depends on these exact names.

- [x] **Step 1: Create the types file**

```go
// Package doctor diagnoses umbra host/guest/CI faults by classifying
// collected evidence into one rung of a documented triage ladder.
//
// The package is deliberately split: Collect does all I/O, Classify is a
// pure function. That split is what makes every rung testable without a
// broken host — which matters, because the faults this diagnoses are rare,
// destructive, and impossible to reproduce on demand.
package doctor

import "time"

// Health is the outcome of a single probe. Unknown means the probe could
// not run (no gh, guest unreachable) — it is never rendered as pass.
type Health string

const (
	Pass    Health = "pass"
	Fail    Health = "fail"
	Unknown Health = "unknown"
)

// Rung identifies a step on the triage ladder, ordered by blast radius:
// host-wide faults first, then per-guest, then per-repo. RungNone is the
// zero value and means healthy.
type Rung int

const (
	RungNone Rung = iota
	RungDaemonDown
	RungNetstackDead
	RungGuestNoIP
	RungGuestSSHStall
	RungRunnerServiceDown
	RungRunnerOffline
	RungBillingLockout
	RungHostHardware
)

// String renders a rung as a short stable slug, used in --json output.
func (r Rung) String() string {
	switch r {
	case RungNone:
		return "healthy"
	case RungDaemonDown:
		return "daemon-down"
	case RungNetstackDead:
		return "netstack-dead"
	case RungGuestNoIP:
		return "guest-no-ip"
	case RungGuestSSHStall:
		return "guest-ssh-stall"
	case RungRunnerServiceDown:
		return "runner-service-down"
	case RungRunnerOffline:
		return "runner-offline"
	case RungBillingLockout:
		return "billing-lockout"
	case RungHostHardware:
		return "host-hardware"
	}
	return "unknown"
}

// LogLine is one parsed line of umbrad.err.log, already filtered to the
// current daemon lifetime by ScanLog.
type LogLine struct {
	Time time.Time
	Text string
	MAC  string // set when the line is a "guest link <MAC> closed" netstack error
}

// CanaryResult is the outcome of the --deep native-binary load canary.
type CanaryResult struct {
	Ran     bool
	Faulted bool
	Detail  string // e.g. "curl exited 132 (SIGILL)"
}

// RunnerEvidence is one systemd actions.runner.* unit inside a guest.
type RunnerEvidence struct {
	Unit   string
	Active bool
}

// GuestEvidence is everything observed about one machine.
type GuestEvidence struct {
	Name       string
	State      string // "running" | "stopped" | ...
	IP         string
	SSHProbed  bool
	SSHOK      bool
	Spare      bool // booted by --deep as the two-guest discriminator
	Runners    []RunnerEvidence
	LoadCanary CanaryResult
}

// RepoEvidence is the GitHub-side view for one repo. Probed is false when
// gh was unavailable, which forces Unknown rather than Pass.
type RepoEvidence struct {
	Repo           string
	Probed         bool
	RunnerOnline   map[string]bool // runner name -> online
	BillingLockout bool            // recent jobs: ~3s, empty runner_name, zero steps
}

// Evidence is the complete observation set handed to Classify.
type Evidence struct {
	DaemonUp    bool
	DaemonStart time.Time
	GHAvailable bool
	DeepRun     bool
	LogLines    []LogLine
	Guests      []GuestEvidence
	Repos       []RepoEvidence
}

// Verdict is one finding. Subject is empty for host-wide rungs, otherwise
// the guest or repo it applies to.
type Verdict struct {
	Rung       Rung     `json:"rung"`
	Health     Health   `json:"health"`
	Subject    string   `json:"subject,omitempty"`
	Reason     string   `json:"reason"`
	Evidence   []string `json:"evidence,omitempty"`
	NextAction string   `json:"next_action,omitempty"`
}
```

- [x] **Step 2: Verify it compiles**

Run: `go build ./internal/doctor/`
Expected: no output (success).

- [x] **Step 3: Commit**

```bash
git add internal/doctor/types.go
git commit -m "feat(doctor): core evidence and verdict types"
```

---

### Task 2: Log scanner with the stale-line cutoff

**Files:**
- Create: `internal/doctor/logscan.go`
- Test: `internal/doctor/logscan_test.go`

**Interfaces:**
- Consumes: `LogLine` from Task 1.
- Produces: `ScanLog(r io.Reader) ([]LogLine, time.Time, error)` — returns only lines at or after the last `umbrad listening` line, plus that daemon-start timestamp.

**Why this task exists:** `umbrad.err.log` is append-only across daemon restarts and reboots. It currently contains netstack-death lines from a fault that a power-cycle already fixed. A scanner without this cutoff would report `netstack-dead` forever on a healthy host. This is the single most likely way doctor gives a confidently wrong answer.

- [x] **Step 1: Write the failing tests**

```go
package doctor

import (
	"strings"
	"testing"
)

// The daemon writes two timestamp shapes — bare and quoted. Both must parse.
const sampleLog = `time=2026-07-19T22:25:49.262+03:00 level=INFO msg="netstack: guest link b2:71:f2:cb:76:64 closed: cannot receive packets from , disconnecting: cannot read size from socket"
time="2026-07-19T22:25:49+03:00" level=error msg="accept tcp 127.0.0.1:60952: use of closed network connection"
time=2026-07-19T22:33:00.856+03:00 level=INFO msg="umbrad listening" socket=/Users/x/.umbra/run/api.sock
time=2026-07-19T22:33:00.857+03:00 level=INFO msg=autostarting machine=fwb-ci5
`

func TestScanLogDropsLinesBeforeDaemonStart(t *testing.T) {
	lines, start, err := ScanLog(strings.NewReader(sampleLog))
	if err != nil {
		t.Fatalf("ScanLog returned error: %v", err)
	}
	if start.IsZero() {
		t.Fatal("daemon start time not detected")
	}
	// The two 22:25 netstack lines predate the 22:33 restart and must be gone.
	for _, l := range lines {
		if strings.Contains(l.Text, "cannot receive packets") {
			t.Errorf("stale pre-restart netstack line survived the cutoff: %q", l.Text)
		}
	}
	if len(lines) != 2 {
		t.Errorf("len(lines) = %d, want 2 (the listening line and the one after it)", len(lines))
	}
}

func TestScanLogExtractsMAC(t *testing.T) {
	const l = `time=2026-07-19T23:00:00.000+03:00 level=INFO msg="umbrad listening"
time=2026-07-19T23:01:00.000+03:00 level=INFO msg="netstack: guest link aa:bb:cc:dd:ee:ff closed: cannot receive packets from , disconnecting"
`
	lines, _, err := ScanLog(strings.NewReader(l))
	if err != nil {
		t.Fatalf("ScanLog returned error: %v", err)
	}
	var got string
	for _, ln := range lines {
		if ln.MAC != "" {
			got = ln.MAC
		}
	}
	if got != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC = %q, want %q", got, "aa:bb:cc:dd:ee:ff")
	}
}

func TestScanLogNoListeningLineKeepsNothing(t *testing.T) {
	// Without a daemon-start marker we cannot establish a cutoff, so we must
	// return no lines rather than risk convicting on stale evidence.
	const l = `time=2026-07-19T22:25:49.262+03:00 level=INFO msg="netstack: guest link b2:71:f2:cb:76:64 closed: cannot receive packets"
`
	lines, start, err := ScanLog(strings.NewReader(l))
	if err != nil {
		t.Fatalf("ScanLog returned error: %v", err)
	}
	if !start.IsZero() {
		t.Errorf("start = %v, want zero", start)
	}
	if len(lines) != 0 {
		t.Errorf("len(lines) = %d, want 0", len(lines))
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/doctor/ -run TestScanLog -v`
Expected: FAIL — `undefined: ScanLog`.

- [x] **Step 3: Implement the scanner**

```go
package doctor

import (
	"bufio"
	"io"
	"regexp"
	"strings"
	"time"
)

var (
	// time=2026-07-19T22:25:49.262+03:00  and  time="2026-07-19T22:25:49+03:00"
	timeRe = regexp.MustCompile(`time="?([0-9T:.+\-]+)"?`)
	macRe  = regexp.MustCompile(`guest link ([0-9a-fA-F:]{17}) closed`)
)

// daemonStartMarker is logged once per umbrad start. It is the only reliable
// in-band signal of when the current daemon lifetime began.
const daemonStartMarker = "umbrad listening"

// ScanLog parses umbrad.err.log and returns only the lines belonging to the
// CURRENT daemon lifetime, along with when that lifetime started.
//
// The cutoff is not an optimization — it is a correctness requirement. The
// log survives daemon restarts and host reboots, so lines from an already-fixed
// fault sit in the file indefinitely. Without the cutoff, a healthy host reports
// a dead netstack forever.
//
// When no start marker is present we cannot establish a cutoff, so we return
// nothing rather than risk convicting on stale evidence.
func ScanLog(r io.Reader) ([]LogLine, time.Time, error) {
	var all []LogLine
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		text := sc.Text()
		m := timeRe.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, m[1])
		if err != nil {
			continue
		}
		l := LogLine{Time: ts, Text: text}
		if mm := macRe.FindStringSubmatch(text); mm != nil {
			l.MAC = mm[1]
		}
		all = append(all, l)
	}
	if err := sc.Err(); err != nil {
		return nil, time.Time{}, err
	}

	// Find the LAST start marker — that is the current lifetime.
	startIdx := -1
	for i, l := range all {
		if strings.Contains(l.Text, daemonStartMarker) {
			startIdx = i
		}
	}
	if startIdx < 0 {
		return nil, time.Time{}, nil
	}
	return all[startIdx:], all[startIdx].Time, nil
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/doctor/ -run TestScanLog -v`
Expected: PASS (3 tests).

- [x] **Step 5: Commit**

```bash
git add internal/doctor/logscan.go internal/doctor/logscan_test.go
git commit -m "feat(doctor): umbrad log scanner with daemon-lifetime cutoff"
```

---

### Task 3: Classify — host-wide rungs (daemon down, netstack death)

**Files:**
- Create: `internal/doctor/classify.go`
- Test: `internal/doctor/classify_test.go`

**Interfaces:**
- Consumes: `Evidence`, `Verdict`, `Rung`, `LogLine`, `GuestEvidence` from Task 1.
- Produces: `Classify(e Evidence) []Verdict`. Later tasks extend this same function; they do not add new entry points.

- [x] **Step 1: Write the failing tests**

```go
package doctor

import (
	"testing"
	"time"
)

func TestClassifyDaemonDownTerminatesLadder(t *testing.T) {
	got := Classify(Evidence{
		DaemonUp: false,
		Guests:   []GuestEvidence{{Name: "fwb-ci5", State: "running", IP: ""}},
	})
	if len(got) != 1 {
		t.Fatalf("len(verdicts) = %d, want 1 (daemon-down must terminate the ladder)", len(got))
	}
	if got[0].Rung != RungDaemonDown {
		t.Errorf("Rung = %v, want RungDaemonDown", got[0].Rung)
	}
	if got[0].NextAction == "" {
		t.Error("NextAction is empty; every failing verdict must carry one")
	}
}

func TestClassifyNetstackDeathNeedsTwoMACsAndLiveFailure(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", IP: "", SSHProbed: true, SSHOK: false},
		},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungNetstackDead {
		t.Fatalf("verdicts = %+v, want a single RungNetstackDead", got)
	}
}

// THE STALE-LOG TRAP. Log lines look damning but every guest is live and
// healthy — this must NOT convict. Regression guard for the exact false
// positive that would otherwise fire on a post-power-cycle host.
func TestClassifyIgnoresNetstackLogLinesWhenGuestsAreReachable(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", IP: "192.168.127.10", SSHProbed: true, SSHOK: true},
		},
	}
	for _, v := range Classify(e) {
		if v.Rung == RungNetstackDead {
			t.Fatal("convicted netstack-dead on log evidence alone while guests were reachable")
		}
	}
}

// Health is a string enum, so its zero value ("") is a fourth, undocumented
// state that would marshal into --json as "health":"". Every verdict must set
// it explicitly; this guard fails loudly if any future rung forgets.
func TestClassifyAlwaysSetsHealth(t *testing.T) {
	cases := []Evidence{
		{DaemonUp: false},
		{DaemonUp: true, Guests: []GuestEvidence{{Name: "g", State: "running", IP: ""}}},
		{DaemonUp: true, Guests: []GuestEvidence{{Name: "g", State: "running", IP: "10.0.0.1", SSHProbed: true, SSHOK: false}}},
	}
	for i, e := range cases {
		for _, v := range Classify(e) {
			if v.Health == "" {
				t.Errorf("case %d: verdict %v has empty Health", i, v.Rung)
			}
		}
	}
}

func TestClassifyHealthyHostReportsNoFailures(t *testing.T) {
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: time.Now(),
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", IP: "192.168.127.10", SSHProbed: true, SSHOK: true},
		},
	}
	got := Classify(e)
	// Assert on the slice itself, not just its contents: a healthy host emits
	// NO verdicts at all, so a range-only check would pass vacuously and keep
	// passing even if the ladder stopped working entirely.
	if len(got) != 0 {
		t.Fatalf("healthy host produced %d verdict(s), want 0: %+v", len(got), got)
	}
	for _, v := range got {
		if v.Health == Fail {
			t.Errorf("healthy host produced a failing verdict: %+v", v)
		}
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/doctor/ -run TestClassify -v`
Expected: FAIL — `undefined: Classify`.

- [x] **Step 3: Implement Classify with the host-wide rungs**

```go
package doctor

import "fmt"

// Classify turns observed Evidence into ordered findings. It is pure: no I/O,
// no clock, no filesystem. That is what lets every rung be tested with a
// literal Evidence struct instead of a deliberately broken host.
//
// Rungs 0-1 are host-wide and TERMINATE the ladder — when the daemon is down
// or the netstack is dead, nothing below can be diagnosed meaningfully and
// reporting lower rungs would just be noise pointing at the wrong fix.
func Classify(e Evidence) []Verdict {
	if !e.DaemonUp {
		return []Verdict{{
			Rung:       RungDaemonDown,
			Health:     Fail,
			Reason:     "umbrad is not responding on its API socket",
			NextAction: "umbra daemon install",
		}}
	}

	if v, ok := classifyNetstack(e); ok {
		return []Verdict{v}
	}

	return classifyGuests(e)
}

// classifyNetstack detects host-wide netstack death.
//
// Two conditions are BOTH required: netstack errors across at least two
// distinct guest MACs, AND at least one running guest that is actually
// unreachable right now. Log evidence alone never convicts — umbrad.err.log
// outlives the fault it recorded, so a healthy host can carry damning-looking
// lines indefinitely. ScanLog already trims to the current daemon lifetime;
// this is the second, independent guard.
func classifyNetstack(e Evidence) (Verdict, bool) {
	macs := map[string]bool{}
	for _, l := range e.LogLines {
		if l.MAC != "" {
			macs[l.MAC] = true
		}
	}
	if len(macs) < 2 {
		return Verdict{}, false
	}
	if !anyRunningGuestUnreachable(e) {
		return Verdict{}, false
	}
	return Verdict{
		Rung:   RungNetstackDead,
		Health: Fail,
		Reason: fmt.Sprintf("netstack errors across %d guest MACs and at least one running guest is unreachable", len(macs)),
		Evidence: []string{
			fmt.Sprintf("%d distinct MACs with 'cannot receive packets' since daemon start", len(macs)),
			"live reachability check failed for a running guest",
		},
		NextAction: "cd ~/Desktop/projects/umbra && make build && make install",
	}, true
}

func anyRunningGuestUnreachable(e Evidence) bool {
	for _, g := range e.Guests {
		if g.State != "running" {
			continue
		}
		if g.IP == "" {
			return true
		}
		if g.SSHProbed && !g.SSHOK {
			return true
		}
	}
	return false
}

// classifyGuests is filled in by Task 4. Declared here so Task 3 compiles.
func classifyGuests(e Evidence) []Verdict { return nil }
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/doctor/ -run TestClassify -v`
Expected: PASS (4 tests).

- [x] **Step 5: Commit**

```bash
git add internal/doctor/classify.go internal/doctor/classify_test.go
git commit -m "feat(doctor): classify host-wide rungs with stale-log guard"
```

---

### Task 4: Classify — per-guest rungs and the two-guest discriminator

**Files:**
- Modify: `internal/doctor/classify.go` (replace the `classifyGuests` stub)
- Test: `internal/doctor/classify_test.go` (append)

**Interfaces:**
- Consumes: `classifyGuests(e Evidence) []Verdict` stub from Task 3.
- Produces: same signature, now returning per-guest verdicts for `RungGuestNoIP`, `RungGuestSSHStall`, `RungRunnerServiceDown`, `RungHostHardware`.

- [x] **Step 1: Write the failing tests**

```go
func TestClassifySingleGuestNoIPSuggestsRecreate(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests:   []GuestEvidence{{Name: "fwb-ci5", State: "running", IP: ""}},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungGuestNoIP {
		t.Fatalf("verdicts = %+v, want one RungGuestNoIP", got)
	}
	if got[0].Subject != "fwb-ci5" {
		t.Errorf("Subject = %q, want %q", got[0].Subject, "fwb-ci5")
	}
}

// THE TWO-GUEST DISCRIMINATOR. Two independent guests failing identically is
// host-level, not two coincidentally damaged images — and it rules out a
// ~20-minute recreate in about 2 minutes.
func TestClassifyTwoGuestsNoIPIsHostLevel(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", IP: ""},
			{Name: "fwb-ci2", State: "running", IP: "", Spare: true},
		},
	}
	got := Classify(e)
	if len(got) != 1 {
		t.Fatalf("len(verdicts) = %d, want 1 host-level verdict", len(got))
	}
	if got[0].Rung != RungHostHardware {
		t.Errorf("Rung = %v, want RungHostHardware", got[0].Rung)
	}
	if got[0].Subject != "" {
		t.Errorf("Subject = %q, want empty (host-wide)", got[0].Subject)
	}
}

// Closes a coverage gap found reviewing Task 3: anyRunningGuestUnreachable
// has two legs (no IP, and probed-but-ssh-failed) and only the first was
// exercised. A guest that HAS an IP but whose ssh is dead is a real netstack
// partial-failure shape, so rung 1 must still convict on it.
func TestClassifyNetstackConvictsWhenSSHFailsDespiteIP(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", IP: "192.168.127.10", SSHProbed: true, SSHOK: false},
		},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungNetstackDead {
		t.Fatalf("verdicts = %+v, want a single RungNetstackDead", got)
	}
}

func TestClassifySSHStall(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", IP: "192.168.127.10", SSHProbed: true, SSHOK: false},
		},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungGuestSSHStall {
		t.Fatalf("verdicts = %+v, want one RungGuestSSHStall", got)
	}
}

func TestClassifyInactiveRunnerUnit(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests: []GuestEvidence{{
			Name: "fwb-ci5", State: "running", IP: "192.168.127.10",
			SSHProbed: true, SSHOK: true,
			Runners: []RunnerEvidence{
				{Unit: "actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-1.service", Active: false},
			},
		}},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungRunnerServiceDown {
		t.Fatalf("verdicts = %+v, want one RungRunnerServiceDown", got)
	}
}

// The bottom rung: stock arm64 binaries taking CPU-level signals means the
// guest is miscomputing. No amount of RAM/CPU tuning fixes that.
func TestClassifyLoadCanaryFaultIsHostHardware(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		DeepRun:  true,
		Guests: []GuestEvidence{{
			Name: "fwb-ci5", State: "running", IP: "192.168.127.10",
			SSHProbed: true, SSHOK: true,
			LoadCanary: CanaryResult{Ran: true, Faulted: true, Detail: "curl exited 132 (SIGILL)"},
		}},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungHostHardware {
		t.Fatalf("verdicts = %+v, want one RungHostHardware", got)
	}
	if got[0].NextAction == "" {
		t.Error("host-hardware verdict must carry the power-cycle next action")
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/doctor/ -run TestClassify -v`
Expected: FAIL — the new tests get zero verdicts (stub returns nil).

- [x] **Step 3: Replace the `classifyGuests` stub**

Delete the stub line `func classifyGuests(e Evidence) []Verdict { return nil }` and add:

```go
// classifyGuests walks the per-guest rungs in blast-radius order, then
// appends the per-repo rungs from Task 5.
func classifyGuests(e Evidence) []Verdict {
	var out []Verdict

	// Two-guest discriminator, evaluated BEFORE the per-guest rungs: if two
	// independent guests fail to obtain an IP, the fault is host-level and
	// recreating either image is wasted effort.
	var noIP []string
	for _, g := range e.Guests {
		if g.State == "running" && g.IP == "" {
			noIP = append(noIP, g.Name)
		}
	}
	if len(noIP) >= 2 {
		return []Verdict{{
			Rung:       RungHostHardware,
			Health:     Fail,
			Reason:     "two independent guests failed to obtain an IP — host-level, not guest-image",
			Evidence:   []string{fmt.Sprintf("guests with no IP: %v", noIP)},
			NextAction: "full power-cycle (shut down, wait, power on) then run Apple Diagnostics — do NOT recreate guests on this boot",
		}}
	}

	// Load canary faults are also host-level and outrank everything per-guest:
	// stock arm64 binaries taking SIGILL/SIGSEGV means the guest is miscomputing.
	for _, g := range e.Guests {
		if g.LoadCanary.Ran && g.LoadCanary.Faulted {
			return []Verdict{{
				Rung:       RungHostHardware,
				Health:     Fail,
				Reason:     "native binaries crashed with CPU-level signals under load",
				Evidence:   []string{fmt.Sprintf("%s: %s", g.Name, g.LoadCanary.Detail)},
				NextAction: "full power-cycle (shut down, wait, power on) then run Apple Diagnostics — config changes cannot fix miscomputing hardware",
			}}
		}
	}

	for _, g := range e.Guests {
		if g.State != "running" {
			continue
		}
		switch {
		case g.IP == "":
			out = append(out, Verdict{
				Rung: RungGuestNoIP, Health: Fail, Subject: g.Name,
				Reason:     "machine reports running but has no IP",
				NextAction: fmt.Sprintf("umbra doctor --deep (confirm host-level first), else: umbra stop %s && umbra rm %s && umbra create %s --role ci-runner --cpus 3 --memory-gib 3 --disk-gib 60 --autostart", g.Name, g.Name, g.Name),
			})
			continue
		case g.SSHProbed && !g.SSHOK:
			out = append(out, Verdict{
				Rung: RungGuestSSHStall, Health: Fail, Subject: g.Name,
				Reason:     "guest has an IP but ssh is not accepting connections",
				NextAction: fmt.Sprintf("umbra stop %s && umbra start %s, then wait for: umbra exec %s cloud-init status", g.Name, g.Name, g.Name),
			})
			continue
		}
		for _, r := range g.Runners {
			if !r.Active {
				out = append(out, Verdict{
					Rung: RungRunnerServiceDown, Health: Fail, Subject: g.Name,
					Reason:     fmt.Sprintf("runner unit %s is not active", r.Unit),
					NextAction: fmt.Sprintf("umbra exec %s sudo systemctl restart %s", g.Name, r.Unit),
				})
			}
		}
	}

	return append(out, classifyRepos(e)...)
}

// classifyRepos is filled in by Task 5. Declared here so Task 4 compiles.
func classifyRepos(e Evidence) []Verdict { return nil }
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/doctor/ -v`
Expected: PASS (all tests from Tasks 2-4).

- [x] **Step 5: Commit**

```bash
git add internal/doctor/classify.go internal/doctor/classify_test.go
git commit -m "feat(doctor): per-guest rungs and two-guest discriminator"
```

---

### Task 5: Classify — GitHub rungs with `unknown` propagation

**Files:**
- Modify: `internal/doctor/classify.go` (replace the `classifyRepos` stub)
- Test: `internal/doctor/classify_test.go` (append)

**Interfaces:**
- Consumes: `classifyRepos(e Evidence) []Verdict` stub from Task 4, `RepoEvidence` from Task 1.
- Produces: same signature, returning `RungRunnerOffline` and `RungBillingLockout` verdicts, plus `Unknown`-health verdicts when `gh` was unavailable.

- [x] **Step 1: Write the failing tests**

```go
func TestClassifyStaleRunnerRegistration(t *testing.T) {
	e := Evidence{
		DaemonUp: true, GHAvailable: true,
		Repos: []RepoEvidence{{
			Repo: "ForceAI-KW/whatsapp-broadcaster", Probed: true,
			RunnerOnline: map[string]bool{"fwb-ci5-1": false},
		}},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungRunnerOffline {
		t.Fatalf("verdicts = %+v, want one RungRunnerOffline", got)
	}
}

func TestClassifyBillingLockout(t *testing.T) {
	e := Evidence{
		DaemonUp: true, GHAvailable: true,
		Repos: []RepoEvidence{{
			Repo: "ForceAI-KW/force-website-builder", Probed: true,
			RunnerOnline:   map[string]bool{"fwb-ci5-1": true},
			BillingLockout: true,
		}},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungBillingLockout {
		t.Fatalf("verdicts = %+v, want one RungBillingLockout", got)
	}
}

// An absent probe must never be reportable as health. Silence is not success.
func TestClassifyMissingGHIsUnknownNotPass(t *testing.T) {
	e := Evidence{
		DaemonUp: true, GHAvailable: false,
		Repos: []RepoEvidence{{Repo: "ForceAI-KW/force-website-builder", Probed: false}},
	}
	got := Classify(e)
	if len(got) != 1 {
		t.Fatalf("len(verdicts) = %d, want 1", len(got))
	}
	if got[0].Health != Unknown {
		t.Errorf("Health = %q, want %q", got[0].Health, Unknown)
	}
	if got[0].Health == Pass {
		t.Error("an unprobed repo was reported as passing")
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/doctor/ -run TestClassify -v`
Expected: FAIL — new tests get zero verdicts (stub returns nil).

- [x] **Step 3: Replace the `classifyRepos` stub**

Delete the stub line `func classifyRepos(e Evidence) []Verdict { return nil }` and add:

```go
// classifyRepos covers the GitHub-side rungs. These matter because a perfectly
// healthy umbra host still leaves every PR blocked when the runner registration
// is stale or the org is billing-locked — and all three states look alike from
// the outside: billing failures die in ~3s, offline runners queue forever, and
// netstack death queues AND makes guests unreachable.
func classifyRepos(e Evidence) []Verdict {
	var out []Verdict
	for _, r := range e.Repos {
		if !r.Probed {
			out = append(out, Verdict{
				Rung: RungRunnerOffline, Health: Unknown, Subject: r.Repo,
				Reason:     "could not probe GitHub (gh missing, unauthenticated, or rate-limited)",
				NextAction: "install and authenticate the GitHub CLI: brew install gh && gh auth login",
			})
			continue
		}
		if r.BillingLockout {
			out = append(out, Verdict{
				Rung: RungBillingLockout, Health: Fail, Subject: r.Repo,
				Reason:     "jobs are failing in ~3s with no runner assigned and zero steps",
				Evidence:   []string{"signature: runner_name empty, steps empty, ~3s duration"},
				NextAction: "clear the org billing block: GitHub org -> Settings -> Billing & plans (only the org owner can do this)",
			})
			continue
		}
		for name, online := range r.RunnerOnline {
			if !online {
				out = append(out, Verdict{
					Rung: RungRunnerOffline, Health: Fail, Subject: r.Repo,
					Reason:     fmt.Sprintf("registered runner %q is offline", name),
					NextAction: fmt.Sprintf("umbra runner add <machine> --repo %s, then delete the stale registration via: gh api --method DELETE repos/%s/actions/runners/<id>", r.Repo, r.Repo),
				})
			}
		}
	}
	return out
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/doctor/ -v`
Expected: PASS (all tests).

- [x] **Step 5: Commit**

```bash
git add internal/doctor/classify.go internal/doctor/classify_test.go
git commit -m "feat(doctor): github rungs with unknown-not-pass propagation"
```

---

### Task 6: Evidence collection and the CLI

**Files:**
- Create: `cmd/umbra/doctor.go`
- Modify: `cmd/umbra/root.go` (register the command, wire the exit-code sentinel)
- Test: `cmd/umbra/doctor_test.go`

**Where collection lives:** in `cmd/umbra/doctor.go`, not in `internal/doctor`. This follows the repo's existing pattern — `stats.go` and `runner.go` both do their ssh collection in `cmd/umbra` — and keeps `internal/doctor` free of cobra and client plumbing. That purity is the whole reason `Classify` is testable, so it is worth protecting.

**Interfaces:**
- Consumes: `Classify`, `ScanLog`, `Evidence` and friends from Tasks 1-5; `sshArgs(mv *client.MachineView, remoteCmd []string) []string` from `cmd/umbra/shell.go:28`.
- Produces: `doctorCmd` cobra command with `--json` and `--deep`; `probeGuest`, `probeRepo`, `runCanary` helpers.

**Note on `--deep`:** this task wires the flag and the load canary, and that is `--deep`'s entire scope. The spare-guest boot is not "deferred to Step 5" — it was **cut**, because booting a guest is a mutation a diagnose-only tool must not perform. (Superseded: the original text also claimed `Classify` returns the host-level verdict "whenever two guests lack an IP". It no longer does. Two guests without a readiness-confirmed IP is the normal state of any host for ~90s after boot, so that signal now requires the load canary to corroborate it and is otherwise `unknown`.)

- [x] **Step 1: Write the failing CLI test**

```go
package main

import (
	"strings"
	"testing"
)

func TestDoctorCanaryScriptCoversBothSignalCanaries(t *testing.T) {
	s := canaryScript
	for _, want := range []string{"curl --version", "openssl", "RC=$?"} {
		if !strings.Contains(s, want) {
			t.Errorf("canaryScript missing %q", want)
		}
	}
	// The canary must be bounded — an unbounded stress loop on a suspect host
	// is exactly the wrong thing to leave running.
	if !strings.Contains(s, "seq 1 ") {
		t.Error("canaryScript is not bounded by a fixed iteration count")
	}
}

func TestDoctorCanaryDetectsSignalExitCodes(t *testing.T) {
	// 132 = SIGILL, 139 = SIGSEGV; both are the decisive host-hardware signature.
	for _, c := range []struct {
		out  string
		want bool
	}{
		{"FAULT rc=132\n", true},
		{"FAULT rc=139\n", true},
		{"ok\n", false},
	} {
		got := canaryFaulted(c.out)
		if got != c.want {
			t.Errorf("canaryFaulted(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}
```

- [x] **Step 2: Run to verify it fails**

Run: `go test ./cmd/umbra/ -run TestDoctor -v`
Expected: FAIL — `undefined: canaryScript`, `undefined: canaryFaulted`.

- [x] **Step 3: Create the CLI**

```go
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/doctor"
	"github.com/ForceAI-KW/umbra/internal/paths"
)

var (
	doctorJSON bool
	doctorDeep bool
)

// canaryScript is the bounded native-binary load canary. curl and openssl are
// correct-arch system binaries with zero Rosetta ambiguity, so a CPU-level
// signal from either means the guest is miscomputing — a host fault, not a
// config problem. Bounded on purpose: never leave stress running on a suspect host.
const canaryScript = `set +e
for i in $(seq 1 150); do
  curl --version >/dev/null 2>&1; RC=$?
  [ $RC -ne 0 ] && echo "FAULT rc=$RC"
done
for j in 1 2 3 4; do
  ( for i in $(seq 1 800); do openssl sha256 /usr/bin/curl >/dev/null 2>&1; RC=$?
      [ $RC -ne 0 ] && echo "FAULT rc=$RC"
    done ) &
done
wait
echo CANARY_DONE
`

// canaryFaulted reports whether the canary saw a CPU-level signal. Exit codes
// 132 (SIGILL) and 139 (SIGSEGV) are the decisive host-hardware signature.
func canaryFaulted(out string) bool {
	return strings.Contains(out, "FAULT rc=132") || strings.Contains(out, "FAULT rc=139")
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose umbra/CI faults and print the next action",
	Long: "Classifies host, guest and CI faults into one rung of the umbra triage ladder.\n" +
		"Read-only by default. --deep additionally runs a bounded native-binary load\n" +
		"canary, which is the only way to detect a host-hardware fault.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ev := doctor.Evidence{DeepRun: doctorDeep}

		if err := apiClient.Ping(cmd.Context()); err == nil {
			ev.DaemonUp = true
		}

		if f, err := os.Open(paths.Logs() + "/umbrad.err.log"); err == nil {
			defer f.Close()
			lines, start, err := doctor.ScanLog(f)
			if err == nil {
				ev.LogLines, ev.DaemonStart = lines, start
			}
		}

		if ev.DaemonUp {
			machines, err := apiClient.ListMachines(cmd.Context())
			if err != nil {
				return err
			}
			for i := range machines {
				ev.Guests = append(ev.Guests, probeGuest(cmd, &machines[i]))
			}
		}

		_, ghErr := exec.LookPath("gh")
		ev.GHAvailable = ghErr == nil

		verdicts := doctor.Classify(ev)

		if doctorJSON {
			if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
				"deep":     doctorDeep,
				"verdicts": verdicts,
			}); err != nil {
				return err
			}
		} else {
			printVerdicts(verdicts)
		}

		for _, v := range verdicts {
			if v.Health == doctor.Fail {
				return errFaultsFound
			}
		}
		return nil
	},
}

// errFaultsFound signals "diagnosis succeeded and found faults" — distinct
// from "the command itself failed". main maps it to exit 1 without printing
// a spurious error, so deferred cleanup still runs and cobra stays in charge
// of the error path.
var errFaultsFound = errors.New("faults found")

func printVerdicts(vs []doctor.Verdict) {
	if len(vs) == 0 {
		fmt.Println("healthy: no faults detected")
		return
	}
	for _, v := range vs {
		subject := v.Subject
		if subject == "" {
			subject = "host"
		}
		fmt.Printf("[%s] %s (%s)\n  %s\n", v.Health, v.Rung, subject, v.Reason)
		for _, e := range v.Evidence {
			fmt.Printf("  evidence: %s\n", e)
		}
		if v.NextAction != "" {
			fmt.Printf("  next: %s\n", v.NextAction)
		}
	}
}

// probeGuest gathers per-guest evidence over the same ssh path shell/exec use.
// Every probe failure degrades that field rather than aborting the diagnosis —
// one unreachable guest must not blind us to the rest of the host.
func probeGuest(cmd *cobra.Command, mv *client.MachineView) doctor.GuestEvidence {
	g := doctor.GuestEvidence{Name: mv.Name, State: string(mv.State), IP: mv.IP}
	if mv.State != "running" || mv.SSHPort == 0 {
		return g
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return g
	}

	g.SSHProbed = true
	args := sshArgs(mv, []string{"true"})
	if err := exec.CommandContext(cmd.Context(), sshPath, args[1:]...).Run(); err == nil {
		g.SSHOK = true
	}
	if !g.SSHOK {
		return g
	}

	uArgs := sshArgs(mv, []string{"systemctl", "list-units", `'actions.runner.*'`, "--no-legend", "--plain"})
	if out, err := exec.CommandContext(cmd.Context(), sshPath, uArgs[1:]...).CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) < 4 || !strings.HasPrefix(f[0], "actions.runner.") {
				continue
			}
			g.Runners = append(g.Runners, doctor.RunnerEvidence{Unit: f[0], Active: f[2] == "active"})
		}
	}

	if doctorDeep {
		cArgs := sshArgs(mv, []string{"bash", "-s"})
		c := exec.CommandContext(cmd.Context(), sshPath, cArgs[1:]...)
		c.Stdin = strings.NewReader(canaryScript)
		out, _ := c.CombinedOutput()
		g.LoadCanary = doctor.CanaryResult{Ran: true, Faulted: canaryFaulted(string(out))}
		if g.LoadCanary.Faulted {
			g.LoadCanary.Detail = "native binary exited with SIGILL/SIGSEGV under load"
		}
	}
	return g
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "JSON output (watchdog probe)")
	doctorCmd.Flags().BoolVar(&doctorDeep, "deep", false, "also run the bounded native-binary load canary (mutating, ~60s)")
}
```

- [x] **Step 4: Register the command and wire the exit code**

In `cmd/umbra/root.go`, append `doctorCmd` to the existing single-line `rootCmd.AddCommand(...)` call (it currently ends `..., pruneCmd, statsCmd)`).

Then teach `execute()` about the sentinel, so a found fault exits 1 without printing a spurious `error:` line. Add `"errors"` to root.go's import block and change the error branch:

```go
func execute() int {
	rootCmd.AddCommand(createCmd, listCmd, startCmd, stopCmd, rmCmd, shellCmd, execCmd, statusCmd, forwardCmd, dockerCmd, daemonCmd, rosettaCmd, setCmd, snapshotCmd, snapshotsCmd, restoreCmd, exportCmd, importCmd, runnerCmd, pruneCmd, statsCmd, doctorCmd)
	if err := rootCmd.Execute(); err != nil {
		// doctor reports faults through the exit code; the findings are
		// already on stdout, so an "error:" line would be noise.
		if errors.Is(err, errFaultsFound) {
			return 1
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
```

- [x] **Step 5: Run tests, build, and verify live**

```bash
go test ./... && go vet ./... && make build
./bin/umbra doctor
./bin/umbra doctor --json
```

Expected: tests PASS, build succeeds. On the current healthy host, `doctor` prints either `healthy: no faults detected` or genuine findings for the stale `gcc-horse-market` / `Force-Media-CRM` runner registrations. It must **not** report `netstack-dead`, despite `umbrad.err.log` containing pre-restart netstack lines — that is the stale-log guard proving itself against real data.

- [x] **Step 6: Commit**

```bash
git add cmd/umbra/doctor.go cmd/umbra/doctor_test.go cmd/umbra/root.go
git commit -m "feat(doctor): evidence collection, load canary, and CLI"
```

---

### Task 7: Documentation parity

**Files:**
- Modify: `README.md` (command table)
- Modify: `VERSION`
- Modify: `docs/superpowers/plans/2026-07-19-umbra-doctor.md` (tick boxes)

**Interfaces:**
- Consumes: the finished `doctor` command.
- Produces: no code.

- [x] **Step 1: Add doctor to the README command table**

Add a row matching the existing table's format:

```markdown
| `umbra doctor [--deep] [--json]` | Diagnose host/guest/CI faults and print the next action |
```

- [x] **Step 2: Bump VERSION**

Change `VERSION` from `0.7.0` to `0.8.0` (`make app` reads it).

- [x] **Step 3: Full gate**

Run: `go test ./... && go vet ./... && make build`
Expected: all PASS.

- [x] **Step 4: Commit**

```bash
git add README.md VERSION docs/superpowers/plans/2026-07-19-umbra-doctor.md
git commit -m "docs: v0.8.0 umbra doctor command reference"
```

- [x] **Step 5: Blast-radius sweep**

Grep the repo and sibling configs for every identifier this feature introduces or touches — `doctor`, `canaryScript`, `ScanLog`, `Classify`, `umbrad listening`, `VERSION` — across code, CI workflows, launchd plists, the ops watchdog, and `.env.example`. State the result in the final reply in the required form. Specifically confirm whether the ops watchdog that consumes `umbra status --json` should also consume `umbra doctor --json`; if so, that is a follow-up in the OS repo, not silently skipped.

---

## Self-Review

**Spec coverage:** every spec section maps to a task — architecture split (T1-T6), all 8 rungs (T3 rungs 0-1, T4 rungs 2-4 + 7, T5 rungs 5-6), `--deep` discriminator (T4 classification + T6 canary), stale-log trap (T2 + T3, two independent guards), error handling / `unknown` (T5), testing (each task's tests), exit code (T6), out-of-scope items (none implemented).

**Placeholder scan:** no TBD/TODO; every code step carries complete code; every test step carries real assertions.

**Type consistency:** `Evidence`/`GuestEvidence`/`RepoEvidence`/`RunnerEvidence`/`CanaryResult`/`LogLine`/`Verdict`/`Rung`/`Health` are declared once in T1 and used with identical field names throughout. `Classify` → `classifyNetstack` → `classifyGuests` → `classifyRepos` chain is stubbed forward so each task compiles independently.

**Known gap, deliberate — SUPERSEDED, retained to show what changed.** This originally read: "`--deep` does not yet auto-boot a stopped spare guest ... the discriminator works whenever the spare happens to be running", framing the boot as a follow-up. Both halves are now wrong:

- The spare-guest boot was **cut, not deferred**. `--deep` runs the load canary and nothing else; booting the spare is a mutation and the ops watchdog treats the spare being stopped as a steady-state invariant.
- The **uncorroborated two-guest discriminator was removed.** "Two guests lack an IP" is the normal state of every host for the ~90s readiness window and after any two restarts — it produced an Apple-Diagnostics verdict for a host whose real fault was memory overcommit. The signal now requires load-canary corroboration and is otherwise `unknown`.
