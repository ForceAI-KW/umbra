package image

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveNoble(t *testing.T) {
	url, sums, name, err := Resolve("ubuntu:noble")
	if err != nil {
		t.Fatal(err)
	}
	if name != "ubuntu-24.04-server-cloudimg-arm64.img" {
		t.Fatalf("name = %q", name)
	}
	if url != "https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-arm64.img" {
		t.Fatalf("url = %q", url)
	}
	if sums != "https://cloud-images.ubuntu.com/releases/noble/release/SHA256SUMS" {
		t.Fatalf("sums = %q", sums)
	}
}

func TestResolveUnknownRefErrors(t *testing.T) {
	if _, _, _, err := Resolve("arch:latest"); err == nil {
		t.Fatal("want error for unsupported ref")
	}
}

func TestParseSHA256SUMS(t *testing.T) {
	sums := []byte("abc123 *ubuntu-24.04-server-cloudimg-arm64.img\ndef456 *other.img\n")
	got, err := parseSHA256SUMS(sums, "ubuntu-24.04-server-cloudimg-arm64.img")
	if err != nil || got != "abc123" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseSHA256SUMS(sums, "missing.img"); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestCloneDiskTruncatesToSize(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.raw")
	if err := os.WriteFile(base, []byte("rawdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "disk.img")
	if err := CloneDisk(base, dst, 1); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(dst)
	if st.Size() != 1<<30 {
		t.Fatalf("size = %d, want %d", st.Size(), 1<<30)
	}
}
