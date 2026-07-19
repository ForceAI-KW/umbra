// Package doctor diagnoses umbra host/guest/CI faults by classifying
// collected evidence into one rung of a documented triage ladder.
//
// The package is deliberately split: Collect does all I/O, Classify is a
// pure function. That split is what makes every rung testable without a
// broken host — which matters, because the faults this diagnoses are rare,
// destructive, and impossible to reproduce on demand.
package doctor

import "time"

// Health is the outcome of a single probe. Unknown means the probe could
// not run (no gh, guest unreachable) — it is never rendered as pass.
type Health string

const (
	// Pass means the probe ran and found the thing healthy.
	Pass Health = "pass"
	// Fail means the probe ran and found a real fault.
	Fail Health = "fail"
	// Unknown means the probe could not run at all, so nothing is known
	// either way — the state that must never be collapsed into Pass.
	Unknown Health = "unknown"
)

// Rung identifies a step on the triage ladder, ordered by triage sequence:
// cheapest and most common causes first, and RungHostHardware last because
// it is the diagnosis of exclusion — you only conclude the hardware is bad
// after everything cheaper has been ruled out. Note this is deliberately NOT
// blast-radius order: host-wide rungs terminate the ladder in Classify by
// explicit control flow, not by their position here. RungNone is the zero
// value and means healthy.
//
// RungUnknown is deliberately declared AFTER RungHostHardware: it is not a step
// on the ladder at all, so placing it inside the sequence would break the
// triage reading above.
type Rung int

const (
	// RungNone is the zero value: nothing is wrong.
	RungNone Rung = iota
	// RungDaemonDown means umbrad itself is not answering — no guest fact
	// below this can even be collected, let alone trusted.
	RungDaemonDown
	// RungNetstackDead means the host's user-mode network stack has stopped
	// forwarding for guests generally; every guest looks broken at once.
	RungNetstackDead
	// RungGuestNoIP means one guest booted but never got a DHCP lease — its
	// image or cloud-init is damaged, while its neighbours are fine.
	RungGuestNoIP
	// RungGuestSSHStall means the guest holds an IP but sshd never came up,
	// which is normally cloud-init still running or wedged.
	RungGuestSSHStall
	// RungRunnerServiceDown means the guest is fully reachable and the
	// actions.runner systemd unit is simply not active.
	RungRunnerServiceDown
	// RungRunnerOffline means the unit runs locally but GitHub still lists the
	// runner as offline — a stale registration, fixed on the GitHub side.
	RungRunnerOffline
	// RungBillingLockout means GitHub refuses to start jobs at all — no runner
	// is ever assigned, so nothing on the host can help.
	RungBillingLockout
	// RungHostHardware is the diagnosis of exclusion: the machine itself is
	// miscomputing, and only a power-cycle plus Apple Diagnostics applies.
	RungHostHardware
	// RungUnknown means a probe could not run, so this fault was never
	// diagnosed. It is NOT a ladder step — it exists so a consumer keying
	// remediation off Rung can tell "undiagnosed" apart from any real fault.
	RungUnknown
)

// String renders a rung as a short stable slug, used in --json output.
func (r Rung) String() string {
	switch r {
	case RungNone:
		return "healthy"
	case RungDaemonDown:
		return "daemon-down"
	case RungNetstackDead:
		return "netstack-dead"
	case RungGuestNoIP:
		return "guest-no-ip"
	case RungGuestSSHStall:
		return "guest-ssh-stall"
	case RungRunnerServiceDown:
		return "runner-service-down"
	case RungRunnerOffline:
		return "runner-offline"
	case RungBillingLockout:
		return "billing-lockout"
	case RungHostHardware:
		return "host-hardware"
	case RungUnknown:
		return "unknown"
	}
	return "unknown"
}

// MarshalText makes Rung serialize as its stable slug rather than its
// integer value — the --json output is a watchdog contract, and an integer
// that shifts when a rung is inserted would silently break it.
func (r Rung) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// LogLine is one parsed line of umbrad.err.log, already filtered to the
// current daemon lifetime by ScanLog.
type LogLine struct {
	Time time.Time
	Text string
	MAC  string // set when the line is a "guest link <MAC> closed" netstack error
}

// CanaryResult is the outcome of the --deep native-binary load canary.
type CanaryResult struct {
	Ran     bool
	Faulted bool
	Detail  string // e.g. "curl exited 132 (SIGILL)"
}

// RunnerEvidence is one systemd actions.runner.* unit inside a guest.
type RunnerEvidence struct {
	Unit   string
	Active bool
}

// GuestEvidence is everything observed about one machine.
type GuestEvidence struct {
	Name  string
	State string // "running" | "stopped" | ... — mirrors vm.State
	IP    string

	// MAC is this guest's link-layer address as recorded in the machine
	// registry, used to CORRELATE netstack log lines with live guests.
	//
	// COLLECTOR CONTRACT — read before leaving this empty. The netstack rung
	// is only allowed to convict when at least two guests are, right now,
	// running AND unreachable AND named by a "cannot receive packets" line in
	// the current daemon lifetime. That message is level=INFO and is emitted
	// on every ORDINARY guest shutdown, so counting MACs in the log without
	// correlating them convicts a perfectly healthy host.
	//
	// If MAC is empty for every guest, the correlation cannot be performed and
	// Classify emits an explicit RungUnknown verdict rather than convicting or
	// staying silent. That verdict is the tripwire: it is how an unpopulated
	// MAC shows up in the output instead of quietly disabling a rung.
	MAC string

	SSHProbed bool
	SSHOK     bool

	// Spare marks a guest kept stopped as a standby. NOTE: --deep does NOT
	// boot it. Booting was in the original design and was deliberately not
	// implemented — --deep stays close to read-only, and the ops watchdog
	// relies on the spare remaining stopped. The field records which machine
	// is the spare; it is not a signal that doctor started anything.
	Spare bool

	Runners    []RunnerEvidence
	LoadCanary CanaryResult
}

// RepoEvidence is the GitHub-side view for one repo. Probed is false when
// gh was unavailable, which forces Unknown rather than Pass.
type RepoEvidence struct {
	Repo           string
	Probed         bool
	RunnerOnline   map[string]bool // runner name -> online
	BillingLockout bool            // recent jobs: ~3s, empty runner_name, zero steps
}

// Evidence is the complete observation set handed to Classify.
type Evidence struct {
	DaemonUp    bool
	DaemonStart time.Time
	GHAvailable bool
	DeepRun     bool
	LogLines    []LogLine
	Guests      []GuestEvidence
	Repos       []RepoEvidence
}

// Verdict is one finding. Subject is empty for host-wide rungs, otherwise
// the guest or repo it applies to.
type Verdict struct {
	Rung       Rung     `json:"rung"`
	Health     Health   `json:"health"`
	Subject    string   `json:"subject,omitempty"`
	Reason     string   `json:"reason"`
	Evidence   []string `json:"evidence,omitempty"`
	NextAction string   `json:"next_action,omitempty"`
}
