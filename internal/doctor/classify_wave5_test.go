package doctor

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// C1 (wave 5): a booting guest is not a broken guest.
//
// MachineView.IP is the READINESS-CONFIRMED address, set by mgr.SetIP only
// after the readiness probe succeeds (up to 90s after StateRunning) and
// cleared on stop. So EVERY guest is running-with-no-IP for its whole boot
// window. Convicting that as guest-no-ip told the operator to destroy and
// recreate a healthy machine; doing it for two guests convicted the HOST's
// hardware and made the ops watchdog stand down.
// ---------------------------------------------------------------------------

func findRung(vs []Verdict, r Rung) *Verdict {
	for i := range vs {
		if vs[i].Rung == r {
			return &vs[i]
		}
	}
	return nil
}

func findSubjectRung(vs []Verdict, subject string, r Rung) *Verdict {
	for i := range vs {
		if vs[i].Rung == r && vs[i].Subject == subject {
			return &vs[i]
		}
	}
	return nil
}

// A guest with a CONFIGURED address but no readiness-confirmed one is booting
// (or its readiness timed out). That is Unknown and re-checkable — never a
// Fail, and above all never a recreate instruction.
func TestClassifyBootingGuestIsUnknownNotRecreate(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", ConfiguredIP: "192.168.127.10", IP: ""},
		},
	}
	got := Classify(e)
	if v := findRung(got, RungGuestNoIP); v != nil {
		t.Fatalf("convicted guest-no-ip on a guest that merely has no readiness IP: %+v", v)
	}
	if len(got) != 1 {
		t.Fatalf("verdicts = %+v, want exactly one", got)
	}
	v := got[0]
	if v.Health != Unknown {
		t.Fatalf("health = %q, want unknown for a booting guest", v.Health)
	}
	if strings.Contains(v.NextAction, "umbra rm") || strings.Contains(v.NextAction, "umbra create") {
		t.Fatalf("next action tells the operator to destroy a booting guest: %q", v.NextAction)
	}
}

// A guest with NO configured address at all is a genuinely broken machine
// record. That still convicts.
func TestClassifyNoConfiguredIPStillConvicts(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", ConfiguredIP: "", IP: ""},
		},
	}
	got := Classify(e)
	v := findRung(got, RungGuestNoIP)
	if v == nil {
		t.Fatalf("verdicts = %+v, want RungGuestNoIP for a machine with no configured address", got)
	}
	if v.Health != Fail {
		t.Fatalf("health = %q, want fail", v.Health)
	}
}

// THE INCIDENT TEST. Two guests booting after a host reboot must not be
// diagnosed as failing hardware. This exact signature was produced on a real
// host and led a human operator to conclude "book Apple service"; the real
// cause was memory overcommit.
func TestClassifyTwoBootingGuestsIsNotHostHardware(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", ConfiguredIP: "192.168.127.10", IP: ""},
			{Name: "fwb-ci2", State: "running", ConfiguredIP: "192.168.127.11", IP: ""},
		},
	}
	got := Classify(e)
	if v := findRung(got, RungHostHardware); v != nil {
		t.Fatalf("convicted host-hardware on two merely-booting guests: %+v", v)
	}
	for _, v := range got {
		if v.Health == Fail {
			t.Fatalf("emitted a Fail for two booting guests: %+v", v)
		}
		if strings.Contains(v.NextAction, "Apple Diagnostics") {
			t.Fatalf("advised Apple Diagnostics without corroboration: %+v", v)
		}
	}
	// It must not be silent either: the discriminator's own inability to
	// conclude has to be disclosed.
	if len(got) == 0 {
		t.Fatal("two guests failing readiness produced no output at all")
	}
}

// With corroboration — a load-canary fault — host-hardware convicts, and the
// two-guest fact rides along as supporting evidence.
func TestClassifyTwoBootingGuestsWithCanaryFaultConvicts(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: "running", ConfiguredIP: "192.168.127.10", IP: "",
				LoadCanary: CanaryResult{Ran: true, Faulted: true, Detail: "curl exited 132 (SIGILL)"}},
			{Name: "fwb-ci2", State: "running", ConfiguredIP: "192.168.127.11", IP: ""},
		},
	}
	got := Classify(e)
	v := findRung(got, RungHostHardware)
	if v == nil {
		t.Fatalf("verdicts = %+v, want RungHostHardware when the canary faulted", got)
	}
	if v.Health != Fail {
		t.Fatalf("health = %q, want fail", v.Health)
	}
	joined := strings.Join(v.Supporting, " | ")
	if !strings.Contains(joined, "SIGILL") {
		t.Fatalf("canary detail missing from evidence: %v", v.Supporting)
	}
	if !strings.Contains(joined, "fwb-ci2") {
		t.Fatalf("two-guest corroboration missing from evidence: %v", v.Supporting)
	}
}

// ---------------------------------------------------------------------------
// I4 (wave 5): a stale runner registration is not an outage.
//
// Verified live: actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-1
// is `loaded inactive dead` while
// actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-force-website-builder-1
// is `active running` and serving CI for the SAME repo. Failing on that made
// `umbra doctor` exit 1 on every run of a healthy host.
// ---------------------------------------------------------------------------

func TestClassifyStaleRunnerUnitBesideActiveOneIsNotAFail(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests: []GuestEvidence{{
			Name: "fwb-ci5", State: "running",
			ConfiguredIP: "192.168.127.10", IP: "192.168.127.10",
			SSHProbed: true, SSHOK: true,
			Runners: []RunnerEvidence{
				{Unit: "actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-1.service", Active: false},
				{Unit: "actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-force-website-builder-1.service", Active: true},
			},
		}},
	}
	got := Classify(e)
	for _, v := range got {
		if v.Health == Fail {
			t.Fatalf("failed on a stale unit beside an active one for the same repo: %+v", v)
		}
	}
	v := findRung(got, RungUnknown)
	if v == nil {
		t.Fatalf("verdicts = %+v, want an Unknown disclosing the stale unit", got)
	}
	if !strings.Contains(v.Reason, "stale") {
		t.Fatalf("reason does not name the stale registration: %q", v.Reason)
	}
	if v.NextAction == "" {
		t.Fatal("stale-unit verdict has no next action")
	}
}

// A repo with NO active runner anywhere on the fleet is still a real outage.
func TestClassifyInactiveRunnerWithNoActivePeerStillFails(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests: []GuestEvidence{{
			Name: "fwb-ci5", State: "running",
			ConfiguredIP: "192.168.127.10", IP: "192.168.127.10",
			SSHProbed: true, SSHOK: true,
			Runners: []RunnerEvidence{
				{Unit: "actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-1.service", Active: false},
			},
		}},
	}
	got := Classify(e)
	v := findRung(got, RungRunnerServiceDown)
	if v == nil {
		t.Fatalf("verdicts = %+v, want RungRunnerServiceDown when no runner is active for the repo", got)
	}
	if v.Health != Fail {
		t.Fatalf("health = %q, want fail", v.Health)
	}
}

// The active peer may live on a DIFFERENT guest — the repo is served either
// way, so the inactive unit is still only a stale registration.
func TestClassifyActivePeerOnAnotherGuestStillMeansStale(t *testing.T) {
	e := Evidence{
		DaemonUp: true,
		Guests: []GuestEvidence{
			{
				Name: "fwb-ci5", State: "running",
				ConfiguredIP: "192.168.127.10", IP: "192.168.127.10",
				SSHProbed: true, SSHOK: true,
				Runners: []RunnerEvidence{
					{Unit: "actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-1.service", Active: false},
				},
			},
			{
				Name: "fwb-ci2", State: "running",
				ConfiguredIP: "192.168.127.11", IP: "192.168.127.11",
				SSHProbed: true, SSHOK: true,
				Runners: []RunnerEvidence{
					{Unit: "actions.runner.ForceAI-KW-force-website-builder.fwb-ci2-1.service", Active: true},
				},
			},
		},
	}
	got := Classify(e)
	for _, v := range got {
		if v.Health == Fail {
			t.Fatalf("failed despite an active runner for the same repo on another guest: %+v", v)
		}
	}
}

// RunnerUnitScope is shared with the collector so the two cannot drift apart.
func TestRunnerUnitScope(t *testing.T) {
	cases := map[string]string{
		"actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-1.service": "ForceAI-KW-force-website-builder",
		"actions.runner.ForceAI-KW.fwb-ci5-1.service":                       "ForceAI-KW",
		"actions.runner.broken":                                             "",
		"not-a-runner.service":                                              "",
		"":                                                                  "",
	}
	for unit, want := range cases {
		if got := RunnerUnitScope(unit); got != want {
			t.Errorf("RunnerUnitScope(%q) = %q, want %q", unit, got, want)
		}
	}
}

// A verdict-free run stays verdict-free: the wave-5 changes must not make a
// genuinely healthy fleet noisy.
func TestClassifyHealthyFleetStaysSilent(t *testing.T) {
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: time.Now().Add(-time.Hour),
		Guests: []GuestEvidence{{
			Name: "fwb-ci5", State: "running",
			ConfiguredIP: "192.168.127.10", IP: "192.168.127.10",
			SSHProbed: true, SSHOK: true,
			Runners: []RunnerEvidence{
				{Unit: "actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-1.service", Active: true},
			},
		}},
	}
	if got := Classify(e); len(got) != 0 {
		t.Fatalf("healthy fleet produced verdicts: %+v", got)
	}
}
