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
%s%s`

// dockerWriteFiles renders the write_files entry for the dockerd systemd
// override. write_files runs before runcmd, so the file exists by the time
// runcmd's `systemctl daemon-reload` runs — avoids a multi-line heredoc
// inside a runcmd YAML list item.
const dockerWriteFiles = `write_files:
  - path: /etc/systemd/system/docker.service.d/override.conf
    owner: root:root
    permissions: "0644"
    content: |
      [Service]
      ExecStart=
      ExecStart=/usr/bin/dockerd -H fd:// -H tcp://0.0.0.0:2375
`

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

	writeFiles := ""
	if m.Role == registry.ReservedDockerName {
		writeFiles = dockerWriteFiles
	}
	userData := fmt.Sprintf(userDataTmpl, sshPub, writeFiles, runcmdSection(hosts, m.Role))
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

// runcmdSection assembles the single cloud-config runcmd: block cloud-init
// permits, merging hosts-propagation lines with the docker provisioning
// lines (when role is the reserved docker machine). Returns "" (no runcmd
// section) when there are no lines to emit.
func runcmdSection(hosts map[string]string, role string) string {
	lines := hostsRuncmdLines(hosts)
	if role == registry.ReservedDockerName {
		lines = append(lines, dockerRuncmdLines()...)
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("runcmd:\n")
	for _, line := range lines {
		fmt.Fprintf(&b, "  - %s\n", line)
	}
	return b.String()
}

// hostsRuncmdLines renders one printf line per hosts entry, appended to the
// guest's /etc/hosts for guest-to-guest name resolution. Entries with an
// empty IP are skipped. Not idempotent across reboots — fine for first
// boot, per M2 scope.
func hostsRuncmdLines(hosts map[string]string) []string {
	names := make([]string, 0, len(hosts))
	for name, ip := range hosts {
		if ip == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	lines := make([]string, 0, len(names))
	for _, name := range names {
		// printf, not `echo -e`: cloud-init runs runcmd via dash, whose echo
		// prints "-e" literally and doesn't expand \t (verified in-guest).
		lines = append(lines, fmt.Sprintf(`printf '%%s\t%%s.umbra.local %%s\n' '%s' '%s' '%s' >> /etc/hosts`, hosts[name], name, name))
	}
	return lines
}

// dockerRuncmdLines renders the docker provisioning runcmd lines for the
// reserved docker machine (verbatim intent from docs/research/dockerd-in-vm.md
// §1). dockerd is installed via get.docker.com (matches Lima's own
// docker.yaml template); its systemd unit is overridden (via the
// dockerWriteFiles entry, which write_files applies before runcmd runs) to
// also listen on tcp://0.0.0.0:2375, firewalled via iptables to only the
// netstack gateway IP — every guest VM shares the same L2 segment, and an
// unauthenticated docker TCP API is root-equivalent access.
func dockerRuncmdLines() []string {
	return []string{
		"command -v docker >/dev/null 2>&1 || (curl -fsSL https://get.docker.com | sh)",
		"usermod -aG docker umbra",
		"systemctl daemon-reload",
		"systemctl enable --now docker",
		"apt-get install -y iptables-persistent",
		fmt.Sprintf("iptables -A INPUT -p tcp --dport 2375 ! -s %s -j DROP", netstack.Gateway),
		"netfilter-persistent save",
	}
}
