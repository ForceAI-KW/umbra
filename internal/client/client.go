// Package client is the CLI/GUI-side client for umbrad's unix-socket API.
// Dial errors are retried with backoff (P10 — first-connection races daemon
// socket registration); HTTP-level errors are never retried.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/snapshot"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

type Client struct {
	http *http.Client
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

// UpdateRequest mirrors api.UpdateRequest; kept as its own type here so the
// CLI doesn't import internal/api. Pointer fields distinguish "not provided"
// from zero values.
type UpdateRequest struct {
	CPUs      *uint   `json:"cpus"`
	MemoryMiB *uint64 `json:"memory_mib"`
	DiskGiB   *uint64 `json:"disk_gib"`
	Autostart *bool   `json:"autostart"`
}

// ForwardView mirrors api.ForwardView; kept as its own type here so the CLI
// doesn't import internal/api.
type ForwardView struct {
	Local    string `json:"local"`
	Remote   string `json:"remote"`
	Protocol string `json:"protocol"`
}

type forwardRequest struct {
	LocalPort int    `json:"local_port"`
	GuestPort int    `json:"guest_port"`
	Protocol  string `json:"protocol"`
}

// DockerStatus mirrors api.DockerStatus; kept as its own type here so the
// CLI doesn't import internal/api.
type DockerStatus struct {
	Installed      bool   `json:"installed"`
	Running        bool   `json:"running"`
	IP             string `json:"ip,omitempty"`
	Socket         string `json:"socket,omitempty"`
	ContextCurrent bool   `json:"context_current"`
}

func New(socketPath string) *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
	}}
}

var backoffs = []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 2 * time.Second}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	var lastErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, "http://umbra"+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Op == "dial" && attempt < len(backoffs) { // dial error → retry
				lastErr = err
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoffs[attempt]):
				}
				continue
			}
			return fmt.Errorf("umbrad unreachable (is the daemon running? `make run-daemon`): %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			var e struct {
				Error string `json:"error"`
			}
			b, _ := io.ReadAll(resp.Body)
			if json.Unmarshal(b, &e) == nil && e.Error != "" {
				return errors.New(e.Error)
			}
			return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
		}
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	return fmt.Errorf("umbrad unreachable after %d attempts: %w", len(backoffs)+1, lastErr)
}

func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/ping", nil, nil)
}
func (c *Client) CreateMachine(ctx context.Context, req CreateRequest) (*MachineView, error) {
	var mv MachineView
	return &mv, c.do(ctx, http.MethodPost, "/v1/machines", req, &mv)
}
func (c *Client) StartMachine(ctx context.Context, name string) (*vm.Info, error) {
	var info vm.Info
	return &info, c.do(ctx, http.MethodPost, "/v1/machines/"+name+"/start", nil, &info)
}
func (c *Client) StopMachine(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/v1/machines/"+name+"/stop", nil, nil)
}
func (c *Client) DeleteMachine(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/machines/"+name, nil, nil)
}
func (c *Client) ListMachines(ctx context.Context) ([]MachineView, error) {
	var out []MachineView
	return out, c.do(ctx, http.MethodGet, "/v1/machines", nil, &out)
}
func (c *Client) GetMachine(ctx context.Context, name string) (*MachineView, error) {
	var mv MachineView
	return &mv, c.do(ctx, http.MethodGet, "/v1/machines/"+name, nil, &mv)
}
func (c *Client) UpdateMachine(ctx context.Context, name string, req UpdateRequest) (*MachineView, error) {
	var mv MachineView
	return &mv, c.do(ctx, http.MethodPatch, "/v1/machines/"+name, req, &mv)
}
func (c *Client) AddForward(ctx context.Context, name string, localPort, guestPort int, protocol string) (*ForwardView, error) {
	var fv ForwardView
	req := forwardRequest{LocalPort: localPort, GuestPort: guestPort, Protocol: protocol}
	return &fv, c.do(ctx, http.MethodPost, "/v1/machines/"+name+"/forwards", req, &fv)
}
func (c *Client) ListForwards(ctx context.Context, name string) ([]ForwardView, error) {
	var out []ForwardView
	return out, c.do(ctx, http.MethodGet, "/v1/machines/"+name+"/forwards", nil, &out)
}
func (c *Client) RemoveForward(ctx context.Context, name string, localPort int, protocol string) error {
	path := fmt.Sprintf("/v1/machines/%s/forwards/%d", name, localPort)
	if protocol == "udp" {
		path += "?protocol=udp"
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) DockerInstall(ctx context.Context) (*MachineView, error) {
	var mv MachineView
	return &mv, c.do(ctx, http.MethodPost, "/v1/docker/install", nil, &mv)
}
func (c *Client) DockerStart(ctx context.Context) (*MachineView, error) {
	var mv MachineView
	return &mv, c.do(ctx, http.MethodPost, "/v1/docker/start", nil, &mv)
}
func (c *Client) DockerStop(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/docker/stop", nil, nil)
}
func (c *Client) DockerStatus(ctx context.Context) (*DockerStatus, error) {
	var ds DockerStatus
	return &ds, c.do(ctx, http.MethodGet, "/v1/docker/status", nil, &ds)
}
func (c *Client) DockerUninstall(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/docker/uninstall", nil, nil)
}

func (c *Client) TakeSnapshot(ctx context.Context, machine, snap string) error {
	return c.do(ctx, http.MethodPost, "/v1/machines/"+machine+"/snapshots", map[string]string{"name": snap}, nil)
}
func (c *Client) ListSnapshots(ctx context.Context, machine string) ([]snapshot.Info, error) {
	var out []snapshot.Info
	err := c.do(ctx, http.MethodGet, "/v1/machines/"+machine+"/snapshots", nil, &out)
	return out, err
}
func (c *Client) RestoreSnapshot(ctx context.Context, machine, snap string) error {
	return c.do(ctx, http.MethodPost, "/v1/machines/"+machine+"/restore", map[string]string{"name": snap}, nil)
}

// ImportMachine hands a locally-extracted machine directory (staged by the
// CLI via internal/export.Read) over to the daemon, which takes ownership:
// validates name, mints a fresh MAC, and moves the dir into its registry.
func (c *Client) ImportMachine(ctx context.Context, name, stagingDir string) (*MachineView, error) {
	var mv MachineView
	req := struct {
		Name       string `json:"name"`
		StagingDir string `json:"staging_dir"`
	}{Name: name, StagingDir: stagingDir}
	return &mv, c.do(ctx, http.MethodPost, "/v1/machines/import", req, &mv)
}

// Rosetta returns the host's Rosetta-for-Linux availability:
// "installed" / "notInstalled" / "notSupported".
func (c *Client) Rosetta(ctx context.Context) (string, error) {
	var out struct {
		Available string `json:"available"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/rosetta", nil, &out); err != nil {
		return "", err
	}
	return out.Available, nil
}
