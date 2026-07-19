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
	Pass    Health = "pass"
	Fail    Health = "fail"
	Unknown Health = "unknown"
)

// Rung identifies a step on the triage ladder, ordered by blast radius:
// host-wide faults first, then per-guest, then per-repo. RungNone is the
// zero value and means healthy.
type Rung int

const (
	RungNone Rung = iota
	RungDaemonDown
	RungNetstackDead
	RungGuestNoIP
	RungGuestSSHStall
	RungRunnerServiceDown
	RungRunnerOffline
	RungBillingLockout
	RungHostHardware
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
	}
	return "unknown"
}

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
	Name       string
	State      string // "running" | "stopped" | ...
	IP         string
	SSHProbed  bool
	SSHOK      bool
	Spare      bool // booted by --deep as the two-guest discriminator
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
