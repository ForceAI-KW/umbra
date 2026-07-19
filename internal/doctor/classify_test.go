package doctor

import (
	"strings"
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

// Updated for C1: the gate is now two CORRELATED guests — each running,
// unreachable, and named by a netstack line in this daemon lifetime — not two
// MACs appearing anywhere in the log. The old single-guest form of this test
// asserted the very behaviour C1 removed.
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
			// Both hold IPs, so the two-guest no-IP discriminator does not fire
			// and this genuinely exercises the netstack rung.
			{Name: "fwb-ci5", State: "running", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.127.10", SSHProbed: true, SSHOK: false},
			{Name: "fwb-ci2", State: "running", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.127.11", SSHProbed: true, SSHOK: false},
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

// Closes a coverage gap found reviewing Task 3: the unreachability test has
// two legs (no IP, and probed-but-ssh-failed) and only the first was
// exercised. A guest that HAS an IP but whose ssh is dead is a real netstack
// partial-failure shape, so rung 1 must still convict on it — now via the C1
// correlation, with a mixed pair covering both legs at once.
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
			// ci5 has an IP but dead ssh; ci2 never got an IP. One of each, so
			// the two-guest no-IP discriminator does not fire.
			{Name: "fwb-ci5", State: "running", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.127.10", SSHProbed: true, SSHOK: false},
			{Name: "fwb-ci2", State: "running", MAC: "aa:bb:cc:dd:ee:02", IP: ""},
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

// Go randomises map iteration order, so a repo with several offline runners
// would otherwise report them differently on every run. Classify promises
// ordered findings, and --json output that reshuffles between identical runs
// is both a bad diff and a flaky test waiting to happen.
func TestClassifyOfflineRunnerOrderIsDeterministic(t *testing.T) {
	e := Evidence{
		DaemonUp: true, GHAvailable: true,
		Repos: []RepoEvidence{{
			Repo: "ForceAI-KW/force-website-builder", Probed: true,
			RunnerOnline: map[string]bool{"zeta-1": false, "alpha-1": false, "mid-1": false},
		}},
	}
	var first []string
	for run := 0; run < 20; run++ {
		var got []string
		for _, v := range Classify(e) {
			got = append(got, v.Reason)
		}
		if len(got) != 3 {
			t.Fatalf("run %d: got %d verdicts, want 3", run, len(got))
		}
		if first == nil {
			first = got
			continue
		}
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("run %d: verdict order changed at %d: %q != %q", run, i, got[i], first[i])
			}
		}
	}
	// Sorted order means alpha-1 first, zeta-1 last.
	if !strings.Contains(first[0], "alpha-1") || !strings.Contains(first[2], "zeta-1") {
		t.Errorf("verdicts not in sorted order: %v", first)
	}
}

// ---------------------------------------------------------------------------
// C1: the netstack gate must CORRELATE log MACs with live guests.
// ---------------------------------------------------------------------------

// THE C1 REGRESSION. "guest link <MAC> closed: cannot receive packets" is
// level=INFO and is emitted on EVERY NORMAL GUEST SHUTDOWN, so a long-lived
// daemon accumulates several distinct MACs as a matter of routine. A host whose
// only current fault is one guest still running cloud-init must NOT be
// diagnosed as host netstack death and told to rebuild umbra.
func TestClassifyRoutineMultiMACLogDoesNotConvictBootingGuest(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Hour),
		LogLines: []LogLine{
			// Both MACs belong to guests that were stopped normally hours ago.
			{Time: now.Add(-50 * time.Minute), Text: "guest link closed", MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now.Add(-40 * time.Minute), Text: "guest link closed", MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			// A third, different guest, currently booting: no IP yet.
			{Name: "fwb-ci5", State: "running", MAC: "aa:bb:cc:dd:ee:09", IP: ""},
		},
	}
	got := Classify(e)
	for _, v := range got {
		if v.Rung == RungNetstackDead {
			t.Fatalf("convicted netstack-dead from routine shutdown log lines: %+v", v)
		}
	}
	if len(got) != 1 || got[0].Rung != RungGuestNoIP {
		t.Fatalf("verdicts = %+v, want a single RungGuestNoIP (the booting guest)", got)
	}
}

// One correlated guest is not enough: a single guest whose link died is a
// per-guest fault, not evidence that the host netstack is dead.
func TestClassifyNetstackNeedsTwoCorrelatedGuests(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			// Only ci5 is both running-unreachable AND named in the log.
			{Name: "fwb-ci5", State: "running", MAC: "aa:bb:cc:dd:ee:01", IP: ""},
			{Name: "fwb-ci2", State: "stopped", MAC: "aa:bb:cc:dd:ee:02"},
		},
	}
	for _, v := range Classify(e) {
		if v.Rung == RungNetstackDead {
			t.Fatalf("convicted netstack-dead on a single correlated guest: %+v", v)
		}
	}
}

// The real netstack death: two running guests, both unreachable, both named in
// the current lifetime's log.
func TestClassifyNetstackConvictsOnTwoCorrelatedGuests(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.127.10", SSHProbed: true, SSHOK: false},
			{Name: "fwb-ci2", State: "running", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.127.11", SSHProbed: true, SSHOK: false},
		},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungNetstackDead {
		t.Fatalf("verdicts = %+v, want a single RungNetstackDead", got)
	}
	if got[0].Health != Fail || got[0].NextAction == "" {
		t.Errorf("verdict = %+v, want Fail with a NextAction", got[0])
	}
}

// THE WAVE-2 CONTRACT. If no guest carries a MAC, the correlation the netstack
// rung depends on cannot be performed. That must be visible as an explicit
// Unknown — never a silent pass, and never a conviction on log evidence alone.
func TestClassifyNetstackWithoutGuestMACsIsUnknownNotSilent(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			// MAC deliberately unset — this is the world before wave 2 lands.
			{Name: "fwb-ci5", State: "running", IP: "", SSHProbed: false},
		},
	}
	got := Classify(e)
	var unknown *Verdict
	for i, v := range got {
		if v.Health == Unknown {
			unknown = &got[i]
		}
		if v.Rung == RungNetstackDead {
			t.Fatalf("convicted netstack-dead without any guest MAC to correlate: %+v", v)
		}
	}
	if unknown == nil {
		t.Fatalf("no Unknown verdict for the impossible correlation; verdicts = %+v", got)
	}
	if unknown.Rung != RungUnknown {
		t.Errorf("Rung = %v, want RungUnknown", unknown.Rung)
	}
	// Degraded, not terminal: the per-guest ladder must still run.
	var sawGuestRung bool
	for _, v := range got {
		if v.Rung == RungGuestNoIP {
			sawGuestRung = true
		}
	}
	if !sawGuestRung {
		t.Errorf("the unknown netstack probe swallowed the per-guest ladder: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// C4: host-hardware evidence outranks the log-derived netstack rung.
// ---------------------------------------------------------------------------

// The canary is the strongest signal in the system. If it faulted, the answer
// is power-cycle, never "rebuild umbra" — even when the netstack rung would
// also have fired.
func TestClassifyCanaryOutranksNetstack(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp: true, DeepRun: true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.127.10", SSHProbed: true, SSHOK: false,
				LoadCanary: CanaryResult{Ran: true, Faulted: true, Detail: "curl exited 132 (SIGILL)"}},
			{Name: "fwb-ci2", State: "running", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.127.11", SSHProbed: true, SSHOK: false},
		},
	}
	got := Classify(e)
	if len(got) != 1 {
		t.Fatalf("len(verdicts) = %d, want 1: %+v", len(got), got)
	}
	if got[0].Rung != RungHostHardware {
		t.Fatalf("Rung = %v, want RungHostHardware (canary must outrank netstack)", got[0].Rung)
	}
	if !strings.Contains(got[0].NextAction, "power-cycle") {
		t.Errorf("NextAction = %q, want the power-cycle remedy", got[0].NextAction)
	}
}

// The two-guest discriminator is a live, present-tense host-level observation;
// the netstack rung is derived from a log. The live observation wins.
func TestClassifyTwoGuestDiscriminatorOutranksNetstack(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, Text: "cannot receive packets", MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", MAC: "aa:bb:cc:dd:ee:01", IP: ""},
			{Name: "fwb-ci2", State: "running", MAC: "aa:bb:cc:dd:ee:02", IP: ""},
		},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungHostHardware {
		t.Fatalf("verdicts = %+v, want a single RungHostHardware", got)
	}
}

// F6: when BOTH host-level signals are present the verdict must carry both
// evidence strings. Same rung and same remedy either way, but the canary detail
// is the most decisive line a human will read and must not be dropped.
func TestClassifyHostHardwareMergesCanaryAndTwoGuestEvidence(t *testing.T) {
	e := Evidence{
		DaemonUp: true, DeepRun: true,
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", IP: "",
				LoadCanary: CanaryResult{Ran: true, Faulted: true, Detail: "curl exited 132 (SIGILL)"}},
			{Name: "fwb-ci2", State: "running", IP: ""},
		},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungHostHardware {
		t.Fatalf("verdicts = %+v, want a single RungHostHardware", got)
	}
	joined := strings.Join(got[0].Evidence, " | ")
	if !strings.Contains(joined, "SIGILL") {
		t.Errorf("canary evidence dropped from the merged verdict: %q", joined)
	}
	if !strings.Contains(joined, "fwb-ci2") {
		t.Errorf("two-guest evidence dropped from the merged verdict: %q", joined)
	}
}

// ---------------------------------------------------------------------------
// F9: "we could not tell" is its own rung.
// ---------------------------------------------------------------------------

func TestClassifyUnprobedRepoUsesRungUnknown(t *testing.T) {
	e := Evidence{
		DaemonUp: true, GHAvailable: false,
		Repos: []RepoEvidence{{Repo: "ForceAI-KW/force-website-builder", Probed: false}},
	}
	got := Classify(e)
	if len(got) != 1 {
		t.Fatalf("len(verdicts) = %d, want 1", len(got))
	}
	if got[0].Rung != RungUnknown {
		t.Errorf("Rung = %v, want RungUnknown — an undiagnosed fault must not be reported as runner-offline", got[0].Rung)
	}
	if got[0].Health != Unknown {
		t.Errorf("Health = %q, want %q", got[0].Health, Unknown)
	}
}

func TestRungUnknownSlugIsStable(t *testing.T) {
	if RungUnknown.String() != "unknown" {
		t.Errorf("RungUnknown.String() = %q, want %q", RungUnknown.String(), "unknown")
	}
	// The existing slugs are a watchdog contract; inserting a rung must not
	// have shifted any of them.
	want := map[Rung]string{
		RungNone: "healthy", RungDaemonDown: "daemon-down", RungNetstackDead: "netstack-dead",
		RungGuestNoIP: "guest-no-ip", RungGuestSSHStall: "guest-ssh-stall",
		RungRunnerServiceDown: "runner-service-down", RungRunnerOffline: "runner-offline",
		RungBillingLockout: "billing-lockout", RungHostHardware: "host-hardware",
	}
	for r, slug := range want {
		if r.String() != slug {
			t.Errorf("%d.String() = %q, want %q", int(r), r.String(), slug)
		}
	}
}

// F8: billing lockout and an offline runner on the same repo. Billing wins —
// GitHub refuses to start jobs at all, so runner state is downstream noise.
func TestClassifyBillingLockoutOutranksOfflineRunnerOnSameRepo(t *testing.T) {
	e := Evidence{
		DaemonUp: true, GHAvailable: true,
		Repos: []RepoEvidence{{
			Repo: "ForceAI-KW/force-website-builder", Probed: true,
			RunnerOnline:   map[string]bool{"fwb-ci5-1": false},
			BillingLockout: true,
		}},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungBillingLockout {
		t.Fatalf("verdicts = %+v, want a single RungBillingLockout", got)
	}
}

// Every failing verdict must carry a remedy — an unactionable failure is a
// report the operator cannot use at 2am.
func TestClassifyEveryFailingVerdictHasNextAction(t *testing.T) {
	now := time.Now()
	cases := []Evidence{
		{DaemonUp: false},
		{DaemonUp: true, Guests: []GuestEvidence{{Name: "g", State: "running", IP: ""}}},
		{DaemonUp: true, Guests: []GuestEvidence{{Name: "g", State: "running", IP: "10.0.0.1", SSHProbed: true, SSHOK: false}}},
		{DaemonUp: true, Guests: []GuestEvidence{
			{Name: "a", State: "running", IP: ""}, {Name: "b", State: "running", IP: ""}}},
		{DaemonUp: true, Guests: []GuestEvidence{{Name: "g", State: "running", IP: "10.0.0.1", SSHProbed: true, SSHOK: true,
			Runners: []RunnerEvidence{{Unit: "actions.runner.x.y.service", Active: false}}}}},
		{DaemonUp: true, Repos: []RepoEvidence{{Repo: "o/r", Probed: true, RunnerOnline: map[string]bool{"n": false}}}},
		{DaemonUp: true, Repos: []RepoEvidence{{Repo: "o/r", Probed: true, BillingLockout: true}}},
		{DaemonUp: true, Repos: []RepoEvidence{{Repo: "o/r", Probed: false}}},
		{DaemonUp: true, DaemonStart: now, LogLines: []LogLine{
			{Time: now, MAC: "aa:bb:cc:dd:ee:01"}, {Time: now, MAC: "aa:bb:cc:dd:ee:02"}},
			Guests: []GuestEvidence{
				{Name: "a", State: "running", MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.1", SSHProbed: true, SSHOK: false},
				{Name: "b", State: "running", MAC: "aa:bb:cc:dd:ee:02", IP: "10.0.0.2", SSHProbed: true, SSHOK: false}}},
	}
	for i, e := range cases {
		for _, v := range Classify(e) {
			if v.Health == "" {
				t.Errorf("case %d: verdict %v has empty Health", i, v.Rung)
			}
			if v.Health == Fail && v.NextAction == "" {
				t.Errorf("case %d: failing verdict %v has no NextAction", i, v.Rung)
			}
		}
	}
}
