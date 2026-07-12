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
| M2 | Networking: gvisor-tap-vsock NAT (VPN-safe), `*.umbra.local` DNS, port forwarding | Not started |
| M3 | Docker: dedicated dockerd VM + `umbra` docker context | Not started |
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
