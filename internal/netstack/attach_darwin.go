//go:build darwin && arm64

package netstack

// Attach is darwin/arm64-only: it wires one guest's network device directly
// to this in-process gvisor-tap-vsock stack via an AF_UNIX/SOCK_DGRAM
// socketpair, per docs/research/gvisor-tap-vsock-api.md §3. No off-darwin
// stub exists — internal/vm/config_darwin.go (already darwin-tagged) is the
// only caller.

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"syscall"

	"github.com/Code-Hex/vz/v3"
	"golang.org/x/sys/unix"
)

// Attach creates an AF_UNIX/SOCK_DGRAM socketpair, hands one end to vz as a
// FileHandleNetworkDeviceAttachment and the other to this stack's
// AcceptVfkit loop, and returns the resulting network device configuration
// plus a cleanup func that tears the guest link down.
//
// vz owns the fd handed to NewFileHandleNetworkDeviceAttachment for the
// lifetime of the VM — it must not be closed here (mirrors vfkit's own
// Socket-attachment teardown).
//
// The caller MUST invoke the returned cleanup exactly once, even if VM setup
// fails downstream after Attach succeeds — otherwise the AcceptVfkit goroutine
// and both socket fds leak for the process lifetime.
func (s *Stack) Attach(mac string) (*vz.VirtioNetworkDeviceConfiguration, func() error, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return nil, nil, fmt.Errorf("attach: parse mac %q: %w", mac, err)
	}

	// AF_UNIX + SOCK_DGRAM socketpair: two already-connected fds, no
	// filesystem path at all.
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("attach: socketpair: %w", err)
	}

	// Match gvisor-tap-vsock's own unixgram buffer sizing
	// (pkg/transport/unixgram_unix.go:25-28) and vz's documented
	// RCVBUF>=2x..4x SNDBUF requirement, on BOTH ends.
	for _, fd := range fds {
		_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 1*1024*1024)
		_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 4*1024*1024)
	}

	vzFile := os.NewFile(uintptr(fds[0]), "vz-net")   // fds[0] -> handed to vz
	gtvFile := os.NewFile(uintptr(fds[1]), "gtv-net") // fds[1] -> handed to gvisor-tap-vsock

	// Once NewFileHandleNetworkDeviceAttachment succeeds, vz's native
	// NSFileHandle owns fds[0] for the VM's lifetime — we must NOT close
	// vzFile after that point (closing it would double-close the fd at the
	// cgo/ObjC boundary, an EBADF/abort that guardedNet's recover cannot
	// catch). vzOwned tracks the exact handoff moment.
	var netConfig *vz.VirtioNetworkDeviceConfiguration
	vzOwned := false
	err = guardedNet("attach", func() error {
		attachment, err := vz.NewFileHandleNetworkDeviceAttachment(vzFile)
		if err != nil {
			return err
		}
		vzOwned = true // fd handed off; never close vzFile from here on
		cfg, err := vz.NewVirtioNetworkDeviceConfiguration(attachment)
		if err != nil {
			return err
		}
		macAddr, err := vz.NewMACAddress(hw)
		if err != nil {
			return err
		}
		cfg.SetMACAddress(macAddr)
		netConfig = cfg
		return nil
	})
	if err != nil {
		if !vzOwned {
			_ = vzFile.Close()
		}
		_ = gtvFile.Close()
		return nil, nil, fmt.Errorf("attach: %w", err)
	}

	gtvConn, err := net.FileConn(gtvFile) // dups the fd; safe to Close() gtvFile after this
	if err != nil {
		// vz owns vzFile now — do not close it; only clean up our own end.
		_ = gtvFile.Close()
		return nil, nil, fmt.Errorf("attach: file conn: %w", err)
	}
	_ = gtvFile.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("netstack: AcceptVfkit panic for %s: %v", mac, r)
			}
		}()
		if err := s.vn.AcceptVfkit(ctx, gtvConn); err != nil {
			log.Printf("netstack: guest link %s closed: %v", mac, err)
		}
	}()

	// Keep vzFile reachable to the GC for the VM's lifetime: os.File installs a
	// finalizer that closes the fd when the *os.File becomes unreachable, which
	// would yank the descriptor out from under vz's NSFileHandle. Pin it inside
	// the cleanup closure (which outlives Attach and is held by the VM).
	cleanup := func() error {
		cancel() // unblocks AcceptVfkit's read loop
		err := gtvConn.Close()
		runtime.KeepAlive(vzFile) // vz-owned fd stays valid until teardown
		return err
	}
	return netConfig, cleanup, nil
}
