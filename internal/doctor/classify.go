package doctor

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ForceAI-KW/umbra/internal/vm"
)

// Classify turns observed Evidence into ordered findings. It is pure: no I/O,
// no clock, no filesystem. That is what lets every rung be tested with a
// literal Evidence struct instead of a deliberately broken host.
//
// PRECEDENCE. Three tiers terminate the ladder, in this order:
//
//  1. Daemon down — nothing below can even be collected.
//  2. Host hardware — the two-guest discriminator and the load canary. These
//     are LIVE, present-tense observations of the machine miscomputing, and
//     they outrank the netstack rung deliberately: the netstack verdict is
//     derived from a log, and answering "rebuild umbra" to a host whose CPU is
//     failing under load sends the operator down a dead end.
//  3. Netstack dead — host-wide, but log-derived, so it yields to tier 2.
//
// Everything below that is per-guest or per-repo and is reported together.
//
// Verdicts for probes that could not RUN (Evidence.Unprobed) are appended to
// EVERY return path, including the terminating tiers. A terminating tier
// answers "what is the fault"; an unprobed record answers "what did we fail
// to look at" — suppressing the second because the first fired would recreate
// the silence-as-health bug at a different layer.
func Classify(e Evidence) []Verdict {
	unknowns := classifyUnprobed(e)

	if !e.DaemonUp {
		return append([]Verdict{{
			Rung:       RungDaemonDown,
			Health:     Fail,
			Reason:     "umbrad is not responding on its API socket",
			NextAction: "umbra daemon install",
		}}, unknowns...)
	}

	host, terminal := classifyHostHardware(e)
	if terminal {
		return append(host, unknowns...)
	}

	v, ok := classifyNetstack(e)
	if ok && v.Health == Fail {
		return append(append([]Verdict{v}, host...), unknowns...)
	}
	// An Unknown netstack verdict means the probe could not be evaluated. That
	// degrades this one rung; it must not swallow the rest of the diagnosis.
	// The same is true of a non-terminal host-hardware verdict: the
	// discriminator could not conclude, which is a disclosure, not a diagnosis.
	out := unknowns
	out = append(out, host...)
	if ok {
		out = append(out, v)
	}
	return append(out, classifyGuests(e)...)
}

// classifyUnprobed renders every probe the collector could not run as an
// explicit Unknown verdict. Health is never Pass here — "we did not look" and
// "we looked and it was fine" must not collapse into the same output.
func classifyUnprobed(e Evidence) []Verdict {
	var out []Verdict
	for _, u := range e.Unprobed {
		v := Verdict{
			Rung:       RungUnknown,
			Health:     Unknown,
			Subject:    u.Subject,
			Reason:     fmt.Sprintf("could not probe %s", u.What),
			NextAction: u.NextAction,
		}
		if u.Detail != "" {
			v.Supporting = []string{u.Detail}
		}
		out = append(out, v)
	}
	return out
}

// classifyHostHardware covers the host-level signals observed live rather than
// inferred from a log. It returns (verdicts, terminal): terminal=true means a
// real host-hardware conviction that ends the ladder, terminal=false means the
// verdicts are disclosures the rest of the diagnosis must still run alongside.
//
// THE ONLY CONVICTING SIGNAL IS THE LOAD CANARY. Stock arm64 binaries taking
// SIGILL/SIGSEGV is a direct, present-tense observation of the machine
// miscomputing. Nothing else here is.
//
// WHY THE TWO-GUEST DISCRIMINATOR NO LONGER CONVICTS ON ITS OWN. It used to
// read "two running guests with an empty IP => the host's hardware is bad =>
// power-cycle and run Apple Diagnostics". Every term in that inference was
// wrong. GuestEvidence.IP is the readiness-CONFIRMED address (see its doc), so
// two guests are in that state for the whole of any ordinary boot — which is
// the steady state of a host reboot with two autostart guests, and is also
// reachable through doctor's own `umbra stop X && umbra start X` advice. The
// verdict it produced is the most expensive wrong answer this tool can give:
// it sends the operator to Apple, and host-hardware is in the ops watchdog's
// UNHEALABLE_RUNGS, so the watchdog stands down during a routine boot.
//
// This is not hypothetical. That exact signature was produced on a real host,
// a human operator concluded "host-level hardware fault, book Apple service",
// and the conclusion had to be retracted — the real cause was memory
// overcommit, which no rung here even looks at.
//
// So the discriminator now needs CORROBORATION, and the corroborating signal
// is the load canary. Correlated netstack log lines were considered and
// rejected: they point at umbra's user-mode network stack, whose remedy is
// "rebuild umbra", not at the CPU — using them to convict host-hardware would
// preempt the netstack rung (tier 2 runs before tier 3) and answer a netstack
// fault with a power-cycle. Uncorroborated, the two-guest fact becomes an
// explicit Unknown that names what doctor cannot yet distinguish, including
// the overcommit cause the ladder has no rung for.
//
// When the canary DOES fault, the two-guest fact still rides along as
// supporting evidence in the same verdict — dropping the canary detail, the
// single most decisive line in the report, was the reason these were merged.
func classifyHostHardware(e Evidence) ([]Verdict, bool) {
	var noAddr, unconfirmed, canary []string
	for _, g := range e.Guests {
		// Only a guest with NO readiness-confirmed IP is a candidate here. A
		// confirmed IP proves the guest has an address whatever the registry
		// record says, so ConfiguredIP is consulted only to explain WHY the
		// confirmed one is missing.
		if g.State == vm.StateRunning && g.IP == "" {
			if g.ConfiguredIP == "" {
				noAddr = append(noAddr, g.Name)
			} else {
				unconfirmed = append(unconfirmed, g.Name)
			}
		}
		if g.LoadCanary.Ran && g.LoadCanary.Faulted {
			canary = append(canary, fmt.Sprintf("%s: %s", g.Name, g.LoadCanary.Detail))
		}
	}

	// Guests that never reached a readiness-confirmed address. Both shapes
	// count: a missing machine record and a failed readiness are both "this
	// guest is not serving", which is what the discriminator is about.
	stalled := append(append([]string{}, noAddr...), unconfirmed...)
	sort.Strings(stalled)

	if len(canary) == 0 {
		if len(stalled) < 2 {
			return nil, false
		}
		return []Verdict{{
			Rung:   RungUnknown,
			Health: Unknown,
			Reason: fmt.Sprintf("%d running guests have not reached a readiness-confirmed address — doctor cannot tell a host-level cause from an ordinary boot without the load canary", len(stalled)),
			Supporting: []string{
				fmt.Sprintf("guests not readiness-confirmed: %v", stalled),
				"this is the NORMAL steady state for the ~90s readiness window after any host reboot with two autostart guests, and after any two restarts",
				twoGuestResidual,
			},
			NextAction: "wait out the readiness window and re-run: umbra list (expect an IP per guest). If they stay empty, run umbra doctor --deep for the load canary, and check host memory overcommit (umbra list vs. physical RAM) BEFORE suspecting hardware",
		}}, false
	}

	reasons := []string{"native binaries crashed with CPU-level signals under load"}
	evidence := append([]string{}, canary...)
	if len(stalled) >= 2 {
		reasons = append(reasons, "and two independent guests never reached a readiness-confirmed address")
		evidence = append(evidence, fmt.Sprintf("guests not readiness-confirmed: %v", stalled))
	}
	return []Verdict{{
		Rung:       RungHostHardware,
		Health:     Fail,
		Reason:     strings.Join(reasons, " "),
		Supporting: evidence,
		NextAction: "full power-cycle (shut down, wait, power on) then run Apple Diagnostics — config changes cannot fix miscomputing hardware, and do NOT recreate guests on this boot",
	}}, true
}

// twoGuestResidual is what the two-guest discriminator still cannot rule out
// even once the readiness window has passed, disclosed in the verdict rather
// than buried here. Doctor has no rung for host resource exhaustion: memory
// overcommit starves guests of the RAM they need to finish booting and
// reproduces this signature exactly, and it was the true cause the one time
// this signature was seen in production. Adding a real rung for it needs host
// memory pressure as evidence, which the collector does not gather today.
const twoGuestResidual = "RESIDUAL: doctor has no rung for host resource exhaustion. Memory overcommit (guests' combined memory-gib exceeding physical RAM) starves guests during boot and produces this exact signature — it was the real cause the one time this was seen in production, and was initially misdiagnosed as failing hardware."

// classifyNetstack detects host-wide netstack death by CORRELATION.
//
// The naive gate — "at least two distinct MACs appear in netstack error lines"
// — does not work, and getting this wrong is the single worst failure this
// tool can produce. "guest link <MAC> closed: cannot receive packets" is
// level=INFO and is emitted on EVERY ORDINARY GUEST SHUTDOWN. Measured against
// the real umbrad.err.log, 7 of 13 daemon lifetimes already carried two or
// more distinct MACs with no fault present at all. Under that gate the rung
// collapses into "any running guest is unreachable => rebuild umbra", which
// both misdiagnoses a guest that is merely still booting AND makes the
// guest-no-ip and guest-ssh-stall rungs unreachable, because this rung
// terminates the ladder before they are ever evaluated.
//
// So a MAC in the log is evidence only when it belongs to a guest that is
// RIGHT NOW running and unreachable, and at least two such guests are needed
// before the fault is host-wide rather than per-guest.
//
// KNOWN RESIDUAL, not fixed: the correlation has no time dimension, so a
// recent ordinary restart of two guests reproduces the signature. See
// restartResidual for the full argument and for what fixing it would require —
// the verdict discloses it to the operator in its own evidence.
//
// Returns (verdict, true) for both a conviction and an Unknown; the caller
// distinguishes them by Health.
func classifyNetstack(e Evidence) (Verdict, bool) {
	// Both sides of this lookup are lowercased. macRe accepts A-F, so a daemon
	// build that logged an uppercase MAC would fail every comparison against
	// the lowercase registry value — and it would do so SILENTLY, because the
	// guests do carry MACs and the tripwire below would therefore not fire.
	// The rung would just quietly stop working.
	logMACs := map[string]bool{}
	for _, l := range e.LogLines {
		if l.MAC != "" {
			logMACs[normalizeMAC(l.MAC)] = true
		}
	}
	if len(logMACs) == 0 {
		return Verdict{}, false
	}

	var unreachable []GuestEvidence
	for _, g := range e.Guests {
		if g.State != vm.StateRunning {
			continue
		}
		if g.IP == "" || (g.SSHProbed && !g.SSHOK) {
			unreachable = append(unreachable, g)
		}
	}
	if len(unreachable) == 0 {
		// THE STALE-LOG TRAP. The log outlives the fault it recorded, so log
		// evidence alone never convicts.
		return Verdict{}, false
	}

	// The tripwire. SCOPED TO THE UNREACHABLE GUESTS, because those are the
	// only ones the correlation below consumes. Scanning all of e.Guests (what
	// this did before wave 4) let a single healthy guest with a MAC satisfy the
	// check while every guest that MATTERED had none: the tripwire was skipped,
	// `correlated` stayed 0, the rung fell through to the per-guest ladder, and
	// netstack death became silently unevaluable. That is partial blindness
	// rendered as a confident per-guest diagnosis.
	//
	// Without a MAC on the guests that matter, the correlation is impossible
	// and the honest answer is "undiagnosed" — not a conviction on log evidence
	// alone, and not silence, which would render an unpopulated field as health.
	anyMAC := false
	for _, g := range unreachable {
		if g.MAC != "" {
			anyMAC = true
			break
		}
	}
	if !anyMAC {
		return Verdict{
			Rung:   RungUnknown,
			Health: Unknown,
			Reason: "cannot evaluate the netstack rung: no guest MAC available to correlate against the netstack log lines",
			Supporting: []string{
				fmt.Sprintf("%d distinct MACs with 'cannot receive packets' since daemon start", len(logMACs)),
				fmt.Sprintf("%d running guest(s) unreachable, none carrying a MAC", len(unreachable)),
			},
			NextAction: "report this: the collector is not populating GuestEvidence.MAC from the machine registry, so netstack death cannot be distinguished from a per-guest fault",
		}, true
	}

	var correlated []string
	// corroborated counts the correlated guests whose ssh probe failed DESPITE
	// a readiness-confirmed address. See the gate below for why that is the
	// discriminator.
	var corroborated []string
	for _, g := range unreachable {
		if g.MAC == "" || !logMACs[normalizeMAC(g.MAC)] {
			continue
		}
		correlated = append(correlated, g.Name)
		if g.IP != "" && g.SSHProbed && !g.SSHOK {
			corroborated = append(corroborated, g.Name)
		}
	}
	if len(correlated) < 2 {
		return Verdict{}, false
	}

	// THE RESTART GATE. Everything above is satisfied by an entirely routine
	// event: `umbra stop && umbra start` on two guests within one daemon
	// lifetime. The shutdown half emits the INFO-level "cannot receive
	// packets" line for each, and both guests are then running-and-unreachable
	// for their whole boot window. It is reachable by following doctor's OWN
	// guest-ssh-stall advice twice, and it convicted with "rebuild umbra".
	//
	// Disclosing that in the verdict's prose was not enough. netstack-dead is
	// in the ops watchdog's UNHEALABLE_RUNGS and the watchdog keys off `rung`
	// and `health` alone, so it stood down on a routine restart and never read
	// the caveat. A residual only the human sees is not a control.
	//
	// The discriminator, available without any daemon change: a guest that is
	// merely restarting has NO runtime IP, because umbrad publishes one only
	// after readiness succeeds. A guest that failed ssh WHILE HOLDING a
	// readiness-confirmed address got all the way up and then stopped
	// answering — no restart produces that. One such guest is enough to rule
	// the restart explanation out for the whole correlated set.
	//
	// This does NOT make the rung unreachable: real netstack death after boot
	// leaves exactly this signature, and it is what the rung was written for
	// (see TestClassifyNetstackStillConvictsWithSSHCorroboration). The case it
	// gives up on is netstack death that begins BEFORE any guest reaches
	// readiness, which is genuinely indistinguishable from a restart on this
	// evidence — that one degrades to Unknown, which is the correct answer to
	// an ambiguity and, unlike a Fail, does not make the watchdog stand down.
	if len(corroborated) == 0 {
		return Verdict{
			Rung:   RungUnknown,
			Health: Unknown,
			Reason: "netstack death cannot be distinguished from a routine restart: the correlated guests are unreachable only because readiness has not published an address yet",
			Supporting: []string{
				fmt.Sprintf("correlated guests: %v", correlated),
				fmt.Sprintf("%d distinct MACs with 'cannot receive packets' since daemon start", len(logMACs)),
				"'cannot receive packets' is level=INFO and is emitted by the shutdown half of an ordinary stop/start, so a recent restart of these guests reproduces this signature exactly",
				"no correlated guest failed ssh while holding a readiness-confirmed address, which is the observation a restart cannot produce",
				fmt.Sprintf("log window scanned: current daemon lifetime, since %s", e.DaemonStart.Format(time.RFC3339)),
				restartResidual,
			},
			NextAction: "if neither guest was restarted recently, re-run doctor after the readiness window (umbra list — expect an IP for each). A netstack fault that survives readiness convicts on the next run; if the addresses appear and ssh works, this was the restart.",
		}, true
	}

	return Verdict{
		Rung:   RungNetstackDead,
		Health: Fail,
		Reason: fmt.Sprintf("%d running guests are unreachable AND named by netstack errors in the current daemon lifetime", len(correlated)),
		Supporting: []string{
			fmt.Sprintf("correlated guests: %v", correlated),
			fmt.Sprintf("%d distinct MACs with 'cannot receive packets' since daemon start", len(logMACs)),
			"live reachability check failed for each correlated guest",
			fmt.Sprintf("ssh-corroborated (readiness-confirmed address, ssh dead): %v — a restart cannot produce this, which is what rules the restart explanation out", corroborated),
			fmt.Sprintf("log window scanned: current daemon lifetime, since %s", e.DaemonStart.Format(time.RFC3339)),
		},
		NextAction: "cd ~/Desktop/projects/umbra && make build && make install",
	}, true
}

// restartResidual describes the netstack rung's false positive. It is now
// attached to the UNKNOWN branch — the case the rung declines to judge — not
// to a conviction.
//
// "guest link <MAC> closed: cannot receive packets" is emitted by the SHUTDOWN
// half of an ordinary `umbra stop && umbra start`. The correlation has no time
// dimension, so two guests restarted within one daemon lifetime and still
// booting satisfy every MAC-correlation condition this rung checks. It is
// reachable through doctor's OWN advice: the guest-ssh-stall next action is
// `umbra stop X && umbra start X`, so following doctor twice manufactures it.
//
// WHAT CHANGED IN WAVE 6. Previously this text rode along inside a Fail
// verdict as a disclosure. That was insufficient in a way the prose could not
// fix: netstack-dead is in the ops watchdog's UNHEALABLE_RUNGS and the
// watchdog keys off `rung` and `health` only, so a routine restart made it
// stand down and no human ever saw the caveat. A residual that only a human
// can act on is not a control on an automated consumer.
//
// The rung now requires SSH CORROBORATION before convicting: at least one
// correlated guest must have failed ssh while holding a readiness-confirmed
// address, which a restarting guest cannot do because it has no address yet.
// Uncorroborated cases become Unknown and carry this string.
//
// WHAT IS STILL NOT FIXED. Netstack death that begins before ANY guest reaches
// readiness is still indistinguishable from a restart here, and lands in the
// Unknown branch rather than convicting. Closing that needs the guest's
// CURRENT BOOT time so the log line can be required to POSTDATE it, and no
// such timestamp exists to pass in as evidence: registry.Machine carries only
// CreatedAt (machine creation, not boot), vm.Info exposes
// Name/State/IP/SSHPort/Zombie with no start time, the in-memory vm.instance
// records none, and MachineView adds nothing further. Reading a boot time
// inside Classify is impossible by construction — Classify is pure — and
// synthesising one here would be a guess dressed as a correlation. The daemon
// change that would close it: record a per-machine start time on the
// transition to running, surface it on vm.Info and MachineView, put it on
// GuestEvidence, and require the log line to postdate it.
const restartResidual = "RESIDUAL: this rung has no time dimension — two guests restarted within this daemon lifetime and still booting reproduce this signature. This verdict is Unknown rather than a conviction for exactly that reason; confirm whether either guest was restarted recently."

// normalizeMAC canonicalises a link-layer address for comparison. See the note
// at the top of classifyNetstack for why this is not cosmetic.
func normalizeMAC(mac string) string { return strings.ToLower(strings.TrimSpace(mac)) }

// classifyMachineState renders a machine's LIFECYCLE state, before any network
// or in-guest rung is meaningful. It returns (verdict, true) when the state
// itself is the finding, and ok=false when the caller should carry on down the
// ladder (running) or say nothing (stopped).
//
// Every vm.State is handled EXPLICITLY. Gating on `State != StateRunning` and
// moving on — which is what this replaced — gave StateCrashed, StateStarting
// and StateStopping ZERO verdicts and ZERO unprobed records, so a fleet with
// one healthy guest and one crashed CI runner printed "healthy: no faults
// detected" and exited 0. `umbra list` already prints `crashed*` for exactly
// that machine, so doctor was strictly LESS informative than list about a
// fault class list already names.
func classifyMachineState(g GuestEvidence) (Verdict, bool) {
	switch g.State {
	case vm.StateRunning:
		return Verdict{}, false // the ladder proper; not a lifecycle finding

	case vm.StateStopped:
		// The ONLY deliberately silent state, and the only one that is a
		// legitimate resting place: the standby spare lives here, and doctor
		// must not report the spare as a fault on every single run. doctor
		// cannot know an operator's intent for a stopped machine, and `umbra
		// list` already shows it, so inventing a verdict here would be noise
		// that trains the operator to ignore output.
		return Verdict{}, false

	case vm.StateStarting, vm.StateStopping:
		// TRANSIENT, so NOT a fault — doctor races `umbra start`/`umbra stop`
		// routinely, and failing on that would make the watchdog stand down
		// during an ordinary restart. But it is not silence either: while a
		// machine is neither up nor down, every rung below is unevaluable, and
		// rendering that as "no faults" is the exact defect this package
		// exists to prevent. So: Unknown, named, and re-checkable.
		return Verdict{
			Rung: RungUnknown, Health: Unknown, Subject: g.Name,
			Reason: fmt.Sprintf("machine is %s — a transient state, so no guest or CI rung can be evaluated for it yet", g.State),
			Supporting: []string{
				"this is not a fault: doctor observed the machine mid-transition",
			},
			NextAction: fmt.Sprintf("re-run doctor once the transition settles: umbra list (expect %s to reach running or stopped)", g.Name),
		}, true

	case vm.StateCrashed:
		if g.Zombie {
			// The worst case. umbrad asked the VM to stop, the stop never
			// confirmed, and the handle is still live — so the guest may still
			// be executing, holding CPU, memory and its netstack attachment,
			// while umbra believes it is gone. Start refuses to relaunch until
			// a stop confirms, so the remedy is a repeated stop, NOT a start.
			return Verdict{
				Rung: RungMachineCrashed, Health: Fail, Subject: g.Name,
				Reason: "machine is crashed with an unconfirmed stop (zombie) — the VM may still be alive",
				Supporting: []string{
					"umbrad still holds a live VM handle for this machine, so it may still be running and consuming host CPU, memory and its netstack attachment",
					"umbra start will refuse to relaunch it until a stop confirms",
				},
				NextAction: fmt.Sprintf("umbra stop %s (repeat until it reports stopped), then umbra start %s — do NOT umbra rm it while the handle is live", g.Name, g.Name),
			}, true
		}
		return Verdict{
			Rung: RungMachineCrashed, Health: Fail, Subject: g.Name,
			Reason: "machine is crashed — it is neither running nor cleanly stopped, so it serves no CI",
			Supporting: []string{
				"a crashed machine is invisible to every rung below this one; nothing about its guest, runners or repos was probed",
			},
			NextAction: fmt.Sprintf("umbra start %s — if that fails, check the daemon log: umbra daemon status", g.Name),
		}, true
	}

	// An unrecognised state means umbra grew a lifecycle state this classifier
	// was never taught. Unknown, loudly — falling through to silence would
	// re-open exactly the hole this function closed.
	return Verdict{
		Rung: RungUnknown, Health: Unknown, Subject: g.Name,
		Reason:     fmt.Sprintf("machine is in an unrecognised state %q, which doctor cannot classify", g.State),
		NextAction: "report this: umbra grew a machine state that internal/doctor does not handle",
	}, true
}

// classifyGuests walks the per-guest rungs, then appends the per-repo rungs.
// The host-level discriminators it used to own now live in
// classifyHostHardware, which Classify evaluates first — see the precedence
// note there.
func classifyGuests(e Evidence) []Verdict {
	var out []Verdict
	served := reposWithAnActiveRunner(e)

	for _, g := range e.Guests {
		if v, ok := classifyMachineState(g); ok {
			out = append(out, v)
			continue
		}
		if g.State != vm.StateRunning {
			continue // stopped; see classifyMachineState for why that is silent
		}
		// The two no-address branches are reached only when there is NO
		// readiness-confirmed IP; a confirmed IP settles the question on its
		// own and the ladder carries on to ssh and the runner units.
		switch {
		case g.IP != "" && g.SSHProbed && !g.SSHOK:
			out = append(out, Verdict{
				Rung: RungGuestSSHStall, Health: Fail, Subject: g.Name,
				Reason:     "guest has an IP but ssh is not accepting connections",
				NextAction: fmt.Sprintf("umbra stop %s && umbra start %s, then wait for: umbra exec %s cloud-init status", g.Name, g.Name, g.Name),
			})
			continue
		case g.IP != "":
			// Reachable: fall through to the runner units below.
		case g.ConfiguredIP == "" && !e.ConfiguredIPReported:
			// AMBIGUOUS, SO IT MUST NOT CONVICT. No machine on this fleet
			// reported a configured address, which happens for two completely
			// different reasons: the record really is damaged, or the daemon
			// is an older build that does not serialise configured_ip at all
			// and every record is fine. The remedies are opposite —
			// `umbra rm && umbra create` versus `make install` — and one of
			// them destroys a healthy CI runner.
			//
			// Wave 5 asserted "a configured address is written at create time
			// and is independent of readiness", which is true of the registry
			// and irrelevant here: what is missing is the REPORT of it, not
			// the address. Unknown, named, and pointing at the field.
			out = append(out, Verdict{
				Rung: RungUnknown, Health: Unknown, Subject: g.Name,
				Reason: "machine is running with no readiness-confirmed IP, and no configured address was reported for any machine — this cannot be told apart from a daemon that predates the field",
				Supporting: []string{
					"no machine on this fleet reported a configured_ip, so the empty value may be the daemon's silence rather than a damaged record",
					"a damaged record and an out-of-date daemon need opposite remedies, so this rung will not guess between them",
				},
				NextAction: fmt.Sprintf("confirm the daemon reports the field: umbra list --json (expect configured_ip). If it is absent, rebuild and restart the daemon (cd ~/Desktop/projects/umbra && make build && make install) and re-run doctor; if it is present for other machines but not %s, that record is damaged", g.Name),
			})
			continue
		case g.ConfiguredIP == "":
			// No address in the machine record, ON A DAEMON THAT DEMONSTRABLY
			// REPORTS THE FIELD for other machines. The silence is this
			// machine's, not the daemon's, so the record is genuinely damaged:
			// it cannot resolve by waiting, and this is the one shape of
			// "no IP" that convicts.
			out = append(out, Verdict{
				Rung: RungGuestNoIP, Health: Fail, Subject: g.Name,
				Reason: "machine reports running but its registry record carries no configured address",
				Supporting: []string{
					"the daemon reported a configured address for other machines on this fleet, so this record is damaged rather than unreported",
					"this is not the boot window: a configured address is written at create time and is independent of readiness",
				},
				NextAction: fmt.Sprintf("umbra doctor --deep (confirm host-level first), else: umbra stop %s && umbra rm %s && umbra create %s --role ci-runner --cpus 3 --memory-gib 3 --disk-gib 60 --autostart", g.Name, g.Name, g.Name),
			})
			continue
		case g.IP == "":
			// Configured but not readiness-confirmed. umbrad sets the runtime
			// IP only after its readiness probe succeeds, up to 90s after the
			// state flips to running — so this is what EVERY healthy guest
			// looks like while it boots. Convicting it (and, for two guests,
			// escalating to failing hardware) told operators to destroy
			// healthy machines. Unknown, named, re-checkable.
			out = append(out, Verdict{
				Rung: RungUnknown, Health: Unknown, Subject: g.Name,
				Reason: "machine is running with a configured address but no readiness-confirmed IP — it is still booting, or its readiness probe timed out",
				Supporting: []string{
					fmt.Sprintf("configured address: %s (present, so the machine record is intact)", g.ConfiguredIP),
					"umbrad publishes the runtime IP only after readiness succeeds, up to 90s after the machine enters running",
				},
				NextAction: fmt.Sprintf("re-run doctor after the readiness window: umbra list (expect an IP for %s). If it never appears, check the guest console and host memory pressure before recreating anything", g.Name),
			})
			continue
		}
		for _, r := range g.Runners {
			if r.Active {
				continue
			}
			// A unit mid-transition is not an outage. systemd reports
			// `activating` while the runner starts, and readiness returns as
			// soon as sshd answers on :22 — so on every host reboot there is a
			// real window where the guest is readiness-confirmed and its runner
			// is still coming up. Convicting there would page an operator for a
			// normal boot.
			if r.Transitional {
				out = append(out, Verdict{
					Rung: RungUnknown, Health: Unknown, Subject: g.Name,
					Reason:     fmt.Sprintf("runner unit %s is mid-transition (activating/deactivating), so its health cannot be judged yet", r.Unit),
					Supporting: []string{"this is the normal state during host reboot or autostart — readiness only waits for sshd, not for the runner service"},
					NextAction: fmt.Sprintf("re-run doctor in ~30s; if it is still transitional: umbra exec %s systemctl status %s", g.Name, r.Unit),
				})
				continue
			}
			// AN INACTIVE UNIT IS NOT AUTOMATICALLY AN OUTAGE. Registering a
			// runner leaves the previous unit behind, enabled but dead, so a
			// repo routinely carries a stale unit alongside the live one that
			// is serving its CI right now. Verified on this host:
			// actions.runner.<org>-<repo>.<guest>-1 sat `loaded inactive dead`
			// while <guest>-<repo>-1 was `active running` and executing jobs —
			// and `umbra doctor` therefore exited 1 on every run of a HEALTHY
			// fleet. A tool that cries wolf on a healthy host trains its
			// operator, and its watchdog, to stop reading it.
			//
			// The discriminator is per-REPO, not per-unit and not per-guest:
			// the repo's CI is served as long as SOME runner for that scope is
			// active anywhere on the fleet.
			if scope := RunnerUnitScope(r.Unit); scope != "" && served[scope] {
				out = append(out, Verdict{
					Rung: RungUnknown, Health: Unknown, Subject: g.Name,
					Reason: fmt.Sprintf("runner unit %s is inactive, but another runner for the same repo scope is active — this is a stale registration, not an outage", r.Unit),
					Supporting: []string{
						fmt.Sprintf("repo scope %q still has at least one active runner unit on this fleet", scope),
						"restarting this unit would re-register a duplicate runner rather than fix anything",
					},
					NextAction: fmt.Sprintf("delete the stale unit once you have confirmed it is unused: umbra exec %s sudo systemctl disable --now %s", g.Name, r.Unit),
				})
				continue
			}
			out = append(out, Verdict{
				Rung: RungRunnerServiceDown, Health: Fail, Subject: g.Name,
				Reason: fmt.Sprintf("runner unit %s is not active and no other runner for its repo scope is active on this fleet", r.Unit),
				Supporting: []string{
					"no active runner means this repo's jobs queue with nothing to pick them up",
				},
				NextAction: fmt.Sprintf("umbra exec %s sudo systemctl restart %s", g.Name, r.Unit),
			})
		}
	}

	return append(out, classifyRepos(e)...)
}

// reposWithAnActiveRunner maps a repo scope to whether ANY guest on the fleet
// is running an active runner unit for it. See the stale-registration note in
// classifyGuests for why this is fleet-wide rather than per-guest.
func reposWithAnActiveRunner(e Evidence) map[string]bool {
	served := map[string]bool{}
	for _, g := range e.Guests {
		for _, r := range g.Runners {
			if !r.Active {
				continue
			}
			if scope := RunnerUnitScope(r.Unit); scope != "" {
				served[scope] = true
			}
		}
	}
	return served
}

// RunnerUnitScope extracts the scope from a systemd unit named
// actions.runner.<scope>.<instance>.service, where <scope> is the escaped
// "<owner>-<repo>" (or a bare org for an org-level runner). It returns "" for
// anything that is not a runner unit.
//
// It lives HERE, in the pure package, because both the classifier (to group
// units by repo) and the collector (to derive which repos to probe on GitHub)
// need it, and two copies of this parse would drift the moment either changed.
// It is pure string handling — no I/O — so it does not compromise Classify.
func RunnerUnitScope(unit string) string {
	s := strings.TrimSuffix(unit, ".service")
	s = strings.TrimPrefix(s, "actions.runner.")
	if s == unit || s == "" {
		return ""
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// classifyRepos covers the GitHub-side rungs. These matter because a perfectly
// healthy umbra host still leaves every PR blocked when the runner registration
// is stale or the org is billing-locked — and all three states look alike from
// the outside: billing failures die in ~3s, offline runners queue forever, and
// netstack death queues AND makes guests unreachable.
func classifyRepos(e Evidence) []Verdict {
	var out []Verdict
	for _, r := range e.Repos {
		if !r.Probed {
			// RungUnknown, NOT RungRunnerOffline. "We could not tell" is not
			// the same fault as "the runner is offline", and a consumer keying
			// remediation off Rung would otherwise act on a fault that was
			// never actually diagnosed.
			//
			// The remedy depends on WHY. "install and authenticate the GitHub
			// CLI" is actively misleading when gh is installed and
			// authenticated and the call failed on a rate limit or an
			// unresolvable repo — it sends the operator to re-do something
			// that is already done while the real cause goes unexamined.
			reason := "could not probe GitHub (gh missing, unauthenticated, or rate-limited)"
			next := "install and authenticate the GitHub CLI: brew install gh && gh auth login"
			if e.GHAvailable {
				reason = "gh is installed but the GitHub probe did not complete (unauthenticated, rate-limited, or the repo could not be resolved from the runner unit name)"
				next = fmt.Sprintf("check which of the three it is: gh auth status; gh api rate_limit; gh api repos/%s", r.Repo)
			}
			out = append(out, Verdict{
				Rung: RungUnknown, Health: Unknown, Subject: r.Repo,
				Reason:     reason,
				NextAction: next,
			})
			continue
		}
		if r.BillingLockout {
			supporting := []string{"signature: runner_name empty, steps empty, ~3s duration"}
			next := "clear the org billing block: GitHub org -> Settings -> Billing & plans (only the org owner can do this)"
			// Three different causes produce an identical fingerprint in the
			// jobs API: an org billing block, exhausted cloud minutes, and no
			// runner matching the requested labels. Surface the labels so a
			// human can tell them apart at a glance instead of being sent to
			// the billing page for a label mismatch.
			if len(r.BillingLabels) > 0 {
				supporting = append(supporting,
					fmt.Sprintf("blocked jobs requested labels: %v", r.BillingLabels))
				if !requestsGitHubHosted(r.BillingLabels) {
					next = fmt.Sprintf("these jobs asked for %v, not a GitHub-hosted label — check the runner is registered and its labels match before looking at billing: umbra doctor, then gh api repos/%s/actions/runners", r.BillingLabels, r.Repo)
				}
			}
			out = append(out, Verdict{
				Rung: RungBillingLockout, Health: Fail, Subject: r.Repo,
				Reason:     "jobs are failing in ~3s with no runner assigned and zero steps",
				Supporting: supporting,
				NextAction: next,
			})
			continue
		}
		// Sort the runner names before emitting. Go randomises map iteration
		// order, and Classify promises ORDERED findings — without this, a repo
		// with two offline runners reports them in a different order on every
		// run, churning --json output and seeding a flaky test the first time
		// anyone asserts on multi-runner results.
		names := make([]string, 0, len(r.RunnerOnline))
		for name := range r.RunnerOnline {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !r.RunnerOnline[name] {
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

// requestsGitHubHosted reports whether the label set names a GitHub-hosted
// runner. Those are the only labels for which "billing" is the likely cause of
// a no-runner-assigned failure; a self-hosted label set failing the same way
// points at a runner that never registered.
func requestsGitHubHosted(labels []string) bool {
	for _, l := range labels {
		switch {
		case strings.HasPrefix(l, "ubuntu-"),
			strings.HasPrefix(l, "windows-"),
			strings.HasPrefix(l, "macos-"):
			return true
		}
	}
	return false
}
