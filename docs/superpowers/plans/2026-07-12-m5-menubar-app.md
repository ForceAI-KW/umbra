# Umbra M5 — SwiftUI menu bar app

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** a menu-bar-only macOS app (`Umbra.app`) that shows daemon + machine + docker status and lets the user start/stop machines, toggle docker, and open a shell — a thin client that shells out to the already-tested `umbra` CLI, no business logic of its own.

**Architecture:** SwiftUI `MenuBarExtra` scene, `.menuBarExtraStyle(.window)`, `LSUIElement` agent app (no Dock icon). All daemon interaction goes through the `umbra` CLI via `Process`/`Pipe` (`umbra status --json`, `umbra start|stop <name>`, `umbra docker start|stop`) — decoded into Codable models mirroring the JSON envelope. Open-shell hands off to Terminal.app via `NSAppleScript` running `umbra shell <name>`. Built with Swift Package Manager (`executableTarget`) + a `make app` target that assembles the `.app` bundle (bundling the `umbra` CLI into `Contents/MacOS/`) and ad-hoc signs it — no `.xcodeproj`.

**Tech Stack:** Swift 6 / SwiftUI (macOS 13+ deploy target), Foundation `Process`, `NSAppleScript`, SwiftPM.

## Global Constraints

- Research cheat-sheet is authoritative: `docs/research/menubar-app.md`.
- The app has NO business logic and NO duplicate HTTP-over-unix client — it only runs the `umbra` binary and decodes its JSON (satisfies P10 retry via the CLI it calls).
- `MenuBarExtra` + `.menuBarExtraStyle(.window)`; `Info.plist` sets `LSUIElement=YES`, a stable `CFBundleIdentifier` (`co.forceai.umbra.menubar`), `CFBundleName=Umbra`. Deploy target macOS 13.
- **Not sandboxed** (Process + AppleScript + reading `~/.umbra/ssh` need no sandbox; same posture as `umbrad`). Ad-hoc `codesign --sign -`, no entitlements file.
- `umbra` CLI path resolution order: (1) bundled `Bundle.main.url(forAuxiliaryExecutable: "umbra")`, (2) `/opt/homebrew/bin/umbra` / `/usr/local/bin/umbra`, (3) `$UMBRA_CLI_PATH` env override / repo `bin/umbra` dev fallback. `Process` does NOT inherit the login shell PATH — always resolve to an absolute path.
- Poll `status --json` every ~2s only while the popover is open (`.onAppear`/`.onDisappear` on the root view start/stop the loop); don't poll closed (battery). Content is fixed-width (`.frame(width: 320)`) to sidestep `.window` resize quirks.
- Codable models mirror `internal/client/client.go` MachineView/DockerStatus + `cmd/umbra/status.go` envelope exactly (json keys `ssh_port`, `memory_mib`, `disk_gib`, `context_current`). Surface `zombie` as a `crashed*` badge like `umbra list`.
- Build additive to the repo: `make build` (Go) untouched; new `make app` depends on it and bundles the just-built `umbra`. Files under `apps/menubar/`.
- Discipline: gofmt not applicable (Swift) — use `swift build` clean + no warnings-as-errors surprises; Swift unit tests via `swift test`; conventional commits + trailers; specific `git add`. The umbra repo CI (`ci.yml`) is Go-only — M5 adds a Swift build/test job to CI (macos runner has Swift).

## File Structure

```
apps/menubar/
  Package.swift                          # executableTarget "UmbraMenuBar" + test target
  Sources/UmbraMenuBar/
    UmbraApp.swift                       # @main App { MenuBarExtra(...).menuBarExtraStyle(.window) }
    Models.swift                         # MachineState, Machine, DockerStatus, StatusResponse (Codable)
    CLIClient.swift                      # umbraCLIPath(), runUmbra(args) async, status()/start()/stop()/dockerStart()/dockerStop()/openShell()
    StatusModel.swift                    # @MainActor ObservableObject: status + poll loop + actions
    MenuBarView.swift                    # the .window content: daemon dot, machine list, docker toggle, shell buttons, quit
  Tests/UmbraMenuBarTests/
    ModelsTests.swift                    # decode real status JSON fixtures
    CLIClientTests.swift                 # path resolution order; AppleScript escaping
  Resources/
    Info.plist                           # LSUIElement, bundle id/name, deploy target
Makefile                                 # + `app` target (assemble+sign Umbra.app)
.github/workflows/ci.yml                 # + swift-build-test job (macos)
README.md                               # M5 section
```

---

### Task 1: SPM scaffold + Codable models + model tests

**Files:** Create `apps/menubar/Package.swift`, `apps/menubar/Sources/UmbraMenuBar/Models.swift`, `apps/menubar/Tests/UmbraMenuBarTests/ModelsTests.swift`.

**Interfaces:** the Codable models from research §3 exactly (MachineState enum; Machine with CodingKeys ssh_port/memory_mib/disk_gib; DockerStatus with context_current; StatusResponse{daemon,error?,machines?,docker?}).

- [ ] **Step 1:** `Package.swift` — swift-tools 5.9+, platforms `.macOS(.v13)`, an `executableTarget` "UmbraMenuBar" (Sources) + a `testTarget` "UmbraMenuBarTests". (Models/tests only for now; the App/UI files come in later tasks — Package.swift lists the target dir, files added incrementally.)
- [ ] **Step 2: Failing test** — `ModelsTests.swift`: decode a real `umbra status --json` fixture string (daemon up, one running machine with ssh_port + memory_mib, docker installed:false) into `StatusResponse`; assert `machines[0].sshPort`, `.state == .running`, `.memoryMiB`, `docker.installed == false`; decode a daemon-down fixture `{"daemon":"down","error":"..."}` → `daemon=="down"`, `machines==nil`. Decode a `state:"crashed"` machine with `zombie:true`.
- [ ] **Step 3: Run** `swift test --package-path apps/menubar` — FAIL (Models not written)
- [ ] **Step 4:** Implement `Models.swift`.
- [ ] **Step 5: Run** — PASS
- [ ] **Step 6: Commit** `feat(menubar): SPM scaffold + Codable status models`

### Task 2: `CLIClient` — path resolution, runUmbra, AppleScript escaping (+tests)

**Files:** Create `apps/menubar/Sources/UmbraMenuBar/CLIClient.swift`, `apps/menubar/Tests/UmbraMenuBarTests/CLIClientTests.swift`.

**Interfaces:**
```swift
enum CLIError: Error { case notFound, nonZeroExit(Int32, String), spawnFailed(Error) }
func umbraCLIPath() -> String?           // resolution order (bundled → homebrew/usrlocal → $UMBRA_CLI_PATH → repo bin); nil if none exists
func runUmbra(_ args: [String], cliPath: String) async throws -> Data   // Process+Pipe, terminationHandler, decode caller-side
func appleScriptEscape(_ s: String) -> String   // escape " and \ for an AppleScript string literal
func openShellScript(machineName: String) -> String  // `tell application "Terminal" to do script "umbra shell <escaped-name>"`
// thin typed wrappers used by the model:
struct CLI { let path: String
  func status() async throws -> StatusResponse
  func start(_ name: String) async throws
  func stop(_ name: String) async throws
  func dockerStart() async throws; func dockerStop() async throws
}
```
- `umbraCLIPath` and `appleScriptEscape`/`openShellScript` are PURE and unit-testable without a real daemon.

- [ ] **Step 1: Failing test** — `CLIClientTests.swift`: (a) `appleScriptEscape` turns `a"b\c` into `a\"b\\c`; `openShellScript(machineName:"dev")` contains `do script "umbra shell dev"` and a machine name with a quote is escaped; (b) `umbraCLIPath` with `$UMBRA_CLI_PATH` set to an existing temp file returns it; with nothing existing returns nil. (Don't test `runUmbra` against a real process here — covered by manual/E2E; keep unit tests pure.)
- [ ] **Step 2: Run** `swift test` — FAIL
- [ ] **Step 3:** Implement `CLIClient.swift` (research §2 Process pattern; §2b resolution; §5 openShell delegates to `umbra shell` so ssh args aren't duplicated). `CLI.status()` runs `["status","--json"]` and decodes StatusResponse; if the process exits non-zero (daemon down still exits 0 with `{"daemon":"down"}` per status.go, so nonzero = a real error), throw.
- [ ] **Step 4: Run** — PASS
- [ ] **Step 5: Commit** `feat(menubar): CLI client (path resolution, Process runner, AppleScript shell handoff)`

### Task 3: `StatusModel` + `MenuBarView` + `UmbraApp`

**Files:** Create `apps/menubar/Sources/UmbraMenuBar/StatusModel.swift`, `MenuBarView.swift`, `UmbraApp.swift`.

**Interfaces:**
- `@MainActor final class StatusModel: ObservableObject`: `@Published var status: StatusResponse?`, `@Published var cliMissing: Bool`, `@Published var busy: Set<String>` (machine names mid-action). `func startPolling()` / `stopPolling()` (a `Task` loop `while !Task.isCancelled { await refresh(); try? await Task.sleep(2s) }`); `func refresh() async`; `func toggleMachine(_ m: Machine) async` (start if stopped, stop if running; marks busy; refresh after); `func toggleDocker() async`; `func openShell(_ m: Machine)`.
- `MenuBarView`: `.frame(width: 320)`; a header row with a colored `Circle()` (green daemon up / red down / gray cli-missing) + "Umbra"; if cli-missing, an explanatory row ("umbra CLI not found — run make build / install"). A `List` of machines: each row = state dot + name + `<state>` (or `crashed*` if zombie) + cpu/mem + a start/stop button (spinner when busy) + a "Shell" button (disabled unless `sshPort != nil && state == .running`). A docker section: installed?/running? + a start/stop toggle. A footer: "Quit" button (`NSApplication.shared.terminate`). `.onAppear { model.startPolling() }` / `.onDisappear { model.stopPolling() }`.
- `UmbraApp`: `@main struct`, `MenuBarExtra("Umbra", systemImage: "cube.fill") { MenuBarView().environmentObject(model) }.menuBarExtraStyle(.window)`.

- [ ] **Step 1:** Implement the three files (UI + model — no unit test for SwiftUI views; the model's pure transitions are thin and exercised by build + manual launch). Add all three to the executableTarget.
- [ ] **Step 2: Run** `swift build --package-path apps/menubar` — compiles clean (no errors).
- [ ] **Step 3: Commit** `feat(menubar): status model + menu bar window UI (machines, docker, shell, quit)`

### Task 4: `make app` bundle + Info.plist + icon

**Files:** Create `apps/menubar/Resources/Info.plist`, `apps/menubar/Resources/AppIcon.icns` (generate a simple one or omit if icns tooling unavailable — the SF Symbol menu bar glyph is what shows; a bundle icon is cosmetic), modify `Makefile`.

**Interfaces:** `make app` (depends on `build`): `swift build -c release --package-path apps/menubar`, assemble `bin/Umbra.app/Contents/{MacOS,Resources}`, copy the release binary as `Contents/MacOS/UmbraMenuBar`, copy `bin/umbra` into `Contents/MacOS/umbra`, copy Info.plist, `codesign --force --deep --sign - bin/Umbra.app`.

- [ ] **Step 1:** `Info.plist` — `CFBundleName=Umbra`, `CFBundleIdentifier=co.forceai.umbra.menubar`, `CFBundleExecutable=UmbraMenuBar`, `CFBundlePackageType=APPL`, `LSUIElement=<true/>`, `LSMinimumSystemVersion=13.0`, `CFBundleShortVersionString=0.5.0`, `CFBundleVersion=1`.
- [ ] **Step 2:** `Makefile` `app` target per research §6. If `AppIcon.icns` generation isn't feasible in the env, skip the icon copy (bundle still valid; menu bar uses the SF Symbol). Add `Umbra.app` to `make clean`.
- [ ] **Step 3: Run** `make app` — produces `bin/Umbra.app`; `codesign -dv bin/Umbra.app` shows an ad-hoc signature; `plutil -lint bin/Umbra.app/Contents/Info.plist` OK.
- [ ] **Step 4: Verify launch:** `open bin/Umbra.app` (from Terminal so failures print); confirm the process starts and stays running (a menu-bar-only app has no window/dock — check `pgrep UmbraMenuBar` succeeds and no crash log). Kill it. Record the result. If it crashes on launch, diagnose (bad Info.plist / bundle layout) — max 2 attempts, else BLOCKED with the crash log.
- [ ] **Step 5: Commit** `feat(menubar): make app bundle (Info.plist LSUIElement, ad-hoc sign, bundled CLI)`

### Task 5: CI Swift job + docs + close M5

**Files:** Modify `.github/workflows/ci.yml`, `README.md`, `docs/superpowers/specs/2026-07-11-umbra-design.md`.

- [ ] **Step 1:** Add a `menubar` job to `ci.yml` (runs-on macos-14, timeout 10): setup-go not needed; `swift build --package-path apps/menubar` + `swift test --package-path apps/menubar`. Add it to `notify-failure`'s `needs`.
- [ ] **Step 2:** README M5 section: `make app` → `open bin/Umbra.app`; what it shows/does; the SF Symbol menu-bar icon; the CLI-path note; not-sandboxed/ad-hoc-signed posture. Mark M5 done in the status table + spec milestone list.
- [ ] **Step 3:** Blast-radius sweep (record result): `co.forceai.umbra.menubar` bundle id, `UmbraMenuBar` executable name consistency across Package.swift/Info.plist/Makefile, `UMBRA_CLI_PATH` env, the CLI json-key mirror (ssh_port/memory_mib/disk_gib/context_current) still matches the Go structs.
- [ ] **Step 4:** Full check: `swift build && swift test` (menubar) + `make build && make app` + `go test ./... -count=1` (Go untouched, still green).
- [ ] **Step 5: Commit** `ci+docs(menubar): swift build/test job; M5 done`

---

## Self-Review

1. **Spec coverage (M5):** menu bar status ✅ (T3 daemon dot + machine list + docker); start/stop machines ✅ (T3 toggle); docker toggle ✅ (T3); open shell ✅ (T2/T3 via `umbra shell` + AppleScript); thin client / no business logic ✅ (shells out to CLI, T2). 
2. **Placeholder scan:** the AppIcon.icns is optional/cosmetic (explicitly conditional); everything else is concrete.
3. **Type consistency:** Codable json keys (T1) mirror the Go structs (verified in T5 blast-radius); `CLI`/`StatusModel`/`MenuBarView` chain typed consistently; executable name `UmbraMenuBar` consistent across Package.swift target / Info.plist CFBundleExecutable / Makefile copy (T4/T5 check).
