// Package cloudinit builds NoCloud seed ISOs (volume label "cidata").
package cloudinit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kdomanski/iso9660"

	"github.com/ForceAI-KW/umbra/internal/registry"
)

const userDataTmpl = `#cloud-config
users:
  - name: umbra
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - %s
packages:
  - chrony
package_update: false
growpart:
  mode: auto
  devices: ["/"]
mounts:
  - [home, /mnt/mac, virtiofs, "defaults,nofail", "0", "0"]
ssh_pwauth: false
`

const metaDataTmpl = `instance-id: umbra-%s
local-hostname: %s
`

// BuildSeed writes <machineDir>/seed.iso. sshPub must be a single-line
// authorized_keys entry (as produced by sshkey.Ensure) — it is interpolated
// into YAML, so anything else is rejected to keep first-boot config
// injection-proof.
func BuildSeed(m *registry.Machine, machineDir, sshPub string) (string, error) {
	if strings.ContainsAny(sshPub, "\n\r") || !strings.HasPrefix(sshPub, "ssh-") {
		return "", fmt.Errorf("sshPub must be a single-line authorized_keys entry starting with \"ssh-\"")
	}
	w, err := iso9660.NewWriter()
	if err != nil {
		return "", err
	}
	defer w.Cleanup()

	userData := fmt.Sprintf(userDataTmpl, sshPub)
	metaData := fmt.Sprintf(metaDataTmpl, m.Name, m.Name)
	if err := w.AddFile(strings.NewReader(userData), "user-data"); err != nil {
		return "", err
	}
	if err := w.AddFile(strings.NewReader(metaData), "meta-data"); err != nil {
		return "", err
	}

	isoPath := filepath.Join(machineDir, "seed.iso")
	f, err := os.OpenFile(isoPath+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if err := w.WriteTo(f, "cidata"); err != nil {
		f.Close()
		os.Remove(isoPath + ".tmp")
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return isoPath, os.Rename(isoPath+".tmp", isoPath)
}
