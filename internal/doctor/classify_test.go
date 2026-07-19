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
