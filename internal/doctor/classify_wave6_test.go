package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/ForceAI-KW/umbra/internal/vm"
)

// ---------------------------------------------------------------------------
// C1 (wave 6) — an ABSENT configured address is ambiguous, so it must not
// convict.
//
// Wave 5 reasoned that ConfiguredIP == "" could only mean a damaged registry
// record, because "a configured address is written at create time". That is
// true of the REGISTRY and false of the EVIDENCE: the address only reaches the
// classifier if the daemon serialises it. A daemon older than this CLI does
// not emit configured_ip at all, so every guest on that host arrives with
// ConfiguredIP == "" while every registry record is perfectly intact. That is
// the live state of this host right now.
//
// "" therefore means EITHER a damaged record OR a daemon that never reported
// the field, and those need opposite responses — one is `umbra rm && umbra
// create`, the other is `make install`. Convicting on an ambiguity is how the
// destroy-a-healthy-guest instruction survived five review waves.
//
// The discriminator is a FLEET fact the collector can establish honestly: if
// any machine reported a configured address, the daemon speaks the field, so a
// machine lacking one is genuinely broken. Evidence.ConfiguredIPReported
// carries that, and Classify stays pure.
// ---------------------------------------------------------------------------

// A daemon that never reports the field must produce Unknown, never a
// destructive Fail.
func TestClassifyAbsentConfiguredIPFromOldDaemonIsUnknown(t *testing.T) {
	e := Evidence{
		DaemonUp:             true,
		ConfiguredIPReported: false, // no machine reported one: daemon predates the field
		Guests: []GuestEvidence{
			{Name: "fwb-ci5", State: vm.StateRunning, ConfiguredIP: "", IP: ""},
		},
	}
	got := Classify(e)
	if v := findRung(got, RungGuestNoIP); v != nil {
		t.Fatalf("convicted guest-no-ip on an unreported address: %+v", v)
	}
	if len(got) != 1 {
		t.Fatalf("verdicts = %+v, want exactly one", got)
	}
	v := got[0]
	if v.Health != Unknown {
		t.Fatalf("health = %q, want unknown when the field was never reported", v.Health)
	}
	if strings.Contains(v.NextAction, "umbra rm") || strings.Contains(v.NextAction, "umbra create") {
		t.Fatalf("told the operator to destroy a guest on an ambiguity: %q", v.NextAction)
	}
	// It must NAME the ambiguity, or the operator cannot act on it.
	joined := strings.Join(v.Supporting, " | ") + " " + v.NextAction
	if !strings.Contains(joined, "configured_ip") {
		t.Errorf("verdict does not name the missing field, so the daemon-skew cause is invisible: %+v", v)
	}
}

// THE RUNG MUST STAY REACHABLE. When a sibling machine DID report a configured
// address the daemon demonstrably speaks the field, so a machine without one
// is a genuinely damaged record and still convicts. Without this the wave-5
// fix would simply delete a rung instead of correcting it.
func TestClassifyAbsentConfiguredIPStillConvictsWhenDaemonReportsTheField(t *testing.T) {
	e := Evidence{
		DaemonUp:             true,
		ConfiguredIPReported: true, // a sibling reported one, so the daemon speaks it
		Guests: []GuestEvidence{
			{Name: "healthy", State: vm.StateRunning, ConfiguredIP: "192.168.127.11", IP: "192.168.127.11", SSHProbed: true, SSHOK: true},
			{Name: "broken", State: vm.StateRunning, ConfiguredIP: "", IP: ""},
		},
	}
	got := Classify(e)
	v := findRung(got, RungGuestNoIP)
	if v == nil {
		t.Fatalf("verdicts = %+v, want RungGuestNoIP for a genuinely empty record", got)
	}
	if v.Health != Fail {
		t.Fatalf("health = %q, want fail", v.Health)
	}
	if v.Subject != "broken" {
		t.Fatalf("subject = %q, want the machine with the damaged record", v.Subject)
	}
}

// The four combinations of (ConfiguredIP present/absent) x (IP present/absent),
// end to end, on a daemon that reports the field.
func TestClassifyAddressMatrix(t *testing.T) {
	cases := []struct {
		name         string
		configured   string
		runtime      string
		wantRung     Rung
		wantHealth   Health
		destructiveN bool // next action may not destroy the guest
	}{
		{
			name:       "both absent -> damaged record, convicts",
			configured: "", runtime: "",
			wantRung: RungGuestNoIP, wantHealth: Fail, destructiveN: true,
		},
		{
			name:       "configured only -> still booting, unknown",
			configured: "192.168.127.10", runtime: "",
			wantRung: RungUnknown, wantHealth: Unknown,
		},
		{
			name:       "both present -> reachable, ladder continues past the address rungs",
			configured: "192.168.127.10", runtime: "192.168.127.10",
			wantRung: RungNone, wantHealth: Pass,
		},
		{
			// A runtime address without a configured one is incoherent, but
			// the machine is demonstrably reachable, so it must not convict on
			// the address rungs.
			name:       "runtime only -> reachable, no address conviction",
			configured: "", runtime: "192.168.127.10",
			wantRung: RungNone, wantHealth: Pass,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := GuestEvidence{
				Name: "g", State: vm.StateRunning,
				ConfiguredIP: c.configured, IP: c.runtime,
			}
			if c.runtime != "" {
				g.SSHProbed, g.SSHOK = true, true
			}
			got := Classify(Evidence{
				DaemonUp: true, ConfiguredIPReported: true,
				Guests: []GuestEvidence{g},
			})
			if c.wantRung == RungNone {
				for _, v := range got {
					if v.Rung == RungGuestNoIP {
						t.Fatalf("reachable guest convicted on an address rung: %+v", v)
					}
				}
				return
			}
			v := findRung(got, c.wantRung)
			if v == nil {
				t.Fatalf("verdicts = %+v, want rung %v", got, c.wantRung)
			}
			if v.Health != c.wantHealth {
				t.Fatalf("health = %q, want %q", v.Health, c.wantHealth)
			}
			if !c.destructiveN && strings.Contains(v.NextAction, "umbra rm") {
				t.Fatalf("non-destructive case produced a recreate instruction: %q", v.NextAction)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// I1 (wave 6) — netstack-dead requires ssh corroboration.
//
// `guest link <MAC> closed: cannot receive packets` is level=INFO and is
// emitted by the SHUTDOWN half of an ordinary restart, so two guests restarted
// within one daemon lifetime reproduce the whole netstack signature while
// booting. Reachable by following doctor's own guest-ssh-stall advice twice.
// The verdict disclosed that residual in its evidence — but netstack-dead is
// in the ops watchdog's UNHEALABLE_RUNGS and the watchdog reads only `rung`
// and `health`, so it stood down on a routine restart and never saw the prose.
//
// The discriminator that does not need a daemon change: a guest that is merely
// RESTARTING has no runtime IP at all, because umbrad publishes it only after
// readiness. A guest whose ssh probe FAILED DESPITE having a readiness-
// confirmed address cannot be explained by a restart — it got far enough to
// pass readiness and then stopped answering. So at least one correlated guest
// must be ssh-corroborated before the rung convicts.
// ---------------------------------------------------------------------------

// The routine two-guest restart: no ssh corroboration anywhere. Must degrade
// to Unknown so the watchdog does not stand down.
func TestClassifyNetstackWithoutSSHCorroborationIsUnknown(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, MAC: "aa:bb:cc:dd:ee:02"},
		},
		ConfiguredIPReported: true,
		Guests: []GuestEvidence{
			// Both booting: configured address present, readiness not done.
			{Name: "a", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:01", ConfiguredIP: "10.0.0.1", IP: ""},
			{Name: "b", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:02", ConfiguredIP: "10.0.0.2", IP: ""},
		},
	}
	got := Classify(e)
	for _, v := range got {
		if v.Rung == RungNetstackDead {
			t.Fatalf("convicted netstack-dead on a signature a routine restart reproduces: %+v", v)
		}
		if v.Health == Fail {
			t.Fatalf("emitted a Fail for two restarting guests: %+v", v)
		}
	}
	// Not silent either — the rung's inability to conclude is the finding.
	var disclosed bool
	for _, v := range got {
		if v.Health == Unknown && strings.Contains(strings.ToLower(v.Reason+strings.Join(v.Supporting, " ")), "netstack") {
			disclosed = true
		}
	}
	if !disclosed {
		t.Fatalf("netstack ambiguity was not disclosed at all; verdicts = %+v", got)
	}
}

// GENUINE NETSTACK DEATH MUST STILL CONVICT. Guests that passed readiness and
// then stopped answering ssh cannot be a restart artefact. Checked explicitly
// because a corroboration requirement that makes the rung unreachable would be
// a regression dressed up as a fix.
func TestClassifyNetstackStillConvictsWithSSHCorroboration(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, MAC: "aa:bb:cc:dd:ee:02"},
		},
		ConfiguredIPReported: true,
		Guests: []GuestEvidence{
			{Name: "a", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:01", ConfiguredIP: "10.0.0.1", IP: "10.0.0.1", SSHProbed: true, SSHOK: false},
			{Name: "b", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:02", ConfiguredIP: "10.0.0.2", IP: "10.0.0.2", SSHProbed: true, SSHOK: false},
		},
	}
	got := Classify(e)
	if len(got) == 0 || got[0].Rung != RungNetstackDead {
		t.Fatalf("genuine netstack death must still convict; verdicts = %+v", got)
	}
	if got[0].Health != Fail {
		t.Fatalf("health = %q, want fail", got[0].Health)
	}
}

// One corroborated guest is enough: a restart cannot produce a guest that
// passed readiness and then lost ssh.
func TestClassifyNetstackConvictsOnASingleCorroboratedGuest(t *testing.T) {
	now := time.Now()
	e := Evidence{
		DaemonUp:    true,
		DaemonStart: now.Add(-time.Minute),
		LogLines: []LogLine{
			{Time: now, MAC: "aa:bb:cc:dd:ee:01"},
			{Time: now, MAC: "aa:bb:cc:dd:ee:02"},
		},
		ConfiguredIPReported: true,
		Guests: []GuestEvidence{
			{Name: "a", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:01", ConfiguredIP: "10.0.0.1", IP: "10.0.0.1", SSHProbed: true, SSHOK: false},
			{Name: "b", State: vm.StateRunning, MAC: "aa:bb:cc:dd:ee:02", ConfiguredIP: "10.0.0.2", IP: ""},
		},
	}
	got := Classify(e)
	if len(got) == 0 || got[0].Rung != RungNetstackDead {
		t.Fatalf("verdicts = %+v, want RungNetstackDead", got)
	}
}
