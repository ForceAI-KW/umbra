package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ForceAI-KW/umbra/internal/api"
	"github.com/ForceAI-KW/umbra/internal/dockerbridge"
	"github.com/ForceAI-KW/umbra/internal/dockerctx"
	"github.com/ForceAI-KW/umbra/internal/netstack"
	"github.com/ForceAI-KW/umbra/internal/paths"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

// dockerReadyTimeout bounds how long Start waits for dockerd's /_ping to
// return 200 once the VM itself is network-reachable (P13).
const dockerReadyTimeout = 120 * time.Second

// dockerController implements api.Docker: it owns the reserved docker-role
// machine's install/start/stop/status/uninstall lifecycle, layered on top of
// the same vm.Manager + provision/ready closures every other machine uses
// (research §4) — vm.Manager itself stays docker-unaware. It additionally
// owns the *dockerbridge.Bridge (host socket -> guest dockerd TCP) and the
// docker CLI context registration, neither of which vm.Manager or
// internal/api know about.
type dockerController struct {
	reg    *registry.Registry
	mgr    *vm.Manager
	st     *netstack.Stack
	prov   api.Provisioner
	ready  func(ctx context.Context, m *registry.Machine) (string, error)
	logger *slog.Logger

	daemonCtx context.Context
	bridgeWG  *sync.WaitGroup

	// opMu serializes Install/Start/Stop/Uninstall as whole operations so a
	// Stop can't complete mid-Start (which would leave a live, unregistered
	// bridge). mu guards only the bridge pointer for quick reads (Status).
	opMu   sync.Mutex
	mu     sync.Mutex
	bridge *dockerbridge.Bridge
}

func newDockerController(reg *registry.Registry, mgr *vm.Manager, st *netstack.Stack,
	prov api.Provisioner, ready func(ctx context.Context, m *registry.Machine) (string, error),
	logger *slog.Logger, daemonCtx context.Context, bridgeWG *sync.WaitGroup) *dockerController {
	return &dockerController{reg: reg, mgr: mgr, st: st, prov: prov, ready: ready, logger: logger, daemonCtx: daemonCtx, bridgeWG: bridgeWG}
}

func dockerRandomMAC() string {
	b := make([]byte, 6)
	rand.Read(b)
	b[0] = (b[0] | 0x02) &^ 0x01 // locally administered, unicast
	parts := make([]string, 6)
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02x", x)
	}
	return strings.Join(parts, ":")
}

func dockerHostBuild() string {
	out, err := exec.Command("/usr/bin/sw_vers", "-buildVersion").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (d *dockerController) view(m *registry.Machine) api.MachineView {
	info := d.mgr.Info(m.Name)
	return api.MachineView{Machine: *m, State: info.State, IP: info.IP, SSHPort: info.SSHPort, Zombie: info.Zombie}
}

func (d *dockerController) sockPath() string { return filepath.Join(paths.Run(), "docker.sock") }

// Install creates the reserved docker machine (idempotent: if it already
// exists, its current view is returned without re-provisioning) with the
// docker defaults from research §4, then runs it through the same
// provision closure every other machine uses — cloudinit.BuildSeed already
// branches on Role=="docker" to inject the dockerd cloud-init profile.
func (d *dockerController) Install(ctx context.Context) (api.MachineView, error) {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	if m, err := d.reg.Load(registry.ReservedDockerName); err == nil {
		return d.view(m), nil
	}

	m := &registry.Machine{
		Name:      registry.ReservedDockerName,
		Role:      registry.ReservedDockerName,
		CPUs:      2,
		MemoryMiB: 4096,
		DiskGiB:   40,
		Image:     "ubuntu:noble",
		MAC:       dockerRandomMAC(),
		HostBuild: dockerHostBuild(),
		CreatedAt: time.Now().UTC(),
	}
	if err := d.reg.Save(m); err != nil {
		return api.MachineView{}, err
	}
	if err := d.prov(ctx, m); err != nil {
		_ = d.reg.Delete(m.Name) // don't leave a half-provisioned docker VM behind
		return api.MachineView{}, fmt.Errorf("provision docker VM: %w", err)
	}
	return d.view(m), nil
}

// Start boots the docker VM, waits for dockerd's Engine API to answer
// /_ping (P13 — the VM can be network-ready before dockerd itself is), then
// wires the host unix-socket bridge and registers/refreshes the "umbra"
// docker context (matching Colima's "reassert context on every start"
// behavior — research §3).
func (d *dockerController) Start(ctx context.Context) (api.MachineView, error) {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	m, err := d.reg.Load(registry.ReservedDockerName)
	if err != nil {
		return api.MachineView{}, fmt.Errorf("docker VM not installed — run `umbra docker install` first: %w", err)
	}
	if err := d.mgr.Start(ctx, registry.ReservedDockerName); err != nil {
		return api.MachineView{}, err
	}
	ip, err := d.ready(ctx, m)
	if err != nil {
		return api.MachineView{}, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, dockerReadyTimeout)
	defer cancel()
	if err := dockerbridge.WaitDockerReady(dialCtx, d.st.DialContextTCP, ip+":2375", dockerReadyTimeout); err != nil {
		return api.MachineView{}, err
	}

	d.mu.Lock()
	staleBridge := d.bridge
	d.mu.Unlock()
	if staleBridge != nil {
		_ = staleBridge.Close() // defensive: shouldn't happen (Stop clears it), but never leak a listener
	}

	br, err := dockerbridge.Listen(d.st, d.sockPath(), ip+":2375")
	if err != nil {
		return api.MachineView{}, fmt.Errorf("listen docker bridge socket: %w", err)
	}
	d.mu.Lock()
	d.bridge = br
	d.mu.Unlock()
	d.bridgeWG.Add(1)
	go func() {
		defer d.bridgeWG.Done()
		br.Serve(d.daemonCtx)
	}()

	if err := dockerctx.Ensure(d.sockPath()); err != nil {
		// The VM + bridge are up; only the CLI convenience registration
		// failed (e.g. docker CLI not installed) — report it but don't tear
		// the VM back down, matching the "brew install docker" hint pattern.
		return api.MachineView{}, err
	}

	return d.view(m), nil
}

// Stop stops the docker VM and closes the socket bridge. The docker CLI
// context is left registered — only Uninstall removes it.
func (d *dockerController) Stop(ctx context.Context) error {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	return d.stopLocked(ctx)
}

// stopLocked is the Stop body; callers must already hold d.opMu.
func (d *dockerController) stopLocked(ctx context.Context) error {
	if err := d.mgr.Stop(ctx, registry.ReservedDockerName); err != nil {
		return err
	}
	d.mu.Lock()
	br := d.bridge
	d.bridge = nil
	d.mu.Unlock()
	if br != nil {
		return br.Close()
	}
	return nil
}

func (d *dockerController) Status(ctx context.Context) api.DockerStatus {
	if _, err := d.reg.Load(registry.ReservedDockerName); err != nil {
		return api.DockerStatus{}
	}
	info := d.mgr.Info(registry.ReservedDockerName)
	return api.DockerStatus{
		Installed:      true,
		Running:        info.State == vm.StateRunning,
		IP:             info.IP,
		Socket:         d.sockPath(),
		ContextCurrent: dockerctx.IsCurrent(),
	}
}

// Uninstall stops the VM (closing the bridge), best-effort deregisters the
// docker CLI context, then deletes the machine (which removes its dir, incl.
// disk image and seed ISO).
func (d *dockerController) Uninstall(ctx context.Context) error {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	if err := d.stopLocked(ctx); err != nil {
		return err
	}
	if err := dockerctx.Remove(); err != nil {
		d.logger.Warn("docker context remove", "err", err)
	}
	return d.reg.Delete(registry.ReservedDockerName)
}
