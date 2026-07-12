//go:build integration

package vm_test

// Boots a real VM via the full daemon stack. Requires: arm64 mac, signed
// umbrad NOT needed here (test binary needs the entitlement instead):
// run via scripts/e2e-smoke.sh normally; this test validates the Go-level
// API without the CLI. Sign the test binary first:
//   go test -tags=integration -c ./internal/vm && codesign --force \
//     --entitlements build/vz.entitlements --sign - vm.test && ./vm.test
// The Makefile target `test-integration` automates exactly that.
//
// UMBRA_ITEST_IMAGES_DIR: if set, the base image is downloaded/cached there
// instead of a fresh t.TempDir. This lets this test and scripts/e2e-smoke.sh
// share one ~600MB download instead of each fetching their own copy.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ForceAI-KW/umbra/internal/cloudinit"
	"github.com/ForceAI-KW/umbra/internal/image"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/sshkey"
	"github.com/ForceAI-KW/umbra/internal/vm"
	"github.com/ForceAI-KW/umbra/internal/vmnet"
)

func TestBootShellStopCycle(t *testing.T) {
	root := t.TempDir()
	machinesDir := filepath.Join(root, "machines")
	reg := registry.New(machinesDir)
	m := &registry.Machine{Name: "itest", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20,
		Image: "ubuntu:noble", MAC: "a6:5e:00:aa:bb:01", IP: "192.168.127.50", CreatedAt: time.Now()}
	if err := reg.Save(m); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

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
	pub, _, err := sshkey.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloudinit.BuildSeed(m, mdir, pub, nil); err != nil {
		t.Fatal(err)
	}

	mgr := vm.NewManager(reg, machinesDir, nil, nil)
	if err := mgr.Start(ctx, m.Name); err != nil {
		t.Fatal(err)
	}
	ip, err := vm.WaitReady(ctx,
		func() (string, bool, error) { return vmnet.LookupIPFromFile(m.MAC) },
		func(addr string) error {
			c, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err == nil {
				c.Close()
			}
			return err
		}, vm.DefaultReadyTimeout)
	if err != nil {
		t.Fatalf("readiness: %v", err)
	}
	t.Logf("machine up at %s", ip)

	if err := mgr.Stop(ctx, m.Name); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := mgr.Info(m.Name).State; got != vm.StateStopped {
		t.Fatalf("state after stop: %s", got)
	}
}
