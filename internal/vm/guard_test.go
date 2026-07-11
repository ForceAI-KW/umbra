package vm

import (
	"errors"
	"testing"
)

func TestGuardedConvertsPanicToError(t *testing.T) {
	err := guarded("stop", func() error {
		panic("runtime/cgo: misuse of an invalid Handle")
	})
	if err == nil || !errors.Is(err, ErrVZPanic) {
		t.Fatalf("want ErrVZPanic, got %v", err)
	}
}

func TestGuardedPassesThroughError(t *testing.T) {
	want := errors.New("boom")
	if err := guarded("start", func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}
