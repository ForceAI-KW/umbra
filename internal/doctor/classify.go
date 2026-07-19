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

// classifyGuests is filled in by Task 4. Declared here so Task 3 compiles.
func classifyGuests(e Evidence) []Verdict { return nil }
