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

// classifyRepos is filled in by Task 5. Declared here so Task 4 compiles.
func classifyRepos(e Evidence) []Verdict { return nil }
