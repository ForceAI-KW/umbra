//go:build integration

package vm_test

// Boots a ci-runner-role machine and asserts the security property that
// distinguishes it from the docker VM: its dockerd is NEVER reachable on
// tcp:2375 from the host (unlike the shared docker VM, which exposes 2375
// firewalled to the gateway). A CI runner executes untrusted PR code, so its
// docker must stay on the local socket only. Requires arm64 Mac + entitlement
// (make test-integration).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ForceAI-KW/umbra/internal/netstack"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

func TestCIRunnerDockerNotExposedOnTCP(t *testing.T) {
	root := t.TempDir()
	machinesDir := filepath.Join(root, "machines")
	reg := registry.New(machinesDir)
	st, err := netstack.New()
	if err != nil {
		t.Fatalf("netstack: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	m := &registry.Machine{Name: "ci1", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20,
		Image: "ubuntu:noble", MAC: "a6:5e:00:aa:bb:c1", IP: "192.168.127.70",
		Role: registry.RoleCIRunner, CreatedAt: time.Now()}
	provisionMachine(t, ctx, reg, root, machinesDir, m, nil)

	mgr := vm.NewManager(reg, machinesDir, st, nil)
	if err := mgr.Start(ctx, m.Name); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background(), m.Name)
	waitReadyNet(t, ctx, st, m.IP)
	t.Logf("ci-runner up at %s", m.IP)

	// Security assertion: dockerd must NOT be reachable on tcp:2375 from the
	// host. The ci-runner profile installs plain docker (local socket only),
	// no tcp override, no 2375 firewall rule — so a dial to :2375 must fail
	// (connection refused / nothing listening), whether or not the docker
	// install has finished.
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	if c, err := st.DialContextTCP(dialCtx, m.IP+":2375"); err == nil {
		c.Close()
		t.Fatalf("SECURITY: ci-runner dockerd reachable on %s:2375 — must be local-socket only", m.IP)
	}
}
