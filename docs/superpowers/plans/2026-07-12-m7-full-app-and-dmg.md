# Umbra M7 — Full macOS app (window + Settings + onboarding) + .dmg

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Umbra is a normal macOS app — dock icon, a main window dashboard to manage machines/docker, a Settings pane, a first-run install flow — shipped as a drag-to-Applications `.dmg`. Still a thin client (shells out to the `umbra` CLI).

**Architecture:** One SwiftUI `App` declaring `Window("Umbra")` + `Settings` + `MenuBarExtra` together, sharing the existing `StatusModel` via `.environmentObject`; `LSUIElement=NO` (regular dock app). The dashboard is a `NavigationSplitView` (machine sidebar + detail, New-Machine sheet, docker + rosetta). First run (daemon unreachable) shows an onboarding install screen that copies the bundled `umbra`+`umbrad` to `/usr/local/bin` (re-signing umbrad with its entitlement) and loads the LaunchAgent — mirroring `scripts/install.sh` exactly. `make app` bundles both binaries **without `--deep`** (which would strip umbrad's entitlement) and verifies the entitlement survived. `make dmg` builds `Umbra-<version>.dmg` via `create-dmg` (hdiutil fallback).

**Tech Stack:** Swift 6 / SwiftUI (macOS 13 target), the existing umbra CLI, create-dmg/hdiutil.

## Global Constraints

- Research cheat-sheet authoritative: `docs/research/full-app-and-dmg.md`.
- **CRITICAL signing rule (§3/§7):** `umbrad` is signed once in `make build` with `build/vz.entitlements`. `make app` must NOT re-sign umbrad and must NOT use `codesign --deep` (it re-signs nested Mach-Os with the outer bundle's — empty — entitlements, silently stripping `com.apple.security.virtualization` → the bundled daemon can't boot VMs). Sign only the outer bundle: `codesign --force --sign - $(APP)`. Add a build-time verify: `codesign -d --entitlements :- $(APP)/Contents/MacOS/umbrad | grep -q virtualization` or fail.
- Regular app: `LSUIElement=NO`; Window auto-opens on launch; Settings via Cmd-,; MenuBarExtra kept as a bonus; quits on Cmd-Q (last-window-close does NOT quit — standard AppKit, no override).
- First-run onboarding == `scripts/install.sh` behavior: copy bundled umbra+umbrad → /usr/local/bin (re-sign umbrad with the bundled `vz.entitlements`), `umbra daemon install --bin /usr/local/bin/umbrad`. LaunchAgent points at /usr/local/bin (survives app update/move — NOT the in-bundle path).
- Thin client: every action shells out to the CLI (`umbra create/start/stop/rm/list`, `docker`, `daemon`, `rosetta`), decoded into Codable models. No business logic, no second source of truth.
- Poll `umbra status --json` every ~2s only while a surface (dashboard window OR menu bar popover) is open; coalesce onto one shared `StatusModel.refresh()` (don't double-poll).
- `.dmg` is ad-hoc signed / non-notarized → Gatekeeper friction: document the Sequoia "System Settings → Privacy & Security → Open Anyway" path + the `xattr -dr com.apple.quarantine` fallback in README + INSTALL.txt.
- Build/test discipline: `swift build` clean (Swift 6 strict concurrency), `swift test` green, `make app`/`make dmg` verified live; conventional commits + trailers; specific `git add`. Go side untouched (all `go test` still green).

## File Structure

```
Makefile                                       # app: bundle umbrad + entitlement-safe sign + verify; + dmg target
apps/menubar/Resources/Info.plist              # LSUIElement=NO
apps/menubar/Resources/vz.entitlements         # copy of build/vz.entitlements (bundled, for onboarding re-sign)
apps/menubar/Sources/UmbraMenuBar/
  UmbraApp.swift          # Window + Settings + MenuBarExtra; LSUIElement=NO; onboarding gate
  CLIClient.swift         # + create/rm/daemonInstall/daemonUninstall/daemonStatus/rosetta/installDaemonToUsrLocal
  StatusModel.swift       # + createMachine/deleteMachine/daemon actions; window-gated shared poll; onboarding state
  Models.swift            # (unchanged; maybe a DaemonState)
  DashboardView.swift     # NEW — NavigationSplitView: sidebar + detail + New Machine sheet + docker + rosetta
  MachineDetailView.swift # NEW — selected machine detail + actions
  NewMachineSheet.swift   # NEW — create form
  SettingsView.swift      # NEW — TabView: Defaults / Daemon / Advanced / About
  OnboardingView.swift    # NEW — first-run install flow
  (MenuBarView.swift unchanged — reuses the shared model)
scripts/install.sh        # (unchanged; onboarding mirrors it)
README.md                 # app + .dmg install + Gatekeeper note
docs/PITFALLS-EXTERNAL.md # maybe a P25 (codesign --deep entitlement strip) — optional
```

---

### Task 1: entitlement-safe `make app` (bundle umbrad, drop --deep, verify)

**Files:** Modify `Makefile`; create `apps/menubar/Resources/vz.entitlements` (copy of `build/vz.entitlements`).

- [ ] **Step 1:** Copy `build/vz.entitlements` → `apps/menubar/Resources/vz.entitlements` (bundled so onboarding can re-sign the copied umbrad). In `make app`: after copying the menubar binary + `cp $(BIN)/umbra …/umbra`, ALSO `cp $(BIN)/umbrad $(APP)/Contents/MacOS/umbrad` and `cp apps/menubar/Resources/vz.entitlements $(APP)/Contents/Resources/vz.entitlements`. Change the final sign from `codesign --force --deep --sign - $(APP)` to `codesign --force --sign - $(APP)` (NO --deep). Do NOT re-sign umbra/umbrad in this target (they arrive signed from `make build`).
- [ ] **Step 2:** Add a verify line to `make app` (fails the target if the entitlement was stripped): `codesign -d --entitlements :- $(APP)/Contents/MacOS/umbrad 2>&1 | grep -q 'com.apple.security.virtualization' || (echo "ERROR: umbrad lost its virtualization entitlement in the bundle" && exit 1)`.
- [ ] **Step 3: Run** `make build && make app` → succeeds; the verify passes. Confirm `codesign -d --entitlements :- bin/Umbra.app/Contents/MacOS/umbrad` shows the virtualization entitlement and `codesign --verify --strict bin/Umbra.app` passes.
- [ ] **Step 4: Commit** `fix(app): bundle umbrad entitlement-safe (drop --deep, verify virtualization entitlement)`

### Task 2: CLIClient + StatusModel — machine create/delete, daemon actions, rosetta, install

**Files:** Modify `apps/menubar/Sources/UmbraMenuBar/CLIClient.swift`, `StatusModel.swift`; tests in `apps/menubar/Tests/UmbraMenuBarTests/`.

**Interfaces (CLIClient additions):**
```swift
func create(_ name: String, cpus: Int, memoryGiB: Int, diskGiB: Int) async throws  // umbra create … then start
func remove(_ name: String) async throws                                            // umbra rm <name>
func daemonInstall(binPath: String) async throws                                    // umbra daemon install --bin
func daemonUninstall() async throws
func rosetta() async throws -> String                                               // umbra rosetta status --? (parse) OR GET via umbra
// onboarding install (mirrors scripts/install.sh): copy bundled umbra+umbrad → /usr/local/bin, re-sign umbrad, daemon install
func installToUsrLocal(bundledUmbra: String, bundledUmbrad: String, entitlements: String) async throws
```
- `rosetta()` — the CLI has `umbra rosetta status` (human text) but for the app parse it; simplest: add a `--json`? No — keep thin: run `umbra rosetta status` and map the output, OR (cleaner) the daemon has `GET /v1/rosetta`; but the app shells out. Pragmatic: `umbra rosetta status` prints "Rosetta: installed|not installed|not supported" — parse that string. (If brittle, add `umbra status --json` already carries docker but not rosetta; a tiny `umbra rosetta status --json` could be added to the Go CLI — OPTIONAL, keep to text-parse for M7.)
- `installToUsrLocal` — a Swift function that copies via Process (`cp`, `codesign --force --entitlements <bundled> --sign - /usr/local/bin/umbrad`, then `daemon install --bin /usr/local/bin/umbrad`). Handle the sudo-if-needed case (if /usr/local/bin not writable, run via `osascript "do shell script … with administrator privileges"` — one elevation prompt). Keep it faithful to install.sh.

**StatusModel additions:** `createMachine(...)`, `deleteMachine(name)`, `installDaemon()` (calls installToUsrLocal with bundle paths from Bundle.main), `daemonInstalled`/`onboardingNeeded` published state; window-gated shared polling (a refcount of open surfaces; poll while count>0; one in-flight refresh).

- [ ] **Step 1: Failing tests** — pure/parseable bits: a test for the rosetta-output parser (maps "Rosetta: installed" → "installed", etc.); a test that the create command builds the right arg array (factor an `createArgs(name,cpus,mem,disk) -> [String]` pure helper and assert it = `["create", name, "--cpus", "4", …]`); AppleScript-privilege string escaping if added. Keep tests pure (no real Process/daemon).
- [ ] **Step 2: Run** `swift test` — FAIL
- [ ] **Step 3:** Implement CLIClient + StatusModel additions.
- [ ] **Step 4: Run** — PASS; `swift build` clean.
- [ ] **Step 5: Commit** `feat(app): CLI create/delete/daemon/rosetta/install + model actions`

### Task 3: scene composition + Dashboard window

**Files:** Modify `UmbraApp.swift`, `apps/menubar/Resources/Info.plist`; create `DashboardView.swift`, `MachineDetailView.swift`, `NewMachineSheet.swift`.

- [ ] **Step 1:** `Info.plist`: set `LSUIElement` to `<false/>` (or remove the key). `UmbraApp.swift`: `body` = `Window("Umbra", id:"main") { DashboardView().environmentObject(model) }` + `Settings { SettingsView().environmentObject(model) }` (SettingsView stub for now, fleshed in T4) + the existing `MenuBarExtra(...) { MenuBarView().environmentObject(model) }.menuBarExtraStyle(.window)`. If daemon onboarding is needed, DashboardView shows `OnboardingView` (T4) instead of the split view when `model.onboardingNeeded` (stub the flag true-when-cli-missing for now).
- [ ] **Step 2:** `DashboardView` — `NavigationSplitView`: sidebar = header (daemon dot + "Umbra" + a gear `SettingsLink`/toolbar), machine `List` (state dot + name, selection-bound), a "+ New Machine" toolbar button (presents `NewMachineSheet`), a footer with Docker status + start/stop and a Rosetta status line. Detail = `MachineDetailView(machine)` or a placeholder when none selected. `.frame(minWidth: 620, minHeight: 420)`. `.onAppear`/`.onDisappear` → model surface-refcount poll.
- [ ] **Step 3:** `MachineDetailView` — name/state header (crashed* if zombie), IP + ssh_port, cpu/mem/disk stat row, start/stop button (spinner while busy), Open Shell (disabled unless running + ssh_port), Delete (`.confirmationDialog` → `model.deleteMachine`). `NewMachineSheet` — form (name TextField, cpus/mem/disk Steppers pre-filled from @AppStorage defaults) → `model.createMachine(...)` → dismiss + refresh.
- [ ] **Step 4: Run** `swift build --package-path apps/menubar` — compiles clean (Swift 6 strict concurrency).
- [ ] **Step 5: Commit** `feat(app): dock app scene composition + dashboard window (machines, docker, rosetta)`

### Task 4: Settings pane + Onboarding first-run flow

**Files:** Create `SettingsView.swift`, `OnboardingView.swift`; wire into `UmbraApp`/`DashboardView`/`StatusModel`.

- [ ] **Step 1:** `SettingsView` — `TabView`: **Defaults** (cpus/mem/disk `@AppStorage` used by NewMachineSheet), **Daemon** (LaunchAgent installed? + Install/Uninstall/Restart buttons → model daemon actions; shows `umbra daemon status`), **Advanced** (CLI path override `@AppStorage` honored by `umbraCLIPath()` before its built-in order; an "install /etc/resolver/umbra.local" button via osascript admin), **About** (version from Bundle.main, GitHub link, Apache-2.0).
- [ ] **Step 2:** `OnboardingView` — shown by DashboardView when `model.onboardingNeeded` (CLI unreachable / daemon down): a welcome + explanation, the first-VM-boot permission note (from INSTALL.txt copy), and an **Install Umbra** button → `model.installDaemon()` (copies bundle binaries to /usr/local/bin + re-signs + daemon install, T2). On success → `model.refresh()` → dashboard. Show progress + errors.
- [ ] **Step 3:** Advanced CLI-path override: make `umbraCLIPath()` check the `@AppStorage`/UserDefaults key first (a small addition to the resolution order). Verify the Advanced tab writes it.
- [ ] **Step 4: Run** `swift build` clean; `swift test` green.
- [ ] **Step 5: Commit** `feat(app): Settings (defaults/daemon/advanced/about) + first-run onboarding install`

### Task 5: `make dmg` + build the .dmg + verify live

**Files:** Modify `Makefile`, README.md, the `release` INSTALL.txt.

- [ ] **Step 1:** `make dmg` (depends on `app`) per research §4: `create-dmg` with `--volname Umbra --window-size 540 380 --icon-size 128 --icon Umbra.app 140 190 --app-drop-link 400 190 $(DMG) $(APP)`; fallback to the plain `hdiutil create -format UDZO` staging recipe if `create-dmg` isn't installed. `DMG := $(BIN)/Umbra-$(VERSION).dmg`. Add to `.PHONY` + `make clean` (bin/ covers it).
- [ ] **Step 2:** Run `make dmg` live (install `create-dmg` via brew if available, else exercise the hdiutil fallback). Verify: the `.dmg` exists; `hdiutil attach` mounts it; the mounted volume has `Umbra.app` + an `Applications` symlink; `codesign --verify --strict "/Volumes/Umbra/Umbra.app"` passes AND the bundled umbrad still has the virtualization entitlement (`codesign -d --entitlements :- .../umbrad | grep virtualization`); detach. Copy the app out and launch it (`open`) — it appears as a **dock app with a window**, no crash; quit. Record all results. Max 2 fix attempts on plumbing, else BLOCKED with logs.
- [ ] **Step 3:** README: an "Install (.dmg)" section — download → open the .dmg → drag Umbra to Applications → first-launch Gatekeeper note (System Settings → Privacy & Security → Open Anyway; `xattr -dr com.apple.quarantine /Applications/Umbra.app` fallback) → the first-VM-boot virtualization prompt. Update the `release` target's INSTALL.txt with the same Gatekeeper note.
- [ ] **Step 4: Commit** `feat(dmg): make dmg (create-dmg + hdiutil fallback); docs: Gatekeeper note`

### Task 6: docs + blast-radius + close M7

**Files:** README.md, docs/superpowers/specs/2026-07-11-umbra-design.md, .github/workflows/ci.yml (if needed).

- [ ] **Step 1:** README: reframe the top — Umbra is a Mac app (screenshot-worthy dashboard) + a CLI + a daemon; the `.dmg` is the primary install for non-developers, `make install`/source for developers. Status: add an M7 row ✅. Menu-bar section notes it's now a full app.
- [ ] **Step 2:** CI: the `menubar` job already runs `swift build`/`swift test` — confirm it still passes with the new views (no `make app`/dmg in CI — needs the Go build + a mac; keep it swift-build/test only). Optionally add a `swift build` of the whole app (it's the same package).
- [ ] **Step 3:** Blast-radius sweep (record): LSUIElement=NO consistency (Info.plist), the vz.entitlements bundled copy vs build/vz.entitlements (should match — add a make check or note), bundle id `co.forceai.umbra.menubar` (rename to `co.forceai.umbra`? — keep to avoid churn, note it), CLI-path override key name consistency (Advanced tab ↔ umbraCLIPath), DMG/VERSION naming.
- [ ] **Step 4:** Full green: `swift build && swift test`, `make build && make app && make dmg`, `go test ./... -count=1` (Go untouched). Mark M7 done in the spec.
- [ ] **Step 5: Commit** `docs(app): README app-first framing; M7 done`

---

## Self-Review

1. **Spec coverage (M7):** dock app + window ✅ (T3); Settings ✅ (T4); onboarding/install ✅ (T4/T2); .dmg drag-to-Applications ✅ (T5); thin client preserved ✅ (T2 shells out); entitlement-safe bundling ✅ (T1, the critical fix).
2. **Placeholder scan:** the rosetta text-parse vs a `--json` addition is an explicit pragmatic choice; the create-dmg-vs-hdiutil fallback is real, not a TBD.
3. **Type consistency:** `StatusModel` shared across Window/Settings/MenuBarExtra via environmentObject; `CLIClient` create/remove/daemon*/rosetta/install consumed by the model; `installToUsrLocal` mirrors install.sh paths; LaunchAgent → /usr/local/bin (T2) matches the onboarding + Settings Daemon tab.
