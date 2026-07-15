package main

import (
	"strings"
	"testing"
)

func TestPruneScriptContents(t *testing.T) {
	must := []string{
		"apt-get clean",
		"docker system prune -af",
		"journalctl --vacuum-size",
		"fstrim -av",
	}
	for _, s := range must {
		if !strings.Contains(pruneScript, s) {
			t.Errorf("pruneScript missing %q", s)
		}
	}
	// Never prune docker volumes — that's guest data loss, not disk reclaim.
	if strings.Contains(pruneScript, "--volumes") {
		t.Error("pruneScript must NOT contain --volumes (would delete container data)")
	}
}

func TestParsePruneFreed(t *testing.T) {
	cases := []struct {
		name   string
		out    string
		want   int64
		wantOK bool
	}{
		{"simple", "PRUNE_FREED 3435973836\n", 3435973836, true},
		{"amid noise", "Deleted Images:\nuntagged: foo\nPRUNE_FREED 1024\n", 1024, true},
		{"trailing junk", "  PRUNE_FREED 2048  \n", 2048, true},
		{"negative (disk grew)", "PRUNE_FREED -512\n", -512, true},
		{"missing", "some other output\n", 0, false},
		{"malformed", "PRUNE_FREED not-a-number\n", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parsePruneFreed(c.out)
			if ok != c.wantOK || got != c.want {
				t.Errorf("parsePruneFreed(%q) = (%d, %v), want (%d, %v)", c.out, got, ok, c.want, c.wantOK)
			}
		})
	}
}
