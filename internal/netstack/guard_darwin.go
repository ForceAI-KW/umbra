//go:build darwin && arm64

package netstack

import (
	"errors"
	"fmt"
)

// ErrVZPanic mirrors internal/vm's guard: a panic at the cgo/Objective-C
// boundary (Code-Hex/vz#124) must be converted to an error, never crash the
// daemon (PITFALLS P1). netstack does not import package vm (it would
// create an import cycle risk and cross unrelated concerns), so the
// recover pattern is duplicated here rather than shared.
var ErrVZPanic = errors.New("vz panicked")

func guardedNet(op string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w during %s: %v", ErrVZPanic, op, r)
		}
	}()
	return fn()
}
