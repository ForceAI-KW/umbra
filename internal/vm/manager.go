package vm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ForceAI-KW/umbra/internal/netstack"
	"github.com/ForceAI-KW/umbra/internal/registry"
)

// netStack is a package-local alias for *netstack.Stack. Spelling the
// launchFn/Manager seam in terms of this alias (rather than the qualified
// "*netstack.Stack") lets manager_test.go reference the exact same type
// without an import of its own — both files live in package vm, so
// unqualified package-level identifiers are visible across them.
type netStack = *netstack.Stack

// nameSetter is the minimal seam Manager needs against the DNS resolver;
// it is satisfied by *netstack.Resolver. Declaring it locally (rather than
// depending on the concrete type) keeps netstack's Resolver out of
// manager_test.go's fakes.
type nameSetter interface {
	Set(name, ip string)
	Remove(name string)
}

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateCrashed  State = "crashed"
)

type Info struct {
	Name   string `json:"name"`
	State  State  `json:"state"`
	IP     string `json:"ip,omitempty"`
	Zombie bool   `json:"zombie,omitempty"`
}

type instance struct {
	// opMu serializes Start/Stop for this instance so only one lifecycle
	// operation runs at a time. It is held for the whole operation.
	opMu sync.Mutex

	// mu guards field access only (state/ip/handle reads and flips). It is
	// released during the (multi-second) launch/stop calls so Info/List
	// remain responsive — e.g. observing StateStarting — while a lifecycle
	// operation is in flight.
	mu     sync.Mutex
	state  State
	ip     string
	handle vzHandle
	// stopFn tears down this VM's netstack attachment (cancels the
	// AcceptVfkit goroutine, closes the gvisor-tap-vsock socket end) once the
	// stop is confirmed.
	stopFn func()
}

type Manager struct {
	reg         *registry.Registry
	machinesDir string
	net         netStack
	dns         nameSetter
	mu          sync.Mutex
	instances   map[string]*instance
}

// launchFn launches a vz VM for m against netstack st, returning the handle
// and a cleanup closure. It is nil on platforms without a vz build
// (config_darwin.go sets it via init() on darwin/arm64). Tests override it
// directly (save/restore) to inject fakes.
var launchFn func(m *registry.Machine, machinesDir string, st netStack) (vzHandle, func(), error)

// NewManager wires reg/machinesDir plus the shared netstack and DNS
// resolver: net is threaded through to launchFn so machines attach via
// socketpair instead of kernel NAT; dns is nil-safe (Set/Remove are skipped)
// so callers that don't care about DNS can pass nil.
func NewManager(reg *registry.Registry, machinesDir string, net netStack, dns nameSetter) *Manager {
	return &Manager{reg: reg, machinesDir: machinesDir, net: net, dns: dns, instances: map[string]*instance{}}
}

func (m *Manager) inst(name string) *instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i, ok := m.instances[name]; ok {
		return i
	}
	i := &instance{state: StateStopped}
	m.instances[name] = i
	return i
}

// Start launches name's VM, or is a no-op if it is already running/starting.
// It refuses to launch when the instance still holds a handle from a stop
// that never confirmed (P9 zombie) — retry only after a successful Stop.
//
// ctx is checked at entry and immediately before the launch call; the vz
// launch itself (launchFn) is not cancellable once started, so a cancelled
// ctx cannot abort a launch already in flight.
func (m *Manager) Start(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	cfg, err := m.reg.Load(name)
	if err != nil {
		return err
	}

	i := m.inst(name)
	// ctx-aware like Stop: an autostart goroutine blocked here must unblock
	// when the daemon shuts down, or wg.Wait() overruns the shutdown budget.
	if err := acquireOpMu(ctx, i, name); err != nil {
		return err
	}
	defer i.opMu.Unlock()

	i.mu.Lock()
	if i.state == StateRunning || i.state == StateStarting {
		i.mu.Unlock()
		return nil
	}
	if i.handle != nil {
		i.mu.Unlock()
		return fmt.Errorf("machine %s has a live or zombie VM handle; run stop first (previous stop failed to confirm)", name)
	}
	i.state = StateStarting
	i.mu.Unlock()

	if launchFn == nil {
		i.mu.Lock()
		i.state = StateCrashed
		i.mu.Unlock()
		return errors.New("vm launch not supported on this platform")
	}

	if err := ctx.Err(); err != nil {
		i.mu.Lock()
		i.state = StateStopped // nothing launched yet; handle is still nil
		i.mu.Unlock()
		return err
	}

	h, stopFn, err := launchFn(cfg, m.machinesDir, m.net) // darwin-only; guarded inside

	i.mu.Lock()
	defer i.mu.Unlock()
	if err != nil {
		i.state = StateCrashed
		// i.handle stays nil: a launch failure never leaves a live/zombie
		// handle behind, so retry is allowed on the next Start().
		return fmt.Errorf("launch %s: %w", name, err)
	}
	i.handle, i.stopFn = h, stopFn
	i.state = StateRunning
	if m.dns != nil {
		// cfg.IP is set by the provision step (Task 8); until then this
		// registers an empty IP, which Resolver.Set ignores (non-IPv4 guard).
		m.dns.Set(name, cfg.IP)
	}
	return nil
}

// acquireOpMu acquires i.opMu without blocking indefinitely past ctx: it
// tries a non-blocking TryLock, and on contention retries every 50ms while
// racing ctx.Done(). This bounds how long Stop() (and therefore StopAll)
// waits behind a concurrent in-flight Start/Stop, so a shutdown budget
// actually bounds wall-clock time instead of blocking on a plain Lock.
// Note: TryLock polling can barge ahead of a plain-Lock waiter for up to
// ~1ms before Go's mutex starvation mode bounds it — acceptable here since
// both Start and Stop now acquire through this helper.
func acquireOpMu(ctx context.Context, i *instance, name string) error {
	for {
		if i.opMu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w while waiting for in-flight operation", name, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Stop tears down name's VM via stopWithEscalation. i.handle is cleared
// ONLY when the stop is confirmed; if escalation never confirms a stopped
// state (P9 zombie), the handle is left in place so Start() refuses to
// double-launch against the same disk image.
func (m *Manager) Stop(ctx context.Context, name string) error {
	i := m.inst(name)
	if err := acquireOpMu(ctx, i, name); err != nil {
		return err
	}
	defer i.opMu.Unlock()

	i.mu.Lock()
	if i.state != StateRunning && i.state != StateCrashed {
		i.mu.Unlock()
		return nil
	}
	handle, stopFn := i.handle, i.stopFn
	i.state = StateStopping
	i.mu.Unlock()

	err := stopWithEscalation(ctx, handle, 30*time.Second, 60*time.Second)
	if stopFn != nil {
		stopFn()
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	if err != nil {
		i.state = StateCrashed
		// handle intentionally NOT cleared: stop was never confirmed, so
		// Start() must refuse until a future Stop() succeeds.
		return err
	}
	i.state = StateStopped
	i.ip = ""
	i.handle = nil
	i.stopFn = nil
	if m.dns != nil {
		m.dns.Remove(name)
	}
	return nil
}

func (m *Manager) StopAll(ctx context.Context) {
	m.mu.Lock()
	names := make([]string, 0, len(m.instances))
	for n := range m.instances {
		names = append(names, n)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func(n string) { defer wg.Done(); _ = m.Stop(ctx, n) }(n)
	}
	wg.Wait()
}

func (m *Manager) SetIP(name, ip string) {
	i := m.inst(name)
	i.mu.Lock()
	i.ip = ip
	i.mu.Unlock()
}

func (m *Manager) Info(name string) Info {
	i := m.inst(name)
	i.mu.Lock()
	defer i.mu.Unlock()
	// StateCrashed with a non-nil handle means a stop was never confirmed
	// (P9 zombie) — the VM may still be alive; callers (e.g. DELETE) must
	// treat it like Running, not like a clean crash.
	zombie := i.state == StateCrashed && i.handle != nil
	return Info{Name: name, State: i.state, IP: i.ip, Zombie: zombie}
}

func (m *Manager) List() []Info {
	machines, _ := m.reg.List()
	out := make([]Info, 0, len(machines))
	for _, mc := range machines {
		out = append(out, m.Info(mc.Name))
	}
	return out
}
