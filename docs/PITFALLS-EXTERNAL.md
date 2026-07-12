# PITFALLS-EXTERNAL — macOS VZ VM managers (Umbra domain research)

**Domain:** macOS Virtualization.framework VM managers + gvisor-tap-vsock + VirtioFS + Code-Hex/vz
**Mined:** 2026-07-11 (quality mode, Sonnet) · **Sources:** 26 repos shallow / 8 deep, ~340 issues, 10 HN threads, 10 SO questions
**Validation:** 12/12 pitfall URLs verified live via `gh api`; 0 dropped.

The two highest-relevance findings for Umbra's architecture: **P1** (cgo Handle panics kill the whole daemon, not one VM) and **P12** (bridged networking entitlement is Apple-gated — don't plan for it).

---

## P1 — vz cgo Handle panic during VM stop crashes the entire daemon
- **What:** `runtime/cgo: misuse of an invalid Handle` panic when stopping (especially force-stopping a hung) VM — takes down the whole host process, i.e. every VM Umbra runs.
- **Where:** https://github.com/Code-Hex/vz/issues/124 (also vz#119, vz#131, lima#1333) — 4 reports
- **Why:** vz delegate callbacks cross the cgo boundary via `cgo.Handle`; teardown racing an in-flight Obj-C callback panics with no recover boundary in vz.
- **Mitigation:** All lifecycle transitions serialized through one per-VM state-machine goroutine; every vz call that can trigger callbacks wrapped in `defer recover()` converting panic → VM-crashed state.
- **File:** `daemon/vm/lifecycle.go`

## P2 — VirtioFS silently desyncs/corrupts under concurrent host+guest writes
- **What:** Stale stat/phantom git diffs/content desync in guest mounts when both sides write; persists until remount/restart.
- **Where:** https://github.com/lima-vm/lima/issues/1957 (also colima#1115, vfkit#70) — 3 reports
- **Why:** VZ virtiofs doesn't guarantee guest page-cache invalidation on cross-side writes; APFS xattr semantics differ from Linux.
- **Mitigation:** Treat the home mount as host-authoritative; fsevents watcher → guest `drop_caches` RPC over vsock after host-side writes; document "don't write the same files from both sides."
- **File:** `daemon/vm/virtiofs.go`

## P3 — gvproxy pegs CPU to 100–1200% after sleep/wake (UDP retry loop)
> **Addressed (M2):** sleep/wake gap-detector supervisor (`internal/netstack/supervisor.go`) probes running machines after a detected wake; we embed gvisor-tap-vsock in-process and connections self-heal per-connection (no separate gvproxy to spin).
- **What:** After Mac sleep/wake (often + VPN change), gvisor-tap-vsock UDP reply loop spins forever; only a stack restart recovers.
- **Where:** https://github.com/containers/gvisor-tap-vsock/issues/584 (also colima#829, colima#1543) — 3 reports
- **Why:** `pkg/services/forwarder/udp_proxy.go` retries ECONNREFUSED with no backoff after macOS tears down interfaces on wake.
- **Mitigation:** NSWorkspace sleep/wake notification → proactively tear down + recreate the embedded VirtualNetwork on wake; vendor-patch udp_proxy with backoff + give-up threshold.
- **File:** `daemon/net/lifecycle_sleepwake.go`

## P4 — Embedded gvproxy DNS misses internal names + SOA/SRV/PTR record types
> **Addressed (M2):** daemon-authoritative `*.umbra.local` resolver (`internal/netstack/dns.go`, add+remove) for host-side lookups + static `/etc/hosts` for guest-side — we do not rely on gvproxy DNS for machine names.
- **What:** Fresh VMs can't resolve internal machine/container names (`ping: bad address`); NOERROR-with-zero-answers on SOA/SRV/PTR breaks DNS-probing tools.
- **Where:** https://github.com/apple/container/issues/856 (also apple/container#1693, lima#4520, gvisor-tap-vsock#612) — 4 reports
- **Why:** gvproxy's DNS implements a minimal record subset; internal-name registration races VM boot.
- **Mitigation:** Daemon owns the authoritative name→IP map, pushed into guests via `/etc/hosts` (cloud-init + on every add/remove); gvproxy DNS only for outbound; readiness check retries internal resolution before marking VM ready.
- **File:** `daemon/net/dns.go`

## P5 — Rosetta breaks after macOS point updates (SIGSEGV / "not installed")
> **Addressed (M6):** live `VZLinuxRosettaDirectoryShare.availability` check + non-fatal `installRosetta()` on first role-gated boot (`internal/vm/rosetta_darwin.go`'s `RosettaAvailability()`/`attachRosetta()`, called from `needsRosetta()` in `internal/vm/config_darwin.go`); a boot-time host-build-drift log (`checkHostBuildDrift`, `internal/vm/config_darwin.go`) flags when `sw_vers -buildVersion` has changed since the machine's `HostBuild` was cached, since availability is a live read on every boot rather than cached, staleness only affects the log signal, not correctness; guest-side binfmt registration is F-flagged (`rosettaRuncmdLines` in `internal/cloudinit/seed.go`) so the handler resolves from inside a container's mount namespace. Exposed via `umbra rosetta status` (`cmd/umbra/rosetta.go`, `GET /v1/rosetta`).
- **What:** amd64 binaries in guests crash or won't launch after a host macOS update, or Rosetta silently absent at first install.
- **Where:** https://github.com/apple/container/issues/1142 (also colima#926, colima#1069, lima#3592) — 4 reports
- **Why:** `VZLinuxRosettaDirectoryShare` binds to a host-build-specific Rosetta runtime; host updates invalidate the cached share.
- **Mitigation:** Check `VZLinuxRosettaDirectoryShare.availability` before attach; trigger `installRosetta()` when missing; re-validate on every daemon boot against `sw_vers -buildVersion` cached at VM creation, re-provision share on change.
- **File:** `daemon/vm/rosetta.go`

## P6 — Infinite "waiting for SSH/guest ready" hang after host macOS upgrade
- **What:** Previously-working persistent VMs never reach ready after a macOS update; no timeout, no diagnostic; users delete + recreate.
- **Where:** https://github.com/abiosoft/colima/issues/629 (also colima#705, lima#1200, lima#1293) — 4 reports
- **Why:** Readiness handshake depends on guest boot + host net stack simultaneously; cached VM network config goes stale across macOS updates; no bounded timeout surfaces it.
- **Mitigation:** Bounded 90s readiness timeout with stage-named structured errors (net-up / handshake / agent); detect host build change since VM creation and re-provision network config preemptively.
- **File:** `daemon/vm/boot_readiness.go`

## P7 — VPN on/off mid-session leaves guests with stale routes; no self-heal
> **Addressed (M2):** userspace gvisor-tap-vsock NAT dials a fresh host socket per new flow (research §f), so new connections pick up the current route after a VPN change without a daemon-side reset.
- **What:** Toggling a VPN after VM start breaks guest connectivity (VPN-only and sometimes public hosts) until VM/network restart.
- **Where:** https://github.com/apple/container/issues/1881 (also lima#2984, colima#392, apple/container#1307) — 4 reports
- **Why:** NAT stacks cache host route/interface state at attach; VPN rewrites default route + DNS mid-session.
- **Mitigation:** `NWPathMonitor`/SCNetworkReachability subscription in daemon; on default-route change, rebuild NAT/forward rules in the embedded VirtualNetwork.
- **File:** `daemon/net/route_watch.go`

## P8 — Guest kernel panic ⇒ stop/rm stop working (zombie VM)
- **What:** Long-running VMs occasionally hit guest NULL-deref panics; afterwards graceful stop never completes; zombie state, occasional host instability.
- **Where:** https://github.com/apple/container/issues/614 (also #946, #881) — 3 reports
- **Why:** `RequestStop` is ACPI-style — nothing listens inside a panicked kernel; vz's graceful path never times out on its own.
- **Mitigation:** Hard-kill fallback: graceful `RequestStop` → bounded timeout → vz immediate `stop(completionHandler:)` + forced cleanup of virtiofs/vsock resources.
- **File:** `daemon/vm/lifecycle.go`

## P9 — Zombie stopped-but-undeletable state under heavy I/O, disk fills
- **What:** Sustained create/stop/rm churn or slow network-backed mounts leave undeletable state accumulating until disk is full.
- **Where:** https://github.com/apple/container/issues/1445 (also lima#4496, colima#1552) — 3 reports
- **Why:** Guest processes blocked in TASK_UNINTERRUPTIBLE on slow FUSE/virtiofs I/O can't take SIGKILL; teardown silently no-ops; orchestrators don't verify.
- **Mitigation:** Never trust stop/rm on send — poll actual state to confirmation with 60s ceiling; on breach force-unmount the virtiofs share (frees the D-state process) then retry kill. Pin guest kernel builds; avoid known-bad ones.
- **File:** `daemon/vm/teardown.go`

## P10 — First client→daemon connection races daemon socket registration
- **What:** CLI/GUI intermittently gets connection-invalid/timeout on first request after install/reinstall; daemon restart "fixes" it.
- **Where:** https://github.com/apple/container/issues/672 (also #857, #561) — 3 reports
- **Why:** Client dials before the daemon finishes registering its listener; no client-side retry.
- **Mitigation:** Client-side bounded retry with backoff (5 attempts, 200ms→2s) in both CLI and menu bar app; daemon readiness = socket exists AND accepts.
- **File:** `daemon/ipc/client_connect.go`

## P11 — gvproxy hard-exits on ENOBUFS under burst traffic (kills all VM networking)
> **Addressed (M2):** the pinned gvisor-tap-vsock (v0.8.9) retries ENOBUFS instead of exiting (research §4e); the M2 supervisor additionally surfaces a wedged stack.
- **What:** Large image pulls / high packet volume → `sendto: no buffer space available` → network stack process exits for every VM.
- **Where:** https://github.com/containers/gvisor-tap-vsock/issues/367 (also #107) — 3 reports
- **Why:** Finite unixgram send buffer to the virtio-net backend; ENOBUFS treated as fatal instead of backpressure.
- **Mitigation:** Supervisor goroutine around the embedded network stack that restarts it with VM socket state intact; vendor-patch the unixgram writer to retry-with-backoff on ENOBUFS.
- **File:** `daemon/net/gvproxy_supervisor.go`

## P12 — Bridged networking entitlement (`com.apple.vm.networking`) is Apple-gated
- **What:** Real bridged networking needs an entitlement Apple only grants to vetted virtualization vendors; third-party vz apps can't get it.
- **Where:** https://github.com/Code-Hex/vz/issues/180 (also vz#138, lima#1259) — 3 reports
- **Why:** Platform security boundary; the only equivalent is a root-privileged helper owning the raw network device (lima's `socket_vmnet` pattern).
- **Mitigation:** Don't request the entitlement. Umbra v1 sticks to userspace NAT (gvisor-tap-vsock) which needs no entitlement; if bridged mode is ever wanted, plan a separately-signed root helper via SMAppService from the start.
- **File:** `docs/runbooks/entitlements-and-codesigning.md`

---

## Near-miss patterns (<3 reports, informational)

1. RAM silently downgraded above ~64GiB on some Intel hosts (vfkit#297) — 2 reports; N/A for arm64-only Umbra.
2. Guest clock drift after host sleep/wake without RTC sync (lima#850) — 2 reports; **relevant to CI runner** — add chrony/systemd-timesyncd + post-wake sync kick to the machine cloud-init.
3. ASAN use-after-free in Code-Hex/vz obj-c bridging (vz#47) — 1 report; reinforces P1's recover-boundary discipline.

## Ecosystem signals

| Package | Signal |
|---|---|
| **Code-Hex/vz** | ⚠️ 0 commits in 90 days (last 2026-02); v3.7.1 from 2025-08. Pin a known-good version; be ready to fork/vendor-patch. No CVEs (OSV). |
| containers/gvisor-tap-vsock | ✅ 81 commits/90d, active. No CVEs (OSV). |
| lima-vm/lima | ✅ 380 commits/90d — best upstream reference for vz-driver patterns. |
| crc-org/vfkit | ✅ 42 commits/90d. |
| apple/container | ✅ 162 commits/90d but young + high networking-bug churn — fast-moving reference, not a stable contract. |
| abiosoft/colima | ✅ 31 commits/90d. |
| cirruslabs/tart | ⚠️ moved to openai/tart (2026 acquisition), 8 commits/90d post-move — monitor. |

## Miner notes

UTM's issue API 502/504'd during mining — not deep-mined. StackOverflow near-zero signal on this niche; GitHub issues + HN carried it all. All pitfalls corroborated across ≥3 independent URLs, mostly across different repos.

## P13 — docker socket race: host connects before dockerd/bridge ready
> **Addressed (M3):** `dockerbridge.WaitDockerReady` polls the Engine `/_ping` before `umbra docker start` returns / the socket bridge is wired — a named "dockerd" readiness stage.
- Class of P10 (client races socket registration) applied to the docker VM. If `umbra docker start` returned as soon as the VM booted (before dockerd's TCP API accepts), the first `docker ps` would fail with a bare "cannot connect".

## P14 — stale docker.sock on daemon restart
> **Addressed (M3):** `dockerbridge.Listen` `os.Remove`s the socket path before bind.
- A crash while the bridge holds `~/.umbra/run/docker.sock` leaves the file on disk; a naive `net.Listen("unix", path)` then fails with "address already in use".

## P15 — docker context left pointing at a dead Umbra install
> **Addressed (M3):** `umbra docker uninstall` runs `dockerctx.Remove` (falls back to the `default` context) before deleting the machine.
- A dangling `umbra` context (its socket gone) makes every bare `docker` command fail until the user runs `docker context use default`.

## P16 — docker0 bridge MTU vs a lower VPN path MTU (informational)
- Docker's default bridge is MTU 1500 and does not auto-detect path MTU. Behind a corporate VPN with a lower path MTU, container `docker pull`/`apt` can black-hole. Manual fix if it bites: set `"mtu"` in the docker VM's `/etc/docker/daemon.json`. Not pre-solved (no VPN in the path most days).

## P17 — container DNS depends on the docker VM's /etc/hosts + netplan being current
> **Addressed (M3):** the docker VM is provisioned through the same shared path as every machine, so it is in the guest `/etc/hosts` map and its resolver points at the gateway (`192.168.127.1`) — container DNS resolves internet + `*.umbra.local` with no new code.

## P18 — per-container `<container>.umbra.local` DNS + auto-published-port forwarding (DEFERRED)
- The design spec's "docker-event-driven DNS + auto port forwarding" (watch the Docker events API, register `<container>.umbra.local`, auto-forward published ports) is a distinct larger feature layered on the M3 socket/context foundation. **Not implemented in M3** — deferred to a later milestone.

## P19 — launchd's minimal PATH breaks `docker` CLI lookups from `umbrad`
Concretely traced: `internal/dockerctx/dockerctx.go:27`'s `lookPath("docker")` (called from
`DockerInstall`/`DockerStart` API handlers) fails under a bare launchd PATH
(`/usr/bin:/bin:/usr/sbin:/sbin`) since Homebrew's `docker` CLI lives at `/opt/homebrew/bin/docker`.
**Every other host-side `exec.Command`** in the codebase (`internal/api/server.go:130`,
`cmd/umbrad/docker.go:71`) already uses an absolute path (`/usr/bin/sw_vers`) and is unaffected —
this is the *only* PATH-dependent call site. **Mitigation**: `EnvironmentVariables.PATH` in the
plist (§1), verified by re-running `umbra docker status` after `umbra daemon install`.

## P20 — the registration token is a ticking clock across a multi-instance install
GitHub's org registration token expires in 1 hour. A cloud-init-baked token (the pattern M3 uses
for static docker provisioning) risks going stale before boot+provisioning completes, and installing
N runner instances sequentially against one token risks the same on a slow guest. **Mitigation**:
fetch a fresh token per `config.sh` invocation, live, over `umbra shell` — never bake it into the
seed ISO (§4).

## P21 — runner VM loses network on host sleep; the M2 supervisor is necessary but not sufficient
`internal/netstack/supervisor.go`'s sleep/wake probe (per `main.go`'s wiring) detects a wake gap and
probes SSH health, logging loudly — but it does not restart the GitHub Actions runner *service*
inside the guest if the runner's own long-lived HTTPS connection to GitHub's Actions backend
(separate from the SSH-based probe path) needs re-establishing after a host sleep. `svc.sh`'s
systemd unit should already auto-reconnect (the runner binary itself retries), but this should be
explicitly verified during §5's verification phase (put the host to sleep/wake mid-verification,
confirm the runner still picks up a dispatched job afterward) rather than assumed.

## P22 — CI cache/build-artifact disk growth silently fills the guest disk
Docker image layers, npm/pnpm caches, and build artifacts accumulate on `fwb-ci2`'s guest disk over
weeks of CI churn — this is P9 from `PITFALLS-EXTERNAL.md` ("zombie... disk fills") applied to a
*healthy*-but-growing disk rather than a crashed one. **Mitigation**: size the guest disk with
headroom over whatever `fwb-ci`'s OrbStack 26GB has proven sufficient for, and add a periodic
`docker system prune -af --volumes` (cron or a runner post-job hook) — not solved by M1–M3's
`growpart`-on-first-boot alone, since that only grows the filesystem to fill the *disk image*, it
doesn't reclaim space once CI churn fills it.

## P23 — codesign/entitlement survives launchd, but *rebuilds* silently break it if the LaunchAgent isn't reloaded
Running `make build` while the old `bin/umbrad` is still the one loaded under launchd is fine (the
running process keeps its own already-checked entitlement in memory) — but developers should know
that `umbra daemon install`/`kickstart -k` after a rebuild is required to pick up the new signed
binary; there's no launchd "auto-reload on file change" behavior. Document as a one-line note in
the `umbra daemon` command help text, not a bug — just a discoverability gap.

## P24 — TCC prompts have no UI to answer under a LaunchAgent (see §1)
Restated as a pitfall for visibility: a first-run VirtioFS home-share TCC prompt cannot be answered
by a LaunchAgent (no windowed session). If the interactive-first-run step in §1 is skipped, the
guest's `/mnt/mac` mount could silently fail for TCC-protected subdirectories the *first* time
`umbrad` ever runs as a LaunchAgent, with no visible dialog — only an `EPERM` buried in guest logs.
