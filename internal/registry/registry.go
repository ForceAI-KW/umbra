// Package registry persists machine configurations as JSON under the machines dir.
package registry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var ErrNotFound = errors.New("machine not found")
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

const ReservedDockerName = "docker"

// RoleCIRunner marks a machine as a GitHub Actions self-hosted CI runner.
// Its cloud-init profile installs plain docker (no tcp://0.0.0.0:2375
// exposure, no iptables 2375 rule) — see internal/cloudinit/seed.go's
// ciRunnerRuncmdLines and docs/research/launchd-and-ci-cutover.md §4.
const RoleCIRunner = "ci-runner"

func ValidName(name string) bool { return nameRe.MatchString(name) }

func IsReserved(name string) bool { return name == ReservedDockerName }

type Machine struct {
	Name      string    `json:"name"`
	CPUs      uint      `json:"cpus"`
	MemoryMiB uint64    `json:"memory_mib"`
	DiskGiB   uint64    `json:"disk_gib"`
	Image     string    `json:"image"`
	MAC       string    `json:"mac"`
	IP        string    `json:"ip,omitempty"`
	Role      string    `json:"role,omitempty"`
	Autostart bool      `json:"autostart"`
	HostBuild string    `json:"host_build"`
	CreatedAt time.Time `json:"created_at"`
}

type Registry struct{ dir string }

func New(dir string) *Registry { return &Registry{dir: dir} }

func (r *Registry) configPath(name string) string {
	return filepath.Join(r.dir, name, "config.json")
}

// Dir returns the per-machine directory (holding config.json, disk.img,
// etc.) under this registry's root. Exported so callers that need to touch
// machine-owned files outside of Save/Load (e.g. resizing disk.img) resolve
// paths through the same registry instance the server was constructed with,
// instead of the global paths.MachineDir — which would silently diverge
// from a test registry rooted at t.TempDir() and risk touching real files
// under ~/.umbra.
func (r *Registry) Dir(name string) string { return filepath.Join(r.dir, name) }

func (r *Registry) Save(m *Machine) error {
	if !ValidName(m.Name) {
		return errors.New("invalid machine name: must match ^[a-z0-9][a-z0-9-]{0,31}$")
	}
	dir := filepath.Join(r.dir, m.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.configPath(m.Name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.configPath(m.Name))
}

func (r *Registry) Load(name string) (*Machine, error) {
	if !ValidName(name) {
		return nil, ErrNotFound // also blocks path traversal via crafted names
	}
	b, err := os.ReadFile(r.configPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var m Machine
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *Registry) List() ([]*Machine, error) {
	entries, err := os.ReadDir(r.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Machine
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := r.Load(e.Name())
		if err != nil {
			continue // dir without config.json is not a machine
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *Registry) Delete(name string) error {
	if _, err := r.Load(name); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(r.dir, name))
}

func (r *Registry) UsedIPs() ([]string, error) {
	machines, err := r.List()
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, m := range machines {
		if m.IP != "" {
			ips = append(ips, m.IP)
		}
	}
	return ips, nil
}
