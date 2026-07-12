package dockerctx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker replaces execCommand with a closure that runs a tiny shell
// script instead of a real docker binary: the script appends the full
// invocation ("docker context inspect umbra") to logPath, then exits 0
// unless fail(args) says otherwise. No real docker binary is ever invoked.
func fakeDocker(t *testing.T, logPath string, fail func(args []string) bool) func(name string, args ...string) *exec.Cmd {
	t.Helper()
	return func(name string, args ...string) *exec.Cmd {
		line := strings.Join(append([]string{name}, args...), " ")
		exit := "0"
		if fail(args) {
			exit = "1"
		}
		script := fmt.Sprintf("echo %s >> %s\nexit %s", shellQuote(line), shellQuote(logPath), exit)
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

func withFakeDocker(t *testing.T, fail func(args []string) bool) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "calls.log")
	origExec, origLookPath := execCommand, lookPath
	execCommand = fakeDocker(t, logPath, fail)
	lookPath = func(string) (string, error) { return "/usr/local/bin/docker", nil }
	t.Cleanup(func() { execCommand, lookPath = origExec, origLookPath })
	return logPath
}

func TestEnsureCreatesWhenContextMissing(t *testing.T) {
	logPath := withFakeDocker(t, func(args []string) bool {
		// "docker context inspect umbra" fails => context doesn't exist yet.
		return len(args) >= 2 && args[0] == "context" && args[1] == "inspect"
	})

	if err := Ensure("/tmp/x/docker.sock"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	calls := readLog(t, logPath)
	want := []string{
		"docker context inspect umbra",
		"docker context create umbra --docker host=unix:///tmp/x/docker.sock",
		"docker context use umbra",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, calls[i], want[i])
		}
	}
}

func TestEnsureUpdatesWhenContextExists(t *testing.T) {
	logPath := withFakeDocker(t, func(args []string) bool { return false }) // every call succeeds => inspect succeeds

	if err := Ensure("/tmp/y/docker.sock"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	calls := readLog(t, logPath)
	want := []string{
		"docker context inspect umbra",
		"docker context update umbra --docker host=unix:///tmp/y/docker.sock",
		"docker context use umbra",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, calls[i], want[i])
		}
	}
}

func TestEnsureRequiresDockerOnPath(t *testing.T) {
	origLookPath := lookPath
	lookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }
	t.Cleanup(func() { lookPath = origLookPath })

	err := Ensure("/tmp/z/docker.sock")
	if err == nil {
		t.Fatal("want error when docker is not on PATH")
	}
	if !strings.Contains(err.Error(), "brew install docker") {
		t.Fatalf("error = %q, want a brew install docker hint", err.Error())
	}
}

func TestRemoveSequenceAndNotFoundIsSwallowed(t *testing.T) {
	logPath := withFakeDocker(t, func(args []string) bool {
		// "use default" fails (already gone) but Remove must still proceed to rm.
		return len(args) >= 2 && args[0] == "context" && args[1] == "use" && len(args) > 2 && args[2] == "default"
	})

	if err := Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	calls := readLog(t, logPath)
	want := []string{
		"docker context use default",
		"docker context rm umbra",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, calls[i], want[i])
		}
	}
}

func TestRemoveSwallowsNotFoundError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	origExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		line := strings.Join(append([]string{name}, args...), " ")
		if len(args) >= 2 && args[0] == "context" && args[1] == "rm" {
			script := fmt.Sprintf("echo %s >> %s\necho 'Error: context \"umbra\": not found' >&2\nexit 1", shellQuote(line), shellQuote(logPath))
			return exec.Command("/bin/sh", "-c", script)
		}
		script := fmt.Sprintf("echo %s >> %s\nexit 0", shellQuote(line), shellQuote(logPath))
		return exec.Command("/bin/sh", "-c", script)
	}
	t.Cleanup(func() { execCommand = origExec })

	if err := Remove(); err != nil {
		t.Fatalf("Remove should swallow a not-found rm error, got: %v", err)
	}
}

func TestCurrentAndIsCurrent(t *testing.T) {
	origExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "echo umbra")
	}
	t.Cleanup(func() { execCommand = origExec })

	cur, err := Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur != "umbra" {
		t.Fatalf("Current = %q, want umbra", cur)
	}
	if !IsCurrent() {
		t.Fatal("IsCurrent should be true")
	}
}
