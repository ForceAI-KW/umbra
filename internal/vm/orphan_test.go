package vm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ForceAI-KW/umbra/internal/registry"
)

// withOrphanSeams overrides the disk-holder probe + reaper seams for the
// duration of the test and restores them on cleanup (same pattern as
// withLaunchFn).
func withOrphanSeams(t *testing.T, holders func(disk string) []int, reap func(pid int) error) {
	t.Helper()
	prevH, prevR := diskHoldersFn, reapHolderFn
	if holders != nil {
		diskHoldersFn = holders
	}
	if reap != nil {
		reapHolderFn = reap
	}
	t.Cleanup(func() { diskHoldersFn, reapHolderFn = prevH, prevR })
}

// TestStartReapsOrphanDiskHolder covers the 2026-07-13 incident class: a
// SIGKILLed daemon (launchctl kickstart -k, crash, reinstall) orphans the
// vz XPC process, which keeps disk.img open while the fresh daemon's
// registry says the machine is stopped. Start() must reap the orphan before
// launching — a second VM on the same disk boot-loops and corrupts the
// guest fs.
func TestStartReapsOrphanDiskHolder(t *testing.T) {
	m, reg := newTestManager(t)
	saveMachine(t, reg, "vm1")
	withLaunchFn(t, fakeLaunch(&fakeVZ{state: vzRunning}, nil))

	reaped := []int{}
	// First probe: one orphan holds the disk. After a reap, it's gone.
	withOrphanSeams(t,
		func(disk string) []int {
			if len(reaped) > 0 {
				return nil
			}
			return []int{4242}
		},
		func(pid int) error {
			reaped = append(reaped, pid)
			return nil
		})

	if err := m.Start(context.Background(), "vm1"); err != nil {
		t.Fatalf("start should reap the orphan and proceed, got: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != 4242 {
		t.Fatalf("expected orphan pid 4242 reaped exactly once, got %v", reaped)
	}
}

// TestStartRefusesWhenHolderSurvivesReap: if the holder cannot be killed
// (or a non-vz process owns the disk — reapHolderFn refuses), Start must
// fail loudly instead of double-mounting.
func TestStartRefusesWhenHolderSurvivesReap(t *testing.T) {
	m, reg := newTestManager(t)
	saveMachine(t, reg, "vm1")
	launched := false
	withLaunchFn(t, func(mc *registry.Machine, dir string, st netStack) (vzHandle, func(), error) {
		launched = true
		return &fakeVZ{state: vzRunning}, func() {}, nil
	})
	withOrphanSeams(t,
		func(disk string) []int { return []int{4242} },
		func(pid int) error { return errors.New("pid 4242 is not a vz process; refusing to kill") })

	err := m.Start(context.Background(), "vm1")
	if err == nil {
		t.Fatal("start must refuse when the disk holder survives")
	}
	if launched {
		t.Fatal("launchFn must never run while the disk is held")
	}
	if !strings.Contains(err.Error(), "disk") {
		t.Fatalf("error should name the disk-holder condition, got: %v", err)
	}
}
