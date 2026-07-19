package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/doctor"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

// ---------------------------------------------------------------------------
// C1 — an incomplete canary must NOT be recorded as "ran, found nothing".
//
// The canary is the single most decisive rung in the system. If ssh dies
// mid-stress, the 3-minute timeout trips, or the guest wedges under the load,
// the output will not contain "FAULT rc=132" — and the old code recorded that
// as a CLEAN canary. A host sick enough to drop ssh mid-stress is exactly the
// host the canary exists to catch.
// ---------------------------------------------------------------------------

func TestCanaryOutcomeRequiresCompletionSentinel(t *testing.T) {
	for _, c := range []struct {
		name       string
		out        string
		err        error
		wantRan    bool
		wantFault  bool
		wantDetail string // substring the unprobed detail must carry
	}{
		{
			name: "clean complete run", out: "CANARY_DONE\n", err: nil,
			wantRan: true, wantFault: false,
		},
		{
			name: "faulted complete run", out: "FAULT rc=132\nCANARY_DONE\n", err: nil,
			wantRan: true, wantFault: true,
		},
		{
			// A SIGILL that WAS observed is decisive even if the run then died:
			// a positive observation stands on its own. Only ABSENCE of a fault
			// needs proof of completion.
			name: "faulted then died", out: "FAULT rc=139\n", err: errors.New("exit status 255"),
			wantRan: true, wantFault: true,
		},
		{
			name: "ssh died mid-canary", out: "", err: errors.New("exit status 255"),
			wantRan: false, wantDetail: "exit status 255",
		},
		{
			name: "timeout", out: "partial output", err: errors.New("signal: killed"),
			wantRan: false, wantDetail: "signal: killed",
		},
		{
			// No error, no sentinel: the shell exited 0 but the script never
			// reached its last line (guest wedged, connection torn down cleanly).
			name: "no error but no sentinel", out: "ok\n", err: nil,
			wantRan: false, wantDetail: canaryDoneSentinel,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, detail := canaryOutcome(c.out, c.err)
			if got.Ran != c.wantRan {
				t.Fatalf("Ran = %v, want %v (detail=%q)", got.Ran, c.wantRan, detail)
			}
			if got.Faulted != c.wantFault {
				t.Errorf("Faulted = %v, want %v", got.Faulted, c.wantFault)
			}
			if c.wantRan && detail != "" {
				t.Errorf("a conclusive canary must not also report a cannot-conclude detail: %q", detail)
			}
			if !c.wantRan {
				if detail == "" {
					t.Fatal("an inconclusive canary must explain why, or it is silence-as-health again")
				}
				if !strings.Contains(detail, c.wantDetail) {
					t.Errorf("detail %q does not mention %q", detail, c.wantDetail)
				}
			}
		})
	}
}

// The completion sentinel emitted by the script and the one checked here must
// be the same string. Before wave 4 the script echoed CANARY_DONE and nothing
// in the codebase ever read it.
func TestCanaryScriptEmitsTheSentinelThatIsChecked(t *testing.T) {
	if !strings.Contains(canaryScript, canaryDoneSentinel) {
		t.Fatalf("canaryScript does not emit %q", canaryDoneSentinel)
	}
	if _, detail := canaryOutcome(canaryDoneSentinel, nil); detail != "" {
		t.Errorf("the script's own sentinel is not accepted as completion: %q", detail)
	}
}

// End-to-end at the classifier boundary: an inconclusive canary must surface as
// Unknown, never as a clean host-hardware reading.
func TestIncompleteCanaryClassifiesAsUnknownNotClean(t *testing.T) {
	res, detail := canaryOutcome("", errors.New("exit status 255"))
	ev := doctor.Evidence{
		DaemonUp: true, DeepRun: true,
		Guests: []doctor.GuestEvidence{{
			Name: "g", State: vm.StateRunning, IP: "10.0.0.1",
			SSHProbed: true, SSHOK: true, LoadCanary: res,
		}},
		Unprobed: []doctor.Unprobed{{Subject: "g", What: "load canary", Detail: detail}},
	}
	got := doctor.Classify(ev)
	sawUnknown := false
	for _, v := range got {
		if v.Health == doctor.Fail {
			t.Errorf("an incomplete canary must not produce a fault verdict: %+v", v)
		}
		if v.Health == doctor.Unknown && strings.Contains(v.Reason, "load canary") {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Fatalf("an incomplete canary must surface as Unknown, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// C2 (collector half) — the zombie bit must reach the classifier.
// ---------------------------------------------------------------------------

func TestGuestEvidenceCarriesZombieBit(t *testing.T) {
	mv := &client.MachineView{
		Machine: registry.Machine{Name: "g"},
		State:   vm.StateCrashed, Zombie: true,
	}
	g, _ := guestEvidenceFor(mv)
	if !g.Zombie {
		t.Fatal("guestEvidenceFor dropped the Zombie bit; doctor would be blind to a VM that may still be alive")
	}
	if g.State != vm.StateCrashed {
		t.Errorf("State = %v, want crashed", g.State)
	}
}
