package dockerbridge

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type tcpDialer struct{ addr string }

func (d tcpDialer) DialContextTCP(ctx context.Context, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", d.addr)
}

func TestBridgePipesToGuest(t *testing.T) {
	// echo server standing in for dockerd's TCP API
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()

	sock := filepath.Join("/tmp", "umbra-br-test.sock")
	os.Remove(sock)
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil { // stale file present
		t.Fatal(err)
	}
	b, err := Listen(tcpDialer{echo.Addr().String()}, sock, "192.168.127.10:2375")
	if err != nil {
		t.Fatalf("listen (stale removal): %v", err)
	}
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Serve(ctx)

	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("ping\n"))
	line, _ := bufio.NewReader(c).ReadString('\n')
	if line != "ping\n" {
		t.Fatalf("echoed %q", line)
	}
}
