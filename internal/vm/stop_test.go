package vm

import (
	"context"
	"testing"
	"time"
)

type fakeVZ struct {
	state          vzState
	requestStopped bool
	hardStopped    bool
	honorGraceful  bool // if true, transition to stopped after RequestStop
	honorHard      bool
}

func (f *fakeVZ) Start() error { f.state = vzRunning; return nil }
func (f *fakeVZ) RequestStop() (bool, error) {
	f.requestStopped = true
	if f.honorGraceful {
		f.state = vzStopped
	}
	return true, nil
}
func (f *fakeVZ) Stop() error {
	f.hardStopped = true
	if f.honorHard {
		f.state = vzStopped
	}
	return nil
}
func (f *fakeVZ) State() vzState { return f.state }

func TestStopGracefulPath(t *testing.T) {
	f := &fakeVZ{state: vzRunning, honorGraceful: true}
	err := stopWithEscalation(context.Background(), f, 50*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if f.hardStopped {
		t.Fatal("hard stop should not fire when graceful works")
	}
}

func TestStopEscalatesToHardKill(t *testing.T) {
	// panicked guest: RequestStop never lands (P8)
	f := &fakeVZ{state: vzRunning, honorGraceful: false, honorHard: true}
	err := stopWithEscalation(context.Background(), f, 20*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !f.hardStopped {
		t.Fatal("expected escalation to hard stop")
	}
}

func TestStopReportsFailureWhenNeverConfirmed(t *testing.T) {
	// zombie: even hard kill doesn't confirm (P9) — must NOT report clean stop
	f := &fakeVZ{state: vzRunning}
	if err := stopWithEscalation(context.Background(), f, 20*time.Millisecond, 50*time.Millisecond); err == nil {
		t.Fatal("want error when stop never confirmed")
	}
}

func TestStopNeverConfirmedWhenStateReadsPanic(t *testing.T) {
	// State() panics on every read (cgo/ObjC boundary misuse, P1). Even
	// though RequestStop/Stop "succeed", a panicking poll must never be
	// mistaken for a confirmed stop — waitState must see vzUnknown, not
	// vzStopped, and stopWithEscalation must report failure (P9).
	f := &panicOnStateVZ{fakeVZ{state: vzRunning, honorGraceful: true, honorHard: true}}
	if err := stopWithEscalation(context.Background(), f, 20*time.Millisecond, 50*time.Millisecond); err == nil {
		t.Fatal("want error when State() reads panic throughout")
	}
}
