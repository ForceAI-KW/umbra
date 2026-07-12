package netstack

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

func queryA(t *testing.T, c *dns.Client, addr, name string) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	resp, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("exchange %s: %v", name, err)
	}
	return resp
}

func TestResolverSetAndQuery(t *testing.T) {
	r, err := NewResolver()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown()

	r.Set("web", "192.168.127.10")

	c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	resp := queryA(t, c, r.Addr(), "web.umbra.local.")

	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %v, want success", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("answers = %d, want 1: %v", len(resp.Answer), resp.Answer)
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("answer type = %T, want *dns.A", resp.Answer[0])
	}
	if a.A.String() != "192.168.127.10" {
		t.Fatalf("A = %s, want 192.168.127.10", a.A.String())
	}
}

func TestResolverRemove(t *testing.T) {
	r, err := NewResolver()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown()

	r.Set("web", "192.168.127.10")
	r.Remove("web")

	c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	resp := queryA(t, c, r.Addr(), "web.umbra.local.")

	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %v, want NXDOMAIN", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 0 {
		t.Fatalf("answers = %d, want 0", len(resp.Answer))
	}
}

func TestResolverUnknownAndOutOfZone(t *testing.T) {
	r, err := NewResolver()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown()

	r.Set("web", "192.168.127.10")

	c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}

	t.Run("unknown name in-zone", func(t *testing.T) {
		resp := queryA(t, c, r.Addr(), "nope.umbra.local.")
		if resp.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %v, want NXDOMAIN", dns.RcodeToString[resp.Rcode])
		}
	})

	t.Run("out of zone, not forwarded", func(t *testing.T) {
		resp := queryA(t, c, r.Addr(), "google.com.")
		if resp.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %v, want NXDOMAIN", dns.RcodeToString[resp.Rcode])
		}
	})
}

func TestResolverCaseAndTrailingDot(t *testing.T) {
	r, err := NewResolver()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown()

	r.Set("web", "192.168.127.10")

	c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	resp := queryA(t, c, r.Addr(), "WEB.umbra.local.")

	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %v, want success", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("answers = %d, want 1", len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok || a.A.String() != "192.168.127.10" {
		t.Fatalf("unexpected answer: %v", resp.Answer)
	}
}

func TestResolverShutdown(t *testing.T) {
	r, err := NewResolver()
	if err != nil {
		t.Fatal(err)
	}
	addr := r.Addr()

	if err := r.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	c := &dns.Client{Net: "udp", Timeout: 500 * time.Millisecond}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn("web.umbra.local."), dns.TypeA)
	if _, _, err := c.Exchange(m, addr); err == nil {
		t.Fatal("expected query to fail after shutdown")
	}
}
