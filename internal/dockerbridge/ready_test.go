package dockerbridge

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// dialListener returns a dial func that connects to ln regardless of the
// requested addr — stands in for the netstack/bridge dialer in tests that
// don't want vz/gvisor-tap-vsock spun up.
func dialListener(ln net.Listener) func(ctx context.Context, addr string) (net.Conn, error) {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}
}

func newPingServer(t *testing.T, handler http.HandlerFunc) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln
}

func TestWaitDockerReady_ImmediateOK(t *testing.T) {
	pollInterval = 10 * time.Millisecond
	ln := newPingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_ping" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	start := time.Now()
	err := WaitDockerReady(context.Background(), dialListener(ln), "192.168.127.2:2375", 2*time.Second)
	if err != nil {
		t.Fatalf("WaitDockerReady: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("expected fast return, took %s", elapsed)
	}
}

func TestWaitDockerReady_EventualOK(t *testing.T) {
	pollInterval = 20 * time.Millisecond
	var attempts int32
	ln := newPingServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 4 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	err := WaitDockerReady(context.Background(), dialListener(ln), "192.168.127.2:2375", 2*time.Second)
	if err != nil {
		t.Fatalf("WaitDockerReady: %v", err)
	}
	if atomic.LoadInt32(&attempts) < 4 {
		t.Fatalf("expected at least 4 attempts, got %d", attempts)
	}
}

func TestWaitDockerReady_TimeoutNamesStage(t *testing.T) {
	pollInterval = 10 * time.Millisecond
	ln := newPingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	err := WaitDockerReady(context.Background(), dialListener(ln), "192.168.127.2:2375", 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "dockerd") {
		t.Fatalf("expected error to name the %q stage, got: %v", "dockerd", err)
	}
}

func TestWaitDockerReady_DialAlwaysFailsTimesOut(t *testing.T) {
	pollInterval = 10 * time.Millisecond
	dial := func(ctx context.Context, addr string) (net.Conn, error) {
		return nil, errors.New("dial refused")
	}

	err := WaitDockerReady(context.Background(), dial, "192.168.127.2:2375", 150*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "dockerd") {
		t.Fatalf("expected error to name the %q stage, got: %v", "dockerd", err)
	}
}

func TestWaitDockerReady_ParentCancel(t *testing.T) {
	pollInterval = 10 * time.Millisecond
	ln := newPingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	err := WaitDockerReady(ctx, dialListener(ln), "192.168.127.2:2375", 5*time.Second)
	if err == nil {
		t.Fatal("expected error from parent cancellation")
	}
}
