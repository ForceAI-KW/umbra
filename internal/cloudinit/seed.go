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
%sssh_pwauth: false
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

// ciRunnerWriteFiles renders the write_files entry for the ensure-docker
// systemd oneshot. write_files runs before runcmd, so the unit file exists
// by the time runcmd's `systemctl enable ensure-docker.service` runs.
const ciRunnerWriteFiles = `write_files:
  - path: /etc/systemd/system/ensure-docker.service
    permissions: '0644'
    content: |
      # Reprovisions docker if a mid-cloud-init reboot interrupted
      # get.docker.com (cloud-init runcmd is once-per-instance and won't
      # retry). ConditionPathExists makes healthy boots a no-op.
      [Unit]
      Description=Ensure docker engine is installed
      Wants=network-online.target
      After=network-online.target
      ConditionPathExists=!/usr/bin/docker
      [Service]
      Type=oneshot
      ExecStart=/bin/sh -c 'curl -fsSL https://get.docker.com | sh && usermod -aG docker umbra'
      TimeoutStartSec=600
      [Install]
      WantedBy=multi-user.target
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
	switch m.Role {
	case registry.ReservedDockerName:
		writeFiles = dockerWriteFiles
	case registry.RoleCIRunner:
		writeFiles = ciRunnerWriteFiles
	}
	userData := fmt.Sprintf(userDataTmpl, sshPub, mountsSection(m.Role), writeFiles, runcmdSection(hosts, m.Role))
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

// needsRosetta reports whether role requires the Rosetta amd64 support
// added in M6: the "vz-rosetta" virtiofs share (mountsSection) plus the
// F-flagged binfmt_misc handler (rosettaRuncmdLines) that lets
// `docker run --platform linux/amd64` work inside the guest. Both the
// reserved docker machine and CI runners need it.
func needsRosetta(role string) bool {
	return role == registry.ReservedDockerName || role == registry.RoleCIRunner
}

// mountsSection renders the cloud-config `mounts:` list: the "home" share
// (all machines) plus the Rosetta virtiofs share for roles that need amd64
// emulation (see needsRosetta). Tag "vz-rosetta" matches the directory
// share configured in internal/vm/config_darwin.go.
func mountsSection(role string) string {
	lines := []string{`[home, /mnt/mac, virtiofs, "defaults,nofail", "0", "0"]`}
	if needsRosetta(role) {
		lines = append(lines, `[vz-rosetta, /mnt/rosetta, virtiofs, "defaults,nofail", "0", "0"]`)
	}
	var b strings.Builder
	b.WriteString("mounts:\n")
	for _, line := range lines {
		fmt.Fprintf(&b, "  - %s\n", line)
	}
	return b.String()
}

// runcmdSection assembles the single cloud-config runcmd: block cloud-init
// permits, merging hosts-propagation lines with the docker/ci-runner
// provisioning lines (when role matches). Returns "" (no runcmd section)
// when there are no lines to emit.
func runcmdSection(hosts map[string]string, role string) string {
	lines := hostsRuncmdLines(hosts)
	switch role {
	case registry.ReservedDockerName:
		lines = append(lines, dockerRuncmdLines()...)
	case registry.RoleCIRunner:
		lines = append(lines, ciRunnerRuncmdLines()...)
	}
	if needsRosetta(role) {
		lines = append(lines, rosettaRuncmdLines()...)
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
		// Close the firewall on tcp:2375 BEFORE dockerd can bind it, so the
		// unauthenticated API is never reachable subnet-wide (other guests,
		// incl. an untrusted CI runner) even for the boot-time window.
		fmt.Sprintf("iptables -A INPUT -p tcp --dport 2375 ! -s %s -j DROP", netstack.Gateway),
		"command -v docker >/dev/null 2>&1 || (curl -fsSL https://get.docker.com | sh)",
		"usermod -aG docker umbra",
		"systemctl daemon-reload",
		// restart, not `enable --now`: get.docker.com already starts dockerd,
		// so `enable --now` no-ops on the running unit and our tcp:2375
		// ExecStart override never takes effect. restart forces the override
		// (the likely cause of intermittent /_ping readiness timeouts).
		"systemctl enable docker",
		"systemctl restart docker",
		// Persist the rule across reboots (iptables-persistent installs the
		// netfilter-persistent save hook). DEBIAN_FRONTEND avoids the
		// interactive "save current rules?" debconf prompt hanging boot.
		"DEBIAN_FRONTEND=noninteractive apt-get install -y iptables-persistent",
		"netfilter-persistent save",
	}
}

// ciRunnerRuncmdLines renders the docker provisioning runcmd lines for a
// GitHub Actions self-hosted CI runner (docs/research/launchd-and-ci-cutover.md
// §4). Deliberately a subset of dockerRuncmdLines: plain docker via
// get.docker.com, no dockerWriteFiles systemd override and no tcp:2375
// iptables rule — a CI runner's dockerd must stay local-socket only, since
// it runs untrusted PR code and must never be reachable over the network.
//
// Also installs the base build toolchain (build-essential/pkg-config + common
// CLI tools). Real CI jobs run `npm ci`/`node-gyp` etc. that compile native
// modules and need a C/C++ toolchain — the bare cloud image lacks it, so a
// docker-only runner fails at "Install dependencies". node/pnpm themselves are
// provided per-run by the workflows' setup-node action, so they're not here.
func ciRunnerRuncmdLines() []string {
	return []string{
		"DEBIAN_FRONTEND=noninteractive apt-get update -qq",
		"DEBIAN_FRONTEND=noninteractive apt-get install -y build-essential pkg-config git curl unzip jq",
		"command -v docker >/dev/null 2>&1 || (curl -fsSL https://get.docker.com | sh)",
		"usermod -aG docker umbra",
		"systemctl enable docker",
		"systemctl restart docker",
		// 4 GiB swap: a 3 GiB CI guest OOM-kills heavy jobs (eslint/next build,
		// exit 137) AND takes the runner service down with them (2026-07-15
		// incident). Swap makes peak jobs slow instead of dead.
		`test -f /swapfile || (fallocate -l 4G /swapfile && chmod 600 /swapfile && mkswap /swapfile)`,
		`swapon /swapfile || true`,
		`grep -q '/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab`,
		`systemctl daemon-reload`,
		`systemctl enable ensure-docker.service`,
	}
}

// rosettaRuncmdLines renders the binfmt_misc registration for the Rosetta
// x86-64 ELF handler, mounted at "vz-rosetta" (config_darwin.go) → /mnt/rosetta.
// The F flag is required: without it the handler can't resolve the
// interpreter path from inside a container's mount namespace, so
// `docker run --platform linux/amd64` would fail even though a bare host
// exec of an amd64 binary would work. Magic/mask/flags verified against
// lima-vm/lima's shipping boot.Linux/05-rosetta-volume.sh (same Code-Hex/vz
// mount mechanism) — see docs/research/rosetta-amd64.md §5. Uses printf, not
// `echo -e`: cloud-init's runcmd executes via dash, whose builtin echo
// doesn't expand \x escapes (same gotcha as hostsRuncmdLines above). The
// binfmt_misc mount-then-register isn't idempotent across every possible
// reboot ordering (mirrors the caveat already on hostsRuncmdLines) — fine
// for first boot, per M2/M6 scope.
func rosettaRuncmdLines() []string {
	return []string{
		`mountpoint -q /proc/sys/fs/binfmt_misc || mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc`,
		`test -f /proc/sys/fs/binfmt_misc/rosetta || printf ':rosetta:M::\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00:\xff\xff\xff\xff\xff\xfe\xfe\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff:/mnt/rosetta/rosetta:OCF' > /proc/sys/fs/binfmt_misc/register`,
	}
}
