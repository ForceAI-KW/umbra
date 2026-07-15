package runner

import (
	"strings"
	"testing"
)

func TestInstallScriptContainsContract(t *testing.T) {
	s := InstallScript(InstallParams{
		RepoURL: "https://github.com/ForceAI-KW/force-website-builder",
		Token:   "REGTOK", RunnerName: "fwb-ci5-fwb-1",
		DirName: "actions-runner-fwb-1", Labels: "wsl2,umbra-ci", Version: "2.328.0",
	})
	for _, want := range []string{
		"actions-runner-linux-arm64-2.328.0.tar.gz",
		`--url "https://github.com/ForceAI-KW/force-website-builder"`,
		`--token "REGTOK"`,
		`--name "fwb-ci5-fwb-1"`,
		`--labels "wsl2,umbra-ci"`,
		"--unattended --replace",
		"svc.sh install",
		"Restart=always",
		"RestartSec=10",
		"systemctl daemon-reload",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("script missing %q", want)
		}
	}
	if strings.Contains(s, "$REG_TOKEN") {
		t.Fatal("script must not depend on caller env — token is inlined")
	}
}

func TestHardenScriptCoversAllRunnerUnits(t *testing.T) {
	s := HardenScript()
	for _, want := range []string{"actions.runner.", "override.conf", "Restart=always", "systemctl daemon-reload"} {
		if !strings.Contains(s, want) {
			t.Fatalf("harden script missing %q", want)
		}
	}
}
