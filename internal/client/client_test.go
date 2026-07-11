package client

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortSocketDir returns a short-path temp dir for unix socket tests. It
// deliberately avoids t.TempDir(), whose path is rooted under $TMPDIR — on
// macOS that's often /var/folders/.../T/..., long enough to overflow
// AF_UNIX's ~104-byte sun_path limit. /tmp (symlinked to /private/tmp on
// darwin) stays well under that.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "umbra-client-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// Daemon socket appears 400ms after the client's first attempt — retry must
// absorb the race (P10, apple/container#672).
func TestClientRetriesUntilSocketAppears(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "api.sock")
	go func() {
		time.Sleep(400 * time.Millisecond)
		l, err := net.Listen("unix", sock)
		if err != nil {
			t.Error(err)
			return
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"ok":true}`))
		})
		http.Serve(l, mux)
	}()
	c := New(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping after retries: %v", err)
	}
}

func TestClientGivesUpWhenNoDaemon(t *testing.T) {
	c := New(filepath.Join(shortSocketDir(t), "nope.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err == nil {
		t.Fatal("want error when daemon never appears")
	}
}
