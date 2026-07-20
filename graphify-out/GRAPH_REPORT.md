# Graph Report - umbra  (2026-07-20)

## Corpus Check
- 117 files · ~110,284 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 1181 nodes · 1665 edges · 70 communities (65 shown, 5 thin omitted)
- Extraction: 86% EXTRACTED · 14% INFERRED · 0% AMBIGUOUS · INFERRED: 227 edges (avg confidence: 0.8)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `33f246d4`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- [[_COMMUNITY_Community 0|Community 0]]
- [[_COMMUNITY_Community 1|Community 1]]
- [[_COMMUNITY_Community 2|Community 2]]
- [[_COMMUNITY_Community 3|Community 3]]
- [[_COMMUNITY_Community 4|Community 4]]
- [[_COMMUNITY_Community 5|Community 5]]
- [[_COMMUNITY_Community 6|Community 6]]
- [[_COMMUNITY_Community 7|Community 7]]
- [[_COMMUNITY_Community 8|Community 8]]
- [[_COMMUNITY_Community 9|Community 9]]
- [[_COMMUNITY_Community 10|Community 10]]
- [[_COMMUNITY_Community 11|Community 11]]
- [[_COMMUNITY_Community 12|Community 12]]
- [[_COMMUNITY_Community 13|Community 13]]
- [[_COMMUNITY_Community 14|Community 14]]
- [[_COMMUNITY_Community 15|Community 15]]
- [[_COMMUNITY_Community 16|Community 16]]
- [[_COMMUNITY_Community 17|Community 17]]
- [[_COMMUNITY_Community 18|Community 18]]
- [[_COMMUNITY_Community 19|Community 19]]
- [[_COMMUNITY_Community 20|Community 20]]
- [[_COMMUNITY_Community 21|Community 21]]
- [[_COMMUNITY_Community 22|Community 22]]
- [[_COMMUNITY_Community 23|Community 23]]
- [[_COMMUNITY_Community 24|Community 24]]
- [[_COMMUNITY_Community 25|Community 25]]
- [[_COMMUNITY_Community 26|Community 26]]
- [[_COMMUNITY_Community 27|Community 27]]
- [[_COMMUNITY_Community 28|Community 28]]
- [[_COMMUNITY_Community 29|Community 29]]
- [[_COMMUNITY_Community 30|Community 30]]
- [[_COMMUNITY_Community 31|Community 31]]
- [[_COMMUNITY_Community 32|Community 32]]
- [[_COMMUNITY_Community 33|Community 33]]
- [[_COMMUNITY_Community 34|Community 34]]
- [[_COMMUNITY_Community 35|Community 35]]
- [[_COMMUNITY_Community 36|Community 36]]
- [[_COMMUNITY_Community 37|Community 37]]
- [[_COMMUNITY_Community 38|Community 38]]
- [[_COMMUNITY_Community 39|Community 39]]
- [[_COMMUNITY_Community 40|Community 40]]
- [[_COMMUNITY_Community 41|Community 41]]
- [[_COMMUNITY_Community 42|Community 42]]
- [[_COMMUNITY_Community 43|Community 43]]
- [[_COMMUNITY_Community 44|Community 44]]
- [[_COMMUNITY_Community 45|Community 45]]
- [[_COMMUNITY_Community 46|Community 46]]
- [[_COMMUNITY_Community 47|Community 47]]
- [[_COMMUNITY_Community 48|Community 48]]
- [[_COMMUNITY_Community 49|Community 49]]
- [[_COMMUNITY_Community 50|Community 50]]
- [[_COMMUNITY_Community 54|Community 54]]
- [[_COMMUNITY_Community 55|Community 55]]
- [[_COMMUNITY_Community 56|Community 56]]
- [[_COMMUNITY_Community 57|Community 57]]
- [[_COMMUNITY_Community 65|Community 65]]

## God Nodes (most connected - your core abstractions)
1. `postJSON()` - 33 edges
2. `PITFALLS-EXTERNAL — macOS VZ VM managers (Umbra domain research)` - 28 edges
3. `writeFile()` - 23 edges
4. `Client` - 23 edges
5. `run()` - 22 edges
6. `newPatchTestServer()` - 21 edges
7. `CLIClientTests` - 18 edges
8. `StatusModel` - 18 edges
9. `CodingKeys` - 17 edges
10. `Umbra` - 16 edges

## Surprising Connections (you probably didn't know these)
- `sshArgs()` --calls--> `SSH()`  [INFERRED]
  cmd/umbra/shell.go → internal/paths/paths.go
- `runStats()` --calls--> `MachineDir()`  [INFERRED]
  cmd/umbra/stats.go → internal/paths/paths.go
- `dockerRandomMAC()` --calls--> `Read()`  [INFERRED]
  cmd/umbrad/docker.go → internal/export/export.go
- `run()` --calls--> `NewManager()`  [INFERRED]
  cmd/umbrad/main.go → internal/vm/manager.go
- `run()` --calls--> `CloneDisk()`  [INFERRED]
  cmd/umbrad/main.go → internal/image/image.go

## Communities (70 total, 5 thin omitted)

### Community 0 - "Community 0"
Cohesion: 0.07
Nodes (56): exposeCall, fakeForwarder, fakeLC, fakeZombieLC, NewServer(), newForwardTestServer(), newImportStagingDir(), newPatchTestServer() (+48 more)

### Community 1 - "Community 1"
Cohesion: 0.06
Nodes (25): Error, addFile(), allowed(), Read(), buildEvilTar(), TestReadRejectsTraversal(), TestWriteReadRoundTrip(), Write() (+17 more)

### Community 2 - "Community 2"
Cohesion: 0.07
Nodes (36): Allocate(), TestAllocateFirstFree(), TestAllocateSkipsUsedAndGateway(), TestValidateRejectsOutOfSubnetAndGateway(), Validate(), InstallResolverFile(), NewResolver(), resolverFilePath() (+28 more)

### Community 3 - "Community 3"
Cohesion: 0.04
Nodes (47): 1. launchd LaunchAgent for `umbrad`, 2. Single-instance guard, 3. `umbra daemon install|uninstall|status`, 4. GitHub Actions self-hosted runner in an Umbra Ubuntu guest, 5. Parallel registration + verification strategy, 6. Cutover kill-switch — HUMAN GATE, Ahmad's hands only, 7. Watchdog probe integration, 8. Pitfalls (continuing the PITFALLS-EXTERNAL.md numbering from P19) (+39 more)

### Community 4 - "Community 4"
Cohesion: 0.07
Nodes (30): InstallParams, HardenScript(), InstallScript(), TestHardenScriptCoversAllRunnerUnits(), TestInstallScriptContainsContract(), TestValidRepo(), TestValidRunnerField(), ValidRepo() (+22 more)

### Community 5 - "Community 5"
Cohesion: 0.09
Nodes (29): shortSocketDir(), TestClientDoesNotRetryPostConnectionFailure(), TestClientGivesUpWhenNoDaemon(), TestClientRetriesUntilSocketAppears(), Bridge, halfCloseWrite(), Listen(), TestBridgePipesToGuest() (+21 more)

### Community 6 - "Community 6"
Cohesion: 0.05
Nodes (42): code:go (func runShell(cmd *cobra.Command, args []string) error {), code:go (package snapshot), code:go (// Package snapshot takes and restores point-in-time copies ), code:go (func Snapshots(name string) string { return filepath.Join(Ma), code:go (mux.HandleFunc("POST /v1/machines/{name}/snapshots", func(w ), code:go (func (c *Client) TakeSnapshot(ctx context.Context, machine, ), code:go (package main), code:bash (go test ./... && make build) (+34 more)

### Community 7 - "Community 7"
Cohesion: 0.07
Nodes (23): checkHostBuildDrift(), efiBootLoader(), genericPlatform(), launchVZ(), needsRosetta(), fakeVZ, guarded(), guardedState() (+15 more)

### Community 8 - "Community 8"
Cohesion: 0.07
Nodes (21): DashboardView, rosettaLabel(), MachineDetailView, MenuBarView, image, OnboardingView, AboutSettingsTab, AdvancedSettingsTab (+13 more)

### Community 9 - "Community 9"
Cohesion: 0.07
Nodes (32): CaseIterable, Codable, CodingKey, Identifiable, String, CodingKeys, autostart, contextCurrent (+24 more)

### Community 10 - "Community 10"
Cohesion: 0.06
Nodes (31): 1. `pkg/types.Configuration` — verified fields, 2. `pkg/virtualnetwork` — construction, accept, dial, mux, 3. Wiring one guest's socket to `Code-Hex/vz` — in-process socketpair recipe, 4. Direct answers, 5. Other gotchas worth carrying into the M2 plan, (a) Can `DHCPStaticLeases` pin MAC→IP so the daemon knows each VM's IP without lease parsing?, Accepting the guest link (vfkit protocol = ours), (b) Can the embedded DNS answer a custom zone (`umbra.local`) with records added/removed at RUNTIME? (+23 more)

### Community 11 - "Community 11"
Cohesion: 0.06
Nodes (31): Build, code:sh (make build && make run-daemon        # terminal 1 (launchd a), code:bash (make build             # builds + ad-hoc codesigns bin/umbra), code:sh (make dmg        # bin/Umbra-<version>.dmg (a drag-to-Applica), code:sh (xattr -dr com.apple.quarantine /Applications/Umbra.app), code:sh (make release   # bin/umbra-<version>-macos-arm64.tar.gz: umb), code:sh (bin/umbra shell dev                       # auto-forwards a ), code:sh (umbra docker install     # creates the reserved "docker" VM ) (+23 more)

### Community 12 - "Community 12"
Cohesion: 0.15
Nodes (26): genericPlist, guiTarget(), Install(), Installed(), isNotLoadedError(), PlistPath(), RenderPlist(), serviceTarget() (+18 more)

### Community 13 - "Community 13"
Cohesion: 0.11
Nodes (8): Client, New(), CreateRequest, DockerStatus, forwardRequest, ForwardView, MachineView, UpdateRequest

### Community 14 - "Community 14"
Cohesion: 0.12
Nodes (15): Current(), IsCurrent(), fakeDocker(), readLog(), shellQuote(), TestCurrentAndIsCurrent(), TestEnsureCreatesWhenContextMissing(), TestEnsureUpdatesWhenContextExists() (+7 more)

### Community 15 - "Community 15"
Cohesion: 0.1
Nodes (22): CreateRequest, Docker, DockerStatus, Forwarder, ForwardView, Lifecycle, MachineView, Provisioner (+14 more)

### Community 16 - "Community 16"
Cohesion: 0.07
Nodes (28): Ecosystem signals, Miner notes, Near-miss patterns (<3 reports, informational), P10 — First client→daemon connection races daemon socket registration, P11 — gvproxy hard-exits on ENOBUFS under burst traffic (kills all VM networking), P12 — Bridged networking entitlement (`com.apple.vm.networking`) is Apple-gated, P13 — docker socket race: host connects before dockerd/bridge ready, P14 — stale docker.sock on daemon restart (+20 more)

### Community 17 - "Community 17"
Cohesion: 0.07
Nodes (26): code:block1 (internal/), code:yaml (version: 2), code:go (type Resolver struct { /* miekg/dns server on 127.0.0.1:<por), code:go (// Supervisor watches for macOS sleep/wake and periodically ), code:go (package ipalloc), code:go (package ipalloc), code:go (// Package ipalloc assigns deterministic IPv4 addresses with), code:go (func TestUsedIPsCollectsAssigned(t *testing.T) {) (+18 more)

### Community 18 - "Community 18"
Cohesion: 0.08
Nodes (25): 0. Recommended architecture (summary), 1. dockerd install in the guest via cloud-init, 2. Bridging the guest's docker.sock to `~/.umbra/run/docker.sock`, 3. `docker context` registration, 4. Docker VM model: reserved machine, not a special type, 5. Rosetta for amd64 images (M6 — hook point only), 6. Does `docker compose` work automatically?, 7. Failure modes / pitfalls (Colima/Lima/Rancher-sourced, specific to this setup) (+17 more)

### Community 19 - "Community 19"
Cohesion: 0.08
Nodes (22): 1. Availability check — verified symbols, 2. Install — verified symbol, 3. Building + attaching the Rosetta share — verified symbols, 4. P5 re-validation hook — where it plugs in, 5. Guest-side binfmt registration — exact bytes, verified against lima-vm/lima's shipping source, 6. Does docker need anything beyond binfmt?, code:block1 ($ go doc github.com/Code-Hex/vz/v3 LinuxRosettaAvailability), code:go (// virtiofs: Rosetta share (M6) — enables `docker run --plat) (+14 more)

### Community 20 - "Community 20"
Cohesion: 0.24
Nodes (18): fakeDNS, fakeLaunch(), newFakeDNS(), newTestManager(), saveMachine(), TestFailedStopRefusesRestart(), TestLaunchErrorAllowsRetry(), TestSlowLaunchObservableAsStarting() (+10 more)

### Community 21 - "Community 21"
Cohesion: 0.09
Nodes (21): 1. App architecture, 2. Talking to the unix socket — shell out to the CLI, don't hand-roll HTTP-over-unix, 3. Codable models, 4. Polling / refresh, 5. Actions, 6. Build: Swift Package Manager, not an `.xcodeproj`, 7. Icons / status, 8. Pitfalls (+13 more)

### Community 22 - "Community 22"
Cohesion: 0.17
Nodes (13): Machine, Registry, IsReserved(), New(), newTestRegistry(), TestListSortedAndDelete(), TestLoadAndDeleteRejectTraversalNames(), TestLoadMissingReturnsErrNotFound() (+5 more)

### Community 23 - "Community 23"
Cohesion: 0.1
Nodes (20): (a) Point real workflows at the new runners — still reversible, (b) Deregister the OrbStack `fwb-ci` runners, (c) Stop/delete the OrbStack `fwb-ci` machine, uninstall OrbStack — IRREVERSIBLE, CI cutover runbook — retiring OrbStack `fwb-ci` for an Umbra `ci-runner` machine, code:sh (umbra create fwb-ci2 --role ci-runner --cpus <N> --memory-gi), code:sh (umbra list                 # fwb-ci2 should show state=runni), code:sh (REG_TOKEN=$(gh api --method POST -H "Accept: application/vnd), code:sh (umbra shell fwb-ci2 -- \) (+12 more)

### Community 24 - "Community 24"
Cohesion: 0.15
Nodes (9): Info, instance, Manager, acquireOpMu(), exposeSSH(), freePort(), nameSetter, reapOrphanHolders() (+1 more)

### Community 25 - "Community 25"
Cohesion: 0.21
Nodes (18): BuildSeed(), ciRunnerRuncmdLines(), dockerRuncmdLines(), hostsRuncmdLines(), mountsSection(), needsRosetta(), rosettaRuncmdLines(), runcmdSection() (+10 more)

### Community 26 - "Community 26"
Cohesion: 0.16
Nodes (15): cloneFile(), cloneFile(), CloneDisk(), convertToRaw(), copyFile(), download(), Ensure(), fetch() (+7 more)

### Community 28 - "Community 28"
Cohesion: 0.12
Nodes (15): code:block1 (internal/singleton/singleton.go      # flock single-instance), code:go (package singleton), code:go (const Label = "com.forceai.umbrad"), File Structure, Global Constraints, Self-Review, Task 1: `internal/singleton` flock guard + `paths.LockFile`, Task 2: wire flock into `umbrad` main (+7 more)

### Community 29 - "Community 29"
Cohesion: 0.12
Nodes (15): code:block1 (internal/registry/registry.go        # + Role field; Reserve), code:go (func TestRoleRoundtripAndReserved(t *testing.T) {), code:go (type Dialer interface { DialContextTCP(ctx context.Context, ), code:go (package dockerbridge), code:go (// WaitDockerReady dials guestAddr (dockerVMIP:2375) via the), File Structure, Global Constraints, Self-Review (+7 more)

### Community 30 - "Community 30"
Cohesion: 0.13
Nodes (14): 1. Coexisting scenes — Window + Settings + MenuBarExtra in one `App`, 2. Dock icon vs `LSUIElement`, 3. First-run onboarding / install flow, 4. Building the `.dmg`, 5. Dashboard window content, 6. Settings pane content, 7. Pitfalls, code:swift (@main) (+6 more)

### Community 31 - "Community 31"
Cohesion: 0.16
Nodes (3): ForwardView, guardedNet(), Stack

### Community 32 - "Community 32"
Cohesion: 0.15
Nodes (12): code:block1 (Makefile                                       # app: bundle), code:swift (func create(_ name: String, cpus: Int, memoryGiB: Int, diskG), File Structure, Global Constraints, Self-Review, Task 1: entitlement-safe `make app` (bundle umbrad, drop --deep, verify), Task 2: CLIClient + StatusModel — machine create/delete, daemon actions, rosetta, install, Task 3: scene composition + Dashboard window (+4 more)

### Community 33 - "Community 33"
Cohesion: 0.15
Nodes (11): Architecture, Components, Data flow, Definition of done (v1), Error handling & reliability, Milestones, Naming, Prior art / research (+3 more)

### Community 34 - "Community 34"
Cohesion: 0.3
Nodes (8): Supervisor, NewSupervisor(), fakeClock(), TestRunExitsPromptlyOnContextCancel(), TestRunOnceMultipleNormalTicksNeverProbe(), TestRunOnceNoProbeOnNormalTick(), TestRunOnceProbesOnLargeGap(), TestRunOnceSurfacesUnhealthyNames()

### Community 35 - "Community 35"
Cohesion: 0.17
Nodes (11): code:block1 (apps/menubar/), code:swift (enum CLIError: Error { case notFound, nonZeroExit(Int32, Str), File Structure, Global Constraints, Self-Review, Task 1: SPM scaffold + Codable models + model tests, Task 2: `CLIClient` — path resolution, runUmbra, AppleScript escaping (+tests), Task 3: `StatusModel` + `MenuBarView` + `UmbraApp` (+3 more)

### Community 36 - "Community 36"
Cohesion: 0.18
Nodes (10): Approach, Build, code:sh (make build    # go daemon (umbrad) + CLI (umbra), ad-hoc cod), code:sh (make test              # Go unit tests), Contributing to Umbra, How to contribute, Repo structure, Security posture (+2 more)

### Community 37 - "Community 37"
Cohesion: 0.2
Nodes (9): code:block1 (internal/vm/config_darwin.go     # Rosetta share (role-gated), File Structure, Global Constraints, Self-Review, Task 1: Rosetta share in config_darwin + availability provider + P5 drift check, Task 2: cloud-init Rosetta mount + binfmt runcmd, Task 3: `umbra rosetta status` + integration test (amd64 live) + docs, Task 4: `make release` + CONTRIBUTING + final OSS polish + close M6 (+1 more)

### Community 38 - "Community 38"
Cohesion: 0.22
Nodes (8): code:block1 (umbra/), code:go (package vmnet), code:go (// Package vmnet resolves guest IPs from macOS bootpd's leas), File Structure (locked), Global Constraints, Self-Review (done at plan-write time), Task 7: `internal/vmnet` — dhcpd_leases IP lookup, Umbra M1 — Core VM Lifecycle Implementation Plan

### Community 39 - "Community 39"
Cohesion: 0.32
Nodes (5): TestWaitReadyHappyPath(), TestWaitReadyNamesIPStageOnTimeout(), TestWaitReadyNamesSSHStageOnTimeout(), WaitReady(), stageError

### Community 40 - "Community 40"
Cohesion: 0.25
Nodes (8): code:bash (cd ~/Desktop/projects/umbra), code:block3 (bin/), code:xml (<?xml version="1.0" encoding="UTF-8"?>), code:make (BIN := bin), code:yaml (name: ci), code:markdown (# Entitlements & codesigning), code:bash (cd ~/Desktop/projects/umbra), Task 1: Repo scaffold, Makefile + codesign, CI, GitHub repo

### Community 41 - "Community 41"
Cohesion: 0.25
Nodes (8): code:go (type Lifecycle interface {), code:go (func New(socketPath string) *Client), code:go (package api), code:go (// Package api exposes umbrad's JSON API over a unix socket.), code:go (package client), code:go (// Package client is the CLI/GUI-side client for umbrad's un), code:go (// umbrad is the Umbra daemon: owns all VMs, serves the unix), Task 10: `internal/api` server + `internal/client` with retry, `cmd/umbrad`

### Community 42 - "Community 42"
Cohesion: 0.25
Nodes (8): code:go (type State string), code:go (package vm), code:go (// Package vm owns VM lifecycle. All vz calls are guarded: a), code:go (package vm), code:go (package vm), code:go (package vm), code:go (//go:build darwin && arm64), Task 8: `internal/vm` — guard, stop escalation, state machine, vz config

### Community 43 - "Community 43"
Cohesion: 0.29
Nodes (7): code:go (package main), code:go (package main), code:go (package main), code:go (package main), code:go (package main), code:go (package main), Task 11: `cmd/umbra` CLI (cobra)

### Community 46 - "Community 46"
Cohesion: 0.5
Nodes (4): code:go (type stageError struct{ Stage, Detail string } // Error(): `), code:go (package vm), code:go (package vm), Task 9: `internal/vm/readiness.go` — staged bounded boot wait (P6)

### Community 47 - "Community 47"
Cohesion: 0.5
Nodes (4): code:bash (#!/usr/bin/env bash), code:go (//go:build integration), code:make (test-integration: build), Task 12: Integration test + E2E smoke (runs on this Mac only)

### Community 48 - "Community 48"
Cohesion: 0.5
Nodes (4): code:go (type Machine struct {), code:go (package registry), code:go (// Package registry persists machine configurations as JSON ), Task 3: `internal/registry` — machine config CRUD

### Community 49 - "Community 49"
Cohesion: 0.5
Nodes (4): code:go (const DefaultImage = "ubuntu:noble"), code:go (package image), code:go (// Package image downloads, verifies, and converts guest bas), Task 6: `internal/image` — Ubuntu cloud image download + qcow2→raw

### Community 50 - "Community 50"
Cohesion: 0.5
Nodes (4): code:`markdown (## Usage (M1)), code:block50, code:bash (cd ~/Desktop/projects/umbra), Task 13: Docs parity + close M1

### Community 55 - "Community 55"
Cohesion: 0.67
Nodes (3): code:go (// Package paths defines the ~/.umbra state-directory layout), code:go (package paths), Task 2: `internal/paths` — state-dir layout

### Community 56 - "Community 56"
Cohesion: 0.67
Nodes (3): code:go (package sshkey), code:go (// Package sshkey manages umbra's dedicated ed25519 keypair ), Task 4: `internal/sshkey` — managed ed25519 keypair

### Community 57 - "Community 57"
Cohesion: 0.67
Nodes (3): code:go (package cloudinit), code:go (// Package cloudinit builds NoCloud seed ISOs (volume label ), Task 5: `internal/cloudinit` — NoCloud seed ISO

## Knowledge Gaps
- **379 isolated node(s):** `GuestStats`, `Info`, `genericPlist`, `InstallParams`, `Dialer` (+374 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **5 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `run()` connect `Community 2` to `Community 0`, `Community 34`, `Community 5`, `Community 39`, `Community 14`, `Community 25`, `Community 26`?**
  _High betweenness centrality (0.140) - this node is a cross-community bridge._
- **Why does `writeFile()` connect `Community 0` to `Community 1`, `Community 2`, `Community 5`, `Community 7`, `Community 12`, `Community 15`, `Community 22`, `Community 26`?**
  _High betweenness centrality (0.110) - this node is a cross-community bridge._
- **Why does `Listen()` connect `Community 5` to `Community 24`, `Community 2`, `Community 14`?**
  _High betweenness centrality (0.082) - this node is a cross-community bridge._
- **Are the 20 inferred relationships involving `writeFile()` (e.g. with `TestUninstallBootoutThenRemovesPlist()` and `TestUninstallSwallowsBootoutNotFoundError()`) actually correct?**
  _`writeFile()` has 20 INFERRED edges - model-reasoned connections that need verification._
- **Are the 20 inferred relationships involving `run()` (e.g. with `EnsureTree()` and `Acquire()`) actually correct?**
  _`run()` has 20 INFERRED edges - model-reasoned connections that need verification._
- **What connects `GuestStats`, `Info`, `genericPlist` to the rest of the system?**
  _379 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Community 0` be split into smaller, more focused modules?**
  _Cohesion score 0.07 - nodes in this community are weakly interconnected._