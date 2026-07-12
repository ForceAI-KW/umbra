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
	"github.com/ForceAI-KW/umbra/internal/ipalloc"
	"github.com/ForceAI-KW/umbra/internal/netstack"
	"github.com/ForceAI-KW/umbra/internal/paths"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/singleton"
	"github.com/ForceAI-KW/umbra/internal/sshkey"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

// forwarderAdapter adapts *netstack.Stack's []netstack.ForwardView return
// type to api.Forwarder's []api.ForwardView, so internal/api never needs to
// import internal/netstack.
type forwarderAdapter struct{ st *netstack.Stack }

func (a forwarderAdapter) Expose(protocol, local, remote string) error {
	return a.st.Expose(protocol, local, remote)
}
func (a forwarderAdapter) Unexpose(protocol, local string) error {
	return a.st.Unexpose(protocol, local)
}
func (a forwarderAdapter) Forwards() ([]api.ForwardView, error) {
	fs, err := a.st.Forwards()
	if err != nil {
		return nil, err
	}
	out := make([]api.ForwardView, len(fs))
	for i, f := range fs {
		out[i] = api.ForwardView{Local: f.Local, Remote: f.Remote, Protocol: f.Protocol}
	}
	return out, nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger) // so components using slog.Default() (supervisor) share this handler
	if err := run(logger); err != nil {
		logger.Error("umbrad exiting", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if err := paths.EnsureTree(); err != nil {
		return err
	}
	// Single-instance guard: a second umbrad (e.g. `make run-daemon` against a
	// running LaunchAgent copy) must fail fast, not race the socket bind or,
	// worse, drive the same VM disks concurrently.
	lock, err := singleton.Acquire(paths.LockFile())
	if err != nil {
		return err
	}
	defer lock.Close()

	reg := registry.New(paths.Machines())

	st, err := netstack.New()
	if err != nil {
		return err
	}
	res, err := netstack.NewResolver()
	if err != nil {
		return err
	}
	if err := netstack.InstallResolverFile(res.Port()); err != nil {
		if errors.Is(err, netstack.ErrResolverPermission) {
			logger.Warn("cannot write /etc/resolver/umbra.local — host-side *.umbra.local lookups won't work (guest /etc/hosts and umbra shell/forward still do); fix with: sudo sh -c 'printf \"nameserver 127.0.0.1\\nport %d\\n\" > /etc/resolver/umbra.local'", "port", res.Port(), "err", err)
		} else {
			logger.Warn("failed to install /etc/resolver/umbra.local, continuing without host-side DNS", "err", err)
		}
	}

	mgr := vm.NewManager(reg, paths.Machines(), st, res)

	provision := func(ctx context.Context, m *registry.Machine) error {
		used, err := reg.UsedIPs()
		if err != nil {
			return err
		}
		ip, err := ipalloc.Allocate(netstack.Subnet, netstack.Gateway, netstack.FirstHost, used)
		if err != nil {
			return err
		}
		m.IP = ip
		if err := reg.Save(m); err != nil { // persist before build so a later BuildSeed/reg.List sees it
			return err
		}

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
		machines, err := reg.List()
		if err != nil {
			return err
		}
		hosts := make(map[string]string, len(machines))
		for _, mc := range machines {
			hosts[mc.Name] = mc.IP
		}
		_, err = cloudinit.BuildSeed(m, mdir, pub, hosts)
		return err
	}

	ready := func(ctx context.Context, m *registry.Machine) (string, error) {
		ip, err := vm.WaitReady(ctx,
			func() (string, bool, error) { return m.IP, true, nil }, // IP is known at create time (static addressing); no lease wait
			func(addr string) error {
				// Per-attempt deadline so a booted-then-silent guest can't
				// consume the whole WaitReady budget on one blocked dial —
				// WaitReady's 90s bound (P6) only checks between attempts.
				dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				c, err := st.DialContextTCP(dialCtx, addr)
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

	// daemonCtx bounds every background goroutine's lifetime (autostart, the
	// supervisor, and — from Task 5 — the docker socket bridge's Serve loop):
	// cancelling it on shutdown lets them all exit promptly so the wg.Waits
	// below don't stall the shutdown budget. Created here (rather than at its
	// former spot right before autostart) so dockerController can capture it.
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()
	var bridgeWG sync.WaitGroup

	docker := newDockerController(reg, mgr, st, provision, ready, logger, daemonCtx, &bridgeWG)

	srv := api.NewServer(reg, mgr, provision, ready, forwarderAdapter{st: st}, docker, vm.RosettaAvailability)

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

	// Supervisor watches for a sleep/wake gap and probes running machines'
	// SSH health afterward; gvisor connections self-heal per-connection
	// (P3/P11, no rebuild API), so this only detects + logs loudly, best
	// effort and non-fatal. See internal/netstack/supervisor.go.
	var supervisorWG sync.WaitGroup
	supervisorWG.Add(1)
	go func() {
		defer supervisorWG.Done()
		probe := func(ctx context.Context) []string {
			var unhealthy []string
			for _, info := range mgr.List() {
				if info.State != vm.StateRunning || info.IP == "" {
					continue
				}
				dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				c, err := st.DialContextTCP(dialCtx, net.JoinHostPort(info.IP, "22"))
				cancel()
				if err != nil {
					unhealthy = append(unhealthy, info.Name)
					continue
				}
				c.Close()
			}
			return unhealthy
		}
		netstack.NewSupervisor(probe).Run(daemonCtx)
	}()

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

	daemonCancel()      // stop/short-circuit any in-flight or pending autostarts, the supervisor, and the docker bridge's Serve loop first
	autostartWG.Wait()  // let them exit before StopAll touches the same instances
	supervisorWG.Wait() // let the supervisor's Run return before StopAll touches the same instances
	bridgeWG.Wait()     // let the docker bridge's Serve return (its listener is closed by daemonCtx cancellation above)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	mgr.StopAll(shutdownCtx) // graceful → hard per VM (P8)
	if err := res.Shutdown(); err != nil {
		logger.Warn("dns resolver shutdown", "err", err)
	}
	if err := netstack.UninstallResolverFile(); err != nil {
		logger.Warn("uninstall /etc/resolver/umbra.local", "err", err)
	}
	_ = httpSrv.Shutdown(shutdownCtx)
	return nil
}
