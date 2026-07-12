package singleton

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireIsExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "umbrad.lock")
	l1, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(p); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second acquire should fail clearly, got %v", err)
	}
	if err := l1.Close(); err != nil {
		t.Fatal(err)
	}
	l2, err := Acquire(p) // released → reacquirable
	if err != nil {
		t.Fatalf("reacquire after close: %v", err)
	}
	l2.Close()
}
