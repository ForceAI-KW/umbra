package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/doctor"
	"github.com/ForceAI-KW/umbra/internal/paths"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

// withUmbraRoot points paths.Root() at a temp dir for the duration of a test,
// so the collector's filesystem probes can be exercised without touching the
// operator's real ~/.umbra.
func withUmbraRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("UMBRA_ROOT", dir)
	return dir
}

// withDeadDaemon points the package-level apiClient at a socket that does not
// exist, so collectEvidence exercises its "daemon down" degradation path.
// apiClient is normally wired by root.go's PersistentPreRun, which does not run
// under `go test`.
func withDeadDaemon(t *testing.T) {
	t.Helper()
	prev := apiClient
	apiClient = client.New(paths.APISocket())
	t.Cleanup(func() { apiClient = prev })
}

func writeKey(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ssh", "id_ed25519"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func findUnprobed(us []doctor.Unprobed, what string) *doctor.Unprobed {
	for i := range us {
		if us[i].What == what {
			return &us[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// C1 — the configured address must reach the classifier.
// ---------------------------------------------------------------------------

// MachineView embeds registry.Machine, so the static create-time address is
// already available beside the readiness-confirmed one. Without carrying it
// across, the classifier cannot tell a booting guest from a broken one.
func TestGuestEvidenceCarriesConfiguredIP(t *testing.T) {
	root := withUmbraRoot(t)
	writeKey(t, root)
	mv := &client.MachineView{
		Machine: registry.Machine{Name: "fwb-ci5", IP: "192.168.127.10"},
		State:   vm.StateRunning,
		IP:      "", // still booting: readiness has not confirmed an address
		SSHPort: 2222,
	}
	g, _ := guestEvidenceFor(mv)
	if g.ConfiguredIP != "192.168.127.10" {
		t.Fatalf("ConfiguredIP = %q, want the registry address", g.ConfiguredIP)
	}
	if g.IP != "" {
		t.Fatalf("IP = %q, want empty — the runtime address is not confirmed yet", g.IP)
	}
}

// End to end through the classifier: a booting guest must not be told to
// destroy itself.
func TestBootingGuestDoesNotProduceARecreateInstruction(t *testing.T) {
	root := withUmbraRoot(t)
	writeKey(t, root)
	mv := &client.MachineView{
		Machine: registry.Machine{Name: "fwb-ci5", IP: "192.168.127.10"},
		State:   vm.StateRunning, IP: "", SSHPort: 2222,
	}
	g, _ := guestEvidenceFor(mv)
	for _, v := range doctor.Classify(doctor.Evidence{DaemonUp: true, Guests: []doctor.GuestEvidence{g}}) {
		if strings.Contains(v.NextAction, "umbra rm") {
			t.Fatalf("booting guest produced a recreate instruction: %+v", v)
		}
	}
}

// ---------------------------------------------------------------------------
// I3 — a LOCAL ssh failure is not a guest fault.
//
// With BatchMode=yes a missing or unloaded ~/.umbra/ssh/id_ed25519 fails
// silently, which rendered as guest-ssh-stall FAIL plus "stop and start the
// guest" for EVERY running guest on the fleet.
// ---------------------------------------------------------------------------

func TestMissingLocalKeyIsAnUnprobedNotAGuestFault(t *testing.T) {
	withUmbraRoot(t) // no key written
	mv := &client.MachineView{
		Machine: registry.Machine{Name: "fwb-ci5", IP: "192.168.127.10"},
		State:   vm.StateRunning, IP: "192.168.127.10", SSHPort: 2222,
	}
	g, unprobed := guestEvidenceFor(mv)
	if g.SSHProbed {
		t.Fatal("SSHProbed set despite there being no local key to probe with")
	}
	u := findUnprobed(unprobed, "ssh")
	if u == nil {
		t.Fatalf("unprobed = %+v, want an ssh record", unprobed)
	}
	if !strings.Contains(u.Detail, "id_ed25519") {
		t.Errorf("detail does not name the missing key: %q", u.Detail)
	}
	if u.NextAction == "" {
		t.Error("no next action for a missing local key")
	}
	// It must not read as a guest problem.
	if strings.Contains(u.NextAction, "umbra stop") {
		t.Errorf("advised restarting the guest for a LOCAL key problem: %q", u.NextAction)
	}
}

// An ssh failure whose output shows authentication was refused is a local
// credential problem, not a wedged guest.
func TestSSHAuthFailureIsDistinguishedFromAWedgedGuest(t *testing.T) {
	authFailures := []string{
		"umbra@127.0.0.1: Permission denied (publickey).",
		"Warning: Identity file /Users/x/.umbra/ssh/id_ed25519 not accessible: No such file or directory.",
		"Host key verification failed.",
		"Received disconnect from 127.0.0.1 port 2222:2: Too many authentication failures",
	}
	for _, out := range authFailures {
		if !sshAuthFailure(out) {
			t.Errorf("sshAuthFailure(%q) = false, want true", out)
		}
	}
	wedged := []string{
		"ssh: connect to host 127.0.0.1 port 2222: Operation timed out",
		"ssh: connect to host 127.0.0.1 port 2222: Connection refused",
		"",
	}
	for _, out := range wedged {
		if sshAuthFailure(out) {
			t.Errorf("sshAuthFailure(%q) = true, want false — that is a guest/network fault", out)
		}
	}
}

// ---------------------------------------------------------------------------
// I2 — the mixed-fleet GitHub blind spot.
//
// The units==0 Unprobed only fired when NO guest yielded units. With one
// unreachable and one reachable guest, the unreachable guest's repos were
// silently omitted.
// ---------------------------------------------------------------------------

func TestUnreachableGuestGetsItsOwnUnprobedEvenWhenAnotherGuestIsFine(t *testing.T) {
	guests := []doctor.GuestEvidence{
		{Name: "fwb-ci5", State: vm.StateRunning, IP: "192.168.127.10", SSHProbed: true, SSHOK: true,
			Runners: []doctor.RunnerEvidence{{Unit: "actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-1.service", Active: true}}},
		{Name: "fwb-ci2", State: vm.StateRunning, IP: "192.168.127.11", SSHProbed: true, SSHOK: false},
	}
	_, _, unprobed := collectGitHubWith(context.Background(), func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("no network in test")
	}, false, guests)

	var found bool
	for _, u := range unprobed {
		if u.Subject == "fwb-ci2" {
			found = true
			if u.NextAction == "" {
				t.Error("per-guest unprobed has no next action")
			}
		}
	}
	if !found {
		t.Fatalf("unprobed = %+v, want a record naming the unreachable guest fwb-ci2", unprobed)
	}
}

// The units==0 message must not send the operator to systemctl when the real
// cause was a FAILED UNIT LISTING on a guest that was perfectly reachable.
func TestUnitsZeroMessageDistinguishesReachableGuests(t *testing.T) {
	guests := []doctor.GuestEvidence{
		{Name: "fwb-ci5", State: vm.StateRunning, IP: "192.168.127.10", SSHProbed: true, SSHOK: true},
	}
	_, _, unprobed := collectGitHubWith(context.Background(), nil, false, guests)
	u := findUnprobed(unprobed, "GitHub repos")
	if u == nil {
		t.Fatalf("unprobed = %+v, want a GitHub repos record", unprobed)
	}
	if !strings.Contains(u.Detail, "reachable") {
		t.Errorf("detail does not distinguish the reachable-guest case: %q", u.Detail)
	}
}

// ---------------------------------------------------------------------------
// I1 — org-level runners.
// ---------------------------------------------------------------------------

// An org-level runner unit names a bare org, which has no owner/repo split.
// Probing it against repos/<owner>/<repo> can never work, so the resulting
// Unknown must SAY it is an org-level runner rather than blaming gh auth.
func TestOrgLevelScopeIsProbedAtTheOrgEndpoint(t *testing.T) {
	gh := func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		// No repo candidate resolves: this is not a repo.
		case strings.Contains(joined, "repos/"):
			return nil, errors.New("404")
		case strings.Contains(joined, "orgs/ForceAI-KW/actions/runners"):
			return []byte(`{"runners":[{"name":"fwb-ci5-1","status":"online"}]}`), nil
		case strings.Contains(joined, "orgs/ForceAI-KW"):
			return []byte("ForceAI-KW"), nil
		}
		return nil, errors.New("unexpected call: " + joined)
	}
	out, _ := collectRepos(context.Background(), gh, []string{"actions.runner.ForceAI-KW.fwb-ci5-1.service"})
	if len(out) != 1 {
		t.Fatalf("repos = %+v, want 1", out)
	}
	if !out[0].Probed {
		t.Fatalf("org-level scope was not probed: %+v", out[0])
	}
	if online, ok := out[0].RunnerOnline["fwb-ci5-1"]; !ok || !online {
		t.Fatalf("org runner status not read: %+v", out[0].RunnerOnline)
	}
}

// When the scope resolves to neither a repo nor an org, the report must not
// claim it is an org-level-runner problem.
func TestUnresolvableScopeStaysUnprobed(t *testing.T) {
	gh := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("404") }
	out, _ := collectRepos(context.Background(), gh, []string{"actions.runner.Nope-Nothing.x-1.service"})
	if len(out) != 1 || out[0].Probed {
		t.Fatalf("repos = %+v, want a single unprobed entry", out)
	}
}

// ---------------------------------------------------------------------------
// MINOR — the collection layer had no tests at all. That is where the C3
// silence-as-health bug actually lived; the classifier tests hand-build
// Unprobed literals and so never exercise it.
// ---------------------------------------------------------------------------

func TestCollectLogMissingFileIsUnprobed(t *testing.T) {
	withUmbraRoot(t) // no log directory at all
	var ev doctor.Evidence
	collectLog(&ev)
	u := findUnprobed(ev.Unprobed, "umbrad.err.log")
	if u == nil {
		t.Fatalf("unprobed = %+v, want an umbrad.err.log record", ev.Unprobed)
	}
	if !strings.Contains(u.Detail, "cannot open") {
		t.Errorf("detail = %q, want it to name the open failure", u.Detail)
	}
	if u.NextAction == "" {
		t.Error("no next action")
	}
	if len(ev.LogLines) != 0 {
		t.Error("log lines recorded from a file that could not be opened")
	}
}

func TestCollectLogUnreadableFileIsUnprobed(t *testing.T) {
	root := withUmbraRoot(t)
	logDir := filepath.Join(root, "log")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(logDir, "umbrad.err.log")
	if err := os.WriteFile(p, []byte("whatever"), 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 000 is not enforced")
	}
	var ev doctor.Evidence
	collectLog(&ev)
	if findUnprobed(ev.Unprobed, "umbrad.err.log") == nil {
		t.Fatalf("unprobed = %+v, want an umbrad.err.log record for an unreadable file", ev.Unprobed)
	}
}

// A log with no daemon-start marker cannot separate current-lifetime lines
// from lines left by a fault fixed weeks ago — the stale-log trap.
func TestCollectLogWithoutStartMarkerIsUnprobed(t *testing.T) {
	root := withUmbraRoot(t)
	logDir := filepath.Join(root, "log")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "time=2026-07-19T10:00:00Z level=INFO msg=\"guest link 5a:94:ef:00:00:01 closed: cannot receive packets\"\n"
	if err := os.WriteFile(filepath.Join(logDir, "umbrad.err.log"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var ev doctor.Evidence
	collectLog(&ev)
	u := findUnprobed(ev.Unprobed, "umbrad.err.log")
	if u == nil {
		t.Fatalf("unprobed = %+v, want a record for a log with no start marker", ev.Unprobed)
	}
	if !strings.Contains(u.Detail, "start marker") {
		t.Errorf("detail = %q, want it to name the missing start marker", u.Detail)
	}
	if len(ev.LogLines) != 0 {
		t.Error("recorded log lines that cannot be scoped to the current lifetime")
	}
}

// collectEvidence must NEVER abort. With no daemon and no log it still returns
// a populated Unprobed set rather than a verdict-free "healthy".
func TestCollectEvidenceDegradesInsteadOfReportingHealthy(t *testing.T) {
	withUmbraRoot(t)
	withDeadDaemon(t)
	// Bounded context: the client retries dial errors through ~5s of backoff
	// before giving up. The degradation path under test is identical either
	// way, and the suite should not pay that wait twice.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	ev := collectEvidence(ctx)
	if ev.DaemonUp {
		t.Fatal("DaemonUp true against a temp root with no daemon socket")
	}
	if len(ev.Unprobed) == 0 {
		t.Fatal("no unprobed records — a half-collected host would report healthy")
	}
	vs := doctor.Classify(ev)
	if len(vs) == 0 {
		t.Fatal("classifier produced no verdicts from a completely unprobed host")
	}
	if newDoctorReport(ev.DeepRun, vs).Healthy {
		t.Fatal("reported healthy after collecting nothing at all")
	}
}

func TestResolveRepoTriesEveryCandidateSplit(t *testing.T) {
	var tried []string
	gh := func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		tried = append(tried, joined)
		if strings.Contains(joined, "repos/ForceAI-KW/force-website-builder") {
			return []byte("ForceAI-KW/force-website-builder\n"), nil
		}
		return nil, errors.New("404")
	}
	got, err := resolveRepo(context.Background(), gh, "ForceAI-KW-force-website-builder")
	if err != nil {
		t.Fatalf("resolveRepo: %v", err)
	}
	if got != "ForceAI-KW/force-website-builder" {
		t.Fatalf("repo = %q", got)
	}
	if len(tried) < 2 {
		t.Errorf("only %d candidate(s) tried, want the earlier splits attempted first: %v", len(tried), tried)
	}
}

func TestResolveRepoErrorsWhenNothingResolves(t *testing.T) {
	gh := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("404") }
	if _, err := resolveRepo(context.Background(), gh, "ForceAI-KW-nope"); err == nil {
		t.Fatal("resolveRepo returned nil error when no candidate resolved")
	}
	// A scope with no separator at all cannot even produce a candidate.
	if _, err := resolveRepo(context.Background(), gh, "single"); err == nil {
		t.Fatal("resolveRepo accepted a scope with no owner/repo separator")
	}
}

func TestProbeBillingLockoutDetectsAndRejects(t *testing.T) {
	runs := `{"workflow_runs":[{"id":7,"conclusion":"failure","updated_at":"%s"}]}`
	recent := "2026-07-19T10:00:00Z"

	for _, c := range []struct {
		name       string
		jobs       string
		wantLocked bool
		wantLabels []string
	}{
		{
			name:       "lockout signature",
			jobs:       `{"jobs":[{"conclusion":"failure","runner_name":"","started_at":"2026-07-19T10:00:00Z","completed_at":"2026-07-19T10:00:03Z","steps":[],"labels":["ubuntu-latest"]}]}`,
			wantLocked: true,
			wantLabels: []string{"ubuntu-latest"},
		},
		{
			// Reached a runner and ran steps: an ordinary CI failure. Sending
			// the operator to the org billing page for a broken test is exactly
			// the misdiagnosis this tool exists to prevent.
			name:       "ordinary failure",
			jobs:       `{"jobs":[{"conclusion":"failure","runner_name":"fwb-ci5-1","started_at":"2026-07-19T10:00:00Z","completed_at":"2026-07-19T10:04:00Z","steps":[{}],"labels":["self-hosted"]}]}`,
			wantLocked: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			gh := func(_ context.Context, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "/jobs") {
					return []byte(c.jobs), nil
				}
				return []byte(fmt.Sprintf(runs, recent)), nil
			}
			locked, labels, err := probeBillingLockout(context.Background(), gh, "o/r")
			if err != nil {
				t.Fatalf("probeBillingLockout: %v", err)
			}
			if locked != c.wantLocked {
				t.Fatalf("locked = %v, want %v", locked, c.wantLocked)
			}
			if c.wantLocked && strings.Join(labels, ",") != strings.Join(c.wantLabels, ",") {
				t.Fatalf("labels = %v, want %v", labels, c.wantLabels)
			}
		})
	}
}

// A long-fixed lockout must not be rediagnosed forever — the same
// stale-evidence trap the log scanner guards against.
func TestProbeBillingLockoutIgnoresStaleRuns(t *testing.T) {
	gh := func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "/jobs") {
			t.Fatal("fetched jobs for a run older than the lookback window")
		}
		return []byte(`{"workflow_runs":[{"id":7,"conclusion":"failure","updated_at":"2020-01-01T00:00:00Z"}]}`), nil
	}
	locked, _, err := probeBillingLockout(context.Background(), gh, "o/r")
	if err != nil {
		t.Fatalf("probeBillingLockout: %v", err)
	}
	if locked {
		t.Fatal("diagnosed a lockout from a run outside the lookback window")
	}
}

func TestProbeBillingLockoutPropagatesGHErrors(t *testing.T) {
	gh := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("rate limited") }
	if _, _, err := probeBillingLockout(context.Background(), gh, "o/r"); err == nil {
		t.Fatal("swallowed a gh error — the repo would be recorded as probed and healthy")
	}
}

// MINOR — every Unprobed record must carry a next action, so the operator is
// never told what failed without being told what to do about it.
func TestEveryUnprobedRecordHasANextAction(t *testing.T) {
	withUmbraRoot(t)
	withDeadDaemon(t)
	// Bounded context: the client retries dial errors through ~5s of backoff
	// before giving up. The degradation path under test is identical either
	// way, and the suite should not pay that wait twice.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	ev := collectEvidence(ctx)
	for _, u := range ev.Unprobed {
		if u.NextAction == "" {
			t.Errorf("unprobed record %q (%s) has no next action", u.What, u.Subject)
		}
	}
}

// The scope parser is shared with the classifier so the two cannot drift.
func TestRepoScopeFromUnitDelegatesToDoctor(t *testing.T) {
	unit := "actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-1.service"
	if got, want := repoScopeFromUnit(unit), doctor.RunnerUnitScope(unit); got != want {
		t.Fatalf("repoScopeFromUnit = %q, doctor.RunnerUnitScope = %q — the two parsers have drifted", got, want)
	}
}
