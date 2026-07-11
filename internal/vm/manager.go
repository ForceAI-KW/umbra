package vm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ForceAI-KW/umbra/internal/registry"
)

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateCrashed  State = "crashed"
)

type Info struct {
	Name  string `json:"name"`
	State State  `json:"state"`
	IP    string `json:"ip,omitempty"`
}

type instance struct {
	mu     sync.Mutex
	state  State
	ip     string
	handle vzHandle
	stopFn func() // releases run loop resources (darwin)
}

type Manager struct {
	reg         *registry.Registry
	machinesDir string
	mu          sync.Mutex
	instances   map[string]*instance
}

func NewManager(reg *registry.Registry, machinesDir string) *Manager {
	return &Manager{reg: reg, machinesDir: machinesDir, instances: map[string]*instance{}}
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

func (m *Manager) Start(ctx context.Context, name string) error {
	cfg, err := m.reg.Load(name)
	if err != nil {
		return err
	}
	i := m.inst(name)
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state == StateRunning || i.state == StateStarting {
		return nil
	}
	i.state = StateStarting
	h, stopFn, err := launchVZ(cfg, m.machinesDir) // darwin-only; guarded inside
	if err != nil {
		i.state = StateCrashed
		return fmt.Errorf("launch %s: %w", name, err)
	}
	i.handle, i.stopFn = h, stopFn
	i.state = StateRunning
	return nil
}

func (m *Manager) Stop(ctx context.Context, name string) error {
	i := m.inst(name)
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state != StateRunning && i.state != StateCrashed {
		return nil
	}
	i.state = StateStopping
	err := stopWithEscalation(ctx, i.handle, 30*time.Second, 60*time.Second)
	if i.stopFn != nil {
		i.stopFn()
	}
	if err != nil {
		i.state = StateCrashed
		return err
	}
	i.state = StateStopped
	i.ip = ""
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
	return Info{Name: name, State: i.state, IP: i.ip}
}

func (m *Manager) List() []Info {
	machines, _ := m.reg.List()
	out := make([]Info, 0, len(machines))
	for _, mc := range machines {
		out = append(out, m.Info(mc.Name))
	}
	return out
}
