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

	var netConfig *vz.VirtioNetworkDeviceConfiguration
	err = guardedNet("attach", func() error {
		attachment, err := vz.NewFileHandleNetworkDeviceAttachment(vzFile)
		if err != nil {
			return err
		}
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
		_ = vzFile.Close()
		_ = gtvFile.Close()
		return nil, nil, fmt.Errorf("attach: %w", err)
	}

	gtvConn, err := net.FileConn(gtvFile) // dups the fd; safe to Close() gtvFile after this
	if err != nil {
		_ = vzFile.Close()
		_ = gtvFile.Close()
		return nil, nil, fmt.Errorf("attach: file conn: %w", err)
	}
	_ = gtvFile.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := s.vn.AcceptVfkit(ctx, gtvConn); err != nil {
			log.Printf("netstack: guest link %s closed: %v", mac, err)
		}
	}()

	cleanup := func() error {
		cancel() // unblocks AcceptVfkit's read loop
		return gtvConn.Close()
	}
	return netConfig, cleanup, nil
}
