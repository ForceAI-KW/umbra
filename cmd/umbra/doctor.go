package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/doctor"
	"github.com/ForceAI-KW/umbra/internal/paths"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

var (
	doctorJSON bool
	doctorDeep bool
)

// canaryScript is the bounded native-binary load canary. curl and openssl are
// correct-arch system binaries with zero Rosetta ambiguity, so a CPU-level
// signal from either means the guest is miscomputing — a host fault, not a
// config problem. Bounded on purpose: never leave stress running on a suspect host.
//
// It ends by echoing canaryDoneSentinel. That sentinel is LOAD-BEARING, not
// decoration: without proof the script reached its last line, "no FAULT in the
// output" is indistinguishable from "the output stops early because ssh died,
// the timeout tripped, or the guest wedged under the stress" — and a host sick
// enough to drop ssh mid-stress is precisely the host this canary exists to
// catch. canaryOutcome is the only correct reader of this output; do not add
// another that checks for FAULT alone.
var canaryScript = `set +e
for i in $(seq 1 150); do
  curl --version >/dev/null 2>&1; RC=$?
  [ $RC -ne 0 ] && echo "FAULT rc=$RC"
done
for j in 1 2 3 4; do
  ( for i in $(seq 1 800); do openssl sha256 /usr/bin/curl >/dev/null 2>&1; RC=$?
      [ $RC -ne 0 ] && echo "FAULT rc=$RC"
    done ) &
done
wait
echo ` + canaryDoneSentinel + `
`

// canaryDoneSentinel is printed by canaryScript's last line and checked by
// canaryOutcome. Defined once and concatenated into the script so the emitter
// and the reader cannot drift apart.
const canaryDoneSentinel = "CANARY_DONE"

// Timeouts. Every probe is bounded so that the fault doctor diagnoses cannot
// also hang doctor — see sshProbeArgs for the same argument at the ssh layer.
const (
	sshProbeTimeout = 20 * time.Second
	// canaryTimeout is generous: the canary is a ~60s stress by design, and a
	// slow-but-working guest must not be cut off and misreported.
	canaryTimeout = 3 * time.Minute
	ghCallTimeout = 20 * time.Second
)

// canaryFaulted reports whether the canary saw a CPU-level signal. Exit codes
// 132 (SIGILL) and 139 (SIGSEGV) are the decisive host-hardware signature.
func canaryFaulted(out string) bool {
	return strings.Contains(out, "FAULT rc=132") || strings.Contains(out, "FAULT rc=139")
}

// canaryOutcome decides what a canary run actually PROVED, from its output and
// the error the command exited with. It returns the result plus, when nothing
// was proved, a non-empty detail for an Unprobed record.
//
// This function is the whole point of the C1 fix. The previous code discarded
// the error and recorded CanaryResult{Ran: true, Faulted: canaryFaulted(out)}
// unconditionally, so a canary that never completed — dead ssh, tripped
// timeout, guest wedged under the stress — recorded a CLEAN reading for the
// single most decisive rung in the system, and doctor answered "no fault"
// about a probe that did not run.
//
// The asymmetry between the two branches is deliberate. A fault that WAS
// OBSERVED is decisive on its own: a native binary took SIGILL, and that
// observation does not become less true because the session died a moment
// later, so it is honoured even alongside an error. Only the ABSENCE of a
// fault needs proof of completion, because absence is exactly what a truncated
// run counterfeits.
func canaryOutcome(out string, err error) (doctor.CanaryResult, string) {
	if canaryFaulted(out) {
		return doctor.CanaryResult{
			Ran: true, Faulted: true,
			Detail: "native binary exited with SIGILL/SIGSEGV under load",
		}, ""
	}
	if err != nil {
		return doctor.CanaryResult{}, fmt.Sprintf(
			"the load canary did not complete: %v (ssh dropped, the %s timeout tripped, or the guest wedged under the stress) — no CPU-level signal was seen, but the run proves nothing either way",
			err, canaryTimeout)
	}
	if !strings.Contains(out, canaryDoneSentinel) {
		return doctor.CanaryResult{}, fmt.Sprintf(
			"the load canary exited without error but never printed its %s completion sentinel, so its output is truncated and a clean reading cannot be trusted",
			canaryDoneSentinel)
	}
	return doctor.CanaryResult{Ran: true, Faulted: false}, ""
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose umbra/CI faults and print the next action",
	Long: "Classifies host, guest and CI faults into one rung of the umbra triage ladder.\n" +
		"Read-only by default. --deep additionally runs a bounded native-binary load\n" +
		"canary, which is the only way to detect a host-hardware fault.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ev := collectEvidence(cmd.Context())
		verdicts := doctor.Classify(ev)

		if doctorJSON {
			enc := json.NewEncoder(os.Stdout)
			// ev.DeepRun, not the flag: the report describes the run that actually
			// happened, from the same evidence the classifier saw.
			if err := enc.Encode(newDoctorReport(ev.DeepRun, verdicts)); err != nil {
				return err
			}
		} else {
			printVerdicts(verdicts)
		}

		if faultsFound(verdicts) {
			return errFaultsFound
		}
		return nil
	},
}

// collectEvidence runs every probe. It NEVER returns an error: a probe that
// cannot run degrades that one field and is recorded in Evidence.Unprobed, so
// the classifier can render it as Unknown. Aborting instead would report a
// half-collected host as healthy, which is the whole C3 defect.
func collectEvidence(ctx context.Context) doctor.Evidence {
	ev := doctor.Evidence{DeepRun: doctorDeep}

	if err := apiClient.Ping(ctx); err == nil {
		ev.DaemonUp = true
	}

	collectLog(&ev)

	if ev.DaemonUp {
		machines, err := apiClient.ListMachines(ctx)
		if err != nil {
			// Degrade, do not abort. Without the machine list every guest and
			// repo rung is unevaluable, so say so rather than printing a
			// verdict-free "healthy".
			ev.Unprobed = append(ev.Unprobed, doctor.Unprobed{
				What:       "machine list",
				Detail:     fmt.Sprintf("umbrad answered its ping but ListMachines failed: %v", err),
				NextAction: "umbra list (if that also fails: umbra daemon status)",
			})
		}
		for i := range machines {
			g, unprobed := probeGuest(ctx, &machines[i])
			ev.Guests = append(ev.Guests, g)
			ev.Unprobed = append(ev.Unprobed, unprobed...)
		}
	}

	repos, ghOK, ghUnprobed := collectGitHub(ctx, ev.Guests)
	ev.Repos, ev.GHAvailable = repos, ghOK
	ev.Unprobed = append(ev.Unprobed, ghUnprobed...)
	return ev
}

// collectLog reads the current daemon lifetime out of umbrad.err.log. Both
// failure modes — unreadable file, and ScanLog rejecting a corrupt daemon-start
// marker — become Unprobed records. ScanLog failing closed is a real anomaly
// worth surfacing; swallowing it would silently restore the stale-log trap it
// exists to prevent.
func collectLog(ev *doctor.Evidence) {
	path := paths.Logs() + "/umbrad.err.log"
	f, err := os.Open(path)
	if err != nil {
		ev.Unprobed = append(ev.Unprobed, doctor.Unprobed{
			What:       "umbrad.err.log",
			Detail:     fmt.Sprintf("cannot open %s: %v", path, err),
			NextAction: "umbra daemon status — the netstack rung cannot be evaluated without this log",
		})
		return
	}
	defer f.Close()

	lines, start, err := doctor.ScanLog(f)
	if err != nil {
		ev.Unprobed = append(ev.Unprobed, doctor.Unprobed{
			What:       "umbrad.err.log",
			Detail:     fmt.Sprintf("scanning %s: %v", path, err),
			NextAction: "inspect the log by hand — the netstack rung is not evaluable until it parses",
		})
		return
	}
	if start.IsZero() {
		// No start marker means we cannot tell current-lifetime lines from
		// lines left over from a fault that was fixed weeks ago.
		ev.Unprobed = append(ev.Unprobed, doctor.Unprobed{
			What:       "umbrad.err.log",
			Detail:     "no daemon-start marker in the log, so current-lifetime lines cannot be separated from stale ones",
			NextAction: "umbra daemon restart, then re-run doctor",
		})
		return
	}
	ev.LogLines, ev.DaemonStart = lines, start
}

// errFaultsFound signals "diagnosis succeeded and found faults" — distinct
// from "the command itself failed". main maps it to exit 1 without printing
// a spurious error, so deferred cleanup still runs and cobra stays in charge
// of the error path.
var errFaultsFound = errors.New("faults found")

// faultsFound decides the exit code. ONLY Fail counts. Unknown means a probe
// could not run, which is not evidence of anything — exiting non-zero on it
// would make every gh-less host look broken to the watchdog.
func faultsFound(vs []doctor.Verdict) bool {
	for _, v := range vs {
		if v.Health == doctor.Fail {
			return true
		}
	}
	return false
}

// doctorReport is the --json document. THIS IS A WATCHDOG CONTRACT:
// ~/.claude/scripts/ci-runner-guard.sh reads verdicts[].rung, .health and
// .next_action, and the rung slug strings. Fields may only be ADDED.
type doctorReport struct {
	Deep bool `json:"deep"`
	// Healthy is true only when nothing at all was found — no fault AND no
	// unprobed probe. It exists so consumers need not reimplement the rule.
	Healthy bool `json:"healthy"`
	// UnknownOnly is true when findings exist but none of them is a fault:
	// the run is not a clean bill of health, but nothing was diagnosed.
	UnknownOnly bool             `json:"unknown_only"`
	Verdicts    []doctor.Verdict `json:"verdicts"`
}

func newDoctorReport(deep bool, vs []doctor.Verdict) doctorReport {
	if vs == nil {
		// Never emit "verdicts":null — a consumer iterating the array should
		// not have to special-case the healthy host.
		vs = []doctor.Verdict{}
	}
	fail := faultsFound(vs)
	return doctorReport{
		Deep:        deep,
		Healthy:     len(vs) == 0,
		UnknownOnly: len(vs) > 0 && !fail,
		Verdicts:    vs,
	}
}

func printVerdicts(vs []doctor.Verdict) {
	if len(vs) == 0 {
		fmt.Println("healthy: no faults detected")
		return
	}
	for _, v := range vs {
		subject := v.Subject
		if subject == "" {
			subject = "host"
		}
		fmt.Printf("[%s] %s (%s)\n  %s\n", v.Health, v.Rung, subject, v.Reason)
		for _, e := range v.Supporting {
			fmt.Printf("  evidence: %s\n", e)
		}
		if v.NextAction != "" {
			fmt.Printf("  next: %s\n", v.NextAction)
		}
	}
}

// guestEvidenceFor builds the non-ssh half of a guest's evidence and decides
// whether an ssh probe is even possible. Split out from probeGuest so the
// "cannot probe" decision is testable without a live host — it is the exact
// place C3's silence lived.
//
// A stopped machine is NOT unprobed: the ladder skips it by design, so
// reporting it would be noise, not honesty.
func guestEvidenceFor(mv *client.MachineView) (doctor.GuestEvidence, []doctor.Unprobed) {
	// BOTH addresses, deliberately. mv.IP is the readiness-confirmed runtime
	// address, published only after umbrad's readiness probe succeeds (up to
	// 90s after the state flips to running) and cleared on stop. mv.Machine.IP
	// is the static address written into the registry at create time — the
	// embedded registry.Machine is already on MachineView, so no daemon change
	// is needed to carry it. Only the PAIR distinguishes "still booting" from
	// "broken machine record"; see doctor.GuestEvidence for what conflating
	// them cost.
	g := doctor.GuestEvidence{
		Name: mv.Name, State: mv.State,
		IP: mv.IP, ConfiguredIP: mv.Machine.IP,
		MAC: mv.MAC, Zombie: mv.Zombie,
	}

	if mv.State != vm.StateRunning {
		// Not probed further, and correctly so — but NOT dropped either: the
		// classifier renders every non-running state (crashed, zombie,
		// starting, stopping) from the State and Zombie fields above.
		return g, nil
	}
	if mv.SSHPort == 0 {
		return g, []doctor.Unprobed{{
			Subject:    mv.Name,
			What:       "ssh",
			Detail:     "machine is running but umbrad has no forwarded ssh port for it",
			NextAction: fmt.Sprintf("umbra stop %s && umbra start %s to re-establish the forward", mv.Name, mv.Name),
		}}
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		return g, []doctor.Unprobed{{
			Subject:    mv.Name,
			What:       "ssh",
			Detail:     "ssh is not on PATH, so no in-guest rung can be evaluated",
			NextAction: "install an ssh client (xcode-select --install)",
		}}
	}
	// THE LOCAL KEY. doctor probes with BatchMode=yes, which never prompts —
	// so a missing or unreadable ~/.umbra/ssh/id_ed25519 makes ssh fail
	// SILENTLY, and every running guest on the fleet then looked like it had
	// wedged: guest-ssh-stall FAIL, with "stop and start the guest" as the
	// remedy for a problem entirely on the operator's own machine. Check the
	// key before probing so the report names the real subject.
	if _, err := os.Stat(sshKeyPath()); err != nil {
		return g, []doctor.Unprobed{{
			Subject:    mv.Name,
			What:       "ssh",
			Detail:     fmt.Sprintf("the local ssh key %s cannot be read: %v — this is a fault on THIS machine, not in the guest", sshKeyPath(), err),
			NextAction: fmt.Sprintf("restore the key (it is created with the machine; check %s), then re-run doctor — do not restart the guest for this", paths.SSH()),
		}}
	}
	return g, nil
}

// sshKeyPath is the identity doctor probes with. Kept in one place so the
// existence check here and the -i flag in sshArgs cannot drift apart.
func sshKeyPath() string { return filepath.Join(paths.SSH(), "id_ed25519") }

// sshAuthFailure reports whether ssh's output shows the connection was
// REFUSED CREDENTIALS rather than the guest being unreachable.
//
// The distinction decides who owns the fault. "Permission denied (publickey)"
// or an unreadable identity file is a local credential problem — the guest may
// be perfectly healthy, and restarting it fixes nothing while costing a CI
// job. A timeout or a refused connection, by contrast, really is the guest or
// the netstack, which is what guest-ssh-stall is for.
func sshAuthFailure(out string) bool {
	for _, sig := range []string{
		"Permission denied",
		"not accessible",
		"Host key verification failed",
		"Too many authentication failures",
		"no such identity",
		"Load key",
		"UNPROTECTED PRIVATE KEY FILE",
	} {
		if strings.Contains(out, sig) {
			return true
		}
	}
	return false
}

// probeGuest gathers per-guest evidence over ssh. Every probe failure degrades
// that field rather than aborting the diagnosis — one unreachable guest must
// not blind us to the rest of the host.
func probeGuest(ctx context.Context, mv *client.MachineView) (doctor.GuestEvidence, []doctor.Unprobed) {
	g, unprobed := guestEvidenceFor(mv)
	if len(unprobed) > 0 || mv.State != vm.StateRunning {
		return g, unprobed
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return g, unprobed
	}

	g.SSHProbed = true
	out, err := runBoundedOutput(ctx, sshProbeTimeout, sshPath, sshProbeArgs(mv, []string{"true"}))
	switch {
	case err == nil:
		g.SSHOK = true
	case sshAuthFailure(out):
		// ssh reached a decision and REFUSED OUR CREDENTIALS. That says
		// nothing about the guest's health, so it must not be reported as
		// one — leaving SSHProbed set here would convict the guest of
		// ssh-stall and advise a restart for a local key problem.
		g.SSHProbed = false
		return g, append(unprobed, doctor.Unprobed{
			Subject:    mv.Name,
			What:       "ssh",
			Detail:     fmt.Sprintf("ssh could not authenticate to the guest (local credential problem, not a guest fault): %s", strings.TrimSpace(firstLine(out))),
			NextAction: fmt.Sprintf("check the local key: ls -l %s, and that its public half is in the guest's authorized_keys — do NOT restart %s for this", sshKeyPath(), mv.Name),
		})
	}
	if !g.SSHOK {
		return g, unprobed
	}

	uCtx, cancel := context.WithTimeout(ctx, sshProbeTimeout)
	defer cancel()
	uArgs := sshProbeArgs(mv, runnerUnitsCommand())
	if out, err := exec.CommandContext(uCtx, sshPath, uArgs[1:]...).CombinedOutput(); err == nil {
		g.Runners = parseRunnerUnits(string(out))
	} else {
		unprobed = append(unprobed, doctor.Unprobed{
			Subject: mv.Name,
			What:    "systemd runner units",
			Detail:  fmt.Sprintf("ssh succeeded but listing units failed: %v", err),
			// Every other Unprobed record carries a next action; this one did
			// not, so the operator was told what failed and nothing about what
			// to do. It also gates the whole GitHub half of the ladder, since
			// the repos to probe are derived from these unit names.
			NextAction: fmt.Sprintf("run it by hand to see the error: umbra exec %s systemctl list-units 'actions.runner.*' --all --no-legend --plain — the GitHub-side rungs stay unevaluated until this works", mv.Name),
		})
	}

	if doctorDeep {
		cCtx, cCancel := context.WithTimeout(ctx, canaryTimeout)
		defer cCancel()
		cArgs := sshProbeArgs(mv, []string{"bash", "-s"})
		c := exec.CommandContext(cCtx, sshPath, cArgs[1:]...)
		c.Stdin = strings.NewReader(canaryScript)
		out, err := c.CombinedOutput()
		res, cannotConclude := canaryOutcome(string(out), err)
		g.LoadCanary = res
		if cannotConclude != "" {
			unprobed = append(unprobed, doctor.Unprobed{
				Subject:    mv.Name,
				What:       "load canary",
				Detail:     cannotConclude,
				NextAction: fmt.Sprintf("re-run: umbra doctor --deep. If it keeps failing to complete, the guest is the suspect — umbra stop %s && umbra start %s, then retry", mv.Name, mv.Name),
			})
		}
	}
	return g, unprobed
}

// runBoundedOutput runs a bounded command and returns its combined output
// alongside the error. The OUTPUT is what makes an ssh failure classifiable:
// the exit status alone is 255 for every failure mode ssh has, so discarding
// the message (which is what this replaced) made "wrong key" and "guest
// wedged" indistinguishable.
func runBoundedOutput(ctx context.Context, d time.Duration, path string, argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, argv[1:]...).CombinedOutput()
	return string(out), err
}

// firstLine keeps a multi-line ssh diagnostic to one readable line in a
// verdict; ssh emits banners and warnings around the message that matters.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

// runnerUnitsCommand is the remote systemctl invocation.
//
// --all is load-bearing: without it systemd omits inactive units entirely, so
// a stopped actions.runner unit is simply ABSENT from the output and the
// runner-service-down rung can never fire for the exact case it targets.
//
// 'actions.runner.*' stays single-quoted: sshArgs joins this into one remote
// command string, so the quotes travel over the wire and are reparsed by the
// REMOTE shell — that is what stops the glob expanding against local files
// before it ever reaches systemctl. Don't "simplify" it.
func runnerUnitsCommand() []string {
	return []string{"systemctl", "list-units", `'actions.runner.*'`, "--all", "--no-legend", "--plain"}
}

// parseRunnerUnits reads `systemctl list-units --all --no-legend --plain`
// output. Columns are UNIT LOAD ACTIVE SUB DESCRIPTION; --plain suppresses the
// leading status bullet that would otherwise shift every column by one for a
// failed unit.
func parseRunnerUnits(out string) []doctor.RunnerEvidence {
	var units []doctor.RunnerEvidence
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !strings.HasPrefix(f[0], "actions.runner.") {
			continue
		}
		units = append(units, doctor.RunnerEvidence{Unit: f[0], Active: f[2] == "active"})
	}
	return units
}

// ---------------------------------------------------------------------------
// GitHub-side collection (rungs 5 and 6)
// ---------------------------------------------------------------------------

// ghExec runs the gh CLI and returns its stdout. It is a function value purely
// so the collection logic is testable without a network or a GitHub token.
type ghExec func(ctx context.Context, args ...string) ([]byte, error)

func realGH(ctx context.Context, args ...string) ([]byte, error) {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, ghCallTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, ghPath, args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// collectGitHub derives the repos to probe from the runner unit names already
// read out of the guests, then probes each one. Deriving beats hardcoding: the
// set of repos a host serves changes whenever a runner is added, and a
// hardcoded list would quietly stop matching.
// It returns an Unprobed record when the repo set could not be derived at all.
// That case is not hypothetical and not harmless: runner units are read over
// ssh, so a wedged guest yields zero units, and the ENTIRE GitHub half of the
// ladder would then report nothing — silently, at exactly the moment CI is
// broken and the operator most needs to know whether the cause is GitHub-side.
// "I could not look" and "I looked and found nothing" must not be the same
// output; that is the same defect the Unprobed machinery exists to prevent.
func collectGitHub(ctx context.Context, guests []doctor.GuestEvidence) ([]doctor.RepoEvidence, bool, []doctor.Unprobed) {
	return collectGitHubWith(ctx, realGH, ghAvailable(), guests)
}

// collectGitHubWith is collectGitHub with the gh runner injected, so the
// derivation logic is testable without a network or a GitHub token.
func collectGitHubWith(ctx context.Context, gh ghExec, ghOK bool, guests []doctor.GuestEvidence) ([]doctor.RepoEvidence, bool, []doctor.Unprobed) {
	var units []string
	var unprobed []doctor.Unprobed
	reachableWithNoUnits := 0

	for _, g := range guests {
		if g.State != vm.StateRunning {
			continue
		}
		// PER-GUEST, not fleet-wide. The fleet-wide check below only fired
		// when NO guest yielded any unit, so a fleet with one unreachable and
		// one healthy guest silently dropped the unreachable guest's repos:
		// the GitHub half of the ladder reported on half the fleet and said
		// nothing about the other half. That is the same "I could not look"
		// rendered as "I looked and found nothing" defect the Unprobed
		// machinery exists to prevent — fixed for the total case in an earlier
		// wave, but not for the partial one.
		if g.SSHProbed && !g.SSHOK {
			unprobed = append(unprobed, doctor.Unprobed{
				Subject:    g.Name,
				What:       "GitHub repos",
				Detail:     "this guest is unreachable over ssh, so its runner units could not be read and the repos it serves were not probed on the GitHub side",
				NextAction: fmt.Sprintf("fix this guest first (see its rung above), then re-run doctor — repos served only by %s are undiagnosed until then", g.Name),
			})
			continue
		}
		if len(g.Runners) == 0 {
			reachableWithNoUnits++
		}
		for _, r := range g.Runners {
			units = append(units, r.Unit)
		}
	}

	if len(units) == 0 {
		// Only describe the case that actually happened. Telling an operator
		// to run systemctl is wrong when the unit listing failed on a guest
		// that was perfectly REACHABLE — the fault is in the listing, not in
		// whether units exist.
		if reachableWithNoUnits == 0 && len(unprobed) > 0 {
			// Every running guest was unreachable, and each already has its
			// own record naming it. A fleet-wide record here would repeat
			// them without adding a fact.
			return nil, ghOK, unprobed
		}
		detail := "no actions.runner units were discovered on any guest, so no repo could be derived"
		next := "if this host serves CI, check: umbra exec <machine> systemctl list-units 'actions.runner.*' --all"
		if reachableWithNoUnits > 0 {
			detail = fmt.Sprintf("%d reachable guest(s) returned no actions.runner units, so no repo could be derived — either none is registered, or the unit listing itself failed", reachableWithNoUnits)
			next = "check whether the listing works at all: umbra exec <machine> systemctl list-units 'actions.runner.*' --all --no-legend --plain (an empty result means no runner is registered; an error means the listing failed)"
		}
		return nil, ghOK, append(unprobed, doctor.Unprobed{
			What:       "GitHub repos",
			Detail:     detail,
			NextAction: next,
		})
	}
	repos, repoUnprobed := collectRepos(ctx, gh, units)
	return repos, ghOK, append(unprobed, repoUnprobed...)
}

func ghAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

// collectRepos probes every repo named by a runner unit. A repo whose probe
// could not complete is returned with Probed:false — never with a fabricated
// healthy reading, because the classifier renders unprobed as Unknown and a
// false Pass here would hide a real billing lockout.
func collectRepos(ctx context.Context, gh ghExec, units []string) ([]doctor.RepoEvidence, []doctor.Unprobed) {
	seen := map[string]bool{}
	var scopes []string
	for _, u := range units {
		s := repoScopeFromUnit(u)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		scopes = append(scopes, s)
	}
	sort.Strings(scopes) // ordered findings; unit order is not stable

	out := make([]doctor.RepoEvidence, 0, len(scopes))
	var unprobed []doctor.Unprobed
	for _, s := range scopes {
		t, err := resolveScope(ctx, gh, s)
		if err != nil {
			out = append(out, doctor.RepoEvidence{Repo: s, Probed: false})
			continue
		}
		ev, err := probeScope(ctx, gh, t)
		if err != nil {
			out = append(out, doctor.RepoEvidence{Repo: t.name, Probed: false})
			continue
		}
		if t.isOrg {
			// The runner half was genuinely probed, but the billing-lockout
			// rung is derived from a REPO's workflow runs and there is no
			// org-wide equivalent. Recording the org as fully probed would
			// assert "no billing lockout" from a probe that never ran.
			unprobed = append(unprobed, doctor.Unprobed{
				Subject:    t.name,
				What:       "billing lockout",
				Detail:     fmt.Sprintf("%q is an org-level runner registration; the lockout signature is read from a repo's workflow runs, and there is no org-wide equivalent, so that rung was not evaluated", t.name),
				NextAction: fmt.Sprintf("check billing directly if jobs are failing in ~3s: gh api orgs/%s/settings/billing/actions", t.name),
			})
		}
		out = append(out, ev)
	}
	return out, unprobed
}

// scopeTarget is a resolved runner scope: either a repo ("owner/repo") or an
// org login. The two need different API endpoints, which is why a scope that
// is really an org used to be permanently unprobeable — repoCandidates
// requires a '-' split and every lookup went to repos/<owner>/<repo>.
type scopeTarget struct {
	name  string
	isOrg bool
}

// resolveScope asks GitHub what a runner scope actually IS. A repo-scoped
// runner unit carries an escaped "<owner>-<repo>"; an org-scoped one carries a
// bare org login. Neither is decidable by string surgery — '-' is legal inside
// both owner and repo names, and an org login may contain one too
// ("ForceAI-KW" splits into a perfectly plausible "ForceAI/KW") — so GitHub is
// the oracle for both questions.
//
// Repo candidates are tried first: a scope that resolves as a repo IS a repo,
// and the repo path additionally supports the billing rung.
func resolveScope(ctx context.Context, gh ghExec, scope string) (scopeTarget, error) {
	if repo, err := resolveRepo(ctx, gh, scope); err == nil {
		return scopeTarget{name: repo}, nil
	}
	out, err := gh(ctx, "api", "orgs/"+scope, "--jq", ".login")
	if err != nil {
		return scopeTarget{}, fmt.Errorf("scope %q resolved as neither a repo nor an org: %w", scope, err)
	}
	if login := strings.TrimSpace(string(out)); login != "" {
		return scopeTarget{name: login, isOrg: true}, nil
	}
	return scopeTarget{}, fmt.Errorf("scope %q resolved as neither a repo nor an org", scope)
}

// probeScope gathers the GitHub-side evidence for a resolved scope. The runner
// listing has the same payload shape at both endpoints; only billing differs.
func probeScope(ctx context.Context, gh ghExec, t scopeTarget) (doctor.RepoEvidence, error) {
	if t.isOrg {
		ev := doctor.RepoEvidence{Repo: t.name}
		out, err := gh(ctx, "api", "orgs/"+t.name+"/actions/runners")
		if err != nil {
			return ev, err
		}
		online, err := parseRunnerStatus(out)
		if err != nil {
			return ev, err
		}
		ev.RunnerOnline = online
		ev.Probed = true
		return ev, nil
	}
	return probeRepo(ctx, gh, t.name)
}

// repoScopeFromUnit extracts the scope from
// actions.runner.<scope>.<instance>.service, where <scope> is the escaped
// "<owner>-<repo>" (or a bare org for an org-level runner — see resolveScope,
// which resolves both shapes).
//
// The parse itself lives in internal/doctor because the CLASSIFIER needs it
// too, to group runner units by repo when deciding whether an inactive unit is
// a stale registration or a real outage. Two copies would drift.
//
// A repo name containing a literal '.' is escaped by systemd and is not
// handled here; such a unit yields an unresolvable scope, which surfaces as
// Probed:false rather than as a wrong repo.
func repoScopeFromUnit(unit string) string { return doctor.RunnerUnitScope(unit) }

// repoCandidates lists every way "<owner>-<repo>" could split. The separator
// is '-', which is also legal INSIDE both an owner and a repo name
// ("ForceAI-KW/umbra" and "Force/my-repo" produce indistinguishable scopes),
// so this cannot be decided by string surgery. Every candidate is offered and
// GitHub itself is the oracle — see resolveRepo.
func repoCandidates(scope string) []string {
	var out []string
	for i, c := range scope {
		if c == '-' {
			out = append(out, scope[:i]+"/"+scope[i+1:])
		}
	}
	return out
}

// resolveRepo asks GitHub which candidate split is real, returning the
// canonical full_name. If none resolves — including because gh is missing,
// unauthenticated, or rate-limited — it errors, and the caller records the
// repo as unprobed.
func resolveRepo(ctx context.Context, gh ghExec, scope string) (string, error) {
	cands := repoCandidates(scope)
	if len(cands) == 0 {
		return "", fmt.Errorf("scope %q has no owner/repo separator", scope)
	}
	for _, c := range cands {
		out, err := gh(ctx, "api", "repos/"+c, "--jq", ".full_name")
		if err != nil {
			continue
		}
		if name := strings.TrimSpace(string(out)); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("no candidate split of %q resolved to a repo (candidates: %v)", scope, cands)
}

func probeRepo(ctx context.Context, gh ghExec, repo string) (doctor.RepoEvidence, error) {
	ev := doctor.RepoEvidence{Repo: repo}

	out, err := gh(ctx, "api", "repos/"+repo+"/actions/runners")
	if err != nil {
		return ev, err
	}
	online, err := parseRunnerStatus(out)
	if err != nil {
		return ev, err
	}
	ev.RunnerOnline = online

	lockout, lockoutLabels, err := probeBillingLockout(ctx, gh, repo)
	if err != nil {
		return ev, err
	}
	ev.BillingLockout = lockout
	ev.BillingLabels = lockoutLabels

	ev.Probed = true
	return ev, nil
}

// parseRunnerStatus reads the same gh api .../actions/runners payload that
// `umbra runner list --repo` already prints, mapping runner name -> online.
func parseRunnerStatus(body []byte) (map[string]bool, error) {
	var resp struct {
		Runners []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"runners"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing actions/runners: %w", err)
	}
	out := make(map[string]bool, len(resp.Runners))
	for _, r := range resp.Runners {
		out[r.Name] = r.Status == "online"
	}
	return out, nil
}

type ghJob struct {
	Conclusion  string            `json:"conclusion"`
	RunnerName  string            `json:"runner_name"`
	StartedAt   time.Time         `json:"started_at"`
	CompletedAt time.Time         `json:"completed_at"`
	Steps       []json.RawMessage `json:"steps"`
	Labels      []string          `json:"labels"`
}

const (
	// billingLockoutMaxDuration bounds the "~3s" in the lockout signature.
	// Widened to 10s so a slow API timestamp does not hide a real lockout,
	// still far below any job that actually reached a runner.
	billingLockoutMaxDuration = 10 * time.Second
	// billingLockoutLookback keeps a long-fixed lockout from being rediagnosed
	// forever — the same stale-evidence trap the log scanner guards against.
	billingLockoutLookback = 7 * 24 * time.Hour
	// billingLockoutRunsScanned bounds the API cost of finding a recent failure.
	billingLockoutRunsScanned = 20
)

// billingLockoutSignature decides whether a run's jobs carry the billing
// fingerprint: they failed, no runner was ever assigned, and no step ran, all
// within a couple of seconds. Every one of those must hold for EVERY failed
// job — a run where one job reached a runner is an ordinary CI failure, and
// sending Ahmad to the org billing page for a broken test is precisely the
// misdiagnosis this tool exists to prevent.
// It also returns the distinct runner labels the blocked jobs asked for. The
// signature CANNOT by itself separate three different causes that look
// identical in the jobs API: an org billing block, exhausted cloud minutes, or
// no runner matching the requested labels. The labels disambiguate at a
// glance — `ubuntu-latest` points at GitHub-hosted billing, whereas a
// self-hosted label set points at a runner that never registered.
func billingLockoutSignature(jobs []ghJob) (bool, []string) {
	failed := 0
	seen := map[string]bool{}
	var labels []string
	for _, j := range jobs {
		if j.Conclusion != "failure" {
			continue
		}
		failed++
		if j.RunnerName != "" || len(j.Steps) != 0 {
			return false, nil
		}
		d := j.CompletedAt.Sub(j.StartedAt)
		if d < 0 || d > billingLockoutMaxDuration {
			return false, nil
		}
		for _, l := range j.Labels {
			if !seen[l] {
				seen[l] = true
				labels = append(labels, l)
			}
		}
	}
	sort.Strings(labels) // deterministic output; map iteration is randomised
	return failed > 0, labels
}

// probeBillingLockout inspects the most recent failed workflow run. Only the
// newest one: a lockout blocks every run, so if the newest failure does not
// carry the signature the org is not locked out right now.
func probeBillingLockout(ctx context.Context, gh ghExec, repo string) (bool, []string, error) {
	out, err := gh(ctx, "api", fmt.Sprintf("repos/%s/actions/runs?per_page=%d", repo, billingLockoutRunsScanned))
	if err != nil {
		return false, nil, err
	}
	var runs struct {
		WorkflowRuns []struct {
			ID         int64     `json:"id"`
			Conclusion string    `json:"conclusion"`
			UpdatedAt  time.Time `json:"updated_at"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(out, &runs); err != nil {
		return false, nil, fmt.Errorf("parsing actions/runs: %w", err)
	}

	for _, r := range runs.WorkflowRuns {
		if r.Conclusion != "failure" {
			continue
		}
		if !r.UpdatedAt.IsZero() && time.Since(r.UpdatedAt) > billingLockoutLookback {
			return false, nil, nil // too old to be describing the present
		}
		jobsOut, err := gh(ctx, "api", fmt.Sprintf("repos/%s/actions/runs/%d/jobs", repo, r.ID))
		if err != nil {
			return false, nil, err
		}
		var jr struct {
			Jobs []ghJob `json:"jobs"`
		}
		if err := json.Unmarshal(jobsOut, &jr); err != nil {
			return false, nil, fmt.Errorf("parsing run jobs: %w", err)
		}
		locked, labels := billingLockoutSignature(jr.Jobs)
		return locked, labels, nil
	}
	return false, nil, nil
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "JSON output (watchdog probe)")
	doctorCmd.Flags().BoolVar(&doctorDeep, "deep", false, "also run the bounded native-binary load canary (~60s per running guest)")
}
