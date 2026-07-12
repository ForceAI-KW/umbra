package netstack

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock returns a fixed sequence of timestamps, one per call, so tests
// can drive Supervisor.runOnce deterministically without real sleeps.
func fakeClock(times ...time.Time) func() time.Time {
	i := 0
	return func() time.Time {
		t := times[i]
		if i < len(times)-1 {
			i++
		}
		return t
	}
}

func TestRunOnceProbesOnLargeGap(t *testing.T) {
	base := time.Unix(0, 0)
	var probeCalls int32
	sv := NewSupervisor(func(ctx context.Context) []string {
		atomic.AddInt32(&probeCalls, 1)
		return []string{"vm-a", "vm-b"}
	})
	sv.wakeThreshold = 30 * time.Second
	sv.now = fakeClock(base.Add(60 * time.Second)) // 60s gap > 30s threshold

	next, probed := sv.runOnce(context.Background(), base)

	if !probed {
		t.Fatal("want probed=true on large gap")
	}
	if got := atomic.LoadInt32(&probeCalls); got != 1 {
		t.Fatalf("want probe called exactly once, got %d", got)
	}
	if !next.Equal(base.Add(60 * time.Second)) {
		t.Fatalf("want next=base+60s, got %v", next)
	}
}

func TestRunOnceSurfacesUnhealthyNames(t *testing.T) {
	base := time.Unix(0, 0)
	var captured []string
	sv := NewSupervisor(func(ctx context.Context) []string {
		names := []string{"vm-a", "vm-b"}
		captured = names
		return names
	})
	sv.wakeThreshold = 30 * time.Second
	sv.now = fakeClock(base.Add(45 * time.Second))

	if _, probed := sv.runOnce(context.Background(), base); !probed {
		t.Fatal("want probed=true")
	}
	if len(captured) != 2 || captured[0] != "vm-a" || captured[1] != "vm-b" {
		t.Fatalf("want probe result surfaced, got %v", captured)
	}
}

func TestRunOnceNoProbeOnNormalTick(t *testing.T) {
	base := time.Unix(0, 0)
	var probeCalls int32
	sv := NewSupervisor(func(ctx context.Context) []string {
		atomic.AddInt32(&probeCalls, 1)
		return nil
	})
	sv.interval = 5 * time.Second
	sv.wakeThreshold = 30 * time.Second
	sv.now = fakeClock(base.Add(5 * time.Second)) // 5s gap == interval, well under threshold

	_, probed := sv.runOnce(context.Background(), base)

	if probed {
		t.Fatal("want probed=false on normal tick")
	}
	if got := atomic.LoadInt32(&probeCalls); got != 0 {
		t.Fatalf("want probe not called, got %d calls", got)
	}
}

func TestRunOnceMultipleNormalTicksNeverProbe(t *testing.T) {
	base := time.Unix(0, 0)
	var probeCalls int32
	sv := NewSupervisor(func(ctx context.Context) []string {
		atomic.AddInt32(&probeCalls, 1)
		return nil
	})
	sv.interval = 5 * time.Second
	sv.wakeThreshold = 30 * time.Second
	sv.now = fakeClock(
		base.Add(5*time.Second),
		base.Add(10*time.Second),
		base.Add(15*time.Second),
		base.Add(20*time.Second),
	)

	prev := base
	for i := 0; i < 4; i++ {
		var probed bool
		prev, probed = sv.runOnce(context.Background(), prev)
		if probed {
			t.Fatalf("tick %d: want probed=false, got true", i)
		}
	}
	if got := atomic.LoadInt32(&probeCalls); got != 0 {
		t.Fatalf("want probe never called across normal ticks, got %d", got)
	}
}

func TestRunExitsPromptlyOnContextCancel(t *testing.T) {
	sv := NewSupervisor(func(ctx context.Context) []string { return nil })
	sv.interval = time.Hour // never ticks during the test

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sv.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of ctx cancellation")
	}
}
