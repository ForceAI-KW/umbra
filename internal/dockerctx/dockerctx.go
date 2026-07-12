// Package dockerctx registers and removes the "umbra" docker CLI context,
// which points the docker CLI at the host unix socket bridged to dockerd
// inside the reserved docker VM. See docs/research/dockerd-in-vm.md §3.
package dockerctx

import (
	"fmt"
	"os/exec"
	"strings"
)

const contextName = "umbra"

// execCommand and lookPath are overridden in tests so Ensure/Remove/Current
// can be exercised without a real docker binary in PATH.
var (
	execCommand = exec.Command
	lookPath    = exec.LookPath
)

// Ensure registers (or updates) the "umbra" docker context to point at
// sockPath and makes it the current context. `docker context create` errors
// on a context that already exists (no upsert flag), so this inspects first
// and falls back to `update` when it's already there — research §3.
func Ensure(sockPath string) error {
	if _, err := lookPath("docker"); err != nil {
		return fmt.Errorf("docker CLI not found in PATH — install it with `brew install docker`: %w", err)
	}

	hostArg := "host=unix://" + sockPath
	if err := execCommand("docker", "context", "inspect", contextName).Run(); err == nil {
		if out, err := execCommand("docker", "context", "update", contextName, "--docker", hostArg).CombinedOutput(); err != nil {
			return fmt.Errorf("docker context update: %w: %s", err, out)
		}
	} else {
		if out, err := execCommand("docker", "context", "create", contextName, "--docker", hostArg).CombinedOutput(); err != nil {
			return fmt.Errorf("docker context create: %w: %s", err, out)
		}
	}
	if out, err := execCommand("docker", "context", "use", contextName).CombinedOutput(); err != nil {
		return fmt.Errorf("docker context use: %w: %s", err, out)
	}
	return nil
}

// Remove switches the docker CLI back to the default context (best-effort —
// a failure here, e.g. already on default, doesn't block cleanup) and
// deletes the umbra context. A "not found" failure on the rm is swallowed
// (P15) so uninstall is safe to run even when install never completed or
// ran twice.
func Remove() error {
	_ = execCommand("docker", "context", "use", "default").Run() // best-effort
	out, err := execCommand("docker", "context", "rm", contextName).CombinedOutput()
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "not found") {
		return fmt.Errorf("docker context rm: %w: %s", err, out)
	}
	return nil
}

// Current returns the docker CLI's current context name. Best-effort — used
// only for status reporting, so callers should treat an error as "unknown".
func Current() (string, error) {
	out, err := execCommand("docker", "context", "show").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// IsCurrent reports whether "umbra" is the docker CLI's current context.
// Best-effort: any error (docker missing, no contexts yet) reports false.
func IsCurrent() bool {
	cur, err := Current()
	return err == nil && cur == contextName
}
