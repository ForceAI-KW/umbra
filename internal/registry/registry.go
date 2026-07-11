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

func ValidName(name string) bool { return nameRe.MatchString(name) }

type Machine struct {
	Name      string    `json:"name"`
	CPUs      uint      `json:"cpus"`
	MemoryMiB uint64    `json:"memory_mib"`
	DiskGiB   uint64    `json:"disk_gib"`
	Image     string    `json:"image"`
	MAC       string    `json:"mac"`
	Autostart bool      `json:"autostart"`
	HostBuild string    `json:"host_build"`
	CreatedAt time.Time `json:"created_at"`
}

type Registry struct{ dir string }

func New(dir string) *Registry { return &Registry{dir: dir} }

func (r *Registry) configPath(name string) string {
	return filepath.Join(r.dir, name, "config.json")
}

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
