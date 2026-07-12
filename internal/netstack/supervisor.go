package netstack

import (
	"context"
	"log/slog"
	"time"
)

// Supervisor guards the P3 (gvproxy UDP retry spin after sleep/wake) and P11
// (ENOBUFS global-write-lock spin) pathologies documented in
// docs/PITFALLS-EXTERNAL.md and docs/research/gvisor-tap-vsock-api.md §4e/§f:
// gvisor-tap-vsock exposes no rebuild/reset API, and per §f its data-plane
// connections self-heal on their own (fresh dial per new flow) once the host
// network stabilizes. There is nothing for Umbra to rebuild — the useful
// signal is detecting that the host slept/woke and probing whether any
// running machine failed to come back healthy, then logging loudly so the
// operator (or a future auto-recovery layer) knows.
//
// Detection is a pragmatic, cgo-free monotonic-gap heuristic: a ticker fires
// every interval; if the wall-clock gap between two ticks is much larger than
// interval, the host was almost certainly asleep in between. A real
// NSWorkspace sleep/wake notification (requires cgo/Obj-C) is deferred to
// M5's menu-bar app.
type Supervisor struct {
	probe         func(ctx context.Context) []string
	interval      time.Duration
	wakeThreshold time.Duration
	now           func() time.Time
	logger        *slog.Logger
}

// NewSupervisor builds a Supervisor. probe is called after a detected
// sleep/wake gap and must return the names of machines that failed their
// post-wake health check (e.g. a DialContextTCP to :22 timing out). probe's
// result is logged, not acted on — see the package doc comment above.
func NewSupervisor(probe func(ctx context.Context) []string) *Supervisor {
	return &Supervisor{
		probe:         probe,
		interval:      5 * time.Second,
		wakeThreshold: 30 * time.Second,
		now:           time.Now,
		logger:        slog.Default(),
	}
}

// runOnce evaluates a single tick given the previous tick's timestamp and
// returns the new "previous" timestamp plus whether probe was invoked. It is
// the deterministic core Run's ticker loop drives; tests call it directly to
// avoid depending on real wall-clock timing.
func (sv *Supervisor) runOnce(ctx context.Context, prev time.Time) (next time.Time, probed bool) {
	now := sv.now()
	elapsed := now.Sub(prev)
	if elapsed > sv.wakeThreshold {
		probed = true
		sv.logger.Info("sleep/wake detected", "gap", elapsed)
		unhealthy := sv.probe(ctx)
		if len(unhealthy) > 0 {
			sv.logger.Warn("machines unhealthy after wake", "machines", unhealthy)
		}
	}
	return now, probed
}

// Run blocks, ticking every sv.interval and checking for a sleep/wake gap on
// each tick, until ctx is cancelled. Call it in a goroutine.
func (sv *Supervisor) Run(ctx context.Context) {
	ticker := time.NewTicker(sv.interval)
	defer ticker.Stop()

	prev := sv.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prev, _ = sv.runOnce(ctx, prev)
		}
	}
}
