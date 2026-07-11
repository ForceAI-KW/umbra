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

type panicOnStateVZ struct{ fakeVZ }

func (f *panicOnStateVZ) State() vzState {
	panic("runtime/cgo: misuse of an invalid Handle")
}

func TestGuardedStateRecoversPanicToVzUnknown(t *testing.T) {
	got := guardedState(&panicOnStateVZ{})
	if got != vzUnknown {
		t.Fatalf("want vzUnknown, got %v", got)
	}
	if got == vzStopped {
		t.Fatal("a panicked State() read must never be reported as vzStopped")
	}
}

func TestGuardedStatePassesThroughRealState(t *testing.T) {
	f := &fakeVZ{state: vzRunning}
	if got := guardedState(f); got != vzRunning {
		t.Fatalf("want vzRunning, got %v", got)
	}
}
