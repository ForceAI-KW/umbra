package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ForceAI-KW/umbra/internal/doctor"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

// ---------------------------------------------------------------------------
// Minor (wave 6) — a guest that was never ssh-probed is not "reachable".
//
// collectGitHubWith skips guests whose ssh probe FAILED (SSHProbed && !SSHOK)
// and records them as unprobed. But a guest whose ssh could not be ATTEMPTED
// at all — no local key, no ssh binary, no forwarded port, all of which leave
// SSHProbed false — fell past that guard into the `len(g.Runners) == 0`
// branch and was counted as a REACHABLE guest that returned no units.
//
// The resulting record told the operator "N reachable guest(s) returned no
// actions.runner units ... either none is registered, or the unit listing
// itself failed" about a guest nothing was ever run against. Both offered
// explanations are wrong, and the honest one — "we never got to look" — is the
// one it omits. It is the silence-as-health shape again, one layer in.
// ---------------------------------------------------------------------------

func TestUnprobedGuestIsNotCountedAsReachableWithNoUnits(t *testing.T) {
	guests := []doctor.GuestEvidence{{
		Name:  "fwb-ci5",
		State: vm.StateRunning,
		// Never probed: the local key was missing, so no ssh ran at all.
		SSHProbed: false,
	}}

	gh := func(ctx context.Context, args ...string) ([]byte, error) { return nil, nil }
	_, _, unprobed := collectGitHubWith(context.Background(), gh, true, guests)

	rec := findUnprobed(unprobed, "GitHub repos")
	if rec == nil {
		t.Fatalf("no GitHub repos record at all for an unprobed guest; got %+v", unprobed)
	}
	if strings.Contains(rec.Detail, "reachable") {
		t.Fatalf("described a guest that was never ssh-probed as reachable: %q", rec.Detail)
	}
	// The record must name the guest, or the operator cannot tell which one
	// was skipped on a multi-guest fleet.
	if !strings.Contains(rec.Detail+rec.Subject, "fwb-ci5") {
		t.Errorf("record does not identify the unprobed guest: %+v", rec)
	}
}

// The genuinely reachable case must keep its existing wording — this minor is
// a correction, not a rewrite of the honest branch.
func TestReachableGuestWithNoUnitsStillReadsAsReachable(t *testing.T) {
	guests := []doctor.GuestEvidence{{
		Name: "fwb-ci5", State: vm.StateRunning,
		SSHProbed: true, SSHOK: true, // probed, answered, genuinely has no units
	}}

	gh := func(ctx context.Context, args ...string) ([]byte, error) { return nil, nil }
	_, _, unprobed := collectGitHubWith(context.Background(), gh, true, guests)

	rec := findUnprobed(unprobed, "GitHub repos")
	if rec == nil {
		t.Fatalf("no GitHub repos record for a reachable guest with no units; got %+v", unprobed)
	}
	if !strings.Contains(rec.Detail, "reachable") {
		t.Fatalf("a genuinely reachable guest lost its reachable wording: %q", rec.Detail)
	}
}
