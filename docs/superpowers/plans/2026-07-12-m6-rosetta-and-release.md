# Umbra M6 — Rosetta (amd64) + OSS release polish

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `docker run --platform linux/amd64` (and x86_64 binaries) run in Umbra's container machines via Rosetta; the repo is polished for its public release with a `make release` artifact step.

**Architecture:** On the docker VM and ci-runner machines (the ones that run containers), `config_darwin.go` attaches a `vz.NewLinuxRosettaDirectoryShare()` (tag `vz-rosetta`) alongside the existing home share — availability-checked, auto-installing if missing, non-fatal on error. cloud-init mounts `vz-rosetta`→`/mnt/rosetta` and registers the F-flagged x86-64 ELF binfmt handler (so it works inside containers). A P5 host-build-drift check re-validates on boot. `umbra rosetta status` surfaces availability. A `make release` bundles the signed binaries + `Umbra.app` into a tarball.

**Tech Stack:** Go 1.25, Code-Hex/vz/v3 v3.7.1 (Rosetta API verified), cloud-init binfmt_misc.

## Global Constraints

- Research cheat-sheet is authoritative: `docs/research/rosetta-amd64.md` (verified vz symbols, exact binfmt magic/mask/flags from lima-vm/lima).
- vz symbols (verified v3.7.1): `vz.LinuxRosettaDirectoryShareAvailability() → LinuxRosettaAvailability` (consts `...NotSupported`/`...NotInstalled`/`...Installed`); `vz.LinuxRosettaDirectoryShareInstallRosetta() error` (synchronous/blocking, may download); `vz.NewLinuxRosettaDirectoryShare() (*LinuxRosettaDirectoryShare, error)` → attach via `NewVirtioFileSystemDeviceConfiguration("vz-rosetta").SetDirectoryShare(share)`.
- Rosetta is **role-gated**: only the reserved `docker` machine + `ci-runner` machines get it (they run containers/amd64). Normal dev machines don't (avoids the boot cost for the common case). Non-fatal: a Rosetta error logs + boots without it (like Lima), never fails the VM launch.
- binfmt registration uses `printf` (not `echo -e` — cloud-init runcmd is dash); the F flag is MANDATORY (container mount-namespace resolution). Magic/mask/interpreter `/mnt/rosetta/rosetta` exactly per research §5.
- P5: at launch, re-check availability every boot (cheap, live); if `m.HostBuild` differs from current `sw_vers -buildVersion`, re-run availability/install and `reg.Save` the new build.
- Every vz call stays inside `guarded()` (P1). All M1–M5 invariants intact.
- Release artifacts are ad-hoc signed (no Developer ID / notarization — deferred; local + OSS-source distribution, same posture as everything else).
- Build/test discipline identical to prior milestones. Rosetta integration test runs on this Mac (Rosetta is installed here).

## File Structure

```
internal/vm/config_darwin.go     # Rosetta share (role-gated) + needsRosetta + P5 build-drift check
internal/vm/rosetta_darwin.go    # attachRosetta helper (availability/install/share) — keeps config_darwin lean (optional split)
internal/cloudinit/seed.go       # rosettaRuncmdLines() + vz-rosetta mount (role-gated) (+ test)
internal/api/server.go           # GET /v1/rosetta → {available: notSupported|notInstalled|installed} (calls a Rosetta interface)
cmd/umbrad/main.go               # wire the rosetta availability provider (darwin: vz; non-darwin: notSupported)
internal/vm/rosetta_other.go     # non-darwin stub: availability = "notSupported"
cmd/umbra/rosetta.go             # umbra rosetta status
cmd/umbra/root.go                # register
Makefile                         # + release target
CONTRIBUTING.md                  # OSS contribution guide
README.md                        # Rosetta section, badges, final polish; M6 done
docs/PITFALLS-EXTERNAL.md        # mark P5 addressed
```

---

### Task 1: Rosetta share in config_darwin + availability provider + P5 drift check

**Files:** Modify `internal/vm/config_darwin.go`; create `internal/vm/rosetta_darwin.go`, `internal/vm/rosetta_other.go`.

**Interfaces:**
- `internal/vm/rosetta_darwin.go` (`//go:build darwin && arm64`): `func RosettaAvailability() string` returns `"notSupported"|"notInstalled"|"installed"` from `vz.LinuxRosettaDirectoryShareAvailability()`. `func attachRosetta() (*vz.VirtioFileSystemDeviceConfiguration, error)` — checks availability, installs if NotInstalled (log before/after + the `softwareupdate --install-rosetta` hint), builds the `vz-rosetta` fs device with `NewLinuxRosettaDirectoryShare()`; returns an error if NotSupported or install fails (caller logs + boots without it).
- `internal/vm/rosetta_other.go` (`//go:build !(darwin && arm64)`): `func RosettaAvailability() string { return "notSupported" }`.
- In `launchVZ` (config_darwin.go): a `needsRosetta(m)` = `m.Role == registry.ReservedDockerName || m.Role == registry.RoleCIRunner`. If true, `if cfg, err := attachRosetta(); err != nil { log.Printf("vm: rosetta unavailable for %s: %v (booting without amd64 support)", m.Name, err) } else { append cfg to the directory-sharing slice }`. All inside the existing `guarded()` closure, alongside the home share, then the combined slice → `SetDirectorySharingDevicesVirtualMachineConfiguration`.
- P5 drift: at launch top, `checkHostBuildDrift(reg, m)` — read current `sw_vers -buildVersion`; if `!= m.HostBuild && m.HostBuild != ""`, log a warning ("host macOS build changed since create — revalidating Rosetta") and `reg.Save` the updated build. (Availability is re-read every boot anyway, so this is mostly the logged signal + keeping HostBuild current.)

- [ ] **Step 1:** No unit test for the vz-touching darwin code (integration in Task 3); `RosettaAvailability` non-darwin stub can have a trivial test. Implement rosetta_darwin.go, rosetta_other.go, and the config_darwin.go wiring + the drift check.
- [ ] **Step 2:** `make build` (darwin) compiles; `go vet ./...` clean; `go build ./...` clean.
- [ ] **Step 3: Commit** `feat(vm): Rosetta directory share on container machines (amd64), P5 build-drift check`

### Task 2: cloud-init Rosetta mount + binfmt runcmd

**Files:** Modify `internal/cloudinit/seed.go`, `internal/cloudinit/seed_test.go`.

**Interfaces:** When `m.Role` is `docker` or `ci-runner`: add the `vz-rosetta`→`/mnt/rosetta` virtiofs mount (nofail) AND append `rosettaRuncmdLines()` (research §5: ensure binfmt_misc mounted, then `printf ':rosetta:M::<magic>:<mask>:/mnt/rosetta/rosetta:OCF' > register` if not already registered). Merge into the single `runcmd:` block with the existing role lines.

- [ ] **Step 1: Failing test** — `TestBuildDockerSeedHasRosetta`: a `Role:"docker"` machine's user-data contains the `vz-rosetta` mount, `/mnt/rosetta/rosetta` (interpreter path), `binfmt_misc/register`, `printf` (not `echo -e`), and the `OCF` flags; a `Role:"ci-runner"` machine also has it; a normal machine does NOT. Docker profile (2375 etc.) still present.
- [ ] **Step 2: Run** — FAIL
- [ ] **Step 3: Implement** `rosettaRuncmdLines()` + the mount, gated on role, merged into runcmd. Use printf with the exact magic/mask from research §5 (escape the backslashes correctly for a Go string literal → the printf sees `\x7fELF...`).
- [ ] **Step 4: Run** `go test ./internal/cloudinit/ -race` — PASS
- [ ] **Step 5: Commit** `feat(cloudinit): Rosetta virtiofs mount + F-flagged x86-64 binfmt (docker/ci-runner)`

### Task 3: `umbra rosetta status` + integration test (amd64 live) + docs

**Files:** Modify `internal/api/server.go` (GET /v1/rosetta), `internal/client/client.go`, create `cmd/umbra/rosetta.go`, modify `cmd/umbra/root.go`, `cmd/umbrad/main.go`; create `internal/vm/rosetta_integration_test.go` (`//go:build integration`); modify `docs/PITFALLS-EXTERNAL.md`, `README.md`.

**Interfaces:**
- API `GET /v1/rosetta` → `{"available":"installed"}` (from a `RosettaProvider` func the server holds, set in main to `vm.RosettaAvailability`). CLI `umbra rosetta status` prints it + a hint if notInstalled.
- Integration test `TestDockerRunAmd64UnderRosetta` (this Mac, Rosetta installed): boot the docker VM (Role docker), WaitDockerReady, bridge, then `docker -H unix://<sock> run --rm --platform linux/amd64 alpine uname -m` → output contains `x86_64`. (Reuses the M3 docker integration harness.)

- [ ] **Step 1:** Implement the API route + CLI + main wiring (RosettaProvider).
- [ ] **Step 2:** Write the integration test. Run it live: `make test-integration` (or the targeted signed binary) for `TestDockerRunAmd64UnderRosetta`. It boots the docker VM (dockerd install — memory-sensitive; allow a long timeout) then runs the amd64 container. Expect `x86_64`. Diagnose+fix plumbing max 2 attempts; if the dockerd-install flakiness (M3-documented) blocks it, verify the amd64 path a lighter way (e.g. `umbra shell docker -- 'ls /mnt/rosetta && cat /proc/sys/fs/binfmt_misc/rosetta'` shows the handler registered + `docker run --platform linux/amd64 hello-world`), and document the result honestly.
- [ ] **Step 3:** Docs: mark P5 addressed in PITFALLS-EXTERNAL.md with the config_darwin ref; README Rosetta section (`docker run --platform linux/amd64` works on the docker VM; `umbra rosetta status`; auto-installs Rosetta on first container-machine boot). Blast-radius sweep (vz-rosetta tag consistency config↔cloudinit, /mnt/rosetta path, needsRosetta role gate).
- [ ] **Step 4:** Full suite `go test ./... -race` + `make build`.
- [ ] **Step 5: Commit** `feat(rosetta): umbra rosetta status; test: amd64-under-Rosetta; docs: P5 addressed`

### Task 4: `make release` + CONTRIBUTING + final OSS polish + close M6

**Files:** Modify `Makefile`, `README.md`, `docs/superpowers/specs/2026-07-11-umbra-design.md`; create `CONTRIBUTING.md`.

- [ ] **Step 1:** `make release` target (depends on `build` + `app`): assembles `bin/umbra-<version>-macos-arm64.tar.gz` containing `umbrad`, `umbra`, `Umbra.app`, LICENSE, and a short INSTALL note. Version from a `VERSION` file or a `git describe`. Ad-hoc signed binaries (already signed by build/app). Verify `make release` produces a valid tarball (`tar tzf` lists the members).
- [ ] **Step 2:** `CONTRIBUTING.md`: how to build (`make build`/`make app`), run tests (`make test`, `make test-integration` needs an arm64 Mac + entitlement, `make app-test` for Swift), the milestone/plan structure (docs/superpowers), the pitfall-driven approach (docs/PITFALLS-EXTERNAL.md), code style (gofmt, the CI gates), and the security posture (ad-hoc signing, not sandboxed). Keep it concise + accurate to the actual repo.
- [ ] **Step 3:** README final pass: a one-line tagline, the status table (all M1–M6 ✅), a concise feature list, quickstart, the `make release` note, links to the design spec + pitfalls + runbooks. Ensure the pitfall count / feature descriptions are current. Mark M6 done in the spec.
- [ ] **Step 4:** Blast-radius sweep (record): release tarball naming, VERSION, CONTRIBUTING references to real make targets, README status table matches reality.
- [ ] **Step 5:** Full green: `go test ./... -count=1`, `swift test --package-path apps/menubar`, `make build && make app && make release`.
- [ ] **Step 6: Commit** `feat(release): make release tarball; docs: CONTRIBUTING + README polish; M6 done`

---

## Self-Review

1. **Spec coverage (M6):** Rosetta amd64 ✅ (T1 share + T2 binfmt, role-gated, P5-revalidated); `umbra rosetta status` ✅ (T3); OSS release polish ✅ (T4 README/CONTRIBUTING); signed release artifacts ✅ (T4 make release, ad-hoc — Developer ID/notarization explicitly deferred as out-of-scope-for-local-OSS-distribution per the spec).
2. **Placeholder scan:** the integration-test fallback (lighter amd64 verification if dockerd-install is memory-flaky) is an explicit, honest contingency, not a TBD.
3. **Type consistency:** `vm.RosettaAvailability()` (darwin/other) → `RosettaProvider` in server → CLI; `vz-rosetta` tag consistent config_darwin↔cloudinit (T3 blast-radius); `needsRosetta` role gate matches the cloudinit role gate (docker + ci-runner).
```
