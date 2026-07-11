package registry

import (
	"errors"
	"testing"
	"time"
)

func newTestRegistry(t *testing.T) *Registry { return New(t.TempDir()) }

func TestSaveLoadRoundtrip(t *testing.T) {
	r := newTestRegistry(t)
	m := &Machine{Name: "fwb-ci", CPUs: 4, MemoryMiB: 8192, DiskGiB: 60,
		Image: "ubuntu:noble", MAC: "a6:5e:00:11:22:33", Autostart: true,
		HostBuild: "25F84", CreatedAt: time.Now().UTC().Truncate(time.Second)}
	if err := r.Save(m); err != nil {
		t.Fatal(err)
	}
	got, err := r.Load("fwb-ci")
	if err != nil {
		t.Fatal(err)
	}
	if got.MAC != m.MAC || !got.Autostart || got.MemoryMiB != 8192 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestLoadMissingReturnsErrNotFound(t *testing.T) {
	if _, err := newTestRegistry(t).Load("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestListSortedAndDelete(t *testing.T) {
	r := newTestRegistry(t)
	for _, n := range []string{"bbb", "aaa"} {
		if err := r.Save(&Machine{Name: n, CPUs: 1, MemoryMiB: 1024, DiskGiB: 10, Image: "ubuntu:noble"}); err != nil {
			t.Fatal(err)
		}
	}
	l, _ := r.List()
	if len(l) != 2 || l[0].Name != "aaa" {
		t.Fatalf("list = %+v", l)
	}
	if err := r.Delete("aaa"); err != nil {
		t.Fatal(err)
	}
	if l, _ = r.List(); len(l) != 1 {
		t.Fatalf("after delete: %+v", l)
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"fwb-ci": true, "a": true, "UPPER": false, "-lead": false,
		"": false, "has space": false, "0123456789012345678901234567890123": false,
	} {
		if ValidName(name) != want {
			t.Fatalf("ValidName(%q) != %v", name, want)
		}
	}
}

func TestLoadAndDeleteRejectTraversalNames(t *testing.T) {
	r := newTestRegistry(t)
	if _, err := r.Load("../../etc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load traversal: want ErrNotFound, got %v", err)
	}
	if err := r.Delete("../../etc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete traversal: want ErrNotFound, got %v", err)
	}
}
