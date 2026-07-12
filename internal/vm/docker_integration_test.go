//go:build integration

package vm_test

// Boots the docker VM on a real netstack, waits for dockerd, runs the socket
// bridge, and drives `docker` against it — the M3 acceptance test. Requires an
// arm64 Mac + the virtualization entitlement on the test binary (make
// test-integration) + the host `docker` CLI. dockerd install pulls packages,
// so this is slow (~4-8 min cold). Set UMBRA_ITEST_IMAGES_DIR to reuse a
// cached base image.

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

func TestDockerRunHelloWorld(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	m := &registry.Machine{Name: "docker", CPUs: 2, MemoryMiB: 4096, DiskGiB: 40,
		Image: "ubuntu:noble", MAC: "a6:5e:00:aa:bb:d0", IP: "192.168.127.60",
		Role: "docker", CreatedAt: time.Now()}
	provisionMachine(t, ctx, reg, root, machinesDir, m, nil)

	mgr := vm.NewManager(reg, machinesDir, st, nil)
	if err := mgr.Start(ctx, m.Name); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background(), m.Name)
	waitReadyNet(t, ctx, st, m.IP)

	// dockerd installs + starts during cloud-init runcmd; give it time.
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
	// docker version over the bridge
	if out, err := exec.CommandContext(ctx, "docker", "-H", host, "version", "--format", "{{.Server.Version}}").Output(); err != nil {
		t.Fatalf("docker version over bridge: %v", err)
	} else {
		t.Logf("dockerd version: %s", strings.TrimSpace(string(out)))
	}

	// the headline: docker run hello-world
	out, err := exec.CommandContext(ctx, "docker", "-H", host, "run", "--rm", "hello-world").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run hello-world: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Hello from Docker") {
		t.Fatalf("hello-world output missing banner:\n%s", out)
	}
}
