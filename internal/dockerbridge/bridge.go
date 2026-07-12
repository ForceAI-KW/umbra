// Package dockerbridge pipes a host unix-socket listener to dockerd's TCP
// API inside the docker VM via the existing in-process gvisor-tap-vsock
// stack. See docs/research/dockerd-in-vm.md §2.
package dockerbridge

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
)

// Dialer is satisfied by *netstack.Stack; abstracted so tests can inject a
// fake TCP dialer without spinning up netstack/vz.
type Dialer interface {
	DialContextTCP(ctx context.Context, addr string) (net.Conn, error)
}

// Bridge accepts connections on a host unix socket and pipes each one,
// bidirectionally, to dockerd's TCP API inside the docker VM. One fresh
// DialContextTCP per accepted connection — mirrors gvisor-tap-vsock's own
// "fresh dial per new flow" self-heal model, so a docker VM restart just
// means the next accept's dial fails/retries; there is no persistent
// forwarder state to repair.
type Bridge struct {
	d         Dialer
	guestAddr string // "192.168.127.X:2375"
	ln        net.Listener
}

// Listen removes any stale socket file (P14) and binds sockPath 0600.
func Listen(d Dialer, sockPath, guestAddr string) (*Bridge, error) {
	_ = os.Remove(sockPath) // best-effort: stale file from an unclean prior exit
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return &Bridge{d: d, guestAddr: guestAddr, ln: ln}, nil
}

// Serve accepts until ctx is cancelled or the listener errors. Run in a
// goroutine, daemonCtx-wired like every other M2 background loop.
func (b *Bridge) Serve(ctx context.Context) {
	go func() { <-ctx.Done(); b.ln.Close() }()
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return // listener closed (shutdown) or fatal — caller's ctx governs
		}
		go b.pipe(ctx, conn)
	}
}

func (b *Bridge) pipe(ctx context.Context, hostConn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("docker bridge: pipe panicked", "err", r)
		}
	}()
	defer hostConn.Close()
	guestConn, err := b.d.DialContextTCP(ctx, b.guestAddr)
	if err != nil {
		slog.Warn("docker bridge: dial docker VM failed", "err", err)
		return // client sees a dropped connection; docker CLI reports "cannot connect"
	}
	defer guestConn.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(guestConn, hostConn); done <- struct{}{} }()
	go func() { io.Copy(hostConn, guestConn); done <- struct{}{} }()
	<-done
}

// Close stops accepting new connections.
func (b *Bridge) Close() error { return b.ln.Close() }
