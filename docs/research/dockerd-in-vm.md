# dockerd-in-VM — design cheat-sheet for Umbra M3 (Docker support)

**Purpose:** answer the concrete design questions M3 (`docs/superpowers/specs/2026-07-11-umbra-design.md` §Milestones) needs answered before a plan is written: how to get `dockerd` running in a guest via cloud-init, how to bridge its socket to `~/.umbra/run/docker.sock` on the host, how to register the `umbra` docker context, how to model the docker VM against the existing `registry.Machine`/`vm.Manager` machinery, and the pitfalls specific to this setup.

**Grounded in the actual codebase** (read before writing this doc): `internal/netstack/netstack.go` (`Stack.DialContextTCP`, `Stack.Expose`/`Unexpose` — TCP/UDP host↔guest only, see §2), `internal/vm/manager.go` (`exposeSSH` pattern this doc's bridge mirrors), `internal/cloudinit/seed.go` (static-IP netplan + `/etc/hosts` cloud-init already in place), `internal/registry/registry.go` (`Machine` struct), `docs/research/gvisor-tap-vsock-api.md` (the verified gvisor-tap-vsock API surface — authoritative for what that library can and cannot do), `docs/PITFALLS-EXTERNAL.md` (P1–P12, existing pitfall numbering convention this doc's §7 continues).

---

## 0. Recommended architecture (summary)

- **One dedicated VM**, provisioned exactly like any other `registry.Machine` (same `vm.Manager`, same netstack attach, same DNS registration), distinguished only by (a) the reserved name `docker` and (b) a docker-specific cloud-init profile that installs + configures `dockerd`. No parallel machine-management code path. This mirrors Colima's own model — see §4.
- **dockerd listens on TCP inside the guest** (`tcp://0.0.0.0:2375`, alongside its normal unix socket), firewalled to accept connections only from the gateway IP (`192.168.127.1`, i.e. only the Umbra host itself). `umbrad` runs a goroutine that `net.Listen("unix", ~/.umbra/run/docker.sock)`s on the host and, per accepted connection, dials `st.DialContextTCP(ctx, dockerVMIP+":2375")` and pipes bytes both ways. See §2 for why this beats an SSH-socket-forward given Umbra's existing in-process architecture.
- **`docker context create umbra --docker host=unix://~/.umbra/run/docker.sock`**, idempotent via an existence check, run by `umbra docker install`.

```
docker CLI (host) → ~/.umbra/run/docker.sock (unix, host-side listener in umbrad)
                        │  (per-conn) st.DialContextTCP(ctx, "192.168.127.X:2375")
                        ▼
                 gvisor-tap-vsock userspace stack (in-process, existing Stack)
                        │
                        ▼
              docker VM (registry.Machine "docker") — dockerd -H fd:// -H tcp://0.0.0.0:2375
              (iptables: only 192.168.127.1 may reach :2375)
```

---

## 1. dockerd install in the guest via cloud-init

### Prior art: what Lima's own official template does

Lima's `templates/docker.yaml` — the closest real prior art (same OS, same VZ-class VM, same "make `docker` work on the host via a context" goal) — provisions with:

```yaml
provision:
  - mode: system
    script: |
      command -v docker >/dev/null 2>&1 && exit 0
      curl -fsSL https://get.docker.com | sh
      systemctl disable --now docker.socket docker.service  # (rootful disabled; rootless path taken)
```
[`lima-vm/lima/templates/docker.yaml`](https://github.com/lima-vm/lima/blob/master/templates/docker.yaml)

i.e. Lima uses the **get.docker.com convenience script**, not `apt install docker.io` and not a hand-rolled `apt.sources` block for Docker's official repo. Docker's own docs call the convenience script "configured for development use only... not recommended for production" ([docs.docker.com/engine/install/ubuntu](https://docs.docker.com/engine/install/ubuntu/)), but for a single-user, NAT'd, non-internet-facing dev VM behind Umbra's userspace network, that caveat is about internet-facing multi-tenant hosts, not this shape — and it's exactly what the most relevant prior art (Lima) ships.

### Recommendation: get.docker.com, three install options compared

| Method | Pros | Cons |
|---|---|---|
| **`apt install docker.io`** (Ubuntu universe) | Pure cloud-init `packages:` list, zero external script trust, fastest | Ubuntu 24.04's `docker.io` lags upstream Docker releases; Compose v2 plugin is a separate, differently-named package (`docker-compose-v2`); no `docker-buildx-plugin` by that name |
| **`curl -fsSL https://get.docker.com \| sh`** (convenience script) — **recommended** | Matches Lima's own template exactly; installs `docker-ce`, `docker-ce-cli`, `containerd.io`, `docker-buildx-plugin`, `docker-compose-plugin` together (same package set as the manual apt-repo method, just scripted); one `runcmd` line | Pulls + trusts a remote script at boot (mitigated: it's fetched once per VM create, over TLS, and is the same script Docker officially publishes and Lima ships in its default template) |
| **Docker's official apt repo** (`download.docker.com/linux/ubuntu`) added via cloud-init's `apt.sources` module | Most "correct"/reproducible; pinnable version | Heaviest cloud-init YAML (GPG key + repo line + explicit package list); no meaningful benefit over the script for a single dev VM — save for M6 hardening if version pinning becomes a real requirement |

**Recommendation for M3: get.docker.com**, exactly matching Lima's prior art. Revisit only if Ahmad wants pinned Docker versions later.

### Concrete cloud-config addition (extends `internal/cloudinit/seed.go`'s existing `userDataTmpl`)

A **new** docker-flavored variant of `BuildSeed` (or a `runcmd` parameter threaded in) needs, beyond the M1/M2 baseline (SSH user, static netplan, `/etc/hosts`):

```yaml
#cloud-config
users:
  - name: umbra
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - %s
packages:
  - chrony
package_update: false
growpart:
  mode: auto
  devices: ["/"]
mounts:
  - [home, /mnt/mac, virtiofs, "defaults,nofail", "0", "0"]
ssh_pwauth: false
runcmd:
  - command -v docker >/dev/null 2>&1 || (curl -fsSL https://get.docker.com | sh)
  - usermod -aG docker umbra
  - mkdir -p /etc/systemd/system/docker.service.d
  - |
    cat > /etc/systemd/system/docker.service.d/override.conf <<'EOF'
    [Service]
    ExecStart=
    ExecStart=/usr/bin/dockerd -H fd:// -H tcp://0.0.0.0:2375
    EOF
  - systemctl daemon-reload
  - systemctl enable --now docker
  # P-docker-tcp-exposure mitigation (§7): only the Umbra host (gateway IP) may
  # reach the unauthenticated TCP API — blocks any other guest VM on the same
  # L2 subnet (e.g. the fwb-ci runner) from reaching dockerd.
  - apt-get install -y iptables-persistent
  - iptables -A INPUT -p tcp --dport 2375 ! -s 192.168.127.1 -j DROP
  - netfilter-persistent save
```

### Pitfalls (install-specific)

- **systemd unit ready-timing**: `runcmd` steps run synchronously during cloud-init's boot sequence, and `get.docker.com`'s own script already does `systemctl enable --now docker` internally — but there is still a real window between "cloud-init reports done" and "dockerd's API is actually accepting connections" (image-layer/graph-driver init, iptables chain setup by dockerd itself for its own NAT rules can take a beat). **Do not treat cloud-init completion as docker-readiness.** Umbra already has the right pattern for this: `internal/vm/readiness.go`'s staged `WaitReady` (named stage + bounded timeout, P6 discipline). M3 needs an analogous **docker-readiness stage** — poll `st.DialContextTCP(ctx, guestIP+":2375")` then (once connected) a lightweight `GET /_ping` on the Docker Engine API (the same unauthenticated liveness endpoint Docker's own healthchecks use) before the daemon reports the docker VM "ready" / wires the socket bridge.
- **docker group**: `usermod -aG docker umbra` only takes effect on **new** login sessions/shells — irrelevant here since Umbra never runs `docker` *inside* the guest as the `umbra` user; the host-side bridge talks to dockerd's **TCP** listener (root-owned process, no socket-permission question) not the unix socket, so this line is only needed if something inside the guest itself (e.g. a provisioning script) needs `docker` without `sudo`. Keep it for that convenience but don't depend on it for the host bridge.
- **storage driver / overlay2 on ext4**: no special handling needed. Ubuntu cloud images' rootfs is ext4 with `d_type=true` (the ext4 default since mkfs.ext4's defaults for years), which is overlay2's only ext4 requirement (`docs.docker.com` storage-driver docs). Non-issue; do not add explicit overlay2 config to `daemon.json` unless a real symptom shows up.
- **apt-get update timing**: the convenience script runs its own `apt-get update` internally before installing — cloud-init's `package_update: false` (kept from the existing template for machine VMs, to avoid a slow first boot) does not starve it.

---

## 2. Bridging the guest's docker.sock to `~/.umbra/run/docker.sock`

### The three options, and why gvisor-tap-vsock's `Expose` can't do this directly

The task's option (a) SSH unix-socket forward, (b) TCP-in-guest + host unix→TCP bridge, (c) vsock — evaluated against what's actually verified in `docs/research/gvisor-tap-vsock-api.md`:

- **`Stack.Expose`/`Unexpose` (§4c of the research doc) is TCP/UDP-only on both ends** — `types.ExposeRequest{Local, Remote, Protocol}` where `Protocol` is `types.TCP`/`types.UDP` and `Local` is a **host TCP/UDP address** (`"127.0.0.1:X"`), never a unix socket path. This is confirmed directly against gvisor-tap-vsock's own issue tracker: [containers/gvisor-tap-vsock#41](https://github.com/containers/gvisor-tap-vsock/issues/41) is literally "port forward should be able to expose a port on a unix socket on the host" (`"Podman API can run on port 2375 and gvproxy can expose it as /var/run/docker.sock on the host"` — exactly Umbra's ask), and the unix-socket-forwarding support that *was* eventually added ([PR #58](https://github.com/containers/gvisor-tap-vsock/pull/58), [PR #66](https://github.com/containers/gvisor-tap-vsock/pull/66), "static unix socket forwarding **over ssh**") is a **separate SSH-based code path** used by the standalone `gvproxy` binary's `--forward-sock`/`--forward-dest`/`--forward-user`/`--forward-identity` CLI flags — it is **not** part of the embeddable `pkg/virtualnetwork.VirtualNetwork` Go API Umbra links in-process (the research doc's exhaustive symbol inventory in §2–§4 has no such method on `VirtualNetwork`). So: **gvisor-tap-vsock's exposed Go API genuinely cannot bridge a host unix socket to a guest unix socket.** Option (b) — dockerd on guest TCP + a bridge Umbra writes itself — is the only one that uses primitives already verified to exist.
- **Option (a) SSH unix→unix forward** (Lima/Colima/Rancher Desktop's actual approach — see below) is real and buildable in Umbra: OpenSSH's client has natively supported unix-domain endpoints on `-L`/`-R` since 6.7 (`ssh -L /host/path:/guest/path -o StreamLocalBindUnlink=yes ...`), and Umbra's `umbra shell` (`cmd/umbra/shell.go`) already `exec`s the real `ssh` binary with an SSH-forwarded port to guest:22 — so a persistent background `ssh -L ~/.umbra/run/docker.sock:/var/run/docker.sock` subprocess is not architecturally alien. But:
  - It needs a **new supervised subprocess** (Umbra has none today for socket forwarding — `umbra shell`'s `ssh` is short-lived and interactive, `syscall.Exec`'d, not managed).
  - **Documented reliability trap**: Rancher Desktop uses exactly this pattern (Lima's SSH-based `portForwards`) and has an open issue for it dying silently: ["Docker socket becomes unreachable - SSH tunnel dies silently (VZ + macOS)"](https://github.com/rancher-sandbox/rancher-desktop/issues/9839) — socket unreachable after 30–90 minutes idle, no error in logs, requires app restart. This is a real, current (not historical) failure mode of the exact mechanism being considered.
  - Requires a keepalive/restart supervisor to be trustworthy — more moving parts than option (b).
- **Option (c) vsock** — N/A. Umbra's guest NIC is a socketpair-based `vz.NewFileHandleNetworkDeviceAttachment` (gvisor-tap-vsock's "vfkit protocol", per `docs/research/gvisor-tap-vsock-api.md` §3), not `VZVirtioSocketDeviceConfiguration` — there is no vsock device in this design at all, so a vsock-based forward would require adding an entirely separate device/transport. Not worth it when option (b) already reuses the existing TCP-dial primitive.

### Recommended: option (b), a host unix-listener → `DialContextTCP` bridge, in-process

This is a direct extension of a pattern that **already exists in the codebase**: `internal/vm/manager.go`'s `exposeSSH` auto-forwards a host TCP port to guest:22 via `st.Expose`. The docker bridge is the same idea one layer down — since `Expose` can't produce a *unix*-socket host listener, `umbrad` owns the listener itself and dials per-connection exactly the way `readiness.go`/`exposeSSH` already dial the guest.

```go
// internal/dockerbridge/bridge.go (new package; sketch — refine in the M3 plan)
package dockerbridge

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"

	"github.com/ForceAI-KW/umbra/internal/netstack"
)

// Bridge accepts connections on a host unix socket and pipes each one,
// bidirectionally, to dockerd's TCP API inside the docker VM via the
// existing in-process gvisor-tap-vsock stack. One fresh DialContextTCP per
// accepted connection — mirrors gvisor-tap-vsock's own "fresh dial per new
// flow" self-heal model (docs/research/gvisor-tap-vsock-api.md §f), so a
// docker VM restart just means the next accept's dial fails/retries; there
// is no persistent forwarder state to repair.
type Bridge struct {
	st        *netstack.Stack
	guestAddr string // "192.168.127.X:2375"
	ln        net.Listener
}

// Listen removes any stale socket file (P-socket-stale, §7) and binds sockPath.
func Listen(st *netstack.Stack, sockPath, guestAddr string) (*Bridge, error) {
	_ = os.Remove(sockPath) // best-effort: stale file from an unclean prior exit
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return &Bridge{st: st, guestAddr: guestAddr, ln: ln}, nil
}

// Serve accepts until ctx is cancelled or the listener errors. Run in a
// goroutine, daemonCtx-wired like every other M2 background loop.
func (b *Bridge) Serve(ctx context.Context) {
	go func() { <-ctx.Done(); b.ln.Close() }()
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return // listener closed (shutdown) or fatal — caller's ctx governs
		}
		go b.pipe(ctx, conn)
	}
}

func (b *Bridge) pipe(ctx context.Context, hostConn net.Conn) {
	defer hostConn.Close()
	guestConn, err := b.st.DialContextTCP(ctx, b.guestAddr)
	if err != nil {
		slog.Warn("docker bridge: dial docker VM failed", "err", err)
		return // client sees a dropped connection; docker CLI reports "cannot connect"
	}
	defer guestConn.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(guestConn, hostConn); done <- struct{}{} }()
	go func() { io.Copy(hostConn, guestConn); done <- struct{}{} }()
	<-done
}

func (b *Bridge) Close() error { return b.ln.Close() }
```

Wiring point: `cmd/umbrad/main.go`, alongside the existing `netstack.New()`/`netstack.NewResolver()` construction — start the bridge only while the `docker` machine is `StateRunning` (start it in the same place `Manager.Start` sets `dns.Set`/`exposeSSH` for that machine, stop/close it on `Manager.Stop`'s confirmed-stop branch). Reuses `vm.Manager`'s existing lifecycle hooks rather than a parallel state machine (see §4).

### Security: is unauthenticated dockerd-on-TCP acceptable here?

**Not by default — needs the iptables restriction from §1, because the exposure is broader than "only the host can reach it."** The task's framing ("only reachable inside the userspace netstack... not a real routable port") is correct for the **host↔guest** direction, but misses the **guest↔guest** direction: all of Umbra's VMs — including the `fwb-ci` self-hosted GitHub Actions runner, which by design executes untrusted workflow code — share the *same* `192.168.127.0/24` L2 segment via gvisor-tap-vsock's single switch (`pkg/tap/switch.go`'s CAM-table L2 forwarding, per `docs/research/gvisor-tap-vsock-api.md` §5). A malicious workflow on `fwb-ci` could reach `docker-vm-ip:2375` directly and — since Docker's TCP API without TLS grants full root-equivalent control (`docker run -v /:/host ... chroot /host`) — take over the docker VM. This is a real instance of Rule D's "blast radius": the docker VM's threat model isn't just "host process reaches it," it's "everything on the subnet reaches it." **Mitigation, already in the §1 cloud-config**: `iptables -A INPUT -p tcp --dport 2375 ! -s 192.168.127.1 -j DROP` — host-originated `DialContextTCP` connections source from the gvisor stack's own `GatewayIP` (`192.168.127.1`), so this rule allows exactly the Umbra host and blocks every other guest. With that rule in place, TCP+bridge is acceptable for Umbra's single-user, locally-trusted-except-CI-runner threat model; without it, it is not.

### Comparison table

| | (b) TCP + host-unix bridge — **recommended** | (a) SSH unix→unix forward |
|---|---|---|
| New gvisor-tap-vsock capability needed | None (`DialContextTCP` already exists, used elsewhere) | None (bypasses gvisor-tap-vsock; raw `ssh` subprocess) |
| New subprocess to supervise | No — pure Go goroutine in `umbrad` | Yes — long-lived `ssh` process, needs keepalive/restart logic |
| Matches existing Umbra patterns | Yes — same shape as `exposeSSH` in `manager.go` | No — `umbra shell`'s `ssh` usage today is short-lived/interactive only |
| dockerd socket security | Unix socket unused for the bridge; TCP API needs the iptables restriction above | Keeps dockerd on its default, already-secure unix socket only |
| Known reliability trap | New surface (fresh) | **Documented and current**: rancher-desktop#9839, SSH tunnel dies silently after 30–90min idle |
| Verdict | **Use this** | Rejected for M3 — more moving parts, and adopts a failure mode competitors are actively fighting |

---

## 3. `docker context` registration

```bash
docker context create umbra --docker "host=unix://$HOME/.umbra/run/docker.sock"
docker context use umbra
```
(exact `--docker host=unix://...` flag form confirmed at [docs.docker.com/engine/manage-resources/contexts](https://docs.docker.com/engine/manage-resources/contexts/); Lima's own `docker.yaml` message block gives the identical pattern: `docker context create lima-{{.Name}} --docker "host=unix://{{.Dir}}/sock/docker.sock"`.)

**Idempotency** — `docker context create` errors (non-zero exit, `"context \"umbra\" already exists"`) on a second run; there is no built-in `--force`/upsert flag. `umbra docker install` should shell out to the real `docker` CLI (simplest — matches Colima/Lima; do **not** hand-roll Docker's `~/.docker/contexts/meta/<sha256-id>/meta.json` format, that's needless complexity for no benefit) guarded like:

```go
func ensureContext(sockPath string) error {
    hostArg := "host=unix://" + sockPath
    if err := exec.Command("docker", "context", "inspect", "umbra").Run(); err == nil {
        // exists — update in case sockPath changed (e.g. UMBRA_ROOT override)
        if out, err := exec.Command("docker", "context", "update", "umbra", "--docker", hostArg).CombinedOutput(); err != nil {
            return fmt.Errorf("docker context update: %w: %s", err, out)
        }
    } else {
        if out, err := exec.Command("docker", "context", "create", "umbra", "--docker", hostArg).CombinedOutput(); err != nil {
            return fmt.Errorf("docker context create: %w: %s", err, out)
        }
    }
    return exec.Command("docker", "context", "use", "umbra").Run()
}
```

**How Colima does it**: as of Colima v0.4.0, Colima sets `umbra`-equivalent (`colima`) as the **current** docker context automatically at VM startup (not just on first install) — confirmed by community docs/FAQ discussion ([abiosoft/colima FAQ](https://colima.run/docs/faq/), [issue #365](https://github.com/abiosoft/colima/issues/365)). Recommend matching that: `umbra docker start` (not just `install`) re-asserts `docker context use umbra` on every start, so a user who manually switched context back to `default` gets `umbra` restored — cheap and matches the "least surprise" UX of the tool this is modeled after.

**Prerequisite to flag**: the `docker` CLI itself must be present on the host (`brew install docker` — CLI-only, no Docker Desktop needed) for both the context commands and for `docker`/`docker compose` to work at all. `umbra docker install` should check `exec.LookPath("docker")` up front and fail with an actionable message (`brew install docker` suggestion) rather than a raw `exec: "docker": executable file not found in $PATH`.

---

## 4. Docker VM model: reserved machine, not a special type

**Recommendation: model it as an ordinary `registry.Machine`**, reusing 100% of `vm.Manager`'s existing Start/Stop/DNS/netstack-attach machinery — distinguished from user machines only by:

1. **A reserved name.** `registry.ValidName` currently accepts `"docker"` as a legal machine name with no special casing. Add a guard in `umbra create`/the API's create handler: reject `name == "docker"` for user-initiated creates (`"docker" is reserved for \`umbra docker install\`"`), and have `umbra docker install` call the *same* internal create path directly with that fixed name.
2. **A different cloud-init profile at creation time** — the only real behavioral difference. `cloudinit.BuildSeed` needs an extra parameter (or a `BuildDockerSeed` sibling function) carrying the §1 `runcmd` block; everything else (SSH key injection, static netplan, `/etc/hosts`, VirtioFS home mount) is identical to a normal machine.
3. **Extra lifecycle wiring** the docker VM alone needs: the `dockerbridge.Bridge` (§2) started/stopped alongside its Start/Stop, and the `docker context` idempotent-registration call (§3) — both live in the `umbra docker install|start|stop` command group, layered *on top of* the normal `Manager.Start(ctx, "docker")`/`Manager.Stop(ctx, "docker")` calls, not inside `vm.Manager` itself. Keeps `vm.Manager` docker-agnostic (it already has no docker awareness — good, keep it that way per Karpathy's surgical-changes guidance).

**Visibility in `umbra list`**: hide it by default (it's an implementation detail, not something Ahmad manages like a normal machine) but don't make it invisible — `registry.Machine` gaining an optional `Role string` field (`"" ` for normal, `"docker"` for the reserved one) lets `umbra list` filter it out by default and `umbra list --all` (or `umbra docker status`) show it. Cheapest correct option; avoids inventing a second registry/store.

### How Colima actually models this (the strongest piece of prior art for this exact question)

Colima does **not** have a special "docker VM type" in its own code at all — every Colima profile (`colima start`, `colima start --profile foo`) is a completely ordinary Lima instance under the hood; "it runs docker" comes entirely from (a) which Lima cloud-config template Colima hands to `limactl` (its own `docker.yaml`-equivalent embedded template) and (b) Colima's own post-boot step that registers the docker context and starts the socket forward. There is exactly **one** VM per profile, and it is simultaneously "the machine" and "the docker host" — Colima has no separate concept of a machine VM vs. a docker VM (unlike Umbra's design, where `fwb-ci` and future general-purpose machines coexist with a docker-dedicated VM). This confirms the §4 recommendation's shape (reuse the ordinary VM machinery, differentiate only by provisioning) while validating that Umbra's *product* decision — a **dedicated** docker VM separate from general machines, per the design spec §Docker — is a deliberate divergence from Colima (Umbra wants `fwb-ci` and other machines to exist independently of whether docker is running), not something to second-guess here.

---

## 5. Rosetta for amd64 images (M6 — hook point only)

Not implemented in M3; note the hook point so M3's cloud-init/attach code doesn't need later rework:

- Host side: mount `VZLinuxRosettaDirectoryShare` into the docker VM exactly like any other machine will get it in M6 (same VirtioFS-style share mechanism already used for the `$HOME` mount — `Code-Hex/vz`'s Rosetta share API, per Apple's [`VZLinuxRosettaDirectoryShare`](https://developer.apple.com/developer/documentation/virtualization/vzlinuxrosettadirectoryshare) docs).
- Guest side: register the mounted Rosetta binary as a `binfmt_misc` handler for the `amd64`/x86-64 ELF magic bytes, via `update-binfmts` (from Ubuntu's `binfmt-support` package) or a static `/etc/binfmt.d/rosetta.conf` entry pointing at the Rosetta share's mount path — this is the standard Lima pattern ([lima-vm/lima#1088](https://github.com/lima-vm/lima/issues/1088), [lima-vm/lima#1443](https://github.com/lima-vm/lima/issues/1443) documents the common `can't create /proc/sys/fs/binfmt_misc/register: nonexistent directory` failure when `binfmt_misc` isn't mounted yet at registration time — ensure `/proc/sys/fs/binfmt_misc` is mounted, e.g. `modprobe binfmt_misc` / mount it, before the registration `runcmd` step).
- Once registered, `docker run --platform linux/amd64 ...` "just works" transparently — no docker-specific config beyond the binfmt registration; this is orthogonal to everything in §1–§4. **Carry P5** (`docs/PITFALLS-EXTERNAL.md`, "Rosetta breaks after macOS point updates") forward into the docker VM specifically — it needs the same build-version re-validation on daemon boot as any other Rosetta-enabled machine.

---

## 6. Does `docker compose` work automatically?

**Yes, once the context + socket are set up — no extra work.** `docker compose` (the CLI plugin, invoked as `docker compose ...`) is a thin client over the same Docker Engine API the `docker` CLI itself uses; it reads the active context exactly like `docker` does (`DOCKER_HOST`/context resolution is shared plumbing in the CLI, not per-subcommand). The only prerequisite is that `docker-compose-plugin` is installed on the **host** (not the guest — compose runs on the host, only issues API calls into the guest's dockerd) — `brew install docker docker-compose` or Docker Desktop's CLI tools cover this; flag it in the same `exec.LookPath` prerequisite check as `docker` itself (§3) if `umbra docker install` wants to be proactive about it, though this is host-machine setup Ahmad already has, not something Umbra needs to provision.

---

## 7. Failure modes / pitfalls (Colima/Lima/Rancher-sourced, specific to this setup)

Continuing the `docs/PITFALLS-EXTERNAL.md` P-numbering convention (P1–P12 already logged); these are **candidates for the M3 plan to add**, not yet added to that file (out of scope for this research doc per the task's "do not modify any other file"):

- **P13 — socket race: host connects before dockerd/bridge is ready.** Umbra's own `internal/api` pattern already names this class of bug (P10, "first client→daemon connection races daemon socket registration") for the main API socket — it recurs identically here. If `umbra docker start` returns as soon as the VM boots (before dockerd's TCP API is actually accepting, §1's readiness gap) and the CLI immediately runs `docker ps`, the bridge's `DialContextTCP` fails and the client sees a bare "cannot connect to the Docker daemon" with no useful diagnostic. **Mitigation**: `umbra docker start` must not return success until the docker-readiness stage (§1) passes — same staged-timeout shape as `vm/readiness.go`'s `WaitReady`, one more named stage (`"dockerd"`) after `"ssh"`.
- **P14 — stale `docker.sock` on daemon restart.** If `umbrad` crashes or is killed while the bridge's listener holds `~/.umbra/run/docker.sock`, the file persists on disk; a naive `net.Listen("unix", path)` on the next start fails with `address already in use` even though nothing is listening. The `dockerbridge.Listen` sketch in §2 already handles this (`os.Remove(sockPath)` before bind) — flag as a **required**, not optional, step; this is exactly the class of bug `internal/vm`'s P9 zombie-handle discipline exists to prevent for VM handles, applied to the socket file.
- **P15 — docker context left pointing at a dead/removed Umbra install.** If `~/.umbra` is wiped (`rm -rf`) or Umbra is uninstalled without running an uninstall step, `docker context ls` still lists `umbra` pointing at a socket that no longer exists, and if it's still the *current* context, every bare `docker` command on the host fails confusingly until the user remembers to `docker context use default`. **Mitigation**: `umbra docker uninstall` (or a `rm`-time hook) must `docker context rm umbra` (falling back to `default` context if `umbra` was current) — don't leave dangling context registrations, matching the general "no cascade-delete surprises, but also no orphaned state" discipline already in the design spec's Error Handling section.
- **P16 — docker0/bridge MTU mismatch causes silent fragmentation.** Docker's default bridge network (`docker0`, and every user-defined bridge unless overridden) defaults to **MTU 1500 unconditionally** — it does not auto-detect the actual end-to-end path MTU (confirmed general Docker networking behavior, corroborated across MTU-troubleshooting guides). Umbra's own gvisor-tap-vsock stack is also configured at `MTU: 1500` (`internal/netstack/netstack.go`), so under normal conditions there's no *guest-internal* mismatch. The real risk is **external**: if Ahmad is behind a corporate VPN with a lower path MTU (common: 1400 or less) between the Mac and the internet, packets from containers can black-hole (ICMP "fragmentation needed" is frequently dropped by corporate VPN gateways, breaking Path MTU Discovery) — symptom: `docker pull`/`apt update` inside containers hangs or times out on larger packets while small requests work fine. **Mitigation, if this bites**: lower `docker0`'s MTU via `/etc/docker/daemon.json`'s `"mtu"` key inside the docker VM to match whatever the host's actual VPN-constrained path MTU is — this is a manual, VPN-dependent tuning knob, not something to bake into the default cloud-init (most days there's no VPN in the path at all). Document as a known troubleshooting step, don't pre-solve it.
- **P17 — DNS inside containers depends on the guest's resolver chain, which depends on the docker VM's `/etc/hosts`/netplan being current.** Container DNS: `container → docker's embedded DNS (127.0.0.11) → guest's /etc/resolv.conf (nameserver 192.168.127.1, set by the same static netplan `cloudinit/seed.go` already writes) → gvisor-tap-vsock's built-in DNS server at the gateway → (for `*.umbra.local`) the zones Umbra registered, or (for everything else) upstream/real internet DNS`. This already works with zero new code because the docker VM is provisioned with the same netplan template as every other machine (§4) — but it means the docker VM **cannot** resolve other machines' names (`fwb-ci.umbra.local`) unless it's also included in the `hosts` map passed to `cloudinit.BuildSeed` on every machine add/remove, exactly like guest-to-guest resolution already works for two normal machines (per M2's `hostsRuncmd`). **Flag for M3's Task list**: the docker VM must be added to (and kept in sync with) the same guest-to-guest `/etc/hosts` propagation every other machine gets — easy to forget since it's provisioned via a different code path (§1's docker-flavored seed) than normal machines, and `hostsRuncmd`'s "not idempotent across reboots" note (already flagged in `seed.go`) applies here too.
- **P18 (informational, not gvisor-tap-vsock-specific) — per-container DNS/port-forward automation is bigger than this doc's scope.** The design spec's Networking section calls for "docker-event-driven DNS + auto port forwarding" (`<container>.umbra.local`, auto-published-port forwarding) as part of M3. That's a distinct, larger feature (watch the Docker events API over the same bridge, react to container start/stop, call `netstack.Resolver.Set`/`Stack.Expose` per container) layered on top of everything in this doc — this cheat-sheet covers the VM+socket+context foundation that feature depends on, not the feature itself; scope it as its own M3 task in the plan.

---

## Sources

- [lima-vm/lima `templates/docker.yaml`](https://github.com/lima-vm/lima/blob/master/templates/docker.yaml) — cloud-init install method, socket portForwards shape, context-create message
- [Docker docs — Install Docker Engine on Ubuntu](https://docs.docker.com/engine/install/ubuntu/) — apt-repo vs convenience-script vs distro-package tradeoffs
- [Docker docs — Configure remote access for the daemon](https://docs.docker.com/engine/daemon/remote-access/) — systemd override syntax, `-H fd:// -H tcp://...`, the "socket-activation + `-H` conflict" warning, TLS/security warnings
- [Docker docs — Manage contexts](https://docs.docker.com/engine/manage-resources/contexts/) — `docker context create/use/ls` exact syntax
- [containers/gvisor-tap-vsock issue #41](https://github.com/containers/gvisor-tap-vsock/issues/41) — confirms host-unix-socket exposure is NOT supported by the `Expose`/`ExposeRequest` API
- [containers/gvisor-tap-vsock PR #58](https://github.com/containers/gvisor-tap-vsock/pull/58), [PR #66](https://github.com/containers/gvisor-tap-vsock/pull/66) — the SSH-based unix-socket-forward path added for the standalone `gvproxy` binary/podman machine, separate from the embeddable Go API
- [abiosoft/colima FAQ](https://colima.run/docs/faq/), [colima discussion #688 "How to forward docker.sock"](https://github.com/abiosoft/colima/discussions/688), [colima issue #365](https://github.com/abiosoft/colima/issues/365) — Colima's context-at-startup behavior, socket location conventions
- [rancher-sandbox/rancher-desktop issue #9839 — "Docker socket becomes unreachable - SSH tunnel dies silently (VZ + macOS)"](https://github.com/rancher-sandbox/rancher-desktop/issues/9839) — the concrete reliability trap of the SSH-unix-forward approach (option a), current/open
- [lima-vm/lima issue #1088](https://github.com/lima-vm/lima/issues/1088), [issue #1443](https://github.com/lima-vm/lima/issues/1443) — Rosetta/binfmt_misc registration pattern and its `nonexistent directory` failure mode
- Apple Developer docs — [`VZLinuxRosettaDirectoryShare`](https://developer.apple.com/documentation/virtualization/vzlinuxrosettadirectoryshare)
- This repo: `docs/research/gvisor-tap-vsock-api.md` (authoritative verified API surface), `docs/PITFALLS-EXTERNAL.md` (P1–P12, numbering convention continued above), `docs/superpowers/specs/2026-07-11-umbra-design.md`, `docs/superpowers/plans/2026-07-12-m2-networking.md`
