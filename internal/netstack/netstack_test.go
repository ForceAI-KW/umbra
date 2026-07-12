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
