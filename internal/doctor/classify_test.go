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
	for _, v := range Classify(e) {
		if v.Health == Fail {
			t.Errorf("healthy host produced a failing verdict: %+v", v)
		}
	}
}
