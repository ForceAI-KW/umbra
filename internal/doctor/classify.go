package doctor

import "fmt"

// Classify turns observed Evidence into ordered findings. It is pure: no I/O,
// no clock, no filesystem. That is what lets every rung be tested with a
// literal Evidence struct instead of a deliberately broken host.
//
// Rungs 0-1 are host-wide and TERMINATE the ladder — when the daemon is down
// or the netstack is dead, nothing below can be diagnosed meaningfully and
// reporting lower rungs would just be noise pointing at the wrong fix.
func Classify(e Evidence) []Verdict {
	if !e.DaemonUp {
		return []Verdict{{
			Rung:       RungDaemonDown,
			Health:     Fail,
			Reason:     "umbrad is not responding on its API socket",
			NextAction: "umbra daemon install",
		}}
	}

	if v, ok := classifyNetstack(e); ok {
		return []Verdict{v}
	}

	return classifyGuests(e)
}

// classifyNetstack detects host-wide netstack death.
//
// Two conditions are BOTH required: netstack errors across at least two
// distinct guest MACs, AND at least one running guest that is actually
// unreachable right now. Log evidence alone never convicts — umbrad.err.log
// outlives the fault it recorded, so a healthy host can carry damning-looking
// lines indefinitely. ScanLog already trims to the current daemon lifetime;
// this is the second, independent guard.
func classifyNetstack(e Evidence) (Verdict, bool) {
	macs := map[string]bool{}
	for _, l := range e.LogLines {
		if l.MAC != "" {
			macs[l.MAC] = true
		}
	}
	if len(macs) < 2 {
		return Verdict{}, false
	}
	if !anyRunningGuestUnreachable(e) {
		return Verdict{}, false
	}
	return Verdict{
		Rung:   RungNetstackDead,
		Health: Fail,
		Reason: fmt.Sprintf("netstack errors across %d guest MACs and at least one running guest is unreachable", len(macs)),
		Evidence: []string{
			fmt.Sprintf("%d distinct MACs with 'cannot receive packets' since daemon start", len(macs)),
			"live reachability check failed for a running guest",
		},
		NextAction: "cd ~/Desktop/projects/umbra && make build && make install",
	}, true
}

func anyRunningGuestUnreachable(e Evidence) bool {
	for _, g := range e.Guests {
		if g.State != "running" {
			continue
		}
		if g.IP == "" {
			return true
		}
		if g.SSHProbed && !g.SSHOK {
			return true
		}
	}
	return false
}

// classifyGuests walks the per-guest rungs in blast-radius order, then
// appends the per-repo rungs from Task 5.
func classifyGuests(e Evidence) []Verdict {
	var out []Verdict

	// Two-guest discriminator, evaluated BEFORE the per-guest rungs: if two
	// independent guests fail to obtain an IP, the fault is host-level and
	// recreating either image is wasted effort.
	var noIP []string
	for _, g := range e.Guests {
		if g.State == "running" && g.IP == "" {
			noIP = append(noIP, g.Name)
		}
	}
	if len(noIP) >= 2 {
		return []Verdict{{
			Rung:       RungHostHardware,
			Health:     Fail,
			Reason:     "two independent guests failed to obtain an IP — host-level, not guest-image",
			Evidence:   []string{fmt.Sprintf("guests with no IP: %v", noIP)},
			NextAction: "full power-cycle (shut down, wait, power on) then run Apple Diagnostics — do NOT recreate guests on this boot",
		}}
	}

	// Load canary faults are also host-level and outrank everything per-guest:
	// stock arm64 binaries taking SIGILL/SIGSEGV means the guest is miscomputing.
	for _, g := range e.Guests {
		if g.LoadCanary.Ran && g.LoadCanary.Faulted {
			return []Verdict{{
				Rung:       RungHostHardware,
				Health:     Fail,
				Reason:     "native binaries crashed with CPU-level signals under load",
				Evidence:   []string{fmt.Sprintf("%s: %s", g.Name, g.LoadCanary.Detail)},
				NextAction: "full power-cycle (shut down, wait, power on) then run Apple Diagnostics — config changes cannot fix miscomputing hardware",
			}}
		}
	}

	for _, g := range e.Guests {
		if g.State != "running" {
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
			out = append(out, Verdict{
				Rung: RungRunnerOffline, Health: Unknown, Subject: r.Repo,
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
		for name, online := range r.RunnerOnline {
			if !online {
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
