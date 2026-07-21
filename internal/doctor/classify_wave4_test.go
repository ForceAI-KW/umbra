package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/ForceAI-KW/umbra/internal/vm"
)

// ---------------------------------------------------------------------------
// C2 — every vm.State must produce SOME output. Silence renders a broken
// machine as health, which is the defect class this whole package exists for.
// ---------------------------------------------------------------------------

// TestClassifyEveryMachineStateIsVisible walks all five vm.State values and
// asserts the machine is named in at least one verdict. Before wave 4,
// crashed/starting/stopping guests produced ZERO verdicts, so a fleet with one
// healthy guest and one CRASHED CI runner printed "healthy: no faults detected"
// and exited 0 — strictly less informative than `umbra list`, which already
// prints `crashed*`.
func TestClassifyEveryMachineStateIsVisible(t *testing.T) {
	for _, st := range []vm.State{
		vm.StateStopped, vm.StateStarting, vm.StateRunning, vm.StateStopping, vm.StateCrashed,
	} {
		e := Evidence{DaemonUp: true, Guests: []GuestEvidence{
			{Name: "g1", State: st, IP: "10.0.0.1", SSHProbed: true, SSHOK: true},
		}}
		got := Classify(e)
		if st == vm.StateStopped {
			// Deliberate exception, see classifyMachineState: a stopped machine
			// is the documented resting state of the spare.
			if len(got) != 0 {
				t.Errorf("state %q: want no verdicts for a stopped machine, got %+v", st, got)
			}
			continue
		}
		if st == vm.StateRunning {
			continue // covered by the per-guest rung tests
		}
		named := false
		for _, v := range got {
			if v.Subject == "g1" {
				named = true
			}
		}
		if !named {
			t.Errorf("state %q: machine g1 appears in NO verdict (%+v) — silence is rendered as health", st, got)
		}
	}
}

func TestClassifyCrashedMachineIsAFault(t *testing.T) {
	got := Classify(Evidence{DaemonUp: true, Guests: []GuestEvidence{
		{Name: "fwb-ci5", State: vm.StateCrashed},
	}})
	if len(got) != 1 {
		t.Fatalf("want exactly one verdict for a crashed machine, got %+v", got)
	}
	v := got[0]
	if v.Rung != RungMachineCrashed || v.Health != Fail {
		t.Fatalf("want RungMachineCrashed/Fail, got %v/%v", v.Rung, v.Health)
	}
	if v.Subject != "fwb-ci5" || v.NextAction == "" {
		t.Errorf("crashed verdict must name the machine and carry a next action: %+v", v)
	}
}

// A zombie is the worst case: the VM may STILL BE ALIVE holding CPU, memory and
// its netstack attachment, so the remedy differs from an ordinary crash.
func TestClassifyZombieMachineIsDistinctFromPlainCrash(t *testing.T) {
	plain := Classify(Evidence{DaemonUp: true, Guests: []GuestEvidence{
		{Name: "g", State: vm.StateCrashed},
	}})
	zombie := Classify(Evidence{DaemonUp: true, Guests: []GuestEvidence{
		{Name: "g", State: vm.StateCrashed, Zombie: true},
	}})
	if len(plain) != 1 || len(zombie) != 1 {
		t.Fatalf("want one verdict each, got plain=%+v zombie=%+v", plain, zombie)
	}
	if zombie[0].Health != Fail {
		t.Errorf("zombie must be a fault, got %v", zombie[0].Health)
	}
	if zombie[0].NextAction == plain[0].NextAction {
		t.Error("a zombie (VM may still be alive) must not get the same remedy as a plain crash")
	}
	if !strings.Contains(strings.ToLower(zombie[0].Reason+strings.Join(zombie[0].Supporting, " ")), "still be alive") {
		t.Errorf("zombie verdict must say the VM may still be alive: %+v", zombie[0])
	}
}

// A transient state is NOT a fault — but it must not be silence either.
func TestClassifyTransientStatesAreUnknownNotSilent(t *testing.T) {
	for _, st := range []vm.State{vm.StateStarting, vm.StateStopping} {
		got := Classify(Evidence{DaemonUp: true, Guests: []GuestEvidence{{Name: "g", State: st}}})
		if len(got) != 1 {
			t.Fatalf("state %q: want one verdict, got %+v", st, got)
		}
		if got[0].Health != Unknown {
			t.Errorf("state %q: want Unknown (transient is not a fault), got %v", st, got[0].Health)
		}
		if got[0].Subject != "g" {
			t.Errorf("state %q: verdict must name the machine, got %+v", st, got[0])
		}
	}
}

// A transient machine must not flip the exit code: `umbra start` racing doctor
// is not a fault, and the watchdog acts only on health=fail.
func TestClassifyTransientStateIsNotAFailure(t *testing.T) {
	for _, st := range []vm.State{vm.StateStarting, vm.StateStopping} {
		for _, v := range Classify(Evidence{DaemonUp: true, Guests: []GuestEvidence{{Name: "g", State: st}}}) {
			if v.Health == Fail {
				t.Errorf("state %q must never produce a Fail verdict: %+v", st, v)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// I3 — the netstack MAC tripwire must scope to the UNREACHABLE guests, which
// are the only ones the correlation consumes.
// ---------------------------------------------------------------------------

func TestClassifyNetstackTripwireScopesToUnreachableGuests(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			// Healthy guest WITH a MAC. Under the old whole-fleet scan this
			// alone satisfied the tripwire and the rung went silently
			// unevaluable — partial blindness rendered as a confident
			// per-guest diagnosis.
			{Name: "healthy", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:09", IP: "10.0.0.9", SSHProbed: true, SSHOK: true},
			// The guests the correlation actually consumes carry no MAC.
			{Name: "a", State: vm.StateRunning, IP: "10.0.0.1", SSHProbed: true, SSHOK: false},
			{Name: "b", State: vm.StateRunning, IP: "10.0.0.2", SSHProbed: true, SSHOK: false},
		},
	}
	sawTripwire := false
	for _, v := range Classify(e) {
		if v.Health == Unknown && strings.Contains(v.Reason, "netstack rung") {
			sawTripwire = true
		}
	}
	if !sawTripwire {
		t.Fatalf("no Unknown netstack tripwire despite the unreachable guests carrying no MAC: %+v", Classify(e))
	}
}

// ---------------------------------------------------------------------------
// I4 — the residual false positive must be documented IN THE VERDICT, because
// no boot timestamp exists anywhere to eliminate it.
// ---------------------------------------------------------------------------

func TestClassifyNetstackVerdictDocumentsRestartResidual(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, MAC: "aa:bb:cc:dd:ee:02"},
		},
		Guests: []GuestEvidence{
			{Name: "a", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.1", SSHProbed: true, SSHOK: false},
			{Name: "b", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:02", IP: "10.0.0.2", SSHProbed: true, SSHOK: false},
		},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungNetstackDead {
		t.Fatalf("want a single netstack conviction, got %+v", got)
	}
	joined := strings.Join(got[0].Supporting, " | ")
	if !strings.Contains(joined, "restart") {
		t.Errorf("netstack verdict must disclose the restart residual in its own evidence, got: %s", joined)
	}
	// DaemonStart is the window this verdict is scoped to; it must be legible
	// to the operator rather than an unread field.
	if !strings.Contains(joined, e.DaemonStart.Format(time.RFC3339)) {
		t.Errorf("netstack verdict must state the daemon-lifetime window it scanned, got: %s", joined)
	}
}

// ---------------------------------------------------------------------------
// Minor — MAC case normalization. macRe accepts A-F, so an uppercase log MAC
// would silently kill the rung with NO tripwire (the guests do carry MACs).
// ---------------------------------------------------------------------------

func TestClassifyNetstackCorrelationIsCaseInsensitive(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, MAC: "AA:BB:CC:DD:EE:01"},
			{Time: now, MAC: "AA:BB:CC:DD:EE:02"},
		},
		Guests: []GuestEvidence{
			{Name: "a", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.1", SSHProbed: true, SSHOK: false},
			{Name: "b", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:02", IP: "10.0.0.2", SSHProbed: true, SSHOK: false},
		},
	}
	got := Classify(e)
	if len(got) != 1 || got[0].Rung != RungNetstackDead {
		t.Fatalf("uppercase log MACs must still correlate, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Minor — resolveRepo failing under rate-limiting must not tell an operator
// with a working gh to "install and authenticate the GitHub CLI".
// ---------------------------------------------------------------------------

func TestClassifyUnprobedRepoAdviceDependsOnGHAvailability(t *testing.T) {
	withGH := Classify(Evidence{
		DaemonUp: true, GHAvailable: true,
		Repos: []RepoEvidence{{Repo: "o/r", Probed: false}},
	})
	withoutGH := Classify(Evidence{
		DaemonUp: true, GHAvailable: false,
		Repos: []RepoEvidence{{Repo: "o/r", Probed: false}},
	})
	if len(withGH) != 1 || len(withoutGH) != 1 {
		t.Fatalf("want one verdict each, got %+v / %+v", withGH, withoutGH)
	}
	if withGH[0].NextAction == withoutGH[0].NextAction {
		t.Fatal("gh-present and gh-missing must not get the same next action")
	}
	if strings.Contains(withGH[0].NextAction, "brew install gh") {
		t.Errorf("gh IS installed; must not advise installing it: %q", withGH[0].NextAction)
	}
	if !strings.Contains(withoutGH[0].NextAction, "brew install gh") {
		t.Errorf("gh is missing; must advise installing it: %q", withoutGH[0].NextAction)
	}
}
