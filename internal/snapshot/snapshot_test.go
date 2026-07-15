package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTakeListRestoreRoundTrip(t *testing.T) {
	mdir := t.TempDir()
	sdir := filepath.Join(mdir, "snapshots")
	writeFile(t, filepath.Join(mdir, "disk.img"), "DISK-V1")
	writeFile(t, filepath.Join(mdir, "config.json"), `{"name":"x"}`)

	if err := Take(mdir, sdir, "s1"); err != nil {
		t.Fatal(err)
	}
	infos, err := List(sdir)
	if err != nil || len(infos) != 1 || infos[0].Name != "s1" {
		t.Fatalf("list=%v err=%v", infos, err)
	}

	writeFile(t, filepath.Join(mdir, "disk.img"), "DISK-V2-CORRUPT")
	if err := Restore(mdir, sdir, "s1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(mdir, "disk.img"))
	if string(b) != "DISK-V1" {
		t.Fatalf("restore did not bring back v1, got %q", b)
	}
}

func TestTakeDuplicateNameFails(t *testing.T) {
	mdir := t.TempDir()
	sdir := filepath.Join(mdir, "snapshots")
	writeFile(t, filepath.Join(mdir, "disk.img"), "D")
	writeFile(t, filepath.Join(mdir, "config.json"), "{}")
	if err := Take(mdir, sdir, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := Take(mdir, sdir, "s1"); err == nil {
		t.Fatal("duplicate snapshot name must fail")
	}
}

func TestRestoreMissingSnapshotFails(t *testing.T) {
	mdir := t.TempDir()
	if err := Restore(mdir, filepath.Join(mdir, "snapshots"), "nope"); err == nil {
		t.Fatal("want error for missing snapshot")
	}
}
