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
	deadline := time.Now().Add(timeout)
	tick := func() { time.Sleep(1 * time.Second) }

	var ip string
	for {
		if time.Now().After(deadline) {
			return "", &stageError{Stage: "ip", Detail: "no DHCP lease appeared for machine MAC (check /var/db/dhcpd_leases and console.log)"}
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

	for {
		if time.Now().After(deadline) {
			return "", &stageError{Stage: "ssh", Detail: fmt.Sprintf("port 22 on %s never accepted (guest booted but sshd/cloud-init not ready — check console.log)", ip)}
		}
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
