package export

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "disk.img"), []byte("DISKDATA"), 0o600)
	os.WriteFile(filepath.Join(src, "config.json"),
		[]byte(`{"name":"orig","cpus":3,"memory_mib":3072,"disk_gib":60,"image":"ubuntu:noble","mac":"aa:bb:cc:dd:ee:ff","autostart":true}`), 0o600)

	tarball := filepath.Join(t.TempDir(), "m.tar.gz")
	if err := Write(src, tarball); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	m, err := Read(tarball, dest)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "orig" || m.CPUs != 3 || !m.Autostart {
		t.Fatalf("config mangled: %+v", m)
	}
	b, err := os.ReadFile(filepath.Join(dest, "disk.img"))
	if err != nil || string(b) != "DISKDATA" {
		t.Fatalf("disk mangled: %q %v", b, err)
	}
}

func TestReadRejectsTraversal(t *testing.T) {
	// a tarball containing ../evil must not escape destDir
	// (build it by hand with archive/tar in the test)
	evil := buildEvilTar(t) // helper writing an entry named "../evil"
	if _, err := Read(evil, t.TempDir()); err == nil {
		t.Fatal("path traversal must be rejected")
	}
}

// buildEvilTar hand-builds a gzip'd tarball containing a single entry named
// "../evil" — Read must reject this before ever opening a file, since the
// entry name is not exactly "config.json" or "disk.img".
func buildEvilTar(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	content := []byte("evil payload")
	hdr := &tar.Header{
		Name: "../evil",
		Mode: 0o600,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
