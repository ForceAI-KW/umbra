package dockerbridge

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// pollInterval is how often WaitDockerReady retries /_ping between attempts.
// Var (not const) so tests can shrink it without stretching wall-clock time.
var pollInterval = 1 * time.Second

// WaitDockerReady dials guestAddr (dockerVMIP:2375) via dial and GETs /_ping
// on the Docker Engine API until it returns 200 OK or timeout elapses. This
// is the P13 socket-race guard: dockerd inside the guest can still be
// starting up after the VM itself reports ready, so callers must poll the
// Engine API directly before wiring up the bridge/socket. On timeout the
// returned error names the "dockerd" stage, matching vm.WaitReady's
// stage-named-error discipline.
func WaitDockerReady(ctx context.Context, dial func(ctx context.Context, addr string) (net.Conn, error), guestAddr string, timeout time.Duration) error {
	start := time.Now()
	deadline := start.Add(timeout)

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dial(ctx, guestAddr)
			},
		},
	}
	defer client.CloseIdleConnections()

	for {
		reqCtx, cancel := context.WithDeadline(ctx, deadline)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://docker/_ping", nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					cancel()
					return nil
				}
			}
		}
		cancel()

		if time.Now().After(deadline) {
			return fmt.Errorf("readiness stage %q timed out: dockerd /_ping on %s did not return 200 after %s", "dockerd", guestAddr, time.Since(start).Round(time.Second))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
