// Package api exposes umbrad's JSON API over a unix socket.
package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ForceAI-KW/umbra/internal/paths"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/snapshot"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

type Lifecycle interface {
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Info(name string) vm.Info
	List() []vm.Info
}

type Provisioner func(ctx context.Context, m *registry.Machine) error

// ForwardView is the API's own port-forward representation, kept separate
// from netstack.ForwardView so this package never imports internal/netstack
// (and its tests can fake Forwarder without a real gvisor-tap-vsock stack).
type ForwardView struct {
	Local    string `json:"local"`
	Remote   string `json:"remote"`
	Protocol string `json:"protocol"`
}

// Forwarder is the seam over the shared netstack for host<->guest port
// forwarding. Satisfied by *netstack.Stack via a small adapter in umbrad
// (its Forwards() returns []netstack.ForwardView, not []api.ForwardView).
type Forwarder interface {
	Expose(protocol, local, remote string) error
	Unexpose(protocol, local string) error
	Forwards() ([]ForwardView, error)
}

// Docker is the seam over the reserved docker-role machine's
// install/start/stop/status/uninstall lifecycle. Implemented in cmd/umbrad
// (which owns the dockerbridge.Bridge and the docker CLI context
// registration) so this package stays docker-unaware beyond routing —
// vm.Manager and internal/api never import internal/dockerbridge or
// internal/dockerctx.
type Docker interface {
	Install(ctx context.Context) (MachineView, error)
	Start(ctx context.Context) (MachineView, error)
	Stop(ctx context.Context) error
	Status(ctx context.Context) DockerStatus
	Uninstall(ctx context.Context) error
}

// DockerStatus is GET /v1/docker/status's response shape.
type DockerStatus struct {
	Installed      bool   `json:"installed"`
	Running        bool   `json:"running"`
	IP             string `json:"ip,omitempty"`
	Socket         string `json:"socket,omitempty"`
	ContextCurrent bool   `json:"context_current"`
}

type Server struct {
	reg     *registry.Registry
	lc      Lifecycle
	prov    Provisioner
	ready   func(ctx context.Context, m *registry.Machine) (string, error)
	fwd     Forwarder
	docker  Docker
	rosetta func() string
}

// NewServer's rosetta param reports host Rosetta-for-Linux availability as
// "installed" / "notInstalled" / "notSupported" (vm.RosettaAvailability in
// production; a stub in tests) — live-read on every GET /v1/rosetta call,
// never cached, so callers see the current state (PITFALLS P5).
func NewServer(reg *registry.Registry, lc Lifecycle, prov Provisioner, ready func(ctx context.Context, m *registry.Machine) (string, error), fwd Forwarder, docker Docker, rosetta func() string) *Server {
	return &Server{reg: reg, lc: lc, prov: prov, ready: ready, fwd: fwd, docker: docker, rosetta: rosetta}
}

type MachineView struct {
	registry.Machine
	State   vm.State `json:"state"`
	IP      string   `json:"ip,omitempty"`
	SSHPort int      `json:"ssh_port,omitempty"`
	Zombie  bool     `json:"zombie,omitempty"`
}

type CreateRequest struct {
	Name      string `json:"name"`
	CPUs      uint   `json:"cpus"`
	MemoryMiB uint64 `json:"memory_mib"`
	DiskGiB   uint64 `json:"disk_gib"`
	Image     string `json:"image"`
	Autostart bool   `json:"autostart"`
	Role      string `json:"role,omitempty"`
}

// UpdateRequest mutates machine config. Pointer fields distinguish
// "not provided" from zero values. cpu/memory/disk require the machine
// stopped; disk only grows (the guest filesystem must then be grown
// inside the guest: sudo growpart /dev/vda 1 && sudo resize2fs /dev/vda1).
type UpdateRequest struct {
	CPUs      *uint   `json:"cpus"`
	MemoryMiB *uint64 `json:"memory_mib"`
	DiskGiB   *uint64 `json:"disk_gib"`
	Autostart *bool   `json:"autostart"`
}

func validPort(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("port %d out of range (1-65535)", p)
	}
	return nil
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func randomMAC() string {
	b := make([]byte, 6)
	rand.Read(b)
	b[0] = (b[0] | 0x02) &^ 0x01 // locally administered, unicast
	parts := make([]string, 6)
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02x", x)
	}
	return strings.Join(parts, ":")
}

func hostBuild() string {
	out, err := exec.Command("/usr/bin/sw_vers", "-buildVersion").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// snapDir is the on-disk snapshots directory for a machine, shared by the
// snapshots/list/restore handlers so the layout is defined in one place.
func (s *Server) snapDir(name string) string {
	return filepath.Join(s.reg.Dir(name), "snapshots")
}

func (s *Server) view(m *registry.Machine) MachineView {
	info := s.lc.Info(m.Name)
	return MachineView{Machine: *m, State: info.State, IP: info.IP, SSHPort: info.SSHPort, Zombie: info.Zombie}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	mux.HandleFunc("GET /v1/machines", func(w http.ResponseWriter, r *http.Request) {
		machines, err := s.reg.List()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		out := make([]MachineView, 0, len(machines))
		for _, m := range machines {
			if m.Role == registry.ReservedDockerName { // only the reserved docker VM is hidden; ci-runner machines are normal, visible machines
				continue
			}
			out = append(out, s.view(m))
		}
		writeJSON(w, 200, out)
	})

	mux.HandleFunc("POST /v1/machines", func(w http.ResponseWriter, r *http.Request) {
		var req CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err)
			return
		}
		if !registry.ValidName(req.Name) {
			writeErr(w, 400, fmt.Errorf("invalid machine name %q", req.Name))
			return
		}
		if registry.IsReserved(req.Name) {
			writeErr(w, 400, fmt.Errorf("%q is reserved — use 'umbra docker install'", req.Name))
			return
		}
		if req.Role != "" && req.Role != registry.RoleCIRunner {
			writeErr(w, 400, errors.New("invalid role (only 'ci-runner' allowed)"))
			return
		}
		if _, err := s.reg.Load(req.Name); err == nil {
			writeErr(w, 409, fmt.Errorf("machine %q already exists", req.Name))
			return
		}
		if req.CPUs == 0 {
			req.CPUs = 4
		}
		if req.MemoryMiB == 0 {
			req.MemoryMiB = 8192
		}
		if req.DiskGiB == 0 {
			req.DiskGiB = 60
		}
		if req.Image == "" {
			req.Image = "ubuntu:noble"
		}
		m := &registry.Machine{Name: req.Name, CPUs: req.CPUs, MemoryMiB: req.MemoryMiB,
			DiskGiB: req.DiskGiB, Image: req.Image, MAC: randomMAC(), Role: req.Role,
			Autostart: req.Autostart, HostBuild: hostBuild(), CreatedAt: time.Now().UTC()}
		if err := s.reg.Save(m); err != nil {
			writeErr(w, 500, err)
			return
		}
		if err := s.prov(r.Context(), m); err != nil {
			_ = s.reg.Delete(m.Name) // don't leave half-provisioned machines
			writeErr(w, 500, fmt.Errorf("provision: %w", err))
			return
		}
		writeJSON(w, 201, s.view(m))
	})

	mux.HandleFunc("GET /v1/machines/{name}", func(w http.ResponseWriter, r *http.Request) {
		m, err := s.reg.Load(r.PathValue("name"))
		if err != nil {
			writeErr(w, 404, err)
			return
		}
		writeJSON(w, 200, s.view(m))
	})

	mux.HandleFunc("POST /v1/machines/{name}/start", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		m, err := s.reg.Load(name)
		if err != nil {
			writeErr(w, 404, err)
			return
		}
		if err := s.lc.Start(r.Context(), name); err != nil {
			writeErr(w, 500, err)
			return
		}
		if _, err := s.ready(r.Context(), m); err != nil {
			writeErr(w, 500, err) // stage-named error from readiness (P6)
			return
		}
		writeJSON(w, 200, s.lc.Info(name))
	})

	mux.HandleFunc("POST /v1/machines/{name}/stop", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if _, err := s.reg.Load(name); err != nil {
			writeErr(w, 404, err)
			return
		}
		if err := s.lc.Stop(r.Context(), name); err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, s.lc.Info(name))
	})

	mux.HandleFunc("DELETE /v1/machines/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if registry.IsReserved(name) {
			writeErr(w, 400, fmt.Errorf("%q is managed by docker — use 'umbra docker uninstall'", name))
			return
		}
		if _, err := s.reg.Load(name); err != nil {
			writeErr(w, 404, err)
			return
		}
		info := s.lc.Info(name)
		if info.State == vm.StateRunning {
			writeErr(w, 409, fmt.Errorf("machine %q is running; stop it first", name))
			return
		}
		if info.Zombie {
			writeErr(w, 409, fmt.Errorf("machine %q crashed with an unconfirmed stop; run stop again before delete", name))
			return
		}
		if err := s.reg.Delete(name); err != nil {
			writeErr(w, 500, err)
			return
		}
		w.WriteHeader(204)
	})

	mux.HandleFunc("PATCH /v1/machines/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if registry.IsReserved(name) {
			writeErr(w, 400, fmt.Errorf("%q is managed by docker — use 'umbra docker' commands", name))
			return
		}
		m, err := s.reg.Load(name)
		if err != nil {
			writeErr(w, 404, err)
			return
		}
		var req UpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err)
			return
		}
		if req.CPUs != nil && *req.CPUs < 1 {
			writeErr(w, 400, errors.New("cpus must be >= 1"))
			return
		}
		if req.MemoryMiB != nil && *req.MemoryMiB < 128 {
			writeErr(w, 400, errors.New("memory_mib must be >= 128"))
			return
		}
		info := s.lc.Info(name)
		resize := req.CPUs != nil || req.MemoryMiB != nil || req.DiskGiB != nil
		if resize && (info.State == vm.StateRunning || info.Zombie) {
			writeErr(w, 409, fmt.Errorf("machine %q must be stopped to change cpu/memory/disk", name))
			return
		}
		if req.DiskGiB != nil && *req.DiskGiB < m.DiskGiB {
			writeErr(w, 400, fmt.Errorf("disk can only grow (current %d GiB)", m.DiskGiB))
			return
		}
		if req.CPUs != nil {
			m.CPUs = *req.CPUs
		}
		if req.MemoryMiB != nil {
			m.MemoryMiB = *req.MemoryMiB
		}
		if req.DiskGiB != nil && *req.DiskGiB > m.DiskGiB {
			img := filepath.Join(s.reg.Dir(name), "disk.img")
			if err := os.Truncate(img, int64(*req.DiskGiB)<<30); err != nil {
				writeErr(w, 500, err)
				return
			}
			// If Save below fails after this truncate succeeds, disk.img on
			// disk is left larger than the persisted DiskGiB. That's
			// harmless: DiskGiB is only read at create/clone time to size a
			// fresh disk.img, never to validate the existing file's actual
			// size, so a retried grow just re-truncates to the same target
			// size — idempotent.
			m.DiskGiB = *req.DiskGiB
		}
		if req.Autostart != nil {
			m.Autostart = *req.Autostart
		}
		if err := s.reg.Save(m); err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, s.view(m))
	})

	mux.HandleFunc("POST /v1/machines/{name}/forwards", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		m, err := s.reg.Load(name)
		if err != nil {
			writeErr(w, 404, err)
			return
		}
		if info := s.lc.Info(name); info.State != vm.StateRunning {
			writeErr(w, 409, fmt.Errorf("machine %q is not running", name))
			return
		}
		var req struct {
			LocalPort int    `json:"local_port"`
			GuestPort int    `json:"guest_port"`
			Protocol  string `json:"protocol"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err)
			return
		}
		proto := req.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" {
			writeErr(w, 400, fmt.Errorf("invalid protocol %q, want \"tcp\" or \"udp\"", proto))
			return
		}
		if err := validPort(req.LocalPort); err != nil {
			writeErr(w, 400, fmt.Errorf("local_port: %w", err))
			return
		}
		if err := validPort(req.GuestPort); err != nil {
			writeErr(w, 400, fmt.Errorf("guest_port: %w", err))
			return
		}
		if m.IP == "" {
			writeErr(w, 500, fmt.Errorf("machine %q has no IP assigned", name))
			return
		}
		local := fmt.Sprintf("127.0.0.1:%d", req.LocalPort)
		remote := fmt.Sprintf("%s:%d", m.IP, req.GuestPort)
		if err := s.fwd.Expose(proto, local, remote); err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 201, ForwardView{Local: local, Remote: remote, Protocol: proto})
	})

	mux.HandleFunc("GET /v1/machines/{name}/forwards", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		m, err := s.reg.Load(name)
		if err != nil {
			writeErr(w, 404, err)
			return
		}
		all, err := s.fwd.Forwards()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		out := make([]ForwardView, 0)
		prefix := m.IP + ":"
		for _, f := range all {
			if m.IP != "" && strings.HasPrefix(f.Remote, prefix) {
				out = append(out, f)
			}
		}
		writeJSON(w, 200, out)
	})

	mux.HandleFunc("DELETE /v1/machines/{name}/forwards/{local_port}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		m, err := s.reg.Load(name)
		if err != nil {
			writeErr(w, 404, err)
			return
		}
		localPort, err := strconv.Atoi(r.PathValue("local_port"))
		if err != nil {
			writeErr(w, 400, fmt.Errorf("invalid local_port %q", r.PathValue("local_port")))
			return
		}
		if err := validPort(localPort); err != nil {
			writeErr(w, 400, fmt.Errorf("local_port: %w", err))
			return
		}
		proto := r.URL.Query().Get("protocol")
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" {
			writeErr(w, 400, fmt.Errorf("invalid protocol %q, want \"tcp\" or \"udp\"", proto))
			return
		}
		// Ownership: only remove a forward that actually targets THIS
		// machine's guest IP, so `rm` on one machine can't tear down
		// another's (e.g. its auto-SSH forward) by local port.
		local := fmt.Sprintf("127.0.0.1:%d", localPort)
		owned := false
		if all, err := s.fwd.Forwards(); err == nil && m.IP != "" {
			for _, f := range all {
				if f.Local == local && strings.HasPrefix(f.Remote, m.IP+":") {
					owned = true
					break
				}
			}
		}
		if !owned {
			writeErr(w, 404, fmt.Errorf("no %s forward on port %d for machine %q", proto, localPort, name))
			return
		}
		if err := s.fwd.Unexpose(proto, local); err != nil {
			writeErr(w, 500, err)
			return
		}
		w.WriteHeader(204)
	})

	mux.HandleFunc("POST /v1/docker/install", func(w http.ResponseWriter, r *http.Request) {
		mv, err := s.docker.Install(r.Context())
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 201, mv)
	})

	mux.HandleFunc("POST /v1/docker/start", func(w http.ResponseWriter, r *http.Request) {
		mv, err := s.docker.Start(r.Context())
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, mv)
	})

	mux.HandleFunc("POST /v1/docker/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := s.docker.Stop(r.Context()); err != nil {
			writeErr(w, 500, err)
			return
		}
		w.WriteHeader(204)
	})

	mux.HandleFunc("GET /v1/docker/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.docker.Status(r.Context()))
	})

	mux.HandleFunc("POST /v1/docker/uninstall", func(w http.ResponseWriter, r *http.Request) {
		if err := s.docker.Uninstall(r.Context()); err != nil {
			writeErr(w, 500, err)
			return
		}
		w.WriteHeader(204)
	})

	mux.HandleFunc("GET /v1/rosetta", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"available": s.rosetta()})
	})

	mux.HandleFunc("POST /v1/machines/{name}/snapshots", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if registry.IsReserved(name) {
			writeErr(w, 400, fmt.Errorf("%q is managed by docker — use 'umbra docker' commands", name))
			return
		}
		if _, err := s.reg.Load(name); err != nil {
			writeErr(w, 404, err)
			return
		}
		if info := s.lc.Info(name); info.State == vm.StateRunning || info.Zombie {
			writeErr(w, 409, fmt.Errorf("machine %q must be stopped to snapshot", name))
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			writeErr(w, 400, fmt.Errorf("snapshot name required"))
			return
		}
		if !registry.ValidName(req.Name) {
			writeErr(w, 400, fmt.Errorf("invalid snapshot name"))
			return
		}
		if err := snapshot.Take(s.reg.Dir(name), s.snapDir(name), req.Name); err != nil {
			writeErr(w, 500, err)
			return
		}
		w.WriteHeader(201)
	})

	mux.HandleFunc("GET /v1/machines/{name}/snapshots", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if registry.IsReserved(name) {
			writeErr(w, 400, fmt.Errorf("%q is managed by docker — use 'umbra docker' commands", name))
			return
		}
		if _, err := s.reg.Load(name); err != nil {
			writeErr(w, 404, err)
			return
		}
		infos, err := snapshot.List(s.snapDir(name))
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, infos)
	})

	mux.HandleFunc("POST /v1/machines/{name}/restore", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if registry.IsReserved(name) {
			writeErr(w, 400, fmt.Errorf("%q is managed by docker — use 'umbra docker' commands", name))
			return
		}
		if _, err := s.reg.Load(name); err != nil {
			writeErr(w, 404, err)
			return
		}
		if info := s.lc.Info(name); info.State == vm.StateRunning || info.Zombie {
			writeErr(w, 409, fmt.Errorf("machine %q must be stopped to restore", name))
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			writeErr(w, 400, fmt.Errorf("snapshot name required"))
			return
		}
		if !registry.ValidName(req.Name) {
			writeErr(w, 400, fmt.Errorf("invalid snapshot name"))
			return
		}
		if err := snapshot.Restore(s.reg.Dir(name), s.snapDir(name), req.Name); err != nil {
			writeErr(w, 500, err)
			return
		}
		w.WriteHeader(204)
	})

	mux.HandleFunc("POST /v1/machines/import", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name       string `json:"name"`
			StagingDir string `json:"staging_dir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err)
			return
		}
		if !registry.ValidName(req.Name) || registry.IsReserved(req.Name) {
			writeErr(w, 400, fmt.Errorf("invalid machine name %q", req.Name))
			return
		}
		if _, err := s.reg.Load(req.Name); err == nil {
			writeErr(w, 409, fmt.Errorf("machine %q already exists", req.Name))
			return
		}
		// req.StagingDir is caller-controlled (this is a JSON API reachable
		// by any direct-socket caller, not just the CLI's own tar extraction
		// path), so it must be confined under paths.Run() before it's used
		// as a os.ReadFile/os.Rename source — otherwise a crafted request
		// could read or move an arbitrary directory on the host. This is
		// the one place server.go reaches into internal/paths: it's
		// validating a request boundary, not resolving a machine file (that
		// still always goes through s.reg.Dir, per the rest of this file).
		clean := filepath.Clean(req.StagingDir)
		if !strings.HasPrefix(clean, paths.Run()+string(os.PathSeparator)) {
			writeErr(w, 400, fmt.Errorf("staging_dir must be under the run dir"))
			return
		}
		// The CLI already extracted the tarball into StagingDir via
		// export.Read (which enforces the config.json/disk.img allowlist,
		// blocking path traversal) — read the config straight from there
		// rather than re-parsing the tarball a second time.
		b, err := os.ReadFile(filepath.Join(req.StagingDir, "config.json"))
		if err != nil {
			writeErr(w, 400, fmt.Errorf("staging dir missing config.json: %w", err))
			return
		}
		var m registry.Machine
		if err := json.Unmarshal(b, &m); err != nil {
			writeErr(w, 400, err)
			return
		}
		// A direct-socket caller could hand us a staging dir with a
		// config.json but no disk.img; without this the rename below would
		// still "succeed" and register an unbootable machine.
		if _, err := os.Stat(filepath.Join(req.StagingDir, "disk.img")); err != nil {
			writeErr(w, 400, fmt.Errorf("staging dir missing disk.img: %w", err))
			return
		}
		m.Name = req.Name
		m.MAC = randomMAC() // never reuse the source host's MAC — same-subnet collision risk
		m.IP = ""           // source host's lease is meaningless here; reallocated on first start
		m.HostBuild = hostBuild()
		m.CreatedAt = time.Now().UTC()
		// os.Rename is same-volume (both StagingDir and s.reg.Dir live under
		// paths.Root()) so taking ownership of the extracted dir is atomic.
		if err := os.Rename(req.StagingDir, s.reg.Dir(req.Name)); err != nil {
			writeErr(w, 500, err)
			return
		}
		if err := s.reg.Save(&m); err != nil {
			// The rename already succeeded, so the registry dir now holds
			// the source host's original config.json (pre-name/MAC-rewrite)
			// with no machine registered to own it: a retry would 409 on a
			// name that doesn't actually work. Best-effort clean it up so a
			// failed import leaves nothing half-registered behind.
			_ = os.RemoveAll(s.reg.Dir(req.Name))
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 201, s.view(&m))
	})

	return mux
}
