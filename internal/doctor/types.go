// Package doctor diagnoses umbra host/guest/CI faults by classifying
// collected evidence into one rung of a documented triage ladder.
//
// The package is deliberately split: Collect does all I/O, Classify is a
// pure function. That split is what makes every rung testable without a
// broken host — which matters, because the faults this diagnoses are rare,
// destructive, and impossible to reproduce on demand.
package doctor

import (
	"time"

	"github.com/ForceAI-KW/umbra/internal/vm"
)

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
	// RungMachineCrashed means a machine is in vm.StateCrashed: it is neither
	// up nor cleanly down, so every rung below it is unevaluable FOR THAT
	// MACHINE. It sits here because it is a lifecycle fact established before
	// any network or in-guest probe is even meaningful.
	//
	// It covers the zombie sub-case too (crashed with a live VM handle — the
	// guest may STILL BE RUNNING and holding CPU, memory and its netstack
	// attachment). The verdict distinguishes them by NextAction.
	RungMachineCrashed
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
	case RungMachineCrashed:
		return "machine-crashed"
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
	State vm.State // the upstream enum, not a second copy of it

	// IP is the READINESS-CONFIRMED runtime address, mirroring
	// client.MachineView.IP. READ THE NEXT PARAGRAPH BEFORE TREATING IT AS
	// "has an address".
	//
	// umbrad sets this via mgr.SetIP only AFTER its readiness probe succeeds,
	// which can take up to vm.DefaultReadyTimeout (90s) after the machine
	// enters StateRunning, and it clears it again on stop. lc.Start flips the
	// state to running and returns immediately; readiness blocks afterwards.
	// So EVERY healthy guest is running-with-an-empty-IP for its entire boot
	// window, and permanently if readiness times out.
	//
	// Empty therefore means "not readiness-confirmed", NOT "no address". The
	// question "does this machine even have an address" is answered by
	// ConfiguredIP.
	IP string

	// ConfiguredIP is the STATIC address assigned at create time and recorded
	// in the machine registry (registry.Machine.IP, e.g. "192.168.127.10").
	// It is present from creation until deletion and is untouched by the
	// lifecycle, so it is the only field that answers "is this machine record
	// intact".
	//
	// The pair is what makes a booting guest distinguishable from a broken
	// one, with no daemon change required:
	//
	//	ConfiguredIP == ""              -> broken machine record: convict.
	//	ConfiguredIP != "" && IP == ""  -> booting, or readiness failed:
	//	                                   Unknown, re-checkable.
	//	both set                         -> reachable; carry on down the ladder.
	//
	// Conflating the two convicted a routine boot as a damaged guest image
	// ("umbra rm && umbra create") and, for two guests at once, as failing
	// host HARDWARE ("power-cycle then run Apple Diagnostics"). That verdict
	// was produced against a real host; the actual cause was memory
	// overcommit, and the ops watchdog stands down on host-hardware.
	ConfiguredIP string

	// Zombie mirrors client.MachineView.Zombie: the machine is crashed AND
	// umbrad still holds a live VM handle for it, because a stop was requested
	// and never confirmed. The guest may STILL BE RUNNING. `umbra list` already
	// surfaces this as `crashed*`; doctor must not be less informative than
	// list about a fault class list already names.
	Zombie bool

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

	// NOTE: there is deliberately no Spare field. The original design had
	// --deep boot the stopped standby guest as a discriminator; that was never
	// implemented, because it would make --deep mutate machine state and the
	// ops watchdog treats the spare being stopped as a steady-state invariant.
	// A Spare bool survived the cut but was never populated by any collector,
	// so it read as "this guest is not the spare" for every guest on the
	// fleet — a field whose zero value is a false claim. Removed in wave 5.

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
	// BillingLabels are the runner labels the blocked jobs requested. The
	// lockout signature cannot separate an org billing block from exhausted
	// minutes or from no runner matching the labels — these make the cause
	// legible to a human without another API call.
	BillingLabels []string
}

// Unprobed records a probe the collector could not RUN AT ALL — ssh missing
// from PATH, no forwarded ssh port, an unreadable umbrad.err.log, an
// unavailable machine list.
//
// It exists because the collector's silence was previously indistinguishable
// from health: with no ssh binary and no readable log, doctor printed
// "healthy: no faults detected" and exited 0. Classify honours "unprobed is
// never pass", but only for facts it is actually told about — this is how the
// collector tells it. Every entry becomes an explicit Unknown verdict.
//
// Subject is empty for host-wide probes, otherwise the guest or repo name.
type Unprobed struct {
	Subject    string
	What       string // the probe, e.g. "ssh", "umbrad.err.log", "machine list"
	Detail     string // why it could not run
	NextAction string // optional; how the operator makes the probe possible
}

// Evidence is the complete observation set handed to Classify.
type Evidence struct {
	DaemonUp bool
	// DaemonStart is when the current daemon lifetime began, per the log's
	// start marker. Classify reports it in the netstack verdict's evidence so
	// the operator can see WHICH window the log correlation covered — a
	// verdict scoped to an invisible window is not auditable.
	DaemonStart time.Time
	// GHAvailable reports whether the gh binary was on PATH. Classify uses it
	// to tell "gh is missing" apart from "gh is installed but the call failed
	// (unauthenticated, rate-limited, repo not resolvable)": those need
	// different remedies, and advising `brew install gh` to someone who
	// already has it authenticated sends them nowhere.
	GHAvailable bool
	// DeepRun records whether --deep was requested. It is what the --json
	// `deep` field is derived from, so the report describes the run that
	// actually happened rather than a separately-read flag.
	DeepRun  bool
	LogLines []LogLine
	Guests   []GuestEvidence
	Repos    []RepoEvidence

	// Unprobed carries the probes that could not run. See Unprobed's doc.
	Unprobed []Unprobed
}

// Verdict is one finding. Subject is empty for host-wide rungs, otherwise
// the guest or repo it applies to.
type Verdict struct {
	Rung    Rung   `json:"rung"`
	Health  Health `json:"health"`
	Subject string `json:"subject,omitempty"`
	Reason  string `json:"reason"`
	// Supporting is the human-readable evidence behind this finding. Named
	// Supporting rather than Evidence so it does not read as the package-level
	// Evidence struct sitting a few lines above it; the json tag stays
	// "evidence" because --json is a watchdog contract.
	Supporting []string `json:"evidence,omitempty"`
	NextAction string   `json:"next_action,omitempty"`
}
