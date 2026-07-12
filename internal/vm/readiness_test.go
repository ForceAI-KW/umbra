package vm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitReadyHappyPath(t *testing.T) {
	ip, err := WaitReady(context.Background(),
		func() (string, bool, error) { return "192.168.64.5", true, nil },
		func(addr string) error { return nil },
		2*time.Second)
	if err != nil || ip != "192.168.64.5" {
		t.Fatalf("got %q, %v", ip, err)
	}
}

func TestWaitReadyNamesIPStageOnTimeout(t *testing.T) {
	_, err := WaitReady(context.Background(),
		func() (string, bool, error) { return "", false, nil },
		func(addr string) error { return nil },
		150*time.Millisecond)
	var se *stageError
	if !errors.As(err, &se) || se.Stage != "ip" {
		t.Fatalf("want ip stageError, got %v", err)
	}
}

func TestWaitReadyNamesSSHStageOnTimeout(t *testing.T) {
	_, err := WaitReady(context.Background(),
		func() (string, bool, error) { return "192.168.64.5", true, nil },
		func(addr string) error { return errors.New("refused") },
		150*time.Millisecond)
	var se *stageError
	if !errors.As(err, &se) || se.Stage != "ssh" {
		t.Fatalf("want ssh stageError, got %v", err)
	}
	if !strings.Contains(err.Error(), `stage "ssh"`) {
		t.Fatalf("error text: %v", err)
	}
}
