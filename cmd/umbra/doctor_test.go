package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/doctor"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

func TestDoctorCanaryScriptCoversBothSignalCanaries(t *testing.T) {
	s := canaryScript
	for _, want := range []string{"curl --version", "openssl", "RC=$?"} {
		if !strings.Contains(s, want) {
			t.Errorf("canaryScript missing %q", want)
		}
	}
	// The canary must be bounded — an unbounded stress loop on a suspect host
	// is exactly the wrong thing to leave running.
	if !strings.Contains(s, "seq 1 ") {
		t.Error("canaryScript is not bounded by a fixed iteration count")
	}
}

func TestDoctorCanaryDetectsSignalExitCodes(t *testing.T) {
	// 132 = SIGILL, 139 = SIGSEGV; both are the decisive host-hardware signature.
	for _, c := range []struct {
		out  string
		want bool
	}{
		{"FAULT rc=132\n", true},
		{"FAULT rc=139\n", true},
		// A CLEAN-LOOKING output. Note what this does and does NOT assert:
		// canaryFaulted is a signature matcher, so "no signature" is all it
		// reports. It is NOT a clean bill of health, and the case below pins
		// that distinction — the version of this test that stopped here is
		// what let a canary that never ran be recorded as "ran, found nothing".
		{"ok\n", false},
	} {
		got := canaryFaulted(c.out)
		if got != c.want {
			t.Errorf("canaryFaulted(%q) = %v, want %v", c.out, got, c.want)
		}
	}

	// The same "ok\n" carries no completion sentinel, so the RESULT must be
	// inconclusive rather than a clean canary.
	if res, detail := canaryOutcome("ok\n", nil); res.Ran || detail == "" {
		t.Errorf("output with no completion sentinel must be inconclusive, got Ran=%v detail=%q", res.Ran, detail)
	}
}

// ---------------------------------------------------------------------------
// C6 — the probe ssh argv must not be able to hang or prompt
// ---------------------------------------------------------------------------

func TestSSHProbeArgsCannotHangOrPrompt(t *testing.T) {
	mv := &client.MachineView{SSHPort: 2222}
	got := strings.Join(sshProbeArgs(mv, []string{"true"}), " ")
	for _, want := range []string{"BatchMode=yes", "ConnectTimeout="} {
		if !strings.Contains(got, want) {
			t.Errorf("sshProbeArgs missing %q: %s", want, got)
		}
	}
	if got[:3] != "ssh" {
		t.Errorf("sshProbeArgs must keep argv[0]=ssh, got %q", got)
	}
	if !strings.HasSuffix(got, " true") {
		t.Errorf("sshProbeArgs dropped the remote command: %s", got)
	}
}

// The shared helper must stay interactive-safe: BatchMode=yes would break
// `umbra shell` for anyone whose key needs a passphrase prompt.
func TestSharedSSHArgsStaysInteractive(t *testing.T) {
	mv := &client.MachineView{SSHPort: 2222}
	got := strings.Join(sshArgs(mv, nil), " ")
	if strings.Contains(got, "BatchMode") {
		t.Errorf("sshArgs must not set BatchMode — it is shared with interactive shell/exec: %s", got)
	}
}

// ---------------------------------------------------------------------------
// C5 — inactive runner units must survive the parse
// ---------------------------------------------------------------------------

func TestParseRunnerUnitsSeesInactiveUnits(t *testing.T) {
	// `systemctl list-units --all --no-legend --plain` output. The inactive
	// row is the whole point of C5: without --all it is absent, and the
	// runner-service-down rung can never fire.
	out := `actions.runner.ForceAI-KW-umbra.fwb-ci5-umbra-1.service loaded active   running GitHub Actions Runner
actions.runner.ForceAI-KW-umbra.fwb-ci5-umbra-2.service loaded inactive dead    GitHub Actions Runner
some-other.service                                      loaded active   running Not a runner
`
	got := parseRunnerUnits(out)
	if len(got) != 2 {
		t.Fatalf("parseRunnerUnits returned %d units, want 2: %+v", len(got), got)
	}
	if !got[0].Active {
		t.Errorf("unit %q should be active", got[0].Unit)
	}
	if got[1].Active {
		t.Errorf("unit %q should be inactive — that is the case rung 4 exists for", got[1].Unit)
	}
}

func TestRunnerUnitCommandRequestsInactiveUnits(t *testing.T) {
	got := strings.Join(runnerUnitsCommand(), " ")
	if !strings.Contains(got, "--all") {
		t.Errorf("runnerUnitsCommand must pass --all or inactive units are invisible: %s", got)
	}
	if !strings.Contains(got, "'actions.runner.*'") {
		t.Errorf("the glob must stay single-quoted for the REMOTE shell: %s", got)
	}
}

// ---------------------------------------------------------------------------
// C2 — repo derivation and the GitHub probes
// ---------------------------------------------------------------------------

func TestRepoScopeFromUnit(t *testing.T) {
	for _, c := range []struct{ unit, want string }{
		{"actions.runner.ForceAI-KW-umbra.fwb-ci5-umbra-1.service", "ForceAI-KW-umbra"},
		{"actions.runner.acme-site.runner-3.service", "acme-site"},
		{"not-a-runner.service", ""},
	} {
		if got := repoScopeFromUnit(c.unit); got != c.want {
			t.Errorf("repoScopeFromUnit(%q) = %q, want %q", c.unit, got, c.want)
		}
	}
}

// The scope is <owner>-<repo> with a separator that is also legal INSIDE both
// halves, so it cannot be split by string surgery alone. Every split must be
// offered as a candidate and resolved against GitHub.
func TestRepoCandidatesCoverEveryDash(t *testing.T) {
	got := repoCandidates("ForceAI-KW-umbra")
	want := []string{"ForceAI/KW-umbra", "ForceAI-KW/umbra"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("repoCandidates = %v, want %v", got, want)
	}
	if len(repoCandidates("noseparator")) != 0 {
		t.Error("a scope with no dash has no owner/repo split")
	}
}

func TestParseRunnerStatus(t *testing.T) {
	body := []byte(`{"total_count":2,"runners":[
	  {"id":1,"name":"fwb-ci5-umbra-1","status":"online"},
	  {"id":2,"name":"fwb-ci5-umbra-2","status":"offline"}]}`)
	got, err := parseRunnerStatus(body)
	if err != nil {
		t.Fatalf("parseRunnerStatus: %v", err)
	}
	if !got["fwb-ci5-umbra-1"] || got["fwb-ci5-umbra-2"] {
		t.Errorf("parseRunnerStatus = %v", got)
	}
}

func TestBillingLockoutSignature(t *testing.T) {
	start := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	lockedOut := []ghJob{{
		Conclusion: "failure", RunnerName: "",
		StartedAt: start, CompletedAt: start.Add(3 * time.Second),
	}}
	if locked, _ := billingLockoutSignature(lockedOut); !locked {
		t.Error("the billing-lockout signature (3s, no runner, zero steps) was not recognised")
	}

	// A real job failure: it ran on a runner and executed steps. Diagnosing
	// this as a billing lockout would send Ahmad to the org billing page for
	// a broken test.
	realFailure := []ghJob{{
		Conclusion: "failure", RunnerName: "fwb-ci5-umbra-1",
		StartedAt: start, CompletedAt: start.Add(4 * time.Minute),
		Steps: []json.RawMessage{[]byte(`{}`)},
	}}
	if locked, _ := billingLockoutSignature(realFailure); locked {
		t.Error("a genuine job failure was misread as a billing lockout")
	}

	// A fast failure that still had steps is a workflow-syntax error, not billing.
	fastButStepped := []ghJob{{
		Conclusion: "failure", RunnerName: "",
		StartedAt: start, CompletedAt: start.Add(2 * time.Second),
		Steps: []json.RawMessage{[]byte(`{}`)},
	}}
	if locked, _ := billingLockoutSignature(fastButStepped); locked {
		t.Error("a fast failure WITH steps is not the billing signature")
	}

	if locked, _ := billingLockoutSignature(nil); locked {
		t.Error("no jobs is not evidence of a lockout")
	}
}

// fakeGH replays canned gh responses keyed by a substring of the argv.
type fakeGH struct {
	replies map[string]string
	errs    map[string]error
	calls   []string
}

func (f *fakeGH) run(_ context.Context, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	f.calls = append(f.calls, joined)
	for k, err := range f.errs {
		if strings.Contains(joined, k) {
			return nil, err
		}
	}
	for k, v := range f.replies {
		if strings.Contains(joined, k) {
			return []byte(v), nil
		}
	}
	return nil, fmt.Errorf("fakeGH: no canned reply for %q", joined)
}

func TestCollectReposPopulatesEvidenceFromUnitNames(t *testing.T) {
	f := &fakeGH{replies: map[string]string{
		"repos/ForceAI-KW/umbra --jq":            "ForceAI-KW/umbra\n",
		"repos/ForceAI-KW/umbra/actions/runners": `{"runners":[{"name":"fwb-ci5-umbra-1","status":"offline"}]}`,
		"repos/ForceAI-KW/umbra/actions/runs":    `{"workflow_runs":[]}`,
	}, errs: map[string]error{
		// The wrong split must not resolve.
		"repos/ForceAI/KW-umbra": fmt.Errorf("gh: 404"),
	}}

	got, _ := collectRepos(context.Background(), f.run, []string{
		"actions.runner.ForceAI-KW-umbra.fwb-ci5-umbra-1.service",
		"actions.runner.ForceAI-KW-umbra.fwb-ci5-umbra-2.service", // same repo, probe once
	})
	if len(got) != 1 {
		t.Fatalf("collectRepos returned %d repos, want 1 (deduped): %+v", len(got), got)
	}
	r := got[0]
	if r.Repo != "ForceAI-KW/umbra" {
		t.Errorf("Repo = %q, want ForceAI-KW/umbra", r.Repo)
	}
	if !r.Probed {
		t.Fatal("Probed = false on a fully successful gh probe")
	}
	if online, ok := r.RunnerOnline["fwb-ci5-umbra-1"]; !ok || online {
		t.Errorf("RunnerOnline = %v, want the runner recorded as offline", r.RunnerOnline)
	}
}

// Never fabricate a healthy reading: gh missing / unauthenticated / rate-limited
// must produce Probed:false, which the classifier renders as Unknown.
func TestCollectReposUnprobedWhenGHFails(t *testing.T) {
	f := &fakeGH{errs: map[string]error{"repos/": fmt.Errorf("gh: HTTP 403 rate limit exceeded")}}
	got, _ := collectRepos(context.Background(), f.run, []string{"actions.runner.acme-site.r-1.service"})
	if len(got) != 1 {
		t.Fatalf("want 1 repo entry even when gh fails, got %d", len(got))
	}
	if got[0].Probed {
		t.Error("Probed = true after gh failed — that is a fabricated healthy reading")
	}
	vs := doctor.Classify(doctor.Evidence{DaemonUp: true, Repos: got})
	if len(vs) != 1 || vs[0].Health != doctor.Unknown || vs[0].Rung != doctor.RungUnknown {
		t.Errorf("an unprobed repo must classify as Unknown, got %+v", vs)
	}
}

// ---------------------------------------------------------------------------
// C3 — a probe that could not RUN must be visible, never silence
// ---------------------------------------------------------------------------

func TestUnprobedProbesClassifyAsUnknownNotHealthy(t *testing.T) {
	// Exactly the C3 scenario: the log could not be read and ssh is missing
	// from PATH. Before the fix this printed "healthy: no faults detected".
	ev := doctor.Evidence{
		DaemonUp: true,
		Unprobed: []doctor.Unprobed{
			{What: "umbrad.err.log", Detail: "no daemon start marker"},
			{Subject: "fwb-ci5", What: "ssh", Detail: "ssh not found in PATH"},
		},
	}
	vs := doctor.Classify(ev)
	if len(vs) != 2 {
		t.Fatalf("want 2 unknown verdicts, got %d: %+v", len(vs), vs)
	}
	for _, v := range vs {
		if v.Health != doctor.Unknown || v.Rung != doctor.RungUnknown {
			t.Errorf("unprobed verdict is not Unknown: %+v", v)
		}
	}
	if newDoctorReport(false, vs).Healthy {
		t.Error("a host with unprobed probes was reported healthy — this is C3")
	}
}

func TestRecordUnprobedForUnreachableGuest(t *testing.T) {
	// A running machine with no forwarded ssh port cannot be probed at all.
	// Silence here is what made the ssh half of the ladder invisible.
	mv := &client.MachineView{
		Machine: registry.Machine{Name: "fwb-ci5", MAC: "aa:bb:cc:dd:ee:ff"},
		State:   vm.StateRunning,
		IP:      "192.168.64.5",
		SSHPort: 0,
	}
	g, unprobed := guestEvidenceFor(mv)
	if g.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC not carried through — the netstack rung cannot correlate without it: %+v", g)
	}
	if g.State != vm.StateRunning {
		t.Errorf("State = %q, want the typed vm.State", g.State)
	}
	if len(unprobed) == 0 {
		t.Fatal("a running guest with no ssh port produced no Unprobed record")
	}
	if unprobed[0].Subject != "fwb-ci5" {
		t.Errorf("Unprobed.Subject = %q, want the guest name", unprobed[0].Subject)
	}
}

func TestGuestEvidenceForStoppedGuestIsNotUnprobed(t *testing.T) {
	// A stopped machine is not a failed probe — the ladder skips it by design.
	mv := &client.MachineView{
		Machine: registry.Machine{Name: "fwb-ci2"},
		State:   vm.StateStopped,
	}
	if _, unprobed := guestEvidenceFor(mv); len(unprobed) != 0 {
		t.Errorf("a stopped guest must not be reported as unprobed: %+v", unprobed)
	}
}

// ---------------------------------------------------------------------------
// C10 — the --json shape is a watchdog contract
// ---------------------------------------------------------------------------

func TestDoctorJSONShapeIsStableForTheWatchdog(t *testing.T) {
	vs := doctor.Classify(doctor.Evidence{
		DaemonUp: true,
		Guests:   []doctor.GuestEvidence{{Name: "fwb-ci5", State: vm.StateRunning, IP: "10.0.0.5", SSHProbed: true}},
	})
	b, err := json.Marshal(newDoctorReport(false, vs))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Decode the way ci-runner-guard.sh's doctor_unhealable_rung() does.
	var got struct {
		Deep        bool `json:"deep"`
		Healthy     bool `json:"healthy"`
		UnknownOnly bool `json:"unknown_only"`
		Verdicts    []struct {
			Rung       string   `json:"rung"`
			Health     string   `json:"health"`
			NextAction string   `json:"next_action"`
			Evidence   []string `json:"evidence"`
		} `json:"verdicts"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("the watchdog could not decode doctor --json: %v\n%s", err, b)
	}
	if len(got.Verdicts) != 1 {
		t.Fatalf("want 1 verdict, got %d: %s", len(got.Verdicts), b)
	}
	if got.Verdicts[0].Rung != "guest-ssh-stall" || got.Verdicts[0].Health != "fail" {
		t.Errorf("rung/health slugs changed — this breaks the watchdog: %s", b)
	}
	if got.Verdicts[0].NextAction == "" {
		t.Error("next_action is empty; the watchdog reports it verbatim")
	}
	// F2 renamed the Go field to Supporting; the JSON tag must not move.
	if !strings.Contains(string(b), `"evidence"`) && len(vs[0].Supporting) > 0 {
		t.Errorf("the evidence json tag changed: %s", b)
	}
}

func TestDoctorJSONEmitsEmptyArrayNotNull(t *testing.T) {
	b, err := json.Marshal(newDoctorReport(false, doctor.Classify(doctor.Evidence{DaemonUp: true})))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"verdicts":null`) {
		t.Errorf(`healthy host emitted "verdicts":null; a watchdog contract must emit []: %s`, b)
	}
	if !strings.Contains(string(b), `"verdicts":[]`) {
		t.Errorf(`want "verdicts":[], got: %s`, b)
	}
}

func TestDoctorReportSummaryFields(t *testing.T) {
	fail := []doctor.Verdict{{Rung: doctor.RungGuestNoIP, Health: doctor.Fail}}
	unknown := []doctor.Verdict{{Rung: doctor.RungUnknown, Health: doctor.Unknown}}

	for _, c := range []struct {
		name             string
		vs               []doctor.Verdict
		healthy, unkOnly bool
	}{
		{"nothing found", nil, true, false},
		{"a real fault", fail, false, false},
		{"only unprobed", unknown, false, true},
		{"fault plus unprobed", append(append([]doctor.Verdict{}, fail...), unknown...), false, false},
	} {
		r := newDoctorReport(false, c.vs)
		if r.Healthy != c.healthy || r.UnknownOnly != c.unkOnly {
			t.Errorf("%s: healthy=%v unknown_only=%v, want %v/%v", c.name, r.Healthy, r.UnknownOnly, c.healthy, c.unkOnly)
		}
	}
}

// ---------------------------------------------------------------------------
// C9 — the exit-code path
// ---------------------------------------------------------------------------

func TestExitCodeOnlyFailsOnFail(t *testing.T) {
	for _, c := range []struct {
		name string
		vs   []doctor.Verdict
		want bool
	}{
		{"healthy", nil, false},
		{"unknown only — the probe could not run, which is not a diagnosis", []doctor.Verdict{{Health: doctor.Unknown}}, false},
		{"pass only", []doctor.Verdict{{Health: doctor.Pass}}, false},
		{"a fault", []doctor.Verdict{{Health: doctor.Unknown}, {Health: doctor.Fail}}, true},
	} {
		if got := faultsFound(c.vs); got != c.want {
			t.Errorf("%s: faultsFound = %v, want %v", c.name, got, c.want)
		}
	}
}

// The lockout fingerprint is identical for an org billing block, exhausted
// minutes, and no runner matching the labels. The labels are the only thing in
// the API that tells them apart, so they must survive into the evidence.
func TestBillingLockoutSignatureReportsRequestedLabels(t *testing.T) {
	jobs := []ghJob{
		{Conclusion: "failure", Labels: []string{"ubuntu-latest"}},
		{Conclusion: "failure", Labels: []string{"self-hosted", "ubuntu-latest"}},
		{Conclusion: "success", Labels: []string{"ignored-because-not-failed"}},
	}
	locked, labels := billingLockoutSignature(jobs)
	if !locked {
		t.Fatal("expected the lockout signature to match")
	}
	// Sorted and de-duplicated, so --json output does not churn between runs.
	want := []string{"self-hosted", "ubuntu-latest"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("labels = %v, want %v", labels, want)
		}
	}
}

// Runner units are read over ssh, so a wedged guest yields zero units and the
// whole GitHub half of the ladder goes dark. It must say so rather than report
// nothing — "could not look" and "looked, found nothing" are different answers,
// and conflating them is the defect the Unprobed machinery exists to prevent.
func TestCollectGitHubReportsUnprobedWhenNoUnitsDiscoverable(t *testing.T) {
	guests := []doctor.GuestEvidence{{
		Name: "fwb-ci5", State: vm.StateRunning, IP: "192.168.127.10",
		SSHProbed: true, SSHOK: false,
	}}
	repos, _, unprobed := collectGitHub(context.Background(), guests)
	if len(repos) != 0 {
		t.Errorf("repos = %v, want none", repos)
	}
	if len(unprobed) != 1 {
		t.Fatalf("unprobed = %v, want exactly 1 record", unprobed)
	}
	if !strings.Contains(unprobed[0].Detail, "unreachable") {
		t.Errorf("detail %q does not explain that the guest was unreachable", unprobed[0].Detail)
	}
	if unprobed[0].NextAction == "" {
		t.Error("unprobed record carries no next action")
	}
}

// A reachable guest with no runner units is a different situation from an
// unreachable one, and the operator needs different advice.
func TestCollectGitHubDistinguishesReachableHostWithNoRunners(t *testing.T) {
	guests := []doctor.GuestEvidence{{
		Name: "dev", State: vm.StateRunning, IP: "192.168.127.11",
		SSHProbed: true, SSHOK: true,
	}}
	_, _, unprobed := collectGitHub(context.Background(), guests)
	if len(unprobed) != 1 {
		t.Fatalf("unprobed = %v, want exactly 1 record", unprobed)
	}
	if strings.Contains(unprobed[0].Detail, "unreachable") {
		t.Errorf("detail %q blames unreachability on a guest whose ssh was fine", unprobed[0].Detail)
	}
}
