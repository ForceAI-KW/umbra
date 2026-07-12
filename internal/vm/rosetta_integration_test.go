//go:build integration

package vm_test

// Boots the docker VM on a real netstack, waits for dockerd, runs the socket
// bridge, and drives `docker run --platform linux/amd64` against it — the
// M6 Rosetta acceptance test (docs/research/rosetta-amd64.md). Requires an
// arm64 Mac with Rosetta installed + the virtualization entitlement on the
// test binary (make test-integration) + the host `docker` CLI. dockerd
// install pulls packages and Rosetta itself may need a first-time download,
// so this is slow and memory-sensitive (docs/research/dockerd-in-vm.md) —
// give it a long timeout. Set UMBRA_ITEST_IMAGES_DIR to reuse a cached base
// image.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ForceAI-KW/umbra/internal/dockerbridge"
	"github.com/ForceAI-KW/umbra/internal/netstack"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

func TestDockerRunAmd64UnderRosetta(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("host docker CLI not installed")
	}
	root := t.TempDir()
	machinesDir := filepath.Join(root, "machines")
	reg := registry.New(machinesDir)
	st, err := netstack.New()
	if err != nil {
		t.Fatalf("netstack: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	m := &registry.Machine{Name: "docker", CPUs: 2, MemoryMiB: 4096, DiskGiB: 40,
		Image: "ubuntu:noble", MAC: "a6:5e:00:aa:bb:d2", IP: "192.168.127.62",
		Role: registry.ReservedDockerName, CreatedAt: time.Now()}
	provisionMachine(t, ctx, reg, root, machinesDir, m, nil)

	mgr := vm.NewManager(reg, machinesDir, st, nil)
	if err := mgr.Start(ctx, m.Name); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background(), m.Name)
	waitReadyNet(t, ctx, st, m.IP)

	// dockerd installs + starts during cloud-init runcmd; give it time
	// (get.docker.com is memory-flaky per docs/research/dockerd-in-vm.md).
	if err := dockerbridge.WaitDockerReady(ctx, st.DialContextTCP, m.IP+":2375", 10*time.Minute); err != nil {
		t.Fatalf("dockerd readiness: %v", err)
	}

	sock := filepath.Join(root, "docker.sock")
	br, err := dockerbridge.Listen(st, sock, m.IP+":2375")
	if err != nil {
		t.Fatalf("bridge listen: %v", err)
	}
	defer br.Close()
	go br.Serve(ctx)

	host := "unix://" + sock
	// The headline: docker run --platform linux/amd64 must actually execute
	// x86_64 code, translated in-guest by Rosetta via the "vz-rosetta"
	// VirtioFS share + binfmt_misc handler (config_darwin.go, cloudinit's
	// rosettaRuncmdLines) — no docker/containerd-specific config needed
	// beyond binfmt registration (docs/research/rosetta-amd64.md §6).
	out, err := exec.CommandContext(ctx, "docker", "-H", host, "run", "--rm",
		"--platform", "linux/amd64", "alpine", "uname", "-m").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run --platform linux/amd64: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "x86_64") {
		t.Fatalf("amd64 container output missing x86_64:\n%s", out)
	}
}
