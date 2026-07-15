# Umbra

Umbra is an open-source, OrbStack-style VM manager for macOS: fast Linux
machines and Docker containers on Apple Silicon, built on Apple's
[Virtualization.framework](https://developer.apple.com/documentation/virtualization).

It ships as three things sharing one engine:

- **A Mac app** (`Umbra.app`) — a real dock app with a dashboard to create,
  start, stop, and shell into machines, manage Docker, and a Settings pane;
  plus a menu-bar cube for quick actions. Install it by dragging the `.dmg`
  to Applications — no terminal required.
- **A CLI** (`umbra`) — the full surface for developers and scripts.
- **A daemon** (`umbrad`) — the Go engine that owns the VMs, networking, and
  launchd autostart.

Non-developers install the `.dmg`; developers build from source
(`make install`). See [Install](#install).

*The darkest core of a shadow — VMs running invisibly behind macOS.*

## Status

| Milestone | Scope | State |
|---|---|---|
| M1 | Core VM lifecycle: Ubuntu machines, shell, VirtioFS home share, verified stop | ✅ Done — warm boot to SSH-ready in ~7s |
| M2 | Networking: gvisor-tap-vsock NAT (VPN-safe), `*.umbra.local` DNS, port forwarding | ✅ Done |
| M3 | Docker: dedicated dockerd VM + `umbra` docker context | ✅ Done |
| M4 | launchd autostart + CI-runner cutover kit (cutover is human-gated) | ✅ Done (kit built; cutover is Ahmad's runbook) |
| M5 | SwiftUI menu bar app | ✅ Done |
| M6 | Rosetta (amd64) + release | ✅ Done |
| M7 | Full dock app (dashboard + Settings + onboarding) + drag-to-Applications `.dmg` | ✅ Done |

## Usage (M1)

```sh
make build && make run-daemon        # terminal 1 (launchd autostart lands in M4)

bin/umbra create dev --cpus 4 --memory-gib 8 --disk-gib 60
bin/umbra start dev                  # first run downloads Ubuntu 24.04 (~600MB, sha256-verified)
bin/umbra shell dev                  # you're in Ubuntu; your Mac home is at /mnt/mac
bin/umbra shell dev -- uname -m      # run a one-off command (aarch64)
bin/umbra exec dev uname -m          # alias for `shell dev -- ...` (docker/kubectl muscle memory)
bin/umbra list
bin/umbra set dev --cpus 6 --memory-gib 12 --disk-gib 80 --autostart true  # resize (stopped only; disk only grows)
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

## Snapshots & migration

```sh
umbra snapshot dev pre-upgrade   # instant point-in-time snapshot of a stopped machine (APFS clone)
umbra snapshots dev              # list a machine's snapshots
umbra restore dev pre-upgrade    # restore a stopped machine's disk from a snapshot
umbra export dev -o dev.tar.gz   # export a stopped machine's config + disk to a tarball
umbra import dev.tar.gz --name dev2  # import a tarball produced by 'umbra export'
```

## Maintenance

```sh
umbra prune dev                  # reclaim guest disk: apt cache, docker prune, journal vacuum, fstrim
umbra stats                      # live cpu/mem/swap/disk table across all machines
```

## Rosetta / amd64 (M6)

`docker run --platform linux/amd64 ...` works unchanged on the docker VM (and
on `ci-runner` machines) because Umbra mounts a Rosetta VirtioFS share
(`vz-rosetta`) into the guest and registers the F-flagged x86-64 `binfmt_misc`
handler — no extra docker/containerd config needed. Rosetta is auto-installed
(if missing) the first time such a machine boots; ordinary machines don't get
the share (role-gated).

```sh
umbra rosetta status     # installed / not installed / not supported
docker run --rm --platform linux/amd64 alpine uname -m   # -> x86_64
```

## The app (M5 + M7)

`Umbra.app` is a full SwiftUI dock app — a thin client that shells out to the
`umbra` CLI (no duplicate HTTP-over-unix). It has three surfaces sharing one
`StatusModel`:

- a **dashboard window** (`NavigationSplitView`) — machine list with a status
  dot, a detail pane (start/stop, open shell, delete), a **+ New Machine**
  sheet, and a Docker + Rosetta footer;
- a **Settings** pane (Cmd-,) — machine-creation defaults, daemon
  install/uninstall/restart, a CLI-path override, and About;
- a **menu-bar cube** for quick start/stop/shell without the window.

On first run (daemon not yet installed) it shows an **onboarding** screen whose
**Install** button copies `umbra`/`umbrad` to `/usr/local/bin`, re-signs
`umbrad` with its virtualization entitlement, and loads the launchd daemon —
the same thing `scripts/install.sh` does.

```sh
make app          # builds + assembles bin/Umbra.app (needs the Swift toolchain / Xcode CLT)
make dmg          # wraps it in bin/Umbra-<version>.dmg (drag-to-Applications)
open bin/Umbra.app
```

It bundles `umbra` + `umbrad` + `vz.entitlements` inside `Contents/`, is
ad-hoc signed **without `--deep`** (so the nested `umbrad` keeps its
`com.apple.security.virtualization` entitlement — `make app` verifies this),
and opens shells by handing `umbra shell <name>` to Terminal.app. Built with
Swift Package Manager (`apps/menubar/`), no `.xcodeproj`.

## launchd daemon + CI-runner cutover (M4)

`umbrad` can auto-start at login as a macOS LaunchAgent instead of running
interactively in a terminal:

```sh
umbra daemon install      # writes + loads the ~/Library/LaunchAgents plist, starts umbrad now
umbra daemon status       # launchagent + API reachability
umbra daemon uninstall    # stops + unloads it
```

A single-instance `flock` guard (`~/.umbra/run/umbrad.lock`) means a stray
`make run-daemon` while the LaunchAgent copy is already up fails fast with a
clear message instead of racing the API socket or a VM disk. After a rebuild
(`make build`), re-run `umbra daemon install` to pick up the new signed
binary — launchd does not auto-reload on file change (P23).

A `ci-runner` role machine (`umbra create <name> --role ci-runner ...`) is a
normal, GitHub-Actions-self-hosted-runner-flavored Umbra machine — provisioned
with its own local-only dockerd (no shared docker VM, no network-exposed
socket), used to run `ForceAI-KW`'s org-level self-hosted runners inside an
Umbra guest instead of the existing OrbStack `fwb-ci` VM.

```sh
umbra runner add fwb-ci2 --repo ForceAI-KW/fwb --count 2  # install + start N runner instances
umbra runner list fwb-ci2 --repo ForceAI-KW/fwb            # local units + GitHub-side status
umbra runner harden fwb-ci2                                # apply Restart=always watchdog to installed units
```

The full cutover kit
— runner install script, a `workflow_dispatch`-only verify workflow template,
and the human-gated cutover procedure — lives at:

- [scripts/install-runner.sh](scripts/install-runner.sh) — installs N GitHub
  Actions runner instances inside a `ci-runner` guest
- [.github/workflow-templates/umbra-ci-verify.yml](.github/workflow-templates/umbra-ci-verify.yml) —
  copy into a target repo during verification only; labeled so it can only
  land on the new runners, never on `fwb-ci`
- [docs/runbooks/ci-cutover.md](docs/runbooks/ci-cutover.md) — the full
  procedure: create + boot `fwb-ci2`, register runners, verify green
  (including a sleep/wake check), then a clearly-marked **human-gate**
  section (flip real workflows over → deregister `fwb-ci` → delete the
  OrbStack VM → uninstall OrbStack) that is **Ahmad's hands only** —
  never automated, never run unattended.

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

## Install

### Install (.dmg) — the app

```sh
make dmg        # bin/Umbra-<version>.dmg (a drag-to-Applications disk image)
```

1. Open `Umbra-<version>.dmg`.
2. Drag **Umbra** onto the **Applications** shortcut in the window.
3. Launch Umbra from Applications. On first run it walks you through an
   **Install** step (copies `umbra`/`umbrad` onto your `PATH`, re-signs
   `umbrad` with its virtualization entitlement, and loads the launchd
   daemon) — the same thing `scripts/install.sh` does, just in the UI.

**First-launch Gatekeeper note.** Umbra is ad-hoc signed, not
notarized/Developer-ID (see
[CONTRIBUTING.md](CONTRIBUTING.md#security-posture)), so a `.dmg` downloaded
via a browser is quarantined and macOS will refuse the first launch. Since
macOS Sequoia (15) right-click → Open no longer bypasses this — instead:

- Try to open Umbra (it will be blocked), then go to **System Settings →
  Privacy & Security**, scroll down, and click **Open Anyway**; or
- clear the quarantine flag directly:
  ```sh
  xattr -dr com.apple.quarantine /Applications/Umbra.app
  ```

**First VM boot.** The first time `umbrad` boots a VM, macOS shows a one-time
Virtualization permission prompt — approve it.

### Install (CLI tarball) — headless / servers

```sh
make release   # bin/umbra-<version>-macos-arm64.tar.gz: umbrad, umbra,
               # Umbra.app, LICENSE, INSTALL.txt
```

Untar it and run `./install.sh` (or follow `INSTALL.txt` manually: copy the
binaries onto your `PATH`, `umbra daemon install`, `open Umbra.app`).

## Design notes

Umbra's lifecycle code is engineered against 24 documented production
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

- [docs/PITFALLS-EXTERNAL.md](docs/PITFALLS-EXTERNAL.md) — 24 production pitfalls (vz / gvisor-tap-vsock / VirtioFS / Rosetta / docker / launchd+CI)
- [docs/superpowers/specs/2026-07-11-umbra-design.md](docs/superpowers/specs/2026-07-11-umbra-design.md) — design spec
- [docs/superpowers/plans/2026-07-11-m1-core-vm-lifecycle.md](docs/superpowers/plans/2026-07-11-m1-core-vm-lifecycle.md) — M1 implementation plan (spec-driven development, TDD)
- [docs/runbooks/entitlements-and-codesigning.md](docs/runbooks/entitlements-and-codesigning.md) — entitlements & codesigning runbook

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) — build/test commands, repo
structure, and the pitfall-driven, plan-before-code approach.

## License

Apache-2.0 — see [LICENSE](LICENSE).
