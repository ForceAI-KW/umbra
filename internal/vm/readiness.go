package vm

import (
	"context"
	"fmt"
	"time"
)

// DefaultReadyTimeout bounds the whole boot-readiness wait (P6 — colima#629:
// unbounded waits hide the failing stage; 90s then a stage-named error).
const DefaultReadyTimeout = 90 * time.Second

type stageError struct {
	Stage  string
	Detail string
}

func (e *stageError) Error() string {
	return fmt.Sprintf("readiness stage %q timed out: %s", e.Stage, e.Detail)
}

func WaitReady(ctx context.Context, lookupIP func() (string, bool, error), dial func(addr string) error, timeout time.Duration) (string, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	tick := func() { time.Sleep(1 * time.Second) }

	var ip string
	for {
		if time.Now().After(deadline) {
			return "", &stageError{Stage: "ip", Detail: fmt.Sprintf("machine IP not available after %s (check console.log)", time.Since(start).Round(time.Second))}
		}
		got, ok, err := lookupIP()
		if err != nil {
			return "", fmt.Errorf("lease lookup: %w", err)
		}
		if ok {
			ip = got
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			tick()
		}
	}

	dialAttempts := 0
	for {
		if time.Now().After(deadline) {
			if dialAttempts == 0 {
				return "", &stageError{Stage: "ssh", Detail: fmt.Sprintf("ip stage consumed the whole %s readiness budget (lease %s arrived late); ssh was never attempted", timeout, ip)}
			}
			return "", &stageError{Stage: "ssh", Detail: fmt.Sprintf("port 22 on %s never accepted after %d attempts in %s (guest booted but sshd/cloud-init not ready — check console.log)", ip, dialAttempts, time.Since(start).Round(time.Second))}
		}
		dialAttempts++
		if dial(ip+":22") == nil {
			return ip, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			tick()
		}
	}
}
