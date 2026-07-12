// Package cloudinit builds NoCloud seed ISOs (volume label "cidata").
package cloudinit

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kdomanski/iso9660"

	"github.com/ForceAI-KW/umbra/internal/netstack"
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
%s`

const metaDataTmpl = `instance-id: umbra-%s
local-hostname: %s
`

// networkConfigTmpl assigns the static IP the daemon allocated via ipalloc
// before boot. Static addressing sidesteps DHCP (and the Ubuntu
// DUID/bootpd trap) entirely: no DHCP client, no lease file, no
// dhcp-identifier dance.
const networkConfigTmpl = `version: 2
ethernets:
  all:
    match: { name: "en*" }
    dhcp4: false
    addresses: [ "%s/24" ]
    routes: [ { to: "default", via: "%s" } ]
    nameservers: { addresses: [ "%s" ] }
`

// BuildSeed writes <machineDir>/seed.iso. sshPub must be a single-line
// authorized_keys entry (as produced by sshkey.Ensure) — it is interpolated
// into YAML, so anything else is rejected to keep first-boot config
// injection-proof. m.IP must already be set (caller allocates it via
// ipalloc before calling BuildSeed). hosts is name->IP and is appended to
// the guest's /etc/hosts for guest-to-guest resolution; empty values are
// skipped.
func BuildSeed(m *registry.Machine, machineDir, sshPub string, hosts map[string]string) (string, error) {
	if strings.ContainsAny(sshPub, "\n\r") || !strings.HasPrefix(sshPub, "ssh-") {
		return "", fmt.Errorf("sshPub must be a single-line authorized_keys entry starting with \"ssh-\"")
	}
	if m.IP == "" {
		return "", fmt.Errorf("machine %q has no IP assigned", m.Name)
	}
	ip := net.ParseIP(m.IP)
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("machine %q has invalid IPv4 address %q", m.Name, m.IP)
	}

	w, err := iso9660.NewWriter()
	if err != nil {
		return "", err
	}
	defer w.Cleanup()

	userData := fmt.Sprintf(userDataTmpl, sshPub, hostsRuncmd(hosts))
	metaData := fmt.Sprintf(metaDataTmpl, m.Name, m.Name)
	networkConfig := fmt.Sprintf(networkConfigTmpl, m.IP, netstack.Gateway, netstack.Gateway)
	if err := w.AddFile(strings.NewReader(userData), "user-data"); err != nil {
		return "", err
	}
	if err := w.AddFile(strings.NewReader(metaData), "meta-data"); err != nil {
		return "", err
	}
	if err := w.AddFile(strings.NewReader(networkConfig), "network-config"); err != nil {
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

// hostsRuncmd renders a cloud-config runcmd block that appends one line per
// hosts entry to the guest's /etc/hosts, for guest-to-guest name
// resolution. Entries with an empty IP are skipped. Returns "" (no runcmd
// section) when hosts is empty. Not idempotent across reboots — fine for
// first boot, per M2 scope.
func hostsRuncmd(hosts map[string]string) string {
	names := make([]string, 0, len(hosts))
	for name, ip := range hosts {
		if ip == "" {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("runcmd:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "  - echo -e \"%s\\t%s.umbra.local %s\" >> /etc/hosts\n", hosts[name], name, name)
	}
	return b.String()
}
