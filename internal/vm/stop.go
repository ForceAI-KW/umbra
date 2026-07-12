package vm

import (
	"context"
	"fmt"
	"time"
)

type vzState int

const (
	vzStopped vzState = iota
	vzRunning
	vzOther
	// vzUnknown is returned when a State() read panics (guardedState). It is
	// a distinct sentinel from vzStopped/vzRunning/vzOther and must never be
	// treated as a confirmed stop.
	vzUnknown
)

// vzHandle is the minimal seam over *vz.VirtualMachine so escalation logic
// is unit-testable off-mac.
type vzHandle interface {
	Start() error
	RequestStop() (bool, error)
	Stop() error
	State() vzState
}

// stopWithEscalation: graceful ACPI RequestStop → gracefulTimeout → hard
// Stop() → poll until confirmed stopped within hardTimeout (P8, P9). Never
// trust a stop call on send — only on observed state.
func stopWithEscalation(ctx context.Context, h vzHandle, gracefulTimeout, hardTimeout time.Duration) error {
	if h == nil {
		return nil // nothing was ever launched — vacuously stopped
	}
	_ = guarded("request-stop", func() error {
		_, err := h.RequestStop()
		return err
	}) // errors fall through to hard path
	if waitState(ctx, h, vzStopped, gracefulTimeout) {
		return nil
	}
	// closure, not a method value: evaluating h.Stop on a nil concrete handle
	// would panic before guarded's recover is installed
	if err := guarded("hard-stop", func() error { return h.Stop() }); err != nil {
		return fmt.Errorf("hard stop failed: %w", err)
	}
	if waitState(ctx, h, vzStopped, hardTimeout) {
		return nil
	}
	return fmt.Errorf("vm did not reach stopped state within %s after hard kill (zombie — P9)", hardTimeout)
}

// waitState polls h's state via guardedState (never a raw h.State() call —
// P1: a panic mid-teardown must not crash the poller, and a panicked read
// must never be mistaken for a confirmed stop).
func waitState(ctx context.Context, h vzHandle, want vzState, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if guardedState(h) == want {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return guardedState(h) == want
}
