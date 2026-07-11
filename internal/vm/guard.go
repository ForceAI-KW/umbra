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
