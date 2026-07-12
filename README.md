# Umbra

Umbra is an open-source, OrbStack-style VM manager for macOS: fast Linux
machines and Docker containers on Apple Silicon, built on Apple's
[Virtualization.framework](https://developer.apple.com/documentation/virtualization)
via a lightweight Go daemon (`umbrad`) and CLI (`umbra`).

*The darkest core of a shadow — VMs running invisibly behind macOS.*

## Status

| Milestone | Scope | State |
|---|---|---|
| M1 | Core VM lifecycle: Ubuntu machines, shell, VirtioFS home share, verified stop | ✅ Done — warm boot to SSH-ready in ~7s |
| M2 | Networking: gvisor-tap-vsock NAT (VPN-safe), `*.umbra.local` DNS, port forwarding | ✅ Done |
| M3 | Docker: dedicated dockerd VM + `umbra` docker context | ✅ Done |
| M4 | launchd autostart + CI-runner machine cutover | Not started |
| M5 | SwiftUI menu bar app | Not started |
| M6 | Rosetta (amd64) + OSS release polish | Not started |

## Usage (M1)

```sh
make build && make run-daemon        # terminal 1 (launchd autostart lands in M4)

bin/umbra create dev --cpus 4 --memory-gib 8 --disk-gib 60
bin/umbra start dev                  # first run downloads Ubuntu 24.04 (~600MB, sha256-verified)
bin/umbra shell dev                  # you're in Ubuntu; your Mac home is at /mnt/mac
bin/umbra shell dev -- uname -m      # run a one-off command (aarch64)
bin/umbra list
bin/umbra stop dev                   # graceful ACPI → hard kill → CONFIRMED stopped
bin/umbra rm dev
bin/umbra status --json              # machine-readable probe (watchdog surface)
```

Machines are Ubuntu 24.04 cloud images provisioned via cloud-init:
passwordless-sudo `umbra` user, dedicated ed25519 key, chrony (clock drift
after host sleep), growpart, and your macOS home mounted read-write at
`/mnt/mac` over VirtioFS.

## Networking (M2)

Machines run on an embedded [gvisor-tap-vsock](https://github.com/containers/gvisor-tap-vsock)
userspace network (subnet `192.168.127.0/24`) — no kernel NAT, no `vmnet`
entitlement, and connectivity survives VPN connect/disconnect. Each machine
gets a deterministic static IP the daemon assigns at create time.

```sh
bin/umbra shell dev                       # auto-forwards a loopback port to guest:22
bin/umbra forward add dev 8080:80         # host 127.0.0.1:8080 -> guest :80
bin/umbra forward list dev
bin/umbra forward rm dev 8080
```

**Names.** `<machine>.umbra.local` resolves from macOS once the daemon can
write `/etc/resolver/umbra.local` — this needs root, so if you start `umbrad`
without `sudo` it logs a one-line `sudo` remedy and everything else still
works. Guests resolve each other by `<name>.umbra.local` via `/etc/hosts`
written at boot; a machine learns the names of machines that already existed
when it booted (restart it to pick up newer ones). The host-side resolver is
always current.

The host cannot route directly into the userspace network, so reaching a guest
always goes through a forward (`umbra shell` sets one up automatically).

## Docker (M3)

Umbra runs a dedicated dockerd VM and bridges its socket to the host, so the
`docker` CLI and `docker compose` work unchanged (requires `brew install docker`
— the CLI only, no Docker Desktop).

```sh
umbra docker install     # creates the reserved "docker" VM (dockerd via cloud-init)
umbra docker start       # boots it, bridges the socket, sets docker context "umbra"
docker run --rm hello-world
docker compose up
umbra docker status
umbra docker stop
umbra docker uninstall   # removes the VM and the docker context
```

The socket is bridged at `~/.umbra/run/docker.sock` (context `umbra`, made
current on every start). dockerd listens on TCP inside the VM, **firewalled to
the host only** (`iptables` drops `:2375` from every source except the gateway)
— every VM shares one L2 segment, so an unauthenticated docker API must not be
reachable by other guests (e.g. a CI runner).

Not yet implemented (deferred): per-container `<name>.umbra.local` DNS and
auto-forwarding of published container ports (design-spec "docker-event-driven"
feature) — M3 delivers the VM + socket + context foundation.

## Build

Requirements: macOS 13+ on Apple Silicon (arm64), Xcode Command Line Tools, Go 1.25+.

```bash
make build             # builds + ad-hoc codesigns bin/umbrad, builds bin/umbra
make test              # unit tests
make test-integration  # boots a real VM (this Mac only, ~40s warm)
./scripts/e2e-smoke.sh # full CLI-level smoke: create→start→shell→stop→rm
```

`umbrad` must always be built via `make build` — it requires the
`com.apple.security.virtualization` entitlement, applied via ad-hoc signing
in the build step. See
[docs/runbooks/entitlements-and-codesigning.md](docs/runbooks/entitlements-and-codesigning.md).

## Design notes

Umbra's lifecycle code is engineered against 12 documented production
failures of the macOS VM ecosystem (vz cgo panics, VirtioFS desync,
gvproxy sleep/wake spins, Rosetta breakage, DHCP DUID traps, …) mined from
Lima/Colima/apple-container/vfkit issue trackers before the first line of
code — see [docs/PITFALLS-EXTERNAL.md](docs/PITFALLS-EXTERNAL.md). Highlights:

- Every Virtualization.framework call runs behind a panic-recovery boundary:
  a crashing VM marks *that machine* crashed, never the daemon (vz#124).
- Stops are never trusted on send — graceful ACPI request, bounded wait,
  hard kill, then *observed-state confirmation* (zombie machines are
  refused restart to prevent double-mounting a disk image).
- Boot readiness is a staged, bounded wait that names the failing stage
  (`ip` vs `ssh`) instead of hanging forever (colima#629).
- Guests force `dhcp-identifier: mac` — Ubuntu's default DUID identifier is
  invisible to macOS bootpd's MAC-keyed lease table.

## Docs

- [docs/PITFALLS-EXTERNAL.md](docs/PITFALLS-EXTERNAL.md) — 12 verified production pitfalls (vz / gvisor-tap-vsock / VirtioFS / Rosetta)
- [docs/superpowers/specs/2026-07-11-umbra-design.md](docs/superpowers/specs/2026-07-11-umbra-design.md) — design spec
- [docs/superpowers/plans/2026-07-11-m1-core-vm-lifecycle.md](docs/superpowers/plans/2026-07-11-m1-core-vm-lifecycle.md) — M1 implementation plan (spec-driven development, TDD)
- [docs/runbooks/entitlements-and-codesigning.md](docs/runbooks/entitlements-and-codesigning.md) — entitlements & codesigning runbook

## License

Apache-2.0 — see [LICENSE](LICENSE).
