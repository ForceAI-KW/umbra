// Package vm owns VM lifecycle. All vz calls are guarded: a panic in the
// cgo/Objective-C boundary (Code-Hex/vz#124) must crash ONE VM's state, never
// the daemon (PITFALLS P1).
package vm

import (
	"errors"
	"fmt"
)

var ErrVZPanic = errors.New("vz panicked")

func guarded(op string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w during %s: %v", ErrVZPanic, op, r)
		}
	}()
	return fn()
}

// guardedState safely reads h.State(), recovering a panic at the cgo/ObjC
// boundary (Code-Hex/vz#124) instead of letting it propagate mid-teardown.
// A panicked read returns vzUnknown, which must never satisfy a
// waitState(want=vzStopped) check — a failed read must never be mistaken
// for a confirmed stop.
func guardedState(h vzHandle) (state vzState) {
	defer func() {
		if r := recover(); r != nil {
			state = vzUnknown
		}
	}()
	return h.State()
}
