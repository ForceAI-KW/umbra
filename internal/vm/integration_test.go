//go:build integration

package vm_test

// Boots real VMs on the embedded gvisor-tap-vsock netstack and exercises the
// full M2 networking path: static-IP boot, DialContextTCP readiness, guest
// internet egress, guest-to-guest name resolution via /etc/hosts, and a
// host->guest port forward. Requires an arm64 Mac + the virtualization
// entitlement on the test binary — run via `make test-integration`, which
// signs the binary. Set UMBRA_ITEST_IMAGES_DIR to reuse a cached base image.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ForceAI-KW/umbra/internal/cloudinit"
	"github.com/ForceAI-KW/umbra/internal/image"
	"github.com/ForceAI-KW/umbra/internal/netstack"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/sshkey"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

// provisionMachine downloads/clones the disk, writes the ssh key + seed with
// the given hosts map. Returns the machine's ssh key path.
func provisionMachine(t *testing.T, ctx context.Context, reg *registry.Registry, root, machinesDir string, m *registry.Machine, hosts map[string]string) string {
	t.Helper()
	if err := reg.Save(m); err != nil {
		t.Fatal(err)
	}
	imagesDir := filepath.Join(root, "images")
	if d := os.Getenv("UMBRA_ITEST_IMAGES_DIR"); d != "" {
		imagesDir = d
	}
	rawBase, err := image.Ensure(ctx, imagesDir, m.Image)
	if err != nil {
		t.Fatal(err)
	}
	mdir := filepath.Join(machinesDir, m.Name)
	if err := image.CloneDisk(rawBase, filepath.Join(mdir, "disk.img"), m.DiskGiB); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := sshkey.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloudinit.BuildSeed(m, mdir, pub, hosts); err != nil {
		t.Fatal(err)
	}
	return priv
}

func waitReadyNet(t *testing.T, ctx context.Context, st *netstack.Stack, ip string) {
	t.Helper()
	_, err := vm.WaitReady(ctx,
		func() (string, bool, error) { return ip, true, nil },
		func(addr string) error {
			c, err := st.DialContextTCP(ctx, addr)
			if err == nil {
				c.Close()
			}
			return err
		}, vm.DefaultReadyTimeout)
	if err != nil {
		t.Fatalf("readiness for %s: %v", ip, err)
	}
}

func TestNetstackBootShellStop(t *testing.T) {
	root := t.TempDir()
	machinesDir := filepath.Join(root, "machines")
	reg := registry.New(machinesDir)
	st, err := netstack.New()
	if err != nil {
		t.Fatalf("netstack: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	m := &registry.Machine{Name: "itest", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20,
		Image: "ubuntu:noble", MAC: "a6:5e:00:aa:bb:01", IP: "192.168.127.50", CreatedAt: time.Now()}
	provisionMachine(t, ctx, reg, root, machinesDir, m, nil)

	mgr := vm.NewManager(reg, machinesDir, st, nil)
	if err := mgr.Start(ctx, m.Name); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background(), m.Name)

	waitReadyNet(t, ctx, st, m.IP)
	t.Logf("machine up at %s (netstack)", m.IP)

	// Guest can reach the internet through the userspace NAT.
	sshRun(t, ctx, st, root, m.IP, "curl -sS -o /dev/null -w '%{http_code}' https://example.com")

	if err := mgr.Stop(ctx, m.Name); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := mgr.Info(m.Name).State; got != vm.StateStopped {
		t.Fatalf("state after stop: %s", got)
	}
}

func TestNetstackForwardAndGuestDNS(t *testing.T) {
	root := t.TempDir()
	machinesDir := filepath.Join(root, "machines")
	reg := registry.New(machinesDir)
	st, err := netstack.New()
	if err != nil {
		t.Fatalf("netstack: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	// Two machines that know each other via /etc/hosts.
	hosts := map[string]string{"one": "192.168.127.51", "two": "192.168.127.52"}
	m1 := &registry.Machine{Name: "one", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20,
		Image: "ubuntu:noble", MAC: "a6:5e:00:aa:bb:02", IP: "192.168.127.51", CreatedAt: time.Now()}
	m2 := &registry.Machine{Name: "two", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20,
		Image: "ubuntu:noble", MAC: "a6:5e:00:aa:bb:03", IP: "192.168.127.52", CreatedAt: time.Now()}
	provisionMachine(t, ctx, reg, root, machinesDir, m1, hosts)
	provisionMachine(t, ctx, reg, root, machinesDir, m2, hosts)

	mgr := vm.NewManager(reg, machinesDir, st, nil)
	for _, m := range []*registry.Machine{m1, m2} {
		if err := mgr.Start(ctx, m.Name); err != nil {
			t.Fatalf("start %s: %v", m.Name, err)
		}
		defer mgr.Stop(context.Background(), m.Name)
		waitReadyNet(t, ctx, st, m.IP)
	}

	// one resolves two by <name>.umbra.local via guest /etc/hosts, which is
	// written by a cloud-init runcmd that completes shortly after sshd comes
	// up — wait for cloud-init to finish before asserting.
	sshRun(t, ctx, st, root, m1.IP, "cloud-init status --wait >/dev/null 2>&1 || true")
	out := sshRun(t, ctx, st, root, m1.IP, "getent hosts two.umbra.local | awk '{print $1}'")
	if got := trimWS(out); got != m2.IP {
		t.Fatalf("guest DNS: two.umbra.local = %q, want %s", got, m2.IP)
	}

	// Host->guest port forward: 127.0.0.1:12222 -> one:22, then dial it.
	if err := st.Expose("tcp", "127.0.0.1:12222", m1.IP+":22"); err != nil {
		t.Fatalf("expose: %v", err)
	}
	c, err := net.DialTimeout("tcp", "127.0.0.1:12222", 5*time.Second)
	if err != nil {
		t.Fatalf("dial forwarded port: %v", err)
	}
	c.Close()
	if err := st.Unexpose("tcp", "127.0.0.1:12222"); err != nil {
		t.Fatalf("unexpose: %v", err)
	}
}
