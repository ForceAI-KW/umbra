# Umbra M2 — Networking (gvisor-tap-vsock) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Replace M1's kernel NAT with an embedded gvisor-tap-vsock userspace network: VPN-safe connectivity, deterministic per-machine static IPs, `<name>.umbra.local` DNS resolvable from macOS, and runtime host→guest port forwarding — all in-process in `umbrad`, no `gvproxy`/`vfkit` subprocess.

**Architecture:** One long-lived `virtualnetwork.VirtualNetwork` owned by the daemon (subnet `192.168.127.0/24`, gateway `.1`). Each machine gets a daemon-assigned static IPv4 (stored in `registry.Machine.IP`), configured guest-side by cloud-init netplan (no DHCP dependency). Each VM's NIC is an `AF_UNIX`/`SOCK_DGRAM` socketpair: one fd → `vz.NewFileHandleNetworkDeviceAttachment`, the other → `vn.AcceptVfkit` in a per-VM goroutine. Readiness dials the guest via `vn.DialContextTCP` (no host socket). A daemon-owned DNS responder on `127.0.0.1` + `/etc/resolver/umbra.local` resolves machine names on the host; `/etc/hosts` entries pushed into each guest cover guest→guest. Port forwards go through `vn.ServicesMux()` driven in-process. A network supervisor guards the two known gvisor spin pathologies (P3/P11).

**Tech Stack:** Go 1.25, github.com/containers/gvisor-tap-vsock (pin the commit verified in research, ~v0.8.9), Code-Hex/vz/v3 v3.7.1, golang.org/x/sys/unix.

## Global Constraints

- Research cheat-sheet is authoritative for every gvisor-tap-vsock symbol: `docs/research/gvisor-tap-vsock-api.md`. Do not call an API not verified there.
- Pitfalls (docs/PITFALLS-EXTERNAL.md) in scope: **P3** sleep/wake network hang, **P4** DNS gaps, **P7** VPN route staleness, **P11** ENOBUFS/global-write-lock spin. Each mitigation cites its P-number in code.
- Subnet `192.168.127.0/24`, gateway `192.168.127.1`, machine IPs from `.10` upward. IPv4 only (gvisor stack registers no IPv6).
- DNS zone `umbra.local` (daemon-authoritative host-side; add AND remove must work — do NOT rely on gvisor's add-only `/dns/add` for host resolution).
- The M1 NAT attachment (`vz.NewNATNetworkDeviceAttachment` in `config_darwin.go`) is REPLACED by the socketpair attachment. Machines created under M1 have no `IP` field yet — assign on next start (migration handled in Task for registry).
- Static IP means `internal/vmnet` (dhcpd_leases parser) is no longer on the readiness path. Keep the package (still correct, may serve debug) but readiness uses `DialContextTCP`. Flag its disuse; do not delete in M2.
- Every vz call still inside `guarded()` (M1 P1 invariant). Every new network goroutine is shutdown-wired (daemonCtx + WaitGroup, per M1's autostart pattern).
- Build/test discipline identical to M1: `//go:build darwin && arm64` on vz-touching files; unit tests use fakes/loopback, integration+E2E on this Mac only; `gofmt -w` + `go mod tidy` + `make lint` + `go test ./... -count=1` green before every commit; conventional commits with the Co-Authored-By + Claude-Session trailers; specific `git add` paths.
- Post-merge backlog carried from M1 that M2 must honor: single-instance socket guard lands with M4 (not here); zombie force-delete still deferred.

## File Structure

```
internal/
  netstack/                     # NEW — owns the VirtualNetwork
    netstack.go                 # New(cfg) → *Stack; Attach(mac)→(netcfg, ip, cleanup); DialContextTCP; Expose/Unexpose; Shutdown
    netstack_test.go            # loopback/httptest — no vz
    attach_darwin.go            # socketpair → vz.NewFileHandleNetworkDeviceAttachment (build-tagged)
    attach_other.go             # stub returning error off darwin
    supervisor.go               # sleep/wake + health-probe guard (P3/P11) (+_test)
    dns.go                      # daemon-side umbra.local resolver on 127.0.0.1 + /etc/resolver mgmt (+_test)
  ipalloc/ipalloc.go            # subnet IP allocation from the registry set (+_test)
internal/registry/registry.go   # + IP field
internal/cloudinit/seed.go      # netplan static addressing + /etc/hosts (network-config rewrite) (+ test updates)
internal/vm/config_darwin.go    # swap NAT attachment → netstack.Attach
internal/vm/manager.go          # Manager holds *netstack.Stack; Start attaches, Stop detaches; SetIP from allocator
cmd/umbrad/main.go              # construct netstack, wire DNS resolver + /etc/resolver, supervisor, shutdown
cmd/umbra/forward.go            # NEW: umbra forward add/list/rm
docs/PITFALLS-EXTERNAL.md       # mark P3/P4/P7/P11 addressed with file refs
README.md                       # M2 status + networking usage
```

---

### Task 1: `internal/ipalloc` — deterministic subnet allocation

**Files:** Create `internal/ipalloc/ipalloc.go`, `internal/ipalloc/ipalloc_test.go`

**Interfaces:**
- Produces:
```go
package ipalloc
// Allocate returns the lowest free IPv4 in subnet (CIDR) at or above firstHost,
// skipping gateway and any IP in `used`. Deterministic. Errors if exhausted.
func Allocate(subnet, gateway string, firstHost int, used []string) (string, error)
// Validate reports whether ip is inside subnet and not the gateway/network/broadcast.
func Validate(subnet, gateway, ip string) error
```

- [ ] **Step 1: Failing test** — `ipalloc_test.go`:

```go
package ipalloc

import "testing"

func TestAllocateSkipsUsedAndGateway(t *testing.T) {
	ip, err := Allocate("192.168.127.0/24", "192.168.127.1", 10, []string{"192.168.127.10", "192.168.127.11"})
	if err != nil {
		t.Fatal(err)
	}
	if ip != "192.168.127.12" {
		t.Fatalf("got %s, want 192.168.127.12", ip)
	}
}

func TestAllocateFirstFree(t *testing.T) {
	ip, err := Allocate("192.168.127.0/24", "192.168.127.1", 10, nil)
	if err != nil || ip != "192.168.127.10" {
		t.Fatalf("got %s %v", ip, err)
	}
}

func TestValidateRejectsOutOfSubnetAndGateway(t *testing.T) {
	if Validate("192.168.127.0/24", "192.168.127.1", "192.168.127.1") == nil {
		t.Fatal("gateway must be rejected")
	}
	if Validate("192.168.127.0/24", "192.168.127.1", "10.0.0.5") == nil {
		t.Fatal("out-of-subnet must be rejected")
	}
	if err := Validate("192.168.127.0/24", "192.168.127.1", "192.168.127.10"); err != nil {
		t.Fatalf("valid ip rejected: %v", err)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/ipalloc/ -v` — FAIL
- [ ] **Step 3: Implement** `ipalloc.go`:

```go
// Package ipalloc assigns deterministic IPv4 addresses within the Umbra subnet.
package ipalloc

import (
	"fmt"
	"net"
)

func Allocate(subnet, gateway string, firstHost int, used []string) (string, error) {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", err
	}
	usedSet := map[string]bool{gateway: true}
	for _, u := range used {
		usedSet[u] = true
	}
	base := ipnet.IP.To4()
	if base == nil {
		return "", fmt.Errorf("subnet %s is not IPv4", subnet)
	}
	ones, bits := ipnet.Mask.Size()
	max := 1 << (bits - ones)
	for host := firstHost; host < max-1; host++ { // -1 skips broadcast
		ip := make(net.IP, 4)
		copy(ip, base)
		v := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
		v += uint32(host)
		cand := net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String()
		if !usedSet[cand] {
			return cand, nil
		}
	}
	return "", fmt.Errorf("subnet %s exhausted", subnet)
}

func Validate(subnet, gateway, ip string) error {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return err
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("%q is not an IPv4 address", ip)
	}
	if !ipnet.Contains(parsed) {
		return fmt.Errorf("%s is outside subnet %s", ip, subnet)
	}
	if ip == gateway {
		return fmt.Errorf("%s is the gateway", ip)
	}
	network := ipnet.IP.String()
	if ip == network {
		return fmt.Errorf("%s is the network address", ip)
	}
	return nil
}
```

- [ ] **Step 4: Run** — PASS
- [ ] **Step 5: Commit** `feat(ipalloc): deterministic subnet IP allocation`

### Task 2: `registry.Machine.IP` field + allocation-aware save

**Files:** Modify `internal/registry/registry.go`, `internal/registry/registry_test.go`

**Interfaces:**
- Adds `IP string \`json:"ip,omitempty"\`` to `registry.Machine`. Add helper `func (r *Registry) UsedIPs() ([]string, error)` returning every machine's non-empty IP (for the allocator).

- [ ] **Step 1: Failing test** — append to `registry_test.go`:

```go
func TestUsedIPsCollectsAssigned(t *testing.T) {
	r := newTestRegistry(t)
	must := func(m *Machine) {
		if err := r.Save(m); err != nil {
			t.Fatal(err)
		}
	}
	must(&Machine{Name: "a", CPUs: 1, MemoryMiB: 512, DiskGiB: 5, Image: "ubuntu:noble", IP: "192.168.127.10"})
	must(&Machine{Name: "b", CPUs: 1, MemoryMiB: 512, DiskGiB: 5, Image: "ubuntu:noble"}) // no IP yet
	ips, err := r.UsedIPs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || ips[0] != "192.168.127.10" {
		t.Fatalf("got %v", ips)
	}
}
```

- [ ] **Step 2: Run** — FAIL
- [ ] **Step 3: Implement**: add the `IP` field to the struct (after `MAC`), and:

```go
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
```

- [ ] **Step 4: Run** `go test ./internal/registry/ -v` — PASS (existing roundtrip test still green; IP is omitempty)
- [ ] **Step 5: Commit** `feat(registry): Machine.IP field + UsedIPs helper`

### Task 3: `internal/netstack` core — VirtualNetwork lifecycle + dial + forwards

**Files:** Create `internal/netstack/netstack.go`, `internal/netstack/netstack_test.go`

**Interfaces:**
- Produces:
```go
package netstack
const (Subnet = "192.168.127.0/24"; Gateway = "192.168.127.1"; FirstHost = 10)
type Stack struct { /* wraps *virtualnetwork.VirtualNetwork + services mux */ }
func New() (*Stack, error)                                        // builds Configuration, calls virtualnetwork.New
func (s *Stack) DialContextTCP(ctx context.Context, addr string) (net.Conn, error) // → vn.DialContextTCP
func (s *Stack) Expose(protocol, local, remote string) error     // in-process POST /services/forwarder/expose
func (s *Stack) Unexpose(protocol, local string) error           // in-process POST /services/forwarder/unexpose
func (s *Stack) Forwards() ([]ForwardView, error)                // GET /services/forwarder/all
func (s *Stack) VN() *virtualnetwork.VirtualNetwork              // for attach_darwin.go
type ForwardView struct { Local, Remote, Protocol string }
```
- Uses the httptest-driven in-process mux pattern from research §4b/§4c (no real socket). `Expose`/`Unexpose` marshal `types.ExposeRequest`/`types.UnexposeRequest`.

- [ ] **Step 1: Failing test** — `netstack_test.go` (no vz; exercises construction + forward expose/unexpose/list via the real mux, and a DialContextTCP error for an unrouted addr):

```go
package netstack

import (
	"context"
	"testing"
	"time"
)

func TestNewAndForwardRoundtrip(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()

	if err := s.Expose("tcp", "127.0.0.1:12222", "192.168.127.10:22"); err != nil {
		t.Fatalf("expose: %v", err)
	}
	fwds, err := s.Forwards()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fwds {
		if f.Local == "127.0.0.1:12222" && f.Remote == "192.168.127.10:22" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expose not listed: %v", fwds)
	}
	if err := s.Unexpose("tcp", "127.0.0.1:12222"); err != nil {
		t.Fatalf("unexpose: %v", err)
	}
}

func TestDialUnroutedFails(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := s.DialContextTCP(ctx, "192.168.127.200:22"); err == nil {
		t.Fatal("dial to unrouted guest should fail")
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/netstack/ -v` — FAIL (compile: package missing). First `go get github.com/containers/gvisor-tap-vsock@<pinned>` and `go mod tidy`.
- [ ] **Step 3: Implement** `netstack.go`:

```go
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
		Protocol: protocol, Local: local, Remote: remote,
	}, nil)
}

func (s *Stack) Unexpose(protocol, local string) error {
	return s.call("/services/forwarder/unexpose", types.UnexposeRequest{
		Protocol: protocol, Local: local,
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
```
Implementer note: verify `types.ExposeRequest`/`UnexposeRequest` field names and the `Protocol` type (string vs `types.TransportProtocol`) against the pinned module — research §4c cites `types.TCP`; if `Protocol` is a typed constant, convert. The `/services/forwarder/all` JSON shape must match `ForwardView` (research §4c: `{local, remote, protocol}`). Adapt struct tags to the real response.

- [ ] **Step 4: Run** — PASS
- [ ] **Step 5: Commit** `feat(netstack): in-process gvisor-tap-vsock stack — dial + port forward` (+go.mod/go.sum)

### Task 4: `attach_darwin.go` — socketpair → vz FileHandle attachment

**Files:** Create `internal/netstack/attach_darwin.go`, `internal/netstack/attach_other.go`

**Interfaces:**
- Produces (darwin): `func (s *Stack) Attach(mac string) (*vz.VirtioNetworkDeviceConfiguration, func() error, error)` — builds the socketpair per research §3, hands fds[0] to vz, runs `AcceptVfkit(ctx, fds[1])` in a goroutine, returns the netcfg + a cleanup that cancels the goroutine. Off-darwin stub returns an error.
- This is the exact recipe from `docs/research/gvisor-tap-vsock-api.md` §3 — implement it verbatim, adapting only verified-differing symbol names.

- [ ] **Step 1:** No unit test (needs vz + real socketpair; covered by integration Task 10). Implement `attach_darwin.go` (build tag `//go:build darwin && arm64`) from research §3's recipe, with the `Stack` receiver and SO_SNDBUF/SO_RCVBUF sizing, wrapped so the vz calls sit inside a `guarded`-style recover (import the vm guard pattern or inline a recover — a panic here must not kill the daemon). `attach_other.go` (`//go:build !(darwin && arm64)`): `func (s *Stack) Attach(mac string) (*vz.VirtioNetworkDeviceConfiguration, func() error, error)` can't reference vz off-darwin — instead define the darwin one returning the vz type and keep the manager calling it only from darwin code paths. Simplest: put `Attach` entirely in `attach_darwin.go` and have `config_darwin.go` (already darwin-tagged) be its only caller; skip `attach_other.go` (nothing off-darwin calls it). Document that netstack.Attach is darwin-only in a doc comment.
- [ ] **Step 2:** `go build ./...` on darwin compiles; `go vet ./...` clean.
- [ ] **Step 3: Commit** `feat(netstack): socketpair vz FileHandle attachment (in-process, no vfkit)`

### Task 5: cloud-init static networking + /etc/hosts

**Files:** Modify `internal/cloudinit/seed.go`, `internal/cloudinit/seed_test.go`

**Interfaces:**
- `BuildSeed` signature grows: `BuildSeed(m *registry.Machine, machineDir, sshPub string, hosts map[string]string) (string, error)` where `hosts` is name→IP for `/etc/hosts` (guest→guest resolution). netplan `network-config` switches from DHCP to static: `addresses: [<m.IP>/24]`, `routes: [{to: default, via: 192.168.127.1}]`, `nameservers.addresses: [192.168.127.1]`. Keeps `dhcp-identifier` note obsolete (no DHCP now) — remove that block, replace with static. `write_files` appends the hosts map to `/etc/hosts`.
- Requires `m.IP` set (caller guarantees). Error if `m.IP == ""`.

- [ ] **Step 1: Failing test** — update `TestBuildSeedProducesCidataISO` to pass an IP + hosts and assert the network-config contains `addresses:` with the IP, `via: 192.168.127.1`, and that user-data (or a write_files entry) contains a hosts line. Add `TestBuildSeedRequiresIP`.
- [ ] **Step 2: Run** — FAIL
- [ ] **Step 3: Implement** the static netplan + `/etc/hosts` write_files. network-config v2:
```yaml
version: 2
ethernets:
  all:
    match: { name: "en*" }
    dhcp4: false
    addresses: [ "<IP>/24" ]
    routes: [ { to: "default", via: "192.168.127.1" } ]
    nameservers: { addresses: [ "192.168.127.1" ] }
```
Keep the injection guard on sshPub; validate `m.IP` via a simple parse. Add hosts entries via cloud-config `write_files` appending to `/etc/hosts` (or `bootcmd`). Update the P-comment: the DUID trap note becomes "static addressing sidesteps DHCP entirely."
- [ ] **Step 4: Run** — PASS
- [ ] **Step 5: Commit** `feat(cloudinit): static netplan addressing + /etc/hosts (retires DHCP dependency)`

### Task 6: `internal/netstack/dns.go` — host-side umbra.local resolver

**Files:** Create `internal/netstack/dns.go`, `internal/netstack/dns_test.go`

**Interfaces:**
- Produces a daemon-owned authoritative resolver (add AND remove, unlike gvisor's add-only zone):
```go
type Resolver struct { /* miekg/dns server on 127.0.0.1:<port>, name→IP map under RWMutex */ }
func NewResolver() (*Resolver, error)           // binds 127.0.0.1:0 (ephemeral) udp+tcp, starts server
func (r *Resolver) Addr() string                // "127.0.0.1:<port>" for /etc/resolver
func (r *Resolver) Set(name, ip string)         // upsert <name>.umbra.local → ip
func (r *Resolver) Remove(name string)
func (r *Resolver) Shutdown() error
// InstallResolverFile writes /etc/resolver/umbra.local pointing at Addr (needs sudo — see note)
func InstallResolverFile(port int) error
func UninstallResolverFile() error
```
- Uses `github.com/miekg/dns` (gvisor-tap-vsock already depends on it transitively — confirm it's usable directly; if not, `go get`). Answers A records for `*.umbra.local` from the map, NXDOMAIN otherwise.
- `/etc/resolver/umbra.local` write needs root. M2 decision: the daemon attempts it, and on permission error logs a clear one-time instruction (`sudo` snippet) rather than failing — host DNS is a convenience; guest `/etc/hosts` (Task 5) and `DialContextTCP` readiness work regardless. Document in README.

- [ ] **Step 1: Failing test** — `dns_test.go`: start `NewResolver`, `Set("web","192.168.127.10")`, query `web.umbra.local.` via a `dns.Client` to `Addr()`, assert the A answer; `Remove("web")` then assert NXDOMAIN; query `nope.umbra.local.` → NXDOMAIN.
- [ ] **Step 2: Run** — FAIL (`go get github.com/miekg/dns` if needed)
- [ ] **Step 3: Implement** the miekg/dns server + map. `InstallResolverFile` writes `nameserver 127.0.0.1\nport <p>\n` to `/etc/resolver/umbra.local` (macOS resolver format), returns a typed permission error the caller can soft-handle.
- [ ] **Step 4: Run** — PASS
- [ ] **Step 5: Commit** `feat(netstack): daemon-authoritative umbra.local DNS resolver (add+remove)`

### Task 7: wire netstack into vm.Manager + config_darwin

**Files:** Modify `internal/vm/manager.go`, `internal/vm/config_darwin.go`

**Interfaces:**
- `NewManager(reg, machinesDir, net *netstack.Stack, dns *netstack.Resolver)` — Manager gains the stack + resolver. `launchFn`/`launchVZ` signature grows a `*netstack.Stack` param so `config_darwin.go` calls `net.Attach(mac)` instead of `vz.NewNATNetworkDeviceAttachment`. On successful Start: `dns.Set(name, ip)`; on Stop (confirmed): `dns.Remove(name)` + run the attach cleanup (cancel AcceptVfkit goroutine). IP comes from the machine config (`registry.Machine.IP`), set by the provision step (Task 8), not from lease lookup.
- The M1 stopFn (no-op) becomes the attach cleanup closure — real resource now.

- [ ] **Step 1: Failing test** — extend `manager_test.go`: the fake `launchFn` already returns a cleanup; add a fake resolver interface so Start calls `dns.Set` and Stop calls `dns.Remove`. Assert Set-on-start / Remove-on-confirmed-stop. (Introduce a small `nameSetter` interface the Manager depends on, satisfied by `*netstack.Resolver`, so tests inject a fake — keeps netstack out of the unit test.)
- [ ] **Step 2: Run** — FAIL
- [ ] **Step 3: Implement**: thread the interface, call Set/Remove at the right transitions (Set after StateRunning commit; Remove in the confirmed-stop branch alongside `dns` nil-guard). In `config_darwin.go` replace the NAT block with `netcfg, cleanup, err := st.Attach(m.MAC)` and return `cleanup` as the stopFn. Keep everything inside `guarded`.
- [ ] **Step 4: Run** `go test ./internal/vm/ -race` — PASS
- [ ] **Step 5: Commit** `feat(vm): attach machines to netstack; DNS register/deregister on lifecycle`

### Task 8: daemon wiring + readiness via DialContextTCP + `umbra forward`

**Files:** Modify `cmd/umbrad/main.go`, `internal/api/server.go`, create `cmd/umbra/forward.go`, modify `cmd/umbra/root.go`

**Interfaces:**
- `umbrad`: construct `netstack.New()` + `netstack.NewResolver()`, attempt `InstallResolverFile` (soft-fail with log), pass both to `NewManager`. Provision step now also **allocates the IP** (`ipalloc.Allocate` over `reg.UsedIPs()`) and saves it to the machine before disk/seed build; `BuildSeed` gets the hosts map (all machines' name→IP). Readiness closure switches from `vmnet.LookupIPFromFile` + real `net.DialTimeout` to `net.IP` known-from-config + `st.DialContextTCP(ctx, ip+":22")`. On shutdown also `dns.Shutdown()` + `UninstallResolverFile` (best-effort).
- API: new routes `POST /v1/machines/{name}/forwards` (body `{local_port, guest_port, protocol}`) → `st.Expose`, `DELETE /v1/machines/{name}/forwards/{local_port}` → `st.Unexpose`, `GET /v1/machines/{name}/forwards`. The api server gains a `Forwarder` interface (Expose/Unexpose/Forwards) satisfied by `*netstack.Stack`.
- CLI: `umbra forward add <name> <localPort>:<guestPort> [--udp]`, `umbra forward list <name>`, `umbra forward rm <name> <localPort>`.

- [ ] **Step 1: Failing test** — api `server_test.go`: fake Forwarder; POST forward → Expose called with `127.0.0.1:<local>`→`<guestIP>:<guest>`; DELETE → Unexpose; GET lists. Machine must exist + be running (404/409 otherwise).
- [ ] **Step 2: Run** — FAIL
- [ ] **Step 3: Implement** the API handlers + interface, the umbrad wiring (allocate IP in provision, hosts map, DialContextTCP readiness), and the cobra `forward` command group.
- [ ] **Step 4: Run** `go test ./... -race` — PASS; `make build` fully green.
- [ ] **Step 5: Commit** `feat(net): IP allocation, DialContextTCP readiness, host↔guest port forwarding CLI+API`

### Task 9: `internal/netstack/supervisor.go` — sleep/wake + spin guard (P3/P11)

**Files:** Create `internal/netstack/supervisor.go`, `internal/netstack/supervisor_test.go`, modify `cmd/umbrad/main.go`

**Interfaces:**
- Produces:
```go
// Supervisor watches for macOS sleep/wake and periodically health-probes the
// stack. On wake it re-probes each running machine's SSH port via
// DialContextTCP; gvisor connections self-heal per-connection (research §f),
// so no full rebuild is needed, but a machine that fails N probes post-wake is
// logged for the caller to recover. Guards the P3 (udp spin) / P11 (ENOBUFS
// global-write-lock) pathologies by detecting a wedged stack and logging loudly.
type Supervisor struct { ... }
func NewSupervisor(s *Stack, probe func(ctx context.Context) []string) *Supervisor // probe returns unhealthy machine names
func (sv *Supervisor) Run(ctx context.Context)   // blocks until ctx done; call in a goroutine
```
- Sleep/wake detection: `NSWorkspace` notifications require Obj-C. M2 pragmatic approach: a monotonic-clock-gap detector — a ticker that fires every 5s; if wall-clock elapsed since last tick ≫ interval (e.g. >30s), the machine slept. Pure Go, no cgo, testable. On detected wake, run the probe. (A real NSWorkspace hook is an M5/menu-bar-era refinement; note it.)

- [ ] **Step 1: Failing test** — inject a fake clock/ticker; simulate a large gap; assert the probe fires and unhealthy names are logged/surfaced. Assert no probe on normal ticks.
- [ ] **Step 2: Run** — FAIL
- [ ] **Step 3: Implement** the gap detector + probe invocation. Keep clock injectable (a `now func() time.Time` field) so the test drives it deterministically.
- [ ] **Step 4: Run** — PASS; wire `NewSupervisor` into umbrad under daemonCtx + WaitGroup.
- [ ] **Step 5: Commit** `feat(netstack): sleep/wake gap-detector supervisor with post-wake health probe (P3/P11)`

### Task 10: integration + E2E for networking; docs

**Files:** Modify `internal/vm/integration_test.go` (or new `internal/netstack/integration_test.go`), `scripts/e2e-smoke.sh`, `docs/PITFALLS-EXTERNAL.md`, `README.md`

- [ ] **Step 1:** Integration test (build-tagged, this Mac): boot a machine on the netstack, assert `st.DialContextTCP(guessIP:22)` connects, SSH runs a command, guest can reach the internet (`curl -sI https://example.com` via shell), and a second machine can ping/resolve the first by `<name>.umbra.local` (guest /etc/hosts). Then `st.Expose` a forward and assert `127.0.0.1:<local>` reaches guest SSH from the host.
- [ ] **Step 2:** Extend `e2e-smoke.sh`: after start, `umbra forward add e2e 12222:22`, assert `ssh -p 12222 umbra@127.0.0.1 true` works, `umbra forward rm`, and (if `/etc/resolver` installed) `dscacheutil -q host -a name e2e.umbra.local` resolves. Keep VPN note: manual VPN toggle check documented, not automated.
- [ ] **Step 3:** Run `make test-integration` + `./scripts/e2e-smoke.sh` on this Mac → green. Diagnose+fix plumbing (max 2 attempts) or BLOCKED with diagnostics per M1 T12 rules.
- [ ] **Step 4:** Docs: mark P3/P4/P7/P11 addressed in PITFALLS-EXTERNAL.md with file refs; README M2 section (networking, `.umbra.local`, `umbra forward`, the sudo note for /etc/resolver, VPN-safe claim). Blast-radius sweep (record result): grep the removed NAT attachment, `LookupIPFromFile` readiness callers, subnet/gateway constants across code+docs.
- [ ] **Step 5: Commit** `test(net): integration + E2E for networking; docs: M2 pitfalls + usage`

---

## Self-Review

1. **Spec coverage (M2):** VPN-safe NAT ✅ (T3 gvisor userspace); `.umbra.local` DNS ✅ host (T6) + guest (T5 /etc/hosts); auto port-forward — **partial**: manual `umbra forward` (T8); docker-driven auto-forward is M3 (correct — no docker yet). Per-machine deterministic IP ✅ (T1/T2/T5). Sleep/wake + VPN resilience ✅ (T9 + gvisor self-heal). 
2. **Placeholder scan:** the two "verify against pinned module" notes (T3 forward types, T4 attach recipe) are API-confirmation instructions grounded in the research doc, not TBDs.
3. **Type consistency:** `netstack.Stack` produced T3, consumed T4/T7/T8; `Resolver` produced T6 consumed T7/T8; `registry.Machine.IP` T2 → cloudinit T5 → provision T8; `Forwarder`/`nameSetter` interfaces keep netstack out of unit tests. `BuildSeed` signature change (T5) is threaded to its only caller (umbrad provision, T8) — flagged.
