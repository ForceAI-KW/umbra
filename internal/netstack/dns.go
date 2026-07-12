package netstack

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// zoneSuffix is the fully-qualified suffix a Resolver is authoritative for.
const zoneSuffix = "umbra.local."

// dnsTTL is the answer TTL. Kept short since name→IP mappings can change as
// machines start/stop.
const dnsTTL = 5

// ErrResolverPermission indicates InstallResolverFile/UninstallResolverFile
// failed because the process lacks permission to write /etc/resolver — the
// caller should log a sudo hint and continue rather than fail hard.
var ErrResolverPermission = errors.New("insufficient permission to write /etc/resolver (rerun with sudo)")

// Resolver is a daemon-owned authoritative DNS server for the umbra.local
// zone. Unlike gvisor-tap-vsock's built-in (add-only) zone, it supports
// removing names as machines are deleted.
type Resolver struct {
	mu      sync.RWMutex
	records map[string]string // bare machine name (lowercase) -> IP

	port int

	udpConn *net.UDPConn
	tcpLn   net.Listener
	udpSrv  *dns.Server
	tcpSrv  *dns.Server
}

// NewResolver binds 127.0.0.1 on an ephemeral UDP+TCP port (both using the
// same port number, as required by macOS's /etc/resolver format) and starts
// serving the umbra.local zone.
func NewResolver() (*Resolver, error) {
	const attempts = 5

	var lastErr error
	for i := 0; i < attempts; i++ {
		r, err := tryBind()
		if err == nil {
			return r, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("netstack: bind dns resolver: %w", lastErr)
}

func tryBind() (*Resolver, error) {
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("listen udp: %w", err)
	}
	port := udpConn.LocalAddr().(*net.UDPAddr).Port

	tcpLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		udpConn.Close()
		return nil, fmt.Errorf("listen tcp: %w", err)
	}

	r := &Resolver{
		records: make(map[string]string),
		port:    port,
		udpConn: udpConn,
		tcpLn:   tcpLn,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	handler := dns.HandlerFunc(r.handleDNS)

	r.udpSrv = &dns.Server{PacketConn: udpConn, Handler: handler, NotifyStartedFunc: wg.Done}
	r.tcpSrv = &dns.Server{Listener: tcpLn, Handler: handler, NotifyStartedFunc: wg.Done}

	errCh := make(chan error, 2)
	go func() { errCh <- r.udpSrv.ActivateAndServe() }()
	go func() { errCh <- r.tcpSrv.ActivateAndServe() }()

	// Wait for both listeners to be actively serving, with a bound so a
	// failure-to-start doesn't hang the caller forever.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case err := <-errCh:
		udpConn.Close()
		tcpLn.Close()
		return nil, fmt.Errorf("activate dns server: %w", err)
	case <-time.After(2 * time.Second):
		udpConn.Close()
		tcpLn.Close()
		return nil, errors.New("timed out waiting for dns server to start")
	}

	return r, nil
}

// Addr returns "127.0.0.1:<port>" suitable for /etc/resolver or a dns.Client.
func (r *Resolver) Addr() string {
	return fmt.Sprintf("127.0.0.1:%d", r.port)
}

// Port returns the ephemeral UDP/TCP port the resolver is bound to.
func (r *Resolver) Port() int {
	return r.port
}

// Set upserts name.umbra.local -> ip. name is the bare machine name (no
// .umbra.local suffix).
func (r *Resolver) Set(name, ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[strings.ToLower(name)] = ip
}

// Remove deletes name from the zone, if present.
func (r *Resolver) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.records, strings.ToLower(name))
}

// Shutdown stops both the UDP and TCP servers.
func (r *Resolver) Shutdown() error {
	return errors.Join(r.udpSrv.Shutdown(), r.tcpSrv.Shutdown())
}

// handleDNS answers A queries for <name>.umbra.local from the in-memory
// zone. Anything unknown in-zone, or out of zone entirely, gets NXDOMAIN —
// this resolver is authoritative-only and does not forward/recurse.
func (r *Resolver) handleDNS(w dns.ResponseWriter, req *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true

	if len(req.Question) != 1 {
		m.SetRcode(req, dns.RcodeFormatError)
		_ = w.WriteMsg(m)
		return
	}

	q := req.Question[0]
	qname := dns.CanonicalName(q.Name)

	if !strings.HasSuffix(qname, "."+zoneSuffix) {
		m.SetRcode(req, dns.RcodeNameError)
		_ = w.WriteMsg(m)
		return
	}

	bare := strings.TrimSuffix(qname, "."+zoneSuffix)

	r.mu.RLock()
	ip, ok := r.records[bare]
	r.mu.RUnlock()

	if !ok {
		m.SetRcode(req, dns.RcodeNameError)
		_ = w.WriteMsg(m)
		return
	}

	if q.Qtype == dns.TypeA {
		rr := &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: dnsTTL},
			A:   net.ParseIP(ip).To4(),
		}
		m.Answer = append(m.Answer, rr)
	}
	// Name exists but a non-A type was asked: return NOERROR with no
	// answers (standard DNS behavior for "name exists, no records of
	// this type"), not NXDOMAIN.
	_ = w.WriteMsg(m)
}

// resolverDir / resolverFile are macOS's per-domain resolver config path.
const resolverDir = "/etc/resolver"

func resolverFilePath() string {
	return filepath.Join(resolverDir, "umbra.local")
}

// InstallResolverFile writes /etc/resolver/umbra.local (macOS resolver
// config format) pointing at 127.0.0.1:<port>. Requires root; on a
// permission error it returns a wrapped ErrResolverPermission instead of
// panicking so the caller can log a sudo hint and continue.
func InstallResolverFile(port int) error {
	if err := os.MkdirAll(resolverDir, 0o755); err != nil {
		return wrapResolverErr("mkdir /etc/resolver", err)
	}
	content := fmt.Sprintf("nameserver 127.0.0.1\nport %d\n", port)
	if err := os.WriteFile(resolverFilePath(), []byte(content), 0o644); err != nil {
		return wrapResolverErr("write /etc/resolver/umbra.local", err)
	}
	return nil
}

// UninstallResolverFile best-effort removes /etc/resolver/umbra.local. A
// missing file is not an error.
func UninstallResolverFile() error {
	err := os.Remove(resolverFilePath())
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return wrapResolverErr("remove /etc/resolver/umbra.local", err)
}

func wrapResolverErr(op string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%s: %w: %v", op, ErrResolverPermission, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}
