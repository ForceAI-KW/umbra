//go:build darwin && arm64

package vm

import (
	"errors"
	"fmt"
	"log"

	"github.com/Code-Hex/vz/v3"
)

// RosettaAvailability reports the host's Rosetta-for-Linux support as a
// stable string, live-read on every call (never cached) so callers always
// see the current state — including after a macOS point update flips it
// (PITFALLS P5).
func RosettaAvailability() string {
	switch vz.LinuxRosettaDirectoryShareAvailability() {
	case vz.LinuxRosettaAvailabilityNotSupported:
		return "notSupported"
	case vz.LinuxRosettaAvailabilityNotInstalled:
		return "notInstalled"
	case vz.LinuxRosettaAvailabilityInstalled:
		return "installed"
	default:
		return "notSupported"
	}
}

// attachRosetta builds the "vz-rosetta" VirtioFS device that exposes Apple's
// Rosetta x86-64 translator to the guest (mounted + registered in
// binfmt_misc guest-side — see internal/cloudinit/seed.go's
// rosettaRuncmdLines), installing Rosetta first if this is its first use.
// Tag "vz-rosetta" matches lima-vm/lima's convention for the same
// Code-Hex/vz API (docs/research/rosetta-amd64.md §3).
//
// Install is a synchronous, potentially long-running (network-fetching)
// call — callers must run this off any latency-sensitive path, same as
// every other slow provisioning step already inside launchVZ's guarded()
// closure.
func attachRosetta() (*vz.VirtioFileSystemDeviceConfiguration, error) {
	switch vz.LinuxRosettaDirectoryShareAvailability() {
	case vz.LinuxRosettaAvailabilityNotSupported:
		return nil, errors.New("rosetta not supported (macOS <13)")
	case vz.LinuxRosettaAvailabilityNotInstalled:
		log.Printf("vm: installing rosetta (first use)...")
		log.Printf("vm: hint: try `softwareupdate --install-rosetta` if stuck")
		if err := vz.LinuxRosettaDirectoryShareInstallRosetta(); err != nil {
			return nil, fmt.Errorf("install rosetta: %w", err)
		}
	}

	fsCfg, err := vz.NewVirtioFileSystemDeviceConfiguration("vz-rosetta")
	if err != nil {
		return nil, err
	}
	// macOS-14+ AOT translation caching (vz.LinuxRosettaUnixSocketCachingOptions)
	// is deferred — optional perf optimization per docs/research/rosetta-amd64.md §3,
	// not required for `docker run --platform linux/amd64` to work.
	share, err := vz.NewLinuxRosettaDirectoryShare()
	if err != nil {
		return nil, err
	}
	fsCfg.SetDirectoryShare(share)
	return fsCfg, nil
}
