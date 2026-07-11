# Umbra — Design Spec

**Date:** 2026-07-11
**Status:** Approved by Ahmad (design conversation, 2026-07-11)
**Repo:** `ForceAI-KW/umbra` · Apache-2.0 · `~/Desktop/projects/umbra`

## Purpose

Open-source, company-internal replacement for OrbStack on Ahmad's Mac (Apple Silicon, macOS 26). It must cover his *actual* OrbStack usage, not OrbStack feature parity:

1. **Linux machines** — persistent Ubuntu VMs, primarily the `fwb-ci` GitHub Actions self-hosted runner. Auto-start at login; structurally eliminate the "OrbStack stopped → runners offline" failure class.
2. **Docker engine** — `docker` CLI / compose / CI scripts work unchanged against a fast daemon via a docker context.
3. **Networking niceties** — VPN-compatible NAT, `<name>.umbra.local` DNS for machines/containers, automatic port forwarding to localhost.
4. **Menu bar GUI** — status, start/stop machines, docker toggle, open shell.

Out of scope (deliberately): Kubernetes, OrbStack's custom file-sharing performance work beyond stock VirtioFS, Windows/Intel support, multi-user.

## Naming

Name **Umbra** (Ahmad, 2026-07-11) — the darkest core of a shadow; VMs running invisibly behind macOS: daemon `umbrad`, CLI `umbra`, app `Umbra.app`, state dir `~/.umbra/`, docker context `umbra`, DNS zone `*.umbra.local`.

## Architecture

```
┌─ Umbra.app (SwiftUI menu bar, thin client) ─┐   ┌─ umbra (Go CLI) ─┐
└──────────────┬─────────────────────────────────┘   └───────┬─────────┘
               │        JSON API over ~/.umbra/run/api.sock   │
        ┌──────▼──────────────────────────────────────────────▼──────┐
        │  umbrad — Go daemon (launchd LaunchAgent, KeepAlive)        │
        │  • Code-Hex/vz → Virtualization.framework                  │
        │  • gvisor-tap-vsock embedded in-process (net + DNS + fwd)  │
        │  • state: ~/.umbra/{machines,images,run,log}                │
        └──┬──────────────────────────┬──────────────────────────────┘
           │                          │
   ┌───────▼────────┐        ┌────────▼─────────┐
   │ docker VM       │        │ machines          │
   │ minimal distro  │        │ Ubuntu cloud img  │
   │ runs dockerd    │        │ + cloud-init      │
   │ socket → host   │        │ (fwb-ci, …)       │
   └─────────────────┘        └───────────────────┘
```

### Components

**`umbrad` (Go daemon)**
- Runs as a launchd LaunchAgent (`com.forceai.umbrad`), starts at login, `KeepAlive` restart on crash.
- Owns all VMs via `Code-Hex/vz` (the Virtualization.framework Go bindings Lima uses).
- JSON-over-unix-socket API at `~/.umbra/run/api.sock` (single consumer surface for CLI + GUI).
- Machine registry + config in `~/.umbra/machines/<name>/config.json`; raw disk images alongside.
- Binaries ad-hoc codesigned with the `com.apple.security.virtualization` entitlement (Lima's approach — no Apple Developer program needed for local use).

**Machines**
- Ubuntu cloud images (arm64) + cloud-init for first boot (user, SSH key, packages).
- Persistent raw disk per machine (`VZDiskImageStorageDeviceAttachment`).
- `$HOME` shared into each machine via VirtioFS (stock `VZVirtioFileSystemDeviceConfiguration`).
- `umbra shell <machine>` → SSH over the gvisor virtual network (key managed by umbrad).
- `autostart: true` machines boot when the daemon starts → CI runner comes back automatically after reboot/crash.

**Docker**
- One dedicated minimal VM (Ubuntu minimal or Alpine, decided in the plan) running `dockerd`.
- dockerd's socket exposed to the host at `~/.umbra/run/docker.sock` (forwarded over vsock/virtual net).
- `umbra docker install` registers docker context `umbra` and sets it current.
- amd64 images work via Rosetta (below).

**Networking (gvisor-tap-vsock, embedded)**
- Userspace NAT stack — no vmnet entitlement games, survives VPN connect/disconnect.
- Built-in DNS server: `<machine>.umbra.local` for machines; `<container>.umbra.local` for docker containers (umbrad watches docker events to register names). Host resolver hookup via `/etc/resolver/umbra.local`.
- Port forwarding: umbrad watches docker for published ports and forwards them to localhost automatically; machines get explicit `umbra forward` plus sensible defaults (SSH).

**CLI `umbra`**
- `umbra create|start|stop|rm|list <machine>`, `umbra shell <machine>`, `umbra docker install|start|stop`, `umbra status [--json]`, `umbra logs`, `umbra forward`.
- `umbra status --json` is the probe surface for the self-healing OS watchdog.

**Menu bar app (SwiftUI)**
- Thin client over the same JSON API. Status dot, per-machine start/stop, docker toggle, "Open shell in Terminal", version/about. No business logic in the app.

**Rosetta**
- `VZLinuxRosettaDirectoryShare` mounted into machines + docker VM; binfmt registration in guests → amd64 docker images and x86 binaries run near-natively.

## Data flow

- CLI/GUI → JSON API on `api.sock` → umbrad → vz VM operations.
- `docker` CLI → `~/.umbra/run/docker.sock` → stream forwarded into docker VM → dockerd.
- Guest traffic → gvisor-tap-vsock userspace stack → host sockets (NAT); inbound via explicit forwards.

## Error handling & reliability

- **Daemon crash = VMs down** (VZ VMs live in the daemon process; OrbStack has the same shape). Mitigations: minimal daemon surface, launchd KeepAlive, autostart machines re-boot in seconds, watchdog probes `umbra status --json`.
- Graceful shutdown: SIGTERM → ACPI shutdown request to guests → 30s → force stop.
- Disk safety: machine disks are plain raw files under `~/.umbra`; they ride the existing nightly Mac backup discipline. `umbra rm` requires the machine be stopped; no cascade deletes.
- Logs: per-machine console logs + daemon log under `~/.umbra/log/`.

## Testing

- **Unit** (Go): config, registry, API handlers, port-forward bookkeeping — CI gate.
- **Integration**: harness boots a throwaway Alpine VM, asserts boot/SSH/mount/teardown. Runs on Apple Silicon macs only (build-tagged).
- **E2E smoke**: create machine → `docker run hello-world` → published-port reachable on localhost → `dig <machine>.umbra.local` resolves. Runs on this Mac (GitHub-hosted macOS runners can't nest VZ); later on the Mac Studio self-hosted runner.
- **UAT**: `docs/uat/cutover.md` — the fwb-ci cutover checklist (below) is the acceptance test.

## Milestones

1. **M1 — Core VM lifecycle**: umbrad + `umbra`; Ubuntu machine boots via cloud-init; `umbra shell` works; VirtioFS home mount.
2. **M2 — Networking**: gvisor-tap-vsock NAT; `.umbra.local` DNS + host resolver; port forwarding.
3. **M3 — Docker**: docker VM, socket forward, `umbra` context; build/run/compose parity; docker-event-driven DNS + auto port forwarding.
4. **M4 — Autostart + fwb-ci cutover**: LaunchAgent, autostart flag, watchdog probe. Cutover: fresh `fwb-ci2` machine in Umbra → install + register runners **in parallel** with OrbStack's → verify green CI runs → deregister old runners → retire OrbStack machine → uninstall OrbStack.
5. **M5 — Menu bar app** (SwiftUI thin client).
6. **M6 — Rosetta + OSS release**: Rosetta share, README/docs, CI, signed release artifacts, publish under `ForceAI-KW`.

**Cutover constraint:** OrbStack stays untouched and running until M4's green-CI verification passes. Zero CI downtime; no migration of the existing 26 GB OrbStack disk (runner setup is scripted, not migrated).

## Prior art / research

`/ecosystem-research` (vz, Lima, vfkit, gvisor-tap-vsock production failures) runs before the implementation plan; its `PITFALLS-EXTERNAL.md` feeds the plan's "Prior art + known traps" section per standing rule.

## Definition of done (v1)

- fwb-ci runners online under Umbra, OrbStack uninstalled.
- `docker` context `umbra` is the daily driver (build/run/compose verified).
- VPN on/off does not break container networking.
- Machine + container DNS names resolve from macOS.
- Menu bar app reflects live state and can start/stop everything.
- Repo public under ForceAI-KW with docs + CI green.
