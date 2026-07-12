//go:build integration

package vm_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ForceAI-KW/umbra/internal/netstack"
)

// sshRun executes a command in the guest over SSH and returns stdout. It dials
// the guest through the netstack by exposing a temporary loopback forward to
// the guest's port 22, since the host `ssh` binary can't dial the userspace
// stack directly. The forward is torn down before returning.
func sshRun(t *testing.T, ctx context.Context, st *netstack.Stack, keyDir, guestIP, cmd string) string {
	t.Helper()
	const localAddr = "127.0.0.1:12200"
	if err := st.Expose("tcp", localAddr, guestIP+":22"); err != nil {
		t.Fatalf("ssh expose: %v", err)
	}
	defer st.Unexpose("tcp", localAddr)

	c := exec.CommandContext(ctx, "ssh",
		"-i", filepath.Join(keyDir, "id_ed25519"),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", // keep the "Permanently added" warning off stderr
		"-o", "ConnectTimeout=10",
		"-p", "12200", "umbra@127.0.0.1", cmd,
	)
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output() // stdout only — command output isn't polluted by ssh chatter
	if err != nil {
		t.Fatalf("ssh %q: %v\nstderr: %s", cmd, err, stderr.String())
	}
	return string(out)
}

func trimWS(s string) string { return strings.TrimSpace(s) }
