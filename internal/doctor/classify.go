package doctor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ForceAI-KW/umbra/internal/vm"
)

// stateRunning is the one machine state the ladder branches on. It is derived
// from the upstream enum rather than written as a literal so that renaming
// vm.StateRunning breaks this build instead of silently making every rung
// stop matching. (Importing vm costs nothing here: it is a constant, and
// Classify stays pure.)
const stateRunning = string(vm.StateRunning)

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
func Classify(e Evidence) []Verdict {
	if !e.DaemonUp {
		return []Verdict{{
			Rung:       RungDaemonDown,
			Health:     Fail,
			Reason:     "umbrad is not responding on its API socket",
			NextAction: "umbra daemon install",
		}}
	}

	if v, ok := classifyHostHardware(e); ok {
		return []Verdict{v}
	}

	v, ok := classifyNetstack(e)
	if ok && v.Health == Fail {
		return []Verdict{v}
	}
	// An Unknown netstack verdict means the probe could not be evaluated. That
	// degrades this one rung; it must not swallow the rest of the diagnosis.
	var out []Verdict
	if ok {
		out = append(out, v)
	}
	return append(out, classifyGuests(e)...)
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
		if g.State == stateRunning && g.IP == "" {
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
		Evidence:   evidence,
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
// Returns (verdict, true) for both a conviction and an Unknown; the caller
// distinguishes them by Health.
func classifyNetstack(e Evidence) (Verdict, bool) {
	logMACs := map[string]bool{}
	for _, l := range e.LogLines {
		if l.MAC != "" {
			logMACs[l.MAC] = true
		}
	}
	if len(logMACs) == 0 {
		return Verdict{}, false
	}

	var unreachable []GuestEvidence
	for _, g := range e.Guests {
		if g.State != stateRunning {
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

	// The wave-2 tripwire. Without MACs on the guests the correlation above is
	// impossible, and the honest answer is "undiagnosed" — not a conviction on
	// log evidence alone, and not silence, which would render an unpopulated
	// field as health.
	anyMAC := false
	for _, g := range e.Guests {
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
			Evidence: []string{
				fmt.Sprintf("%d distinct MACs with 'cannot receive packets' since daemon start", len(logMACs)),
				fmt.Sprintf("%d running guest(s) unreachable, none carrying a MAC", len(unreachable)),
			},
			NextAction: "report this: the collector is not populating GuestEvidence.MAC from the machine registry, so netstack death cannot be distinguished from a per-guest fault",
		}, true
	}

	var correlated []string
	for _, g := range unreachable {
		if g.MAC != "" && logMACs[g.MAC] {
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
		Evidence: []string{
			fmt.Sprintf("correlated guests: %v", correlated),
			fmt.Sprintf("%d distinct MACs with 'cannot receive packets' since daemon start", len(logMACs)),
			"live reachability check failed for each correlated guest",
		},
		NextAction: "cd ~/Desktop/projects/umbra && make build && make install",
	}, true
}

// classifyGuests walks the per-guest rungs, then appends the per-repo rungs.
// The host-level discriminators it used to own now live in
// classifyHostHardware, which Classify evaluates first — see the precedence
// note there.
func classifyGuests(e Evidence) []Verdict {
	var out []Verdict

	for _, g := range e.Guests {
		if g.State != stateRunning {
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
			out = append(out, Verdict{
				Rung: RungUnknown, Health: Unknown, Subject: r.Repo,
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
