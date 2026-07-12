package launchagent

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// genericPlist mirrors just enough of the Apple plist DTD shape to prove
// RenderPlist's output is well-formed XML.
type genericPlist struct {
	XMLName xml.Name `xml:"plist"`
	Version string   `xml:"version,attr"`
}

func TestRenderPlistIsValidXML(t *testing.T) {
	body := RenderPlist("/tmp/x/bin/umbrad", "/tmp/x/log")

	// Strip the DOCTYPE line — encoding/xml has no DTD/external-entity
	// resolver, so leave the XML declaration but drop the DOCTYPE.
	s := string(body)
	lines := strings.Split(s, "\n")
	var kept []string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "<!DOCTYPE") {
			continue
		}
		kept = append(kept, l)
	}
	stripped := strings.Join(kept, "\n")

	var p genericPlist
	if err := xml.Unmarshal([]byte(stripped), &p); err != nil {
		t.Fatalf("RenderPlist output is not valid XML: %v\n%s", err, stripped)
	}
	if p.Version != "1.0" {
		t.Fatalf("plist version = %q, want 1.0", p.Version)
	}
}

func TestRenderPlistContainsRequiredFields(t *testing.T) {
	binPath := "/tmp/x/bin/umbrad"
	logDir := "/tmp/x/log"
	s := string(RenderPlist(binPath, logDir))

	checks := []string{
		Label,
		binPath,
		"<key>RunAtLoad</key>\n    <true/>",
		"SuccessfulExit",
		"<false/>",
		"/opt/homebrew/bin",
		logDir,
	}
	for _, want := range checks {
		if !strings.Contains(s, want) {
			t.Fatalf("RenderPlist output missing %q\n---\n%s", want, s)
		}
	}
}

// fakeLaunchctl replaces execCommand with a closure that appends the full
// invocation to logPath and exits 0 unless fail(args) says otherwise. No
// real launchctl binary is ever invoked.
func fakeLaunchctl(t *testing.T, logPath string, fail func(args []string) bool) func(name string, args ...string) *exec.Cmd {
	t.Helper()
	return func(name string, args ...string) *exec.Cmd {
		line := strings.Join(append([]string{name}, args...), " ")
		exit := "0"
		if fail(args) {
			exit = "1"
		}
		script := fmt.Sprintf("echo %s >> %s\necho fake launchctl output >&2\nexit %s", shellQuote(line), shellQuote(logPath), exit)
		return exec.Command("/bin/sh", "-c", script)
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func readLog(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func withFakeLaunchctl(t *testing.T, fail func(args []string) bool) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "calls.log")
	orig := execCommand
	execCommand = fakeLaunchctl(t, logPath, fail)
	t.Cleanup(func() { execCommand = orig })
	return logPath
}

func withTempHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestInstallIssuesBootoutBootstrapEnableKickstartInOrder(t *testing.T) {
	withTempHome(t)
	logPath := withFakeLaunchctl(t, func(args []string) bool { return false })

	binPath := "/tmp/x/bin/umbrad"
	logDir := "/tmp/x/log"
	if err := Install(binPath, logDir); err != nil {
		t.Fatalf("Install: %v", err)
	}

	uid := strconv.Itoa(os.Getuid())
	guiTarget := "gui/" + uid
	svcTarget := guiTarget + "/" + Label
	plistPath := PlistPath()

	want := []string{
		"launchctl bootout " + svcTarget,
		"launchctl bootstrap " + guiTarget + " " + plistPath,
		"launchctl enable " + svcTarget,
		"launchctl kickstart -k " + svcTarget,
	}
	calls := readLog(t, logPath)
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, calls[i], want[i])
		}
	}

	// Plist must actually be written to disk.
	if _, err := os.Stat(plistPath); err != nil {
		t.Fatalf("plist not written: %v", err)
	}
}

func TestInstallSwallowsBootoutNotLoadedError(t *testing.T) {
	withTempHome(t)
	// bootout fails (not previously loaded); bootstrap/enable/kickstart succeed.
	logPath := withFakeLaunchctl(t, func(args []string) bool {
		return len(args) >= 1 && args[0] == "bootout"
	})

	if err := Install("/tmp/x/bin/umbrad", "/tmp/x/log"); err != nil {
		t.Fatalf("Install should swallow bootout failure: %v", err)
	}

	calls := readLog(t, logPath)
	if len(calls) != 4 {
		t.Fatalf("calls = %v, want 4 calls (bootout still attempted+ignored)", calls)
	}
}

func TestInstallFailsOnBootstrapError(t *testing.T) {
	withTempHome(t)
	withFakeLaunchctl(t, func(args []string) bool {
		return len(args) >= 1 && args[0] == "bootstrap"
	})

	if err := Install("/tmp/x/bin/umbrad", "/tmp/x/log"); err == nil {
		t.Fatal("Install should fail when bootstrap fails")
	}
}

func TestUninstallBootoutThenRemovesPlist(t *testing.T) {
	withTempHome(t)
	logPath := withFakeLaunchctl(t, func(args []string) bool { return false })

	plistPath := PlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	uid := strconv.Itoa(os.Getuid())
	svcTarget := "gui/" + uid + "/" + Label
	calls := readLog(t, logPath)
	want := []string{"launchctl bootout " + svcTarget}
	if len(calls) != len(want) || calls[0] != want[0] {
		t.Fatalf("calls = %v, want %v", calls, want)
	}

	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist should be removed, stat err = %v", err)
	}
}

func TestUninstallSwallowsBootoutNotFoundError(t *testing.T) {
	withTempHome(t)
	logPath := withFakeLaunchctl(t, func(args []string) bool { return true })
	// Override to produce a "not found"-ish message on stderr/stdout.
	execCommand = func(name string, args ...string) *exec.Cmd {
		line := strings.Join(append([]string{name}, args...), " ")
		script := fmt.Sprintf("echo %s >> %s\necho 'Could not find service' \nexit 1", shellQuote(line), shellQuote(logPath))
		return exec.Command("/bin/sh", "-c", script)
	}

	plistPath := PlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall should swallow 'not found' bootout error: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist should still be removed even when bootout was a no-op: %v", err)
	}
}

func TestUninstallIsIdempotentWhenPlistMissing(t *testing.T) {
	withTempHome(t)
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "echo 'Could not find service' >&2; exit 1")
	}
	t.Cleanup(func() { execCommand = exec.Command })

	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall on missing plist should not error: %v", err)
	}
}

func TestPlistPathUnderHomeLibraryLaunchAgents(t *testing.T) {
	withTempHome(t)
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if got := PlistPath(); got != want {
		t.Fatalf("PlistPath() = %q, want %q", got, want)
	}
}

func TestInstalledReflectsPlistFileExistence(t *testing.T) {
	withTempHome(t)
	if Installed() {
		t.Fatal("Installed() should be false before plist exists")
	}

	plistPath := PlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !Installed() {
		t.Fatal("Installed() should be true once plist exists")
	}
}
