package vm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ForceAI-KW/umbra/internal/registry"
)

func newTestManager(t *testing.T) (*Manager, *registry.Registry) {
	t.Helper()
	reg := registry.New(t.TempDir())
	return NewManager(reg, t.TempDir()), reg
}

func saveMachine(t *testing.T, reg *registry.Registry, name string) {
	t.Helper()
	if err := reg.Save(&registry.Machine{
		Name: name, CPUs: 1, MemoryMiB: 512, DiskGiB: 10,
		Image: "ubuntu:noble", MAC: "a6:5e:00:11:22:33",
	}); err != nil {
		t.Fatal(err)
	}
}

// withLaunchFn overrides the package-level launchFn seam for the duration
// of the test and restores the previous value on cleanup.
func withLaunchFn(t *testing.T, fn func(m *registry.Machine, machinesDir string) (vzHandle, func(), error)) {
	t.Helper()
	prev := launchFn
	launchFn = fn
	t.Cleanup(func() { launchFn = prev })
}

func fakeLaunch(h vzHandle, err error) func(m *registry.Machine, machinesDir string) (vzHandle, func(), error) {
	return func(m *registry.Machine, machinesDir string) (vzHandle, func(), error) {
		if err != nil {
			return nil, nil, err
		}
		return h, func() {}, nil
	}
}

// TestFailedStopRefusesRestart covers finding (P9/manager): a stop that
// never confirms (zombie handle) must make Start() refuse to relaunch —
// otherwise disk.img could be double-mounted by a second live VM.
func TestFailedStopRefusesRestart(t *testing.T) {
	m, reg := newTestManager(t)
	saveMachine(t, reg, "vm1")
	// zombie: never reaches vzStopped no matter what RequestStop/Stop do.
	withLaunchFn(t, fakeLaunch(&fakeVZ{state: vzRunning}, nil))

	if err := m.Start(context.Background(), "vm1"); err != nil {
		t.Fatalf("initial start: %v", err)
	}

	// Bound stopWithEscalation's polling via a short-lived ctx so the test
	// doesn't wait out the manager's hardcoded 30s/60s escalation timeouts:
	// waitState returns as soon as ctx.Done() fires.
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := m.Stop(stopCtx, "vm1"); err == nil {
		t.Fatal("want stop error for a zombie VM that never confirms stopped")
	}

	if err := m.Start(context.Background(), "vm1"); err == nil {
		t.Fatal("want Start refused: previous stop left a live/zombie handle")
	}
	if got := m.Info("vm1").State; got != StateCrashed {
		t.Fatalf("want StateCrashed after unconfirmed stop, got %v", got)
	}
}

// TestSuccessfulStopAllowsRestart covers finding (P9/manager): a confirmed
// stop clears the handle, so Start() is allowed again.
func TestSuccessfulStopAllowsRestart(t *testing.T) {
	m, reg := newTestManager(t)
	saveMachine(t, reg, "vm1")
	withLaunchFn(t, fakeLaunch(&fakeVZ{state: vzRunning, honorGraceful: true}, nil))

	if err := m.Start(context.Background(), "vm1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.Stop(context.Background(), "vm1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := m.Info("vm1").State; got != StateStopped {
		t.Fatalf("want StateStopped after confirmed stop, got %v", got)
	}

	withLaunchFn(t, fakeLaunch(&fakeVZ{state: vzRunning, honorGraceful: true}, nil))
	if err := m.Start(context.Background(), "vm1"); err != nil {
		t.Fatalf("want Start allowed again after confirmed stop, got %v", err)
	}
	if got := m.Info("vm1").State; got != StateRunning {
		t.Fatalf("want StateRunning after restart, got %v", got)
	}
}

// TestLaunchErrorAllowsRetry covers finding (P9/manager): a launch failure
// never sets a live handle, so retry is allowed immediately (no stop
// required first).
func TestLaunchErrorAllowsRetry(t *testing.T) {
	m, reg := newTestManager(t)
	saveMachine(t, reg, "vm1")
	withLaunchFn(t, fakeLaunch(nil, errors.New("boom")))

	if err := m.Start(context.Background(), "vm1"); err == nil {
		t.Fatal("want launch error")
	}
	if got := m.Info("vm1").State; got != StateCrashed {
		t.Fatalf("want StateCrashed after launch failure, got %v", got)
	}

	withLaunchFn(t, fakeLaunch(&fakeVZ{state: vzRunning}, nil))
	if err := m.Start(context.Background(), "vm1"); err != nil {
		t.Fatalf("want retry allowed after a launch failure, got %v", err)
	}
	if got := m.Info("vm1").State; got != StateRunning {
		t.Fatalf("want StateRunning after retry, got %v", got)
	}
}

// TestSlowLaunchObservableAsStarting covers finding (per-instance op
// serialization): Start() must not hold i.mu across the whole launch, or
// Info() would block/stale-read instead of observing StateStarting while a
// slow launch is in flight.
func TestSlowLaunchObservableAsStarting(t *testing.T) {
	m, reg := newTestManager(t)
	saveMachine(t, reg, "vm1")

	launchStarted := make(chan struct{})
	release := make(chan struct{})
	withLaunchFn(t, func(mc *registry.Machine, machinesDir string) (vzHandle, func(), error) {
		close(launchStarted)
		<-release
		return &fakeVZ{state: vzRunning}, func() {}, nil
	})

	done := make(chan error, 1)
	go func() { done <- m.Start(context.Background(), "vm1") }()

	<-launchStarted
	if got := m.Info("vm1").State; got != StateStarting {
		t.Fatalf("want StateStarting while launch is in flight, got %v", got)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := m.Info("vm1").State; got != StateRunning {
		t.Fatalf("want StateRunning after launch completes, got %v", got)
	}
}

// TestStartHonorsCanceledContext covers finding (ctx honored): a context
// canceled before Start begins must short-circuit without attempting a
// launch.
func TestStartHonorsCanceledContext(t *testing.T) {
	m, reg := newTestManager(t)
	saveMachine(t, reg, "vm1")
	launchCalled := false
	withLaunchFn(t, func(mc *registry.Machine, machinesDir string) (vzHandle, func(), error) {
		launchCalled = true
		return &fakeVZ{state: vzRunning}, func() {}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Start(ctx, "vm1"); err == nil {
		t.Fatal("want error for an already-canceled context")
	}
	if launchCalled {
		t.Fatal("launchFn must not be called for an already-canceled context")
	}
	if got := m.Info("vm1").State; got != StateStopped {
		t.Fatalf("want StateStopped after early cancel, got %v", got)
	}
}

// TestStartErrorsWhenLaunchFnNil covers the platform-unsupported seam: if
// launchFn was never wired (non-darwin/arm64 build), Start() must fail with
// a clear error instead of nil-dereferencing.
func TestStartErrorsWhenLaunchFnNil(t *testing.T) {
	m, reg := newTestManager(t)
	saveMachine(t, reg, "vm1")
	prev := launchFn
	launchFn = nil
	t.Cleanup(func() { launchFn = prev })

	if err := m.Start(context.Background(), "vm1"); err == nil {
		t.Fatal("want error when launchFn is nil")
	}
}

// Regression: a machine crashed by a FAILED LAUNCH has state=Crashed with a
// nil handle. Stop()/StopAll() on it must not panic the daemon (nil method
// value reached stopWithEscalation's hard path before the guard installed)
// and must settle the machine back to Stopped.
func TestStopAfterFailedLaunchDoesNotPanic(t *testing.T) {
	m, reg := newTestManager(t)
	saveMachine(t, reg, "m1")
	withLaunchFn(t, fakeLaunch(nil, errors.New("boom")))

	if err := m.Start(context.Background(), "m1"); err == nil {
		t.Fatal("want launch error")
	}
	if got := m.Info("m1").State; got != StateCrashed {
		t.Fatalf("state after failed launch: %v", got)
	}
	if err := m.Stop(context.Background(), "m1"); err != nil {
		t.Fatalf("Stop on crashed/nil-handle machine: %v", err)
	}
	if got := m.Info("m1").State; got != StateStopped {
		t.Fatalf("state after Stop: %v", got)
	}
	m.StopAll(context.Background()) // must not panic either
}
