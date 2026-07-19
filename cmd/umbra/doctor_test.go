package main

import (
	"strings"
	"testing"
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
		{"ok\n", false},
	} {
		got := canaryFaulted(c.out)
		if got != c.want {
			t.Errorf("canaryFaulted(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}
