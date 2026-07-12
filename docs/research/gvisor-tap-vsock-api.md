# gvisor-tap-vsock — API cheat-sheet for in-process embedding (Umbra M2)

**Purpose**: reference for wiring `github.com/containers/gvisor-tap-vsock` as a LIBRARY inside the
Umbra Go daemon, with the guest side attached directly to macOS
`Virtualization.framework` (`Code-Hex/vz`) via `VZFileHandleNetworkDeviceAttachment` — no
separate `gvproxy` process, no `vfkit` process.

## Version inspected

- Repo: `github.com/containers/gvisor-tap-vsock`
- Commit: `95578750dcab8629c45a65e85599ea59a4a62b52` (2026-07-09), HEAD of `main` at research time
- Nearest tag: `v0.8.9` (module `go.mod` requires `go 1.25.6`)
- Cloned to (scratch, not committed): `git clone --depth 1 https://github.com/containers/gvisor-tap-vsock`
- Companion repos also inspected (for the vz wiring recipe, since gvisor-tap-vsock itself does not
  import `Code-Hex/vz`):
  - `github.com/crc-org/vfkit` @ `f02465693e0cec8bb3b92a9d8324644c63a2e7f2` (2026-07-06) — gvisor-tap-vsock's
    `go.mod` pins `github.com/crc-org/vfkit v0.6.4`, vendored under `vendor/github.com/crc-org/vfkit/pkg/config`
    (config structs only — the actual vz-attachment code in `pkg/vf/virtionet.go` is NOT vendored by
    gvisor-tap-vsock, so it was read from a fresh clone of vfkit itself).
  - `github.com/Code-Hex/vz/v3` @ `0d35cf3a3a8b834ee3b5bf61e4946971b2c0d61a`, tag family `v3.7.1` (the
    version vfkit's `go.mod` pins).

All line numbers below are `file:line` into these clones at the commits above.

## Import paths (for umbra's go.mod)

```go
import (
    "github.com/containers/gvisor-tap-vsock/pkg/types"          // Configuration, Zone, Record, ExposeRequest...
    "github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork" // New(), VirtualNetwork
    // pkg/client is NOT needed in-process — it's an HTTP client for talking to a *separate* gvproxy
    // process. We call the http.Handler returned by VirtualNetwork.Mux()/ServicesMux() directly.

    "github.com/Code-Hex/vz/v3" // NewFileHandleNetworkDeviceAttachment, VirtioNetworkDeviceConfiguration
)
```

---

## 1. `pkg/types.Configuration` — verified fields

`pkg/types/configuration.go:8-62`:

```go
type Configuration struct {
    Debug bool                       `yaml:"debug,omitempty"`
    CaptureFile string                `yaml:"capture-file,omitempty"` // pcap, Wireshark-readable

    MTU int                           `yaml:"mtu,omitempty"`          // gvisor stack link MTU

    Subnet string                     `yaml:"subnet,omitempty"`       // CIDR, e.g. "192.168.127.0/24"
    GatewayIP string                  `yaml:"gatewayIP,omitempty"`    // e.g. "192.168.127.1"
    DeviceIP string                   `yaml:"deviceIP,omitempty"`     // informational only, not read by New()
    HostIP string                     `yaml:"hostIP,omitempty"`       // informational only, not read by New()
    GatewayMacAddress string          `yaml:"gatewayMacAddress,omitempty"`

    DNS []Zone                        `yaml:"dns,omitempty"`          // built-in DNS zones (static at boot)
    DNSSearchDomains []string         `yaml:"dnsSearchDomains,omitempty"` // pushed via DHCP option 119

    Forwards map[string]string        `yaml:"forwards,omitempty"`     // host->guest at boot: "127.0.0.1:2222" -> "192.168.127.2:22"
    NAT map[string]string             `yaml:"nat,omitempty"`          // dest-IP rewrite for guest-originated traffic

    GatewayVirtualIPs []string        `yaml:"gatewayVirtualIPs,omitempty"` // extra IPs gateway answers ARP for

    DHCPStaticLeases map[string]string `yaml:"dhcpStaticLeases,omitempty"` // KEY=IP, VALUE=MAC — see §4a

    VpnKitUUIDMacAddresses map[string]string `yaml:"vpnKitUUIDMacAddresses,omitempty"` // Hyperkit only, irrelevant to us

    Protocol Protocol                 `yaml:"-"`                      // only consulted by Mux()'s /connect handler; irrelevant to us — we call AcceptVfkit directly

    Ec2MetadataAccess bool            `yaml:"ec2MetadataAccess,omitempty"`
}

type Protocol string
const (
    HyperKitProtocol Protocol = "hyperkit" // stream, 16-bit LE length prefix + handshake
    QemuProtocol      Protocol = "qemu"     // stream, 32-bit BE length prefix
    BessProtocol       Protocol = "bess"     // SOCK_SEQPACKET, no framing
    StdioProtocol      Protocol = "stdio"    // HyperKitProtocol minus handshake
    VfkitProtocol       Protocol = "vfkit"    // SOCK_DGRAM ("bare L2 packets"), no framing — THIS IS OURS
)

type Zone struct {
    Name      string   `yaml:"name,omitempty"`      // e.g. "umbra.local."  (miekg/dns wants trailing dot to match DNS query names)
    Records   []Record `yaml:"records,omitempty"`
    DefaultIP net.IP   `yaml:"defaultIP,omitempty"` // answered when no Record matches inside the zone
}

type Record struct {
    Name   string         `yaml:"name,omitempty"`   // exact label match against query minus zone suffix
    IP     net.IP         `yaml:"ip,omitempty"`
    Regexp *regexp.Regexp `json:",omitempty" yaml:"regexp,omitempty"` // alternative to Name
}
```

Gotcha: `Zone.Name` is compared with `strings.HasSuffix(q.Name, "."+zone.Name)`
(`pkg/services/dns/dns.go:60-61`) where `q.Name` is the raw DNS wire query (always
FQDN-with-trailing-dot). So `Zone.Name` should itself end in `.` (e.g. `"umbra.local."`), otherwise
the suffix match still works because Go's `+ "."` produces `"..."` only if you already had a dot —
verify with a quick unit test before relying on it. `Record.Name` is matched against
`strings.TrimSuffix(q.Name, zoneSuffix)` — i.e. it's the label *without* the zone, e.g. for zone
`umbra.local.` and machine `web`, the query is `web.umbra.local.` and `Record.Name` must be `"web"`.

---

## 2. `pkg/virtualnetwork` — construction, accept, dial, mux

### `New`

`pkg/virtualnetwork/virtualnetwork.go:36-93`:

```go
func New(configuration *types.Configuration) (*VirtualNetwork, error)
```

What it does, in order: parses `Subnet`; creates an `IPPool` and calls
`ipPool.Reserve(GatewayIP, GatewayMacAddress)` then, for every `DHCPStaticLeases` entry,
`ipPool.Reserve(ip, mac)` (`virtualnetwork.go:44-48`); builds the gvisor `tap.LinkEndpoint` with the
configured MTU/MAC/virtual-IPs; builds the `tcpip.Stack` (TCP/UDP/ICMPv4/ARP over IPv4 only — no
IPv6 protocol factory is registered); calls `addServices()` which wires the TCP/UDP/ICMP
forwarders, the DNS server, the DHCP server, and the host→guest port-forwarder, and returns their
combined `http.Handler` as `n.servicesMux`.

Blocking behavior: `New()` itself does not block — it starts the DNS UDP/TCP listener goroutines
and the DHCP server goroutine internally (`pkg/virtualnetwork/services.go:88-97,106-109`) and
returns immediately. Errors from those background goroutines are only logged
(`log.Error(err)` / `log.Error(server.Serve())`), never propagated back to the caller — if DHCP or
DNS fails to bind you will not get an error from `New()`, only a log line. **Gotcha for
production**: wrap/replace the logrus output or watch for these log lines if you need to alert on
DHCP/DNS startup failure.

### Accepting the guest link (vfkit protocol = ours)

`pkg/virtualnetwork/vfkit.go:10-12`:

```go
func (n *VirtualNetwork) AcceptVfkit(ctx context.Context, conn net.Conn) error
```

Internally: `n.networkSwitch.Accept(ctx, conn, types.VfkitProtocol)` (`pkg/tap/switch.go:85-102`).
**This call blocks** until `ctx` is cancelled or a read error occurs (EOF/socket close/etc) — run
it in its own goroutine per VM, one goroutine per machine, and cancel `ctx` (or close `conn`) to
tear the VM's network connection down. Return value is the terminal error (nil only if `ctx` was
what stopped it; a wrapped read error otherwise — see `pkg/tap/switch.go:96-100`,
`fmt.Errorf("cannot receive packets from %s, disconnecting: %w", ...)`).

Because `VfkitProtocol.Stream() == false` (`pkg/tap/protocols.go:74-79`), the switch takes the
**non-stream** path: `rxNonStream` (`pkg/tap/switch.go:237-254`) does one `conn.Read()` per
Ethernet frame into a 128 KiB buffer (`maxStreamPacketSize`, `switch.go:35`) — this relies on
`SOCK_DGRAM`/`unixgram` preserving datagram boundaries (one `Read()` == one frame, no length
prefix needed). On the tx side, `txBuf` (`pkg/tap/switch.go:185-206`) also writes the raw frame with
no prefix for non-stream protocols. **This confirms**: vfkit's wire protocol is bare L2 Ethernet
frames over a connected `SOCK_DGRAM` socket — exactly what `vz.NewFileHandleNetworkDeviceAttachment`
expects/produces (see §3).

### Host dialing INTO the virtual network in-process

`pkg/virtualnetwork/conn.go:14-49`:

```go
func (n *VirtualNetwork) Dial(network, addr string) (net.Conn, error)                       // network must be "tcp"
func (n *VirtualNetwork) DialContextTCP(ctx context.Context, addr string) (net.Conn, error) // network is implicitly "tcp"
func (n *VirtualNetwork) Listen(network, addr string) (net.Listener, error)                 // network must be "tcp"
```

`addr` is `"ip:port"` (`net.SplitHostPort`), IP must parse as IPv4 (`ip.To4()`). Both `Dial` and
`DialContextTCP` are thin wrappers over `gvisor.dev/gvisor/pkg/tcpip/adapters/gonet.DialTCP` /
`gonet.DialContextTCP` against the internal `tcpip.Stack` on `NIC: 1`. **This directly answers (d)**:
yes — daemon-side readiness/SSH checks can dial straight into a guest IP:port with zero real
sockets and zero host port-forwards, e.g.:

```go
conn, err := vn.DialContextTCP(ctx, "192.168.127.2:22")
```

Only TCP is exposed this way (no `DialUDP`/`DialContextUDP` wrapper on `VirtualNetwork` — UDP would
require reaching into the unexported `stack` field, which is not possible from outside the package).

### `Mux()` / `ServicesMux()` — the only entry points for runtime port-forward/DNS mutation

`pkg/virtualnetwork/mux.go`:

```go
func (n *VirtualNetwork) ServicesMux() *http.ServeMux // mounts /services/*, /stats, /cam, /leases, /tunnel
func (n *VirtualNetwork) Mux() *http.ServeMux          // ServicesMux() + /connect (protocol handshake+accept path)
```

`Mux()`'s `/connect` handler hijacks the HTTP connection and calls
`n.networkSwitch.Accept(context.Background(), conn, n.configuration.Protocol)`
(`pkg/virtualnetwork/mux.go:85-104`) — this is the entry point used when gvproxy runs as a real
daemon accepting e.g. qemu/hyperkit sockets over a real listener. **We don't use `/connect`** — we
call `AcceptVfkit` directly with our socketpair-derived `net.Conn` (§3), bypassing the HTTP layer
entirely for the data path. We DO need `ServicesMux()` (or `Mux()`) for the **control plane**:
runtime port-forward expose/unexpose and DNS zone add (§4b, §4c) — there is no non-HTTP Go API for
either. Since it's an `http.Handler`, you can drive it purely in-process with
`httptest.NewRequest` + `mux.ServeHTTP(httptest.NewRecorder(), req)` — **no real TCP socket
required**, or you can bind it to `127.0.0.1:<port>` with `http.Serve` if you want the debug
`/stats`, `/cam`, `/leases` endpoints reachable for troubleshooting.

`addServices` (`pkg/virtualnetwork/services.go:24-54`) mounts:
- `/forwarder/*` → `forwarder.PortsForwarder.Mux()` (§4c)
- `/dhcp/*` → `dhcp.Server.Mux()` (leases list only, no runtime static-lease API — set those via
  `Configuration.DHCPStaticLeases` before `New()`)
- `/dns/*` → `dns.Server.Mux()` (§4b)

`ServicesMux()` strips `/services` and remounts those three under it, i.e. real paths are
`/services/forwarder/...`, `/services/dhcp/...`, `/services/dns/...`.

---

## 3. Wiring one guest's socket to `Code-Hex/vz` — in-process socketpair recipe

**Reference used**: `vfkit`'s own host-side vz glue,
`vfkit-research/pkg/vf/virtionet.go:117-147` (`toVz()`), which is the authoritative example of
"how a `*os.File` connected-datagram-socket becomes a vz network attachment" — vfkit itself gets
that `*os.File` either from a real `net.DialUnix("unixgram", ...)` to a filesystem-path socket
(`virtionet.go:49-115`, `connectUnixPath`) **or** it's handed a pre-opened `*os.File` directly via
`config.VirtioNet.Socket *os.File` (`vendor/github.com/crc-org/vfkit/pkg/config/virtio.go:99-108`,
comment: *"file parameter is holding a connected datagram socket"*). Umbra is in-process, so we skip
the filesystem-socket dance entirely and use `syscall.Socketpair` to create both ends as
already-connected local fds — no path, no cleanup, no PID/random-suffix filename collision risk.

`vz.NewFileHandleNetworkDeviceAttachment` signature, `vz-research/network.go:186-213`:

```go
// file parameter is holding a connected datagram socket.
// Only supported on macOS 11+.
func NewFileHandleNetworkDeviceAttachment(file *os.File) (*FileHandleNetworkDeviceAttachment, error)
```

It validates the fd is `SOCK_DGRAM` (`validateDatagramSocket`, `network.go:215-228`) and that
`getsockname` returns one of `SockaddrInet4`/`SockaddrInet6`/`SockaddrUnix` — an unbound
`AF_UNIX`/`SOCK_DGRAM` socketpair fd satisfies this (`Getsockname` returns a `SockaddrUnix` with an
empty `Name`, which still matches the type switch, `network.go:230-237`).

MTU setter, `network.go:239-267`:

```go
func (f *FileHandleNetworkDeviceAttachment) SetMaximumTransmissionUnit(mtu int) error // macOS 13+, range [1500, 65535]
func (f *FileHandleNetworkDeviceAttachment) MaximumTransmissionUnit() int             // default 1500
```

Doc comment on `SetMaximumTransmissionUnit` (`network.go:244-247`) is a real constraint, not just
advice: *"the client side of the associated datagram socket must be properly configured with the
appropriate values for `SO_SNDBUF`/`SO_RCVBUF`... the system expects `SO_RCVBUF` to be at least
double `SO_SNDBUF`, and for optimal performance, 4x."* gvisor-tap-vsock's own unixgram code follows
exactly this ratio: `SO_SNDBUF = 1 MiB`, `SO_RCVBUF = 4 MiB`
(`pkg/transport/unixgram_unix.go:25-28`) — replicate that on both ends of our socketpair fds, and
if you raise MTU above 1500 (macOS 13+) set it on **both** the vz attachment
(`SetMaximumTransmissionUnit`) and `Configuration.MTU` (which sets the gvisor-side link MTU,
`pkg/virtualnetwork/virtualnetwork.go:50-57` → `tap.NewLinkEndpoint`) — they are two independent
numbers with no cross-validation; a mismatch will just silently fragment/drop, not error.

`vz.VirtioNetworkDeviceConfiguration` wiring, `vz-research/network.go:299` +
`vfkit-research/pkg/vf/virtionet.go:117-147`:

```go
attachment, err := vz.NewFileHandleNetworkDeviceAttachment(hostSideFile)
netConfig, err := vz.NewVirtioNetworkDeviceConfiguration(attachment)
mac, err := vz.NewMACAddress(macAddr) // or vz.NewRandomLocallyAdministeredMACAddress()
netConfig.SetMACAddress(mac)
// append netConfig to your vz.VirtualMachineConfiguration.networkDevices
```

### Concrete in-process recipe (no vfkit binary, no filesystem socket path)

```go
import (
    "net"
    "os"
    "syscall"

    "github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
    "github.com/Code-Hex/vz/v3"
    "golang.org/x/sys/unix"
)

func attachVM(vn *virtualnetwork.VirtualNetwork, macAddr net.HardwareAddr) (*vz.VirtioNetworkDeviceConfiguration, func() error, error) {
    // AF_UNIX + SOCK_DGRAM socketpair: two already-connected fds, no filesystem path at all.
    fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
    if err != nil {
        return nil, nil, err
    }

    // Match gvisor-tap-vsock's own unixgram buffer sizing (pkg/transport/unixgram_unix.go:25-28)
    // and vz's documented RCVBUF>=2x..4x SNDBUF requirement, on BOTH ends.
    for _, fd := range fds {
        _ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 1*1024*1024)
        _ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 4*1024*1024)
    }

    vzFile := os.NewFile(uintptr(fds[0]), "vz-net")   // fds[0] -> handed to vz
    gtvFile := os.NewFile(uintptr(fds[1]), "gtv-net") // fds[1] -> handed to gvisor-tap-vsock

    attachment, err := vz.NewFileHandleNetworkDeviceAttachment(vzFile)
    if err != nil {
        return nil, nil, err
    }
    // Only if MTU > 1500 and host is macOS 13+; must match Configuration.MTU used in virtualnetwork.New().
    // _ = attachment.SetMaximumTransmissionUnit(cfg.MTU)

    netConfig, err := vz.NewVirtioNetworkDeviceConfiguration(attachment)
    if err != nil {
        return nil, nil, err
    }
    mac, err := vz.NewMACAddress(macAddr)
    if err != nil {
        return nil, nil, err
    }
    netConfig.SetMACAddress(mac)

    gtvConn, err := net.FileConn(gtvFile) // dups the fd; safe to Close() gtvFile after this
    if err != nil {
        return nil, nil, err
    }
    _ = gtvFile.Close()

    ctx, cancel := context.WithCancel(context.Background())
    go func() {
        if err := vn.AcceptVfkit(ctx, gtvConn); err != nil {
            log.Printf("gvisor-tap-vsock: guest link closed: %v", err)
        }
    }()

    cleanup := func() error {
        cancel()             // unblocks AcceptVfkit's read loop
        return gtvConn.Close()
    }
    return netConfig, cleanup, nil
}
```

Notes / gotchas:
- `vzFile` (fds[0]) must stay open for the lifetime of the VM — `vz.NewFileHandleNetworkDeviceAttachment`
  keeps the fd (via `C.int(file.Fd())`, `network.go:200`); do not `Close()` it yourself — vz's VM
  teardown owns it (mirrors vfkit's own `Shutdown()` closing `dev.Socket`,
  `vfkit-research/pkg/vf/virtionet.go:173-186`). If you close it early, the guest's NIC goes dark
  with no clean error surfaced to gvisor-tap-vsock (it'll just see read/write errors on `gtvConn`
  and `AcceptVfkit` returns a wrapped error).
- `net.FileConn` dups the underlying fd, so closing `gtvFile` after `net.FileConn` succeeds is
  correct and matches vfkit's own pattern (`conn.File()` + `conn.Close()`,
  `vfkit-research/pkg/vf/virtionet.go:103-109`) — the returned `net.Conn` (`gtvConn`) is what you
  actually pass to `AcceptVfkit`.
- One socketpair per VM. `AcceptVfkit`/`networkSwitch.Accept` assigns each accepted `conn` a fresh
  internal connection ID (`pkg/tap/switch.go:104-113`) and learns its MAC dynamically off the first
  frame received (CAM table, `pkg/tap/switch.go:293-333`) — you do NOT need to tell the switch the
  MAC in advance; it self-learns from frames. (DHCP static-lease MAC pinning, §4a, is a *separate*,
  IP-allocation-level concern from the switch's L2 CAM table.)

---

## 4. Direct answers

### (a) Can `DHCPStaticLeases` pin MAC→IP so the daemon knows each VM's IP without lease parsing?

**Yes.** Exact shape: `Configuration.DHCPStaticLeases map[string]string` where **key = IP string,
value = MAC string** (confirmed by the consuming loop, `pkg/virtualnetwork/virtualnetwork.go:46-48`:
`for ip, mac := range configuration.DHCPStaticLeases { ipPool.Reserve(net.ParseIP(ip), mac) }`).
`ipPool.Reserve` (`pkg/tap/ip_pool.go:67-72`) writes directly into the pool's `leases` map
(`leases[ip] = mac`) **synchronously inside `virtualnetwork.New()`**, before any VM boots or sends a
DHCP packet. Then `dhcp.handler`'s `ipPool.GetOrAssign(mac)` (`pkg/services/dhcp/dhcp.go:35`,
`pkg/tap/ip_pool.go:41-65`) checks existing leases *by MAC* first and returns the pre-reserved IP if
found — the DHCP handshake becomes a formality; it doesn't allocate anything new. So: the daemon can
compute/know each machine's IP the moment it decides the MAC (e.g. at VM-create time), by simply
choosing the IP itself and writing `cfg.DHCPStaticLeases[ip] = mac` into the `Configuration` passed
to `virtualnetwork.New()` — no lease-file parsing, no waiting for the guest to actually DHCP.
Example: `cfg.DHCPStaticLeases = map[string]string{"192.168.127.10": "52:54:00:aa:bb:01"}`.

Caveat: this is **boot-time only** — `DHCPStaticLeases` is read once inside `New()`. There is no
runtime "add a static lease" HTTP/Go API (`/dhcp/*` mux only exposes `GET /leases`, read-only,
`pkg/virtualnetwork/services.go:101-110`, `pkg/services/dhcp/dhcp.go` has no `/add` route). For
Umbra's per-machine add/remove lifecycle, either (i) pre-plan the whole subnet's MAC→IP map before
calling `New()` once for the daemon's lifetime, or (ii) call `ipPool.Reserve`/`GetOrAssign`
yourself — but both are unexported from `pkg/tap`, so this is only possible if Umbra vendors/forks
that one file, or accepts "restart the `VirtualNetwork` to add a new static lease" as the model. In
practice this is fine: `VirtualNetwork` is cheap to construct and you likely want one long-lived
instance with the full planned subnet pre-populated at daemon startup, not per-VM-add.

### (b) Can the embedded DNS answer a custom zone (`umbra.local`) with records added/removed at RUNTIME?

**Added: yes, via HTTP only. Removed: no API at all — see workaround.**
- Add: `POST /dns/add` (mounted as `POST /services/dns/add` under `ServicesMux()`) with a JSON
  `types.Zone` body (`pkg/services/dns/dns.go:286-299`, handler calls `s.addZone(req)`). `addZone`
  (`dns.go:303-315`, **unexported**) merges by zone `Name`: if a zone with that name already exists
  it **appends** the new request's `Records` onto the existing zone's records
  (`req.Records = append(req.Records, zone.Records...); s.handler.zones[i] = req`); if not, it
  appends a whole new `Zone`. This is thread-safe (`zonesLock sync.RWMutex`,
  `dns.go:26-30,303-306`) and takes effect on the very next query — no restart needed.
  Example client call in-process (via `ServiceMux` `http.Handler`, no real socket):
  ```go
  req := httptest.NewRequest(http.MethodPost, "/services/dns/add", bytes.NewReader(mustJSON(types.Zone{
      Name: "umbra.local.",
      Records: []types.Record{{Name: "web-1", IP: net.ParseIP("192.168.127.10")}},
  })))
  w := httptest.NewRecorder()
  vn.ServicesMux().ServeHTTP(w, req) // or vn.Mux() if you also need /connect
  ```
  Or use `pkg/client.Client.AddDNS(&types.Zone{...})` (`pkg/client/client.go:130-155`) if you'd
  rather bind `ServicesMux()` to a real loopback port and drive it over real HTTP.
- **Remove/replace a single record at runtime: no API.** There is no `DELETE`/`/dns/remove`
  endpoint, no exported `removeZone`/`removeRecord`, and `addZone`'s merge semantics mean you cannot
  even "add a Zone with the same Name and fewer Records" to shrink it — records only ever
  accumulate (`req.Records = append(req.Records, zone.Records...)`, old records survive any
  re-`POST`). **Workaround for Umbra's machine-remove case**: keep your own authoritative
  `map[machineName]Record` outside gvisor-tap-vsock, and instead of trying to mutate the live DNS
  server, either (i) accept that stale records answer with a since-dead IP until the whole zone/VN
  is rebuilt (harmless if you also stop routing/DHCP-reserving that IP — the A record just goes
  nowhere), or (ii) don't use `types.Zone{Records: [...]}` static records at all for
  frequently-changing hostnames — instead implement a small custom `upstreamResolver`
  (`pkg/services/dns/dns.go:17-24`, the interface `dns.New`/`dns.NewWithUpstreamResolver`
  (`dns.go:246-256`) takes) backed by Umbra's live machine registry, and pass it in place of the
  default `&net.Resolver{}` — this requires constructing the DNS server yourself
  (`dns.NewWithUpstreamResolver(udpConn, tcpLn, zones, upstream)`) instead of going through
  `virtualnetwork.New()`'s internal `dnsServer()` (`pkg/virtualnetwork/services.go:64-99`), i.e. a
  deeper integration than the public `VirtualNetwork` API supports out of the box. Flag this as an
  M2 design decision: **static zone add-only is fine for a slow-changing subnet; a churny
  add/remove-VM-per-minute model needs the custom-resolver route or a periodic `VirtualNetwork`
  rebuild.**

### (c) Port forwarding — runtime expose/unexpose

**HTTP mux only — no exported direct Go call** (the `forwarder.PortsForwarder` instance that owns
`Expose`/`Unexpose` is created inside `addServices()` and only its `.Mux()` `http.Handler` escapes
`pkg/virtualnetwork/services.go:112-126`; `VirtualNetwork` has no field/getter exposing it).

- Expose TCP `127.0.0.1:X` → `guestIP:Y`: `POST /forwarder/expose` (→
  `/services/forwarder/expose` under `ServicesMux()`), body `types.ExposeRequest{Local: "127.0.0.1:X",
  Remote: "guestIP:Y", Protocol: types.TCP}` (`pkg/services/forwarder/ports.go:294-326`,
  `pkg/types/handshake.go:12-16`). Internally calls `f.Expose(types.TCP, local, remote)`
  (`ports.go:70-260`, TCP branch at `ports.go:229-256`) which builds an `inetaf/tcpproxy.Proxy`
  bound to `local` that dials `gonet.DialContextTCP` against the gvisor stack for every accepted
  connection (fresh dial per connection, see §f).
- Unexpose: `POST /forwarder/unexpose`, body `types.UnexposeRequest{Local: "127.0.0.1:X", Protocol:
  types.TCP}` (`ports.go:327-345`) → `f.Unexpose(protocol, local)` (`ports.go:266-275`) looks up the
  proxy by `key(protocol, local)` and calls its `underlying.Close()` (for TCP that's `(*tcpproxy.Proxy).Close`).
  Errors with `"proxy not found"` if not currently exposed.
- List all: `GET /forwarder/all` → JSON array of `{local, remote, protocol}` sorted by local then
  protocol (`ports.go:279-293`).
- In-process call pattern is identical to §4b — drive `vn.ServicesMux()` with
  `httptest.NewRequest`/`ServeHTTP`, no real socket required, OR use
  `pkg/client.Client.Expose`/`.Unexpose`/`.List()` (`pkg/client/client.go:53-105`) if bound to a real
  loopback listener.
- Boot-time equivalent (no HTTP needed): `Configuration.Forwards map[string]string` — key is local
  addr (`"udp:"` prefix routes to UDP, otherwise TCP), value is remote `guestIP:port`
  (`pkg/virtualnetwork/services.go:112-126`, `forwardHostVM`). Fine for forwards known at daemon
  startup; runtime add/remove of forwards (the actual Umbra machine-add/remove case) must go through
  the HTTP mux.

### (d) Can the HOST dial into the virtual network in-process (no port-forward needed)?

**Yes** — see §2, `VirtualNetwork.DialContextTCP(ctx, "guestIP:port") (net.Conn, error)`
(`pkg/virtualnetwork/conn.go:26-37`). This is the right primitive for Umbra's readiness/SSH checks:
dial the guest's SSH port directly from the daemon process, no `Forwards`/`Expose` entry needed at
all. Only TCP; `network` param elsewhere in this file is validated to literally equal `"tcp"`
(`conn.go:51-53`) so don't bother trying `"tcp4"`/etc.

### (e) `udp_proxy` #584 (ECONNREFUSED spin) and #367 (ENOBUFS) — status in this version

- **#367 (ENOBUFS on unixgram write): FIXED, and the fix is explicitly commented with the issue
  link.** `pkg/tap/switch.go:194-205` (`txBuf`):
  ```go
  for {
      if _, err := conn.Write(buf); err != nil {
          if errors.Is(err, syscall.ENOBUFS) {
              // socket buffer can be full keep retrying sending the same data
              // again until it works or we get a different error
              // https://github.com/containers/gvisor-tap-vsock/issues/367
              continue
          }
          return err
      }
      return nil
  }
  ```
  This is a **busy-retry loop with no backoff and no cap** — if the peer never drains (e.g. our VM
  is paused/hung), this spins the switch's single writer goroutine (guarded by
  `e.writeLock sync.Mutex`, `switch.go:50,186-187`) at 100% CPU indefinitely, blocking delivery to
  *every* other connected VM too (the write lock is global to the `Switch`, not per-connection).
  **Flag for Umbra**: a hung/paused VM's peer socket filling up will stall the whole gateway's TX
  path system-wide, not just that VM. Worth a watchdog/circuit-breaker at the Umbra layer (e.g.
  detect a VM that's been paused > N seconds and proactively tear down its `AcceptVfkit` goroutine)
  since gvisor-tap-vsock itself has no timeout/backoff here.
- **#584 (UDP proxy ECONNREFUSED spin): still present, unchanged as of this commit — NOT fixed
  with backoff.** `pkg/services/forwarder/udp_proxy.go:69-105` (`replyLoop`):
  ```go
  again:
      read, err := proxyConn.Read(readBuf)
      ...
      if err != nil {
          if err, ok := err.(*net.OpError); ok && err.Err == syscall.ECONNREFUSED {
              // This will happen if the last write failed
              // (e.g: nothing is actually listening on the
              // proxied port on the container), ignore it
              // and continue until UDPConnTrackTimeout
              // expires:
              goto again
          }
          return
      }
  ```
  This is NOT a hot-spin like #367's `continue` (there's no work between iterations besides another
  blocking `Read()` on a UDP socket, which only returns once another packet/error arrives — it's not
  CPU-bound), but functionally it means: once a UDP datagram is sent to a guest port nobody's
  listening on, the connection stays tracked and keeps looping on `ECONNREFUSED` reads until
  `UDPConnTrackTimeout` (90s, `udp_proxy.go:20`) naturally expires the *read deadline* set at the top
  of the loop (`proxyConn.SetReadDeadline(time.Now().Add(UDPConnTrackTimeout))`,
  `udp_proxy.go:79`) — so it's bounded by 90s per idle/refused flow, not truly stuck forever, but
  it is exactly the "spins for the full timeout instead of tearing down immediately on first
  ECONNREFUSED" behavior #584 describes as wasteful. No git history/tags reference "584" in this
  checkout (`git log --all --grep=584` empty — likely fixed/discussed upstream in an already-closed
  or still-open issue not yet reflected in code, or the issue number doesn't map to a merged PR in
  this repo's history at this commit). **Treat as: confirmed present in code, not confirmed whether
  upstream considers it "fixed"** — if Umbra relies on fast guest-UDP-port-closed detection (e.g.
  DNS-over-UDP health checks), do not expect sub-second failure signaling; expect up to 90s of
  silent retry before the tracked flow is dropped.

### (f) API for draining/resetting connections on host network change — or self-heal per-connection?

**No explicit API — and there is no such API to call.** Grepped the whole `pkg/` tree for
reset/drain/flush/reconnect/network-change hooks; the only "reconnect" logic in the repo is
`pkg/sshclient/bastion.go`'s SSH bastion reconnect (unrelated to the data-plane forwarders).
Self-heal is implicit and per-connection by construction:
- **Guest→host TCP** (`pkg/services/forwarder/tcp.go:20-61`): every new inbound `tcp.ForwarderRequest`
  from the guest triggers a **fresh** `net.Dial("tcp", ...)` to the host (`tcp.go:34`) — a new
  connection is opened per guest TCP session, so a host network change only affects
  already-established flows (whose host-side socket is now on a dead route/interface); brand-new
  guest connections after the change dial fresh and pick up the new route automatically via the Go
  runtime's normal dial-time interface/route resolution.
- **Guest→host UDP** (`pkg/services/forwarder/udp.go:17-56`): same pattern — a fresh
  `net.Dial("udp", ...)` per new `(srcIP,srcPort)` conntrack entry (`udp.go:43-45`), entries expire
  after `UDPConnTrackTimeout` (90s) of inactivity (`udp_proxy.go:20,79,137`) and get re-dialed fresh
  on the next packet.
- **Host→guest port-forwards** (`PortsForwarder.Expose`, TCP/UDP cases, `ports.go:202-256`): the
  *listener* on the host side (`net.ListenUDP`/`tcpproxy.Proxy` `AddRoute`) is long-lived and does
  NOT get re-created on network change, but each **accepted connection** on that listener dials
  fresh into the gvisor stack (`gonet.DialContextTCP`/`gonet.DialUDP`) at accept-time — this path is
  guest-internal (dialing 192.168.127.x, not a real host NIC) so it's not affected by host network
  changes at all.
- **Conclusion for Umbra**: no watchdog/reset call is needed or possible against
  gvisor-tap-vsock for host network-change events — existing long-lived flows through host-egress
  forwarders may die and need the *application* (inside the guest or on the host) to reconnect
  (normal TCP/UDP behavior, not something gvisor-tap-vsock can paper over), but the daemon itself
  doesn't need to do anything to gvisor-tap-vsock's internals — new connections just work. If Umbra
  wants faster detection of "this VM's uplink died," build it at the Umbra layer (e.g. periodic
  `DialContextTCP` health probe into the guest, §4d) rather than expecting a
  gvisor-tap-vsock-provided signal.

---

## 5. Other gotchas worth carrying into the M2 plan

- **IPv4 only.** `createStack` only registers `ipv4.NewProtocol`/`arp.NewProtocol` and
  `tcp`/`udp`/`icmp.NewProtocol4` (`pkg/virtualnetwork/virtualnetwork.go:109-120`) — no IPv6 anywhere
  in the stack. `DHCPStaticLeases`/`Subnet`/all IP fields must be IPv4; `net.ParseIP(...).To4()` is
  called throughout and will silently produce a nil/zero address for anything IPv6-shaped, which
  then fails downstream in confusing ways (e.g. `tcpip.AddrFrom4Slice(nil)`).
- **Single global write lock.** `Switch.writeLock` (`pkg/tap/switch.go:50`) serializes ALL frame
  writes to ALL connected VMs through one mutex (`txBuf`, `switch.go:185-206`) — under many
  concurrent VMs this is a contention point; combined with the ENOBUFS retry-spin (§4e) a single
  stuck VM peer can starve every other VM's outbound traffic.
  the codebase has no work-around today; if this becomes a real bottleneck for Umbra's target VM
  count, it's a fork-and-patch, not a config knob.
- **DHCP/DNS startup errors are swallowed.** `New()` never returns those goroutines' bind errors
  (§2) — Umbra needs its own port-availability pre-check or log-scraping if it wants to catch
  "someone else is already listening on this gateway IP" type failures at daemon startup rather
  than silently running with a dead DHCP/DNS server.
- **No native Go UDP-dial-into-guest API** (§2/§4d) — if Umbra ever needs to health-check a
  UDP-based guest service in-process, there's no `VirtualNetwork.DialUDP`; would need to reach into
  `pkg/tap`/`gonet` directly by forking, or route it through the TCP-only `DialContextTCP` health
  check design instead (e.g. use SSH/TCP as the readiness signal, which the plan already implies).
- **`ipPool`/`forwarder.PortsForwarder`/`dns.Server` are all unexported internals** reachable only
  via the two escape hatches gvisor-tap-vsock deliberately exposes: (1) `Configuration` fields
  consumed once at `New()` time, and (2) the `ServicesMux()`/`Mux()` `http.Handler`. Any runtime
  mutation Umbra needs beyond "add a DNS zone" / "expose or unexpose a TCP/UDP forward" is not
  reachable through the public API surface — plan the M2 machine-lifecycle design (add/remove VM)
  around what those two escape hatches actually support, not around what feels natural for a
  from-scratch design.
