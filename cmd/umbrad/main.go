// umbrad is the Umbra daemon: owns all VMs, serves the unix-socket API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ForceAI-KW/umbra/internal/api"
	"github.com/ForceAI-KW/umbra/internal/cloudinit"
	"github.com/ForceAI-KW/umbra/internal/image"
	"github.com/ForceAI-KW/umbra/internal/paths"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/sshkey"
	"github.com/ForceAI-KW/umbra/internal/vm"
	"github.com/ForceAI-KW/umbra/internal/vmnet"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("umbrad exiting", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if err := paths.EnsureTree(); err != nil {
		return err
	}
	reg := registry.New(paths.Machines())
	mgr := vm.NewManager(reg, paths.Machines())

	provision := func(ctx context.Context, m *registry.Machine) error {
		rawBase, err := image.Ensure(ctx, paths.Images(), m.Image)
		if err != nil {
			return err
		}
		mdir := paths.MachineDir(m.Name)
		if err := image.CloneDisk(rawBase, filepath.Join(mdir, "disk.img"), m.DiskGiB); err != nil {
			return err
		}
		pub, _, err := sshkey.Ensure(paths.SSH())
		if err != nil {
			return err
		}
		_, err = cloudinit.BuildSeed(m, mdir, pub)
		return err
	}

	ready := func(ctx context.Context, m *registry.Machine) (string, error) {
		ip, err := vm.WaitReady(ctx,
			func() (string, bool, error) { return vmnet.LookupIPFromFile(m.MAC) },
			func(addr string) error {
				c, err := net.DialTimeout("tcp", addr, 2*time.Second)
				if err == nil {
					c.Close()
				}
				return err
			},
			vm.DefaultReadyTimeout)
		if err != nil {
			return "", err
		}
		mgr.SetIP(m.Name, ip)
		return ip, nil
	}

	srv := api.NewServer(reg, mgr, provision, ready)

	sock := paths.APISocket()
	_ = os.Remove(sock) // stale socket from a previous run
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}

	httpSrv := &http.Server{Handler: srv.Handler()}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(l) }()
	logger.Info("umbrad listening", "socket", sock)

	// daemonCtx bounds every autostart goroutine's lifetime: cancelling it on
	// shutdown lets Start's entry ctx check and WaitReady's ctx select exit
	// fast, so wg.Wait() below doesn't stall the shutdown budget.
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()
	var autostartWG sync.WaitGroup

	// autostart-flagged machines (fwb-ci pattern; launchd wiring lands in M4)
	if machines, err := reg.List(); err == nil {
		for _, m := range machines {
			if m.Autostart {
				autostartWG.Add(1)
				go func(name string) {
					defer autostartWG.Done()
					if daemonCtx.Err() != nil {
						return
					}
					logger.Info("autostarting", "machine", name)
					ctx, cancel := context.WithTimeout(daemonCtx, 5*time.Minute)
					defer cancel()
					if err := mgr.Start(ctx, name); err != nil {
						logger.Error("autostart failed", "machine", name, "err", err)
						return
					}
					if mc, err := reg.Load(name); err == nil {
						if _, err := ready(ctx, mc); err != nil {
							logger.Error("autostart readiness failed", "machine", name, "err", err)
						}
					}
				}(m.Name)
			}
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case s := <-sig:
		logger.Info("shutting down", "signal", s.String())
	}

	daemonCancel()     // stop/short-circuit any in-flight or pending autostarts first
	autostartWG.Wait() // let them exit before StopAll touches the same instances

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	mgr.StopAll(shutdownCtx) // graceful → hard per VM (P8)
	_ = httpSrv.Shutdown(shutdownCtx)
	return nil
}
