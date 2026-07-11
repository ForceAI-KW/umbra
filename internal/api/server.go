// Package api exposes umbrad's JSON API over a unix socket.
package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

type Lifecycle interface {
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Info(name string) vm.Info
	List() []vm.Info
}

type Provisioner func(ctx context.Context, m *registry.Machine) error

type Server struct {
	reg   *registry.Registry
	lc    Lifecycle
	prov  Provisioner
	ready func(ctx context.Context, m *registry.Machine) (string, error)
}

func NewServer(reg *registry.Registry, lc Lifecycle, prov Provisioner, ready func(ctx context.Context, m *registry.Machine) (string, error)) *Server {
	return &Server{reg: reg, lc: lc, prov: prov, ready: ready}
}

type MachineView struct {
	registry.Machine
	State  vm.State `json:"state"`
	IP     string   `json:"ip,omitempty"`
	Zombie bool     `json:"zombie,omitempty"`
}

type CreateRequest struct {
	Name      string `json:"name"`
	CPUs      uint   `json:"cpus"`
	MemoryMiB uint64 `json:"memory_mib"`
	DiskGiB   uint64 `json:"disk_gib"`
	Image     string `json:"image"`
	Autostart bool   `json:"autostart"`
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

func (s *Server) view(m *registry.Machine) MachineView {
	info := s.lc.Info(m.Name)
	return MachineView{Machine: *m, State: info.State, IP: info.IP, Zombie: info.Zombie}
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
			DiskGiB: req.DiskGiB, Image: req.Image, MAC: randomMAC(),
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

	return mux
}
