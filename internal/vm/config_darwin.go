//go:build darwin && arm64

package vm

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/Code-Hex/vz/v3"

	"github.com/ForceAI-KW/umbra/internal/registry"
)

// init wires the manager's testable launchFn seam to the real vz launch on
// darwin/arm64; on other platforms launchFn stays nil and Start() errors.
func init() {
	launchFn = launchVZ
}

// realVZ adapts *vz.VirtualMachine to vzHandle.
type realVZ struct{ vm *vz.VirtualMachine }

func (r *realVZ) Start() error               { return guarded("start", func() error { return r.vm.Start() }) }
func (r *realVZ) RequestStop() (bool, error) { return r.vm.RequestStop() }
func (r *realVZ) Stop() error                { return r.vm.Stop() }
func (r *realVZ) State() vzState {
	switch r.vm.State() {
	case vz.VirtualMachineStateStopped, vz.VirtualMachineStateError:
		return vzStopped
	case vz.VirtualMachineStateRunning:
		return vzRunning
	default:
		return vzOther
	}
}

// launchVZ builds the configuration and starts the VM. Every vz call is
// inside guarded() — a cgo panic marks this VM crashed, not the daemon (P1).
func launchVZ(m *registry.Machine, machinesDir string, st netStack) (vzHandle, func(), error) {
	mdir := filepath.Join(machinesDir, m.Name)
	var handle *realVZ
	var attachCleanup func() error
	err := guarded("launch", func() error {
		bootLoader, err := efiBootLoader(mdir)
		if err != nil {
			return err
		}
		platform, err := genericPlatform(mdir)
		if err != nil {
			return err
		}
		cfg, err := vz.NewVirtualMachineConfiguration(bootLoader, m.CPUs, m.MemoryMiB*1024*1024)
		if err != nil {
			return err
		}
		cfg.SetPlatformVirtualMachineConfiguration(platform)

		// storage: root disk + cloud-init seed
		var storages []vz.StorageDeviceConfiguration
		for _, img := range []string{filepath.Join(mdir, "disk.img"), filepath.Join(mdir, "seed.iso")} {
			att, err := vz.NewDiskImageStorageDeviceAttachment(img, false)
			if err != nil {
				return fmt.Errorf("attach %s: %w", img, err)
			}
			blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
			if err != nil {
				return err
			}
			storages = append(storages, blk)
		}
		cfg.SetStorageDevicesVirtualMachineConfiguration(storages)

		// network: attach directly to the embedded netstack via an
		// AF_UNIX/SOCK_DGRAM socketpair (M2) — replaces M1's kernel NAT
		// device. attachCleanup must run exactly once even if a later step
		// in this closure fails, so it's captured before any error can
		// short-circuit the function (handled in the err != nil branch
		// below).
		netCfg, cleanup, err := st.Attach(m.MAC)
		if err != nil {
			return err
		}
		attachCleanup = cleanup
		cfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netCfg})

		// virtiofs: share $HOME as tag "home" (mounted at /mnt/mac by cloud-init)
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home directory: %w", err)
		}
		fsCfg, err := vz.NewVirtioFileSystemDeviceConfiguration("home")
		if err != nil {
			return err
		}
		shared, err := vz.NewSharedDirectory(home, false)
		if err != nil {
			return err
		}
		single, err := vz.NewSingleDirectoryShare(shared)
		if err != nil {
			return err
		}
		fsCfg.SetDirectoryShare(single)
		cfg.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{fsCfg})

		// serial console → log file
		serialAtt, err := vz.NewFileSerialPortAttachment(filepath.Join(mdir, "console.log"), false)
		if err != nil {
			return err
		}
		serial, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAtt)
		if err != nil {
			return err
		}
		cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serial})

		// entropy
		entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
		if err != nil {
			return err
		}
		cfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

		if ok, verr := cfg.Validate(); !ok {
			if verr == nil {
				verr = errors.New("invalid configuration")
			}
			return fmt.Errorf("vz config invalid: %w", verr)
		}
		machine, err := vz.NewVirtualMachine(cfg)
		if err != nil {
			return err
		}
		if err := machine.Start(); err != nil {
			return err
		}
		handle = &realVZ{vm: machine}
		return nil
	})
	if err != nil {
		// attachCleanup may be set even though launch ultimately failed
		// (e.g. a later step errored, or guarded() caught a panic) — the
		// netstack docs require it be invoked exactly once regardless, or
		// the AcceptVfkit goroutine and both socket fds leak.
		if attachCleanup != nil {
			if cerr := attachCleanup(); cerr != nil {
				log.Printf("vm: netstack detach %s after failed launch: %v", m.Name, cerr)
			}
		}
		return nil, nil, err
	}
	stopFn := func() {
		if cerr := attachCleanup(); cerr != nil {
			log.Printf("vm: netstack detach %s: %v", m.Name, cerr)
		}
	}
	return handle, stopFn, nil
}

func efiBootLoader(mdir string) (vz.BootLoader, error) {
	storePath := filepath.Join(mdir, "efi-vars.fd")
	if _, err := os.Stat(storePath); os.IsNotExist(err) {
		if _, err := vz.NewEFIVariableStore(storePath, vz.WithCreatingEFIVariableStore()); err != nil {
			return nil, err
		}
	}
	store, err := vz.NewEFIVariableStore(storePath)
	if err != nil {
		return nil, err
	}
	return vz.NewEFIBootLoader(vz.WithEFIVariableStore(store))
}

func genericPlatform(mdir string) (vz.PlatformConfiguration, error) {
	idPath := filepath.Join(mdir, "machine-id.bin")
	var mid *vz.GenericMachineIdentifier
	if b, err := os.ReadFile(idPath); err == nil {
		mid, err = vz.NewGenericMachineIdentifierWithData(b)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		mid, err = vz.NewGenericMachineIdentifier()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(idPath, mid.DataRepresentation(), 0o600); err != nil {
			return nil, err
		}
	}
	return vz.NewGenericPlatformConfiguration(vz.WithGenericMachineIdentifier(mid))
}
