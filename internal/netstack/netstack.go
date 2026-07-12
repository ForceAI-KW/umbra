// Package netstack embeds gvisor-tap-vsock as an in-process userspace network
// for Umbra guests: VPN-safe NAT, in-process host dialing, runtime port
// forwarding. See docs/research/gvisor-tap-vsock-api.md.
package netstack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
)

const (
	Subnet    = "192.168.127.0/24"
	Gateway   = "192.168.127.1"
	FirstHost = 10
)

type Stack struct {
	vn  *virtualnetwork.VirtualNetwork
	mux http.Handler
}

func New() (*Stack, error) {
	cfg := &types.Configuration{
		MTU:               1500,
		Subnet:            Subnet,
		GatewayIP:         Gateway,
		GatewayMacAddress: "5a:94:ef:e4:0c:dd",
		Protocol:          types.VfkitProtocol,
		DNSSearchDomains:  []string{"umbra.local"},
	}
	vn, err := virtualnetwork.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Stack{vn: vn, mux: vn.ServicesMux()}, nil
}

func (s *Stack) VN() *virtualnetwork.VirtualNetwork { return s.vn }

func (s *Stack) DialContextTCP(ctx context.Context, addr string) (net.Conn, error) {
	return s.vn.DialContextTCP(ctx, addr)
}

func (s *Stack) call(path string, body any, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return fmt.Errorf("%s: %s: %s", path, rec.Result().Status, rec.Body.String())
	}
	if out != nil {
		return json.Unmarshal(rec.Body.Bytes(), out)
	}
	return nil
}

func (s *Stack) Expose(protocol, local, remote string) error {
	return s.call("/services/forwarder/expose", types.ExposeRequest{
		Protocol: types.TransportProtocol(protocol), Local: local, Remote: remote,
	}, nil)
}

func (s *Stack) Unexpose(protocol, local string) error {
	return s.call("/services/forwarder/unexpose", types.UnexposeRequest{
		Protocol: types.TransportProtocol(protocol), Local: local,
	}, nil)
}

type ForwardView struct {
	Local    string `json:"local"`
	Remote   string `json:"remote"`
	Protocol string `json:"protocol"`
}

func (s *Stack) Forwards() ([]ForwardView, error) {
	req := httptest.NewRequest(http.MethodGet, "/services/forwarder/all", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("list forwards: %s", rec.Result().Status)
	}
	var out []ForwardView
	return out, json.Unmarshal(rec.Body.Bytes(), &out)
}

// Shutdown is a placeholder — virtualnetwork.New starts background goroutines
// but exposes no Close(); the stack lives for the daemon's lifetime and dies
// with the process. Kept for symmetry and future cleanup.
func (s *Stack) Shutdown() {}
