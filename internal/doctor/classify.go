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

	if v, ok := classifyHostHardware(e); ok {
		return append([]Verdict{v}, unknowns...)
	}

	v, ok := classifyNetstack(e)
	if ok && v.Health == Fail {
		return append([]Verdict{v}, unknowns...)
	}
	// An Unknown netstack verdict means the probe could not be evaluated. That
	// degrades this one rung; it must not swallow the rest of the diagnosis.
	out := unknowns
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

// classifyHostHardware covers the two host-level signals that are observed
// live rather than inferred from a log: two independent guests failing
// identically, and stock arm64 binaries taking CPU-level signals under load.
//
// Both produce the same rung and the same remedy, so when both are present
// they are merged into ONE verdict carrying BOTH evidence strings. Returning
// early on the first would drop the canary detail — the single most decisive
// line in the whole report — from the output a human actually reads.
func classifyHostHardware(e Evidence) (Verdict, bool) {
	var noIP []string
	var canary []string
	for _, g := range e.Guests {
		if g.State == vm.StateRunning && g.IP == "" {
			noIP = append(noIP, g.Name)
		}
		if g.LoadCanary.Ran && g.LoadCanary.Faulted {
			canary = append(canary, fmt.Sprintf("%s: %s", g.Name, g.LoadCanary.Detail))
		}
	}

	// Two independent guests failing to obtain an IP is host-level, not two
	// coincidentally damaged images — and it rules out a ~20-minute recreate.
	twoGuest := len(noIP) >= 2
	if !twoGuest && len(canary) == 0 {
		return Verdict{}, false
	}

	var reasons, evidence []string
	if len(canary) > 0 {
		reasons = append(reasons, "native binaries crashed with CPU-level signals under load")
		evidence = append(evidence, canary...)
	}
	if twoGuest {
		reasons = append(reasons, "two independent guests failed to obtain an IP — host-level, not guest-image")
		evidence = append(evidence, fmt.Sprintf("guests with no IP: %v", noIP))
	}
	return Verdict{
		Rung:       RungHostHardware,
		Health:     Fail,
		Reason:     strings.Join(reasons, "; "),
		Supporting: evidence,
		NextAction: "full power-cycle (shut down, wait, power on) then run Apple Diagnostics — config changes cannot fix miscomputing hardware, and do NOT recreate guests on this boot",
	}, true
}

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
	for _, g := range unreachable {
		if g.MAC != "" && logMACs[normalizeMAC(g.MAC)] {
			correlated = append(correlated, g.Name)
		}
	}
	if len(correlated) < 2 {
		return Verdict{}, false
	}
	return Verdict{
		Rung:   RungNetstackDead,
		Health: Fail,
		Reason: fmt.Sprintf("%d running guests are unreachable AND named by netstack errors in the current daemon lifetime", len(correlated)),
		Supporting: []string{
			fmt.Sprintf("correlated guests: %v", correlated),
			fmt.Sprintf("%d distinct MACs with 'cannot receive packets' since daemon start", len(logMACs)),
			"live reachability check failed for each correlated guest",
			fmt.Sprintf("log window scanned: current daemon lifetime, since %s", e.DaemonStart.Format(time.RFC3339)),
			restartResidual,
		},
		NextAction: "cd ~/Desktop/projects/umbra && make build && make install",
	}, true
}

// restartResidual is the KNOWN, UNELIMINATED false positive of the netstack
// rung, disclosed in the verdict itself rather than buried in a comment the
// operator will never read.
//
// "guest link <MAC> closed: cannot receive packets" is emitted by the SHUTDOWN
// half of an ordinary `umbra stop && umbra start`. The correlation has no time
// dimension, so two guests restarted within one daemon lifetime and still
// booting satisfy every condition this rung checks and get told to rebuild
// umbra. It is reachable through doctor's OWN advice: the guest-ssh-stall next
// action is `umbra stop X && umbra start X`, so following doctor twice can
// manufacture it.
//
// WHY IT IS DOCUMENTED RATHER THAN FIXED. Eliminating it needs the guest's
// CURRENT BOOT time, so the log line can be required to POSTDATE it. No such
// timestamp exists anywhere to pass in as evidence: registry.Machine carries
// only CreatedAt (machine creation, not boot), vm.Info exposes
// Name/State/IP/SSHPort/Zombie with no start time, the in-memory vm.instance
// records none, and client.MachineView adds nothing further. Reading a boot
// time inside Classify is impossible by construction — Classify is pure — and
// synthesising one from anything else here would be a fabricated correlation
// that reads as rigour while being a guess. Disclosing a residual beats faking
// its absence. Removing it is a daemon change: record a per-machine start time
// on state transition to running, surface it on vm.Info and MachineView, put it
// on GuestEvidence, and gate the correlation on it.
const restartResidual = "RESIDUAL: this rung has no time dimension — two guests restarted within this daemon lifetime and still booting can also produce this signature. Before rebuilding umbra, confirm neither guest was restarted recently."

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

	for _, g := range e.Guests {
		if v, ok := classifyMachineState(g); ok {
			out = append(out, v)
			continue
		}
		if g.State != vm.StateRunning {
			continue // stopped; see classifyMachineState for why that is silent
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
