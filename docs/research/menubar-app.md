# SwiftUI menu bar app — design + build cheat-sheet (Umbra M5)

**Purpose**: design + build reference the M5 plan (`docs/superpowers/plans/...`) gets written
from. Covers app architecture, how the app talks to `umbrad`, Codable models, refresh/polling,
the four actions (start/stop machine, docker toggle, open shell), and how to build a distributable
`.app` from this repo's `make`-based, no-Xcode-GUI workflow.

Grounded in the actual daemon/CLI contract: `paths.APISocket()` = `~/.umbra/run/api.sock`
(`internal/paths/paths.go`), `paths.SSH()` = `~/.umbra/ssh` with key `id_ed25519`
(`cmd/umbra/shell.go`), CLI commands `umbra status --json`, `umbra start/stop <name>`,
`umbra docker start/stop/status` (`cmd/umbra/status.go`, `machines.go`, `docker.go`), ad-hoc
codesigning of `umbrad` only — the CLI needs no entitlement
(`docs/runbooks/entitlements-and-codesigning.md`). Design spec commitment
(`docs/superpowers/specs/2026-07-11-umbra-design.md:74-75`): "Thin client over the same JSON API
… No business logic in the app." P10 (`docs/PITFALLS-EXTERNAL.md`) requires bounded retry
(5 attempts, 200ms→2s) on first connection "in both CLI and menu bar app" — see §2 for why
shelling out to the CLI gets this for free instead of re-implementing it in Swift.

---

## 1. App architecture

**Recommendation: SwiftUI `MenuBarExtra`, not `NSStatusItem`/AppKit.**

`MenuBarExtra` is the native SwiftUI scene type for menu-bar-only apps, introduced in SwiftUI for
macOS 13 Ventura ([Apple docs](https://developer.apple.com/documentation/SwiftUI/MenuBarExtra)).
Since `umbrad` already requires macOS 13+ for Virtualization.framework, there's no deployment-target
reason to fall back to the older `NSStatusItem` + `NSPopover`/`NSMenu` AppKit dance that pre-Ventura
SwiftUI menu bar apps had to use. `MenuBarExtra` gives you a declarative `Scene`:

```swift
@main
struct UmbraApp: App {
    var body: some Scene {
        MenuBarExtra("Umbra", systemImage: "cube.fill") {
            MenuBarView()
        }
        .menuBarExtraStyle(.window)
    }
}
```

**Style: `.window`, not `.menu`.** Two styles exist
([`MenuBarExtraStyle` docs](https://developer.apple.com/documentation/swiftui/menubarextrastyle)):
- `.menu` (default) renders as a native macOS dropdown menu — text, buttons, dividers only; button
  styles and images are ignored to match system menu chrome, and the menu blocks the run loop while
  open. Fine for a flat action list, wrong for a status dot + scrollable machine list + toggles.
- `.window` renders a popover-like window that can host arbitrary SwiftUI content — `List`,
  `Toggle`, per-row `Button`s, colored status dots, progress indicators. This is what a "status +
  machine list with start/stop + docker toggle + open-shell" surface needs
  ([nilcoalescing.com walkthrough](https://nilcoalescing.com/blog/BuildAMacOSMenuBarUtilityInSwiftUI/),
  [sarunw.com walkthrough](https://sarunw.com/posts/swiftui-menu-bar-app/)). OrbStack's own menu bar
  app is the closest real analog — it lets you "start, stop, restart… open logs, terminals" for
  containers and "stop and restart… launch terminals" for machines, all from one menu bar surface
  ([OrbStack menu bar docs](https://docs.orbstack.dev/menu-bar)) — i.e. exactly M5's shape, and it's
  a rich window, not a flat menu.

  → **Use `.menuBarExtraStyle(.window)`.**

**Agent app (no Dock icon): `LSUIElement`.** Set `Application is agent (UIElement)` = `YES` in
`Info.plist` (raw key `LSUIElement`,
[Apple docs](https://developer.apple.com/documentation/bundleresources/information-property-list/lsuielement)).
This is the standard way to keep a menu-bar-only app out of the Dock and the Cmd-Tab switcher and
out of Force Quit. In an Xcode project it's a checkbox in the target's Info tab; in an SPM-assembled
bundle (§6) it's just a key in the `Info.plist` you hand-write and copy into `Contents/`.

**Known `MenuBarExtra` rough edges** (worth knowing before committing, not blockers):
Apple's native implementation "doesn't animate, doesn't close the pop-up when the user interacts
with other menu items", and has no first-party way to know when the window opened/closed or to
programmatically dismiss it (`FluidMenuBarExtra` README,
[github.com/wadetregaskis/FluidMenuBarExtra](https://github.com/wadetregaskis/FluidMenuBarExtra);
Apple feedback [FB13683950](https://github.com/feedback-assistant/reports/issues/475),
[FB11984872](https://github.com/feedback-assistant/reports/issues/383)). For M5's scope (status +
start/stop + toggle + shell button) these are cosmetic, not functional — ship with stock
`MenuBarExtra` first; only reach for a third-party wrapper like `FluidMenuBarExtra` or
`MenuBarExtraAccess` if the open/close detection in §4 proves too flaky in practice.

---

## 2. Talking to the unix socket — shell out to the CLI, don't hand-roll HTTP-over-unix

Three options exist:

| Option | What it takes | Verdict |
|---|---|---|
| (a) `Network.framework` `NWConnection` to `NWEndpoint.unix(path:)` + hand-write HTTP/1.1 | `NWEndpoint.unix(path:)` is real and works for AF_UNIX ([Apple docs / forum thread](https://developer.apple.com/forums/thread/756756), [darrellroot.github.io writeup](https://darrellroot.github.io/networking/network-socket/)) — but you're still writing an HTTP/1.1 request line + headers by hand and parsing status/headers/chunked-or-`Content-Length` body back out, reimplementing what `net/http`'s client already does on the Go side. `sockaddr_un`'s 104-byte `sun_path` limit applies to `NWEndpoint.unix` too (same limit `internal/client/client_test.go`'s `shortSocketDir` works around on the Go side) — `~/.umbra/run/api.sock` is well under it, not a concern here. | Possible, but duplicates protocol logic in a second language for no functional gain. |
| (b) raw `socket()`/`connect()` + `sockaddr_un` + hand-rolled HTTP | Same HTTP-parsing burden as (a), plus lower-level POSIX socket code (`Darwin.socket`, `Darwin.connect`) instead of `Network.framework`'s async API. | Strictly worse than (a) — no reason to drop below `Network.framework`. |
| (c) shell out to `umbra` CLI (`Process` + `Pipe`, parse `umbra status --json`) | The Go CLI already does the unix-socket HTTP dance via `internal/client/client.go`'s `New(sock)` — a custom `http.Transport.DialContext` that dials `"unix"` — **and already has the P10 bounded-retry-with-backoff baked in** (`client.go`'s `backoffs` = `200ms→2s`, 5 attempts, dial-errors-only). `umbra status --json` (`cmd/umbra/status.go`) already assembles exactly the payload shape the menu bar needs: `{"daemon":"up"/"down","machines":[...],"docker":{...}}`. | **Recommended.** |

**Recommend (c).** For a genuinely thin, no-business-logic client, shelling out to the
already-built-and-tested `umbra` binary is simpler *and* more robust than (a)/(b): the retry
policy, JSON shape, and HTTP-over-unix plumbing all live once, in Go, already covered by
`internal/client/client_test.go` (`TestClientRetriesUntilSocketAppears`,
`TestClientDoesNotRetryPostConnectionFailure`, `TestClientGivesUpWhenNoDaemon`). Re-implementing
that in Swift via `NWConnection` would mean carrying the same retry/parsing logic in two languages,
with the Swift copy untested against the real race the Go tests already pin down. It's also the
literal-most reading of "no business logic in the app" — the app doesn't even need to know the HTTP
framing, just how to run a subprocess and decode JSON.

> Note on the design spec's architecture diagram (`docs/superpowers/specs/2026-07-11-umbra-design.md:25-27`),
> which draws the menu bar app hitting `api.sock` directly, same as the CLI: shelling out to
> `umbra` still satisfies that diagram at the *system* level — the app still only talks to
> `umbrad` via `~/.umbra/run/api.sock`, just through the CLI process as a thin transport shim
> instead of a duplicate Swift HTTP-over-unix client. The "menu bar app" line in P10's mitigation
> (`docs/PITFALLS-EXTERNAL.md`) is satisfied because the retry lives in the CLI it shells out to.

### `Process` + `Pipe` pattern

```swift
import Foundation

enum CLIError: Error { case nonZeroExit(Int32, String), spawnFailed(Error) }

func runUmbra(_ args: [String]) async throws -> Data {
    try await withCheckedThrowingContinuation { cont in
        let process = Process()
        process.executableURL = URL(fileURLWithPath: umbraCLIPath()) // §2b
        process.arguments = args
        process.environment = ProcessInfo.processInfo.environment // inherit login-session PATH

        let stdout = Pipe()
        let stderr = Pipe()
        process.standardOutput = stdout
        process.standardError = stderr

        process.terminationHandler = { proc in
            let outData = stdout.fileHandleForReading.readDataToEndOfFile()
            let errData = stderr.fileHandleForReading.readDataToEndOfFile()
            if proc.terminationStatus == 0 {
                cont.resume(returning: outData)
            } else {
                let msg = String(data: errData, encoding: .utf8) ?? ""
                cont.resume(throwing: CLIError.nonZeroExit(proc.terminationStatus, msg))
            }
        }
        do { try process.run() } catch { cont.resume(throwing: CLIError.spawnFailed(error)) }
    }
}

// usage:
let data = try await runUmbra(["status", "--json"])
let status = try JSONDecoder().decode(StatusResponse.self, from: data)
```

Notes on this pattern, cited against real prior art
([Adonis Gaitatzis — Running Terminal Programs from Swift](https://gaitatzis.medium.com/running-terminal-programs-from-swift-680db09a02b4),
[Eclectic Light Co. — Lessons from Swift 2: Processes](https://eclecticlight.co/2017/01/17/lessons-from-swift-2-processes-running-commands-predicates-playgrounds/)):
- Use `process.executableURL` (macOS 10.13+), not the deprecated `launchPath` string property.
- Read both pipes and wait for `terminationHandler`, not `waitUntilExit()` on the main actor — large
  JSON on `stdout` can deadlock if you `waitUntilExit()` before draining a full pipe buffer (64KB);
  `status --json` output is small here but the pattern above avoids the trap regardless.
- Don't invoke via `/bin/zsh -c "umbra ..."` — call the binary directly with an argument array. No
  shell needed since there's no interpolation/globbing to worry about, and it avoids the
  shell-launch + `.zshrc` sourcing overhead entirely.

### CLI path discovery (§2b)

`umbra status --json` isn't on a fixed system path — in this repo's dev workflow the binary lives
at `bin/umbra` (built by `make build`, per the Makefile). Resolution order the app should use, first
match wins:
1. **Bundled with the app** — `Bundle.main.url(forAuxiliaryExecutable: "umbra")` if the CLI binary
   is copied into `Umbra.app/Contents/MacOS/` at build time (the sturdiest option once M5/M6 produce
   a real distributable — the app is then self-contained and immune to `PATH`/install-location
   drift).
2. **`/opt/homebrew/bin/umbra` / `/usr/local/bin/umbra`** — if `make install` or a future Homebrew
   formula symlinks the CLI onto `PATH` (matches the daemon plist's own `PATH`,
   `docs/research/launchd-and-ci-cutover.md`: `/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin`).
3. **Dev fallback** — `~/Desktop/projects/umbra/bin/umbra`, or an `UMBRA_CLI_PATH` env var override,
   for running the SwiftUI app straight from `swift run`/Xcode against this repo's `bin/` during M5
   development, without needing step 1/2 installed yet.

`Process` does **not** read the invoking user's shell `PATH` (a GUI app launched by `launchd`/Finder
gets a minimal login-session environment, not your `.zshrc` PATH) — this is the exact same
`/opt/homebrew` issue the M4 launchd plist already had to special-case
(`docs/research/launchd-and-ci-cutover.md`). Don't rely on `Process` + bare `"umbra"` +
`exec.LookPath`-style resolution; resolve an absolute path explicitly using the order above.

---

## 3. Codable models

Mirror `internal/client/client.go`'s `MachineView`/`DockerStatus` and `cmd/umbra/status.go`'s
JSON envelope (`{"daemon":..., "machines":..., "docker":...}`):

```swift
enum MachineState: String, Codable {
    case stopped, starting, running, stopping, crashed
}

struct Machine: Codable, Identifiable {
    var id: String { name }
    let name: String
    let state: MachineState
    let ip: String?
    let sshPort: Int?
    let cpus: Int
    let memoryMiB: Int
    let diskGiB: Int
    let image: String?
    let autostart: Bool
    let zombie: Bool?

    enum CodingKeys: String, CodingKey {
        case name, state, ip, cpus, image, autostart, zombie
        case sshPort = "ssh_port"
        case memoryMiB = "memory_mib"
        case diskGiB = "disk_gib"
    }
}

struct DockerStatus: Codable {
    let installed: Bool
    let running: Bool
    let ip: String?
    let socket: String?
    let contextCurrent: Bool

    enum CodingKeys: String, CodingKey {
        case installed, running, ip, socket
        case contextCurrent = "context_current"
    }
}

struct StatusResponse: Codable {
    let daemon: String              // "up" | "down"
    let error: String?              // present when daemon == "down"
    let machines: [Machine]?
    let docker: DockerStatus?
}
```

`Machine.zombie` mirrors `MachineView.Zombie` (`internal/client/client.go`) — surface it in the UI
(state badge "crashed*") the same way `umbra list` does
(`cmd/umbra/machines.go`: `"crashed*"` + the `hasZombie` footer hint), don't drop it silently.

---

## 4. Polling / refresh

- A `Timer.publish(every: 2...3, on: .main, in: .common)` (or a `Task` with
  `try await Task.sleep(for: .seconds(2))` in a loop, which composes better with Swift concurrency
  and cancellation) drives refresh while the popover is open.
- State lives in an `@Observable` model (macOS 14+ `Observation` framework) or, for a macOS
  13-compatible baseline, an `ObservableObject` with `@Published var status: StatusResponse?`.
- **Only poll while the window is open.** `MenuBarExtra` has no first-party "did open/close" event
  as of macOS 15 (`FB13683950`, cited above) — the practical workaround is watching
  `NSApplication.shared.keyWindow` becoming non-nil, or `scenePhase` if using a wrapper like
  `FluidMenuBarExtra`
  ([Damian Mehers — Detecting when a MenuBarExtra window is opened](https://damian.fyi/swift/2022/12/29/detecting-when-a-swiftui-menubarextra-with-window-style-is-opened.html)).
  Minimum viable version for M5: fire one refresh in `.onAppear` of the popover's root view (runs
  each time the window is shown) and start/stop the `Timer`/loop there and in `.onDisappear` — cruder
  than a real `isInserted`/open-state binding, but requires no third-party dependency and is
  accurate enough for a 2-3s poll cadence. Revisit with `MenuBarExtraAccess` or `FluidMenuBarExtra`
  only if `.onAppear`/`.onDisappear` prove unreliable in practice.
- Do not poll on a timer while the window is closed — there's nothing to render, and it wastes a
  `Process` spawn + daemon round-trip every 2-3s indefinitely (battery/CPU on an always-running menu
  bar agent). A cheap always-on signal (icon color = daemon up/down) can stay on a much slower
  cadence (e.g. 30s) independent of the rich-content poll, if you want the icon itself to reflect
  daemon liveness even with the window closed.

---

## 5. Actions

**Start/stop a machine, docker toggle** — same `Process`-based call pattern as §2, async, then
re-run the status poll immediately after (don't wait for the next timer tick — the whole point of a
thin client is the daemon is the source of truth, so re-fetch right after a mutating call):

```swift
try await runUmbra(["start", machine.name])
await refreshStatus()
```

```swift
try await runUmbra(["stop", machine.name])
try await runUmbra(["docker", "start"])   // / "stop"
```

Disable the row's button (or show a spinner) between issuing the command and the next successful
refresh, since `umbra start`/`stop` block until the daemon confirms state (`cmd/umbra/machines.go`)
— i.e. the `Process` call itself won't return until the machine is actually up/down, so there's a
real multi-second wait to show progress for, not just network latency.

**Open shell — the tricky one.** The daemon doesn't expose a shell over the API; `umbra shell`
(`cmd/umbra/shell.go`) works by `syscall.Exec`-ing straight into `ssh` in the CLI's own process,
which only makes sense inside an interactive terminal. The menu bar app needs to hand off to an
actual terminal emulator instead. Recommended, in order of simplicity:

1. **`open` with a `-a` target and inline command via `NSWorkspace`/`Process`.** Simplest reliable
   option: run
   ```swift
   let sshCmd = "ssh -i \(sshKeyPath) -o StrictHostKeyChecking=accept-new -p \(port) umbra@127.0.0.1"
   let script = "tell application \"Terminal\" to do script \"\(sshCmd)\""
   ```
   via **`NSAppleScript`**, not shelling out to `/usr/bin/osascript` as a subprocess — `NSAppleScript`
   runs in-process and doesn't need a second `Process` spawn
   ([Apple Developer Forums — do script with command](https://developer.apple.com/forums/thread/681647)).
   Escape `sshCmd` for the AppleScript string literal (backslash/quote escaping) before interpolating.
   This is the standard "open Terminal running a specific command" recipe and is what most
   AppleScript-based launchers use; caveat noted in the same forum thread — if Terminal isn't already
   running, launching it *and* running `do script` in the same call can race and open two windows,
   so consider a short `NSWorkspace.shared.launchApplication` (or `open -a Terminal` with no args)
   pre-warm before the `do script` call if this is observed in testing.
2. **Alternative: `NSWorkspace.open(URL, configuration:)` is not usable here** — there's no
   `ssh://` URL scheme Terminal.app registers for pre-filled commands, so this path doesn't apply;
   AppleScript's `do script` is the actual mechanism, including for third-party terminals (iTerm
   accepts the analogous `tell application "iTerm" to create window with default profile command
   "<cmd>"`).
3. Build the exact `ssh` invocation from `cmd/umbra/shell.go`'s own args, so the menu bar app and
   the CLI never drift: `-i ~/.umbra/ssh/id_ed25519`, `-o StrictHostKeyChecking=accept-new`,
   `-o UserKnownHostsFile=~/.umbra/ssh/known_hosts`, `-p <ssh_port>`, `umbra@127.0.0.1`. Get
   `ssh_port` from the `Machine` the row is already showing (`GET /v1/machines` → `ssh_port`); if
   it's `0`/nil, the row's button should be disabled with the same reasoning `shell.go` uses
   ("not reachable — start it first").

   Simpler alternative worth calling out explicitly: **shell out to `umbra shell <name>` inside the
   Terminal `do script` command instead of reconstructing the `ssh` invocation in Swift** —
   `do script "umbra shell \(machine.name)"` — so the exact-args logic lives once, in
   `cmd/umbra/shell.go`, not duplicated in the Swift layer. This is more in the spirit of "no
   business logic in the app" than re-deriving the `ssh` flags; recommend this over embedding the
   raw `ssh` command.

**No sandbox.** None of `Process`, AppleScript `do script` targeting another app, or reading
`~/.umbra/ssh/*` work under the App Sandbox without extra entitlements/XPC plumbing — a sandboxed
child process can only inherit the *exact same* sandbox via `com.apple.security.inherit`, and
Apple-events-to-other-apps (`do script`) is specifically restricted by the sandbox
([Indie Stack — Sandbox Inheritance Tax](https://indiestack.com/2017/09/sandbox-inheritance-tax/);
[Apple — Configuring the macOS App Sandbox](https://developer.apple.com/documentation/xcode/configuring-the-macos-app-sandbox)).
Since this is a local-only, not-App-Store tool (like `umbrad` itself, ad-hoc signed, no Developer ID
requirement), **do not enable App Sandbox** — same posture the daemon already takes.

---

## 6. Build: Swift Package Manager, not an `.xcodeproj`

**Recommendation: SPM executable target + a `Makefile`/script step that assembles a real `.app`
bundle.** This fits the repo's existing `make`-based, no-Xcode-GUI workflow (`make build` already
builds `umbrad`/`umbra`, `Makefile`) with one more target.

**Why a real `.app` bundle, not just `swift build`'s bare executable + `Info.plist` embedded via a
linker `-sectcreate` trick:** the `-sectcreate __TEXT __info_plist` approach
([polpiella.dev — Adding an Info.plist to a Swift executable](https://www.polpiella.dev/info-plist-swift-cli/))
does let a *bare* Mach-O carry Info.plist metadata without a Package.swift resource, but it's
designed for lightweight menu-bar prototypes run via `swift run` — a persistent background agent
benefits from a real bundle identity (stable `CFBundleIdentifier` for Launch Services / login-item
registration / future notifications), which a loose executable doesn't reliably get. The
`scriptingosx.com` writeup on packaging SPM executables is explicit about this:  a GUI app "should
be bundled and launched as a `.app` instead of running the raw executable directly to avoid missing
Dock, activation, and bundle-identity issues"
([Build a notarized package with a Swift Package Manager executable](https://scriptingosx.com/2023/08/build-a-notarized-package-with-a-swift-package-manager-executable/)).

**The pattern** (matches Joseph Long's Makefile-based `.app` bundling —
[App Bundles with a Makefile](https://joseph-long.com/writing/app-bundles-with-a-makefile/) —
and the general SPM-executable-as-GUI-app recipe):

```
apps/menubar/Package.swift        # executableTarget "Umbra"
apps/menubar/Sources/Umbra/...    # SwiftUI sources (App, Views, CLIClient, models)
apps/menubar/Resources/Info.plist # LSUIElement=YES, CFBundleIdentifier, CFBundleName, etc.
apps/menubar/Resources/AppIcon.icns
```

```makefile
# added to the repo Makefile (or apps/menubar/Makefile), alongside the existing `build` target
APP := $(BIN)/Umbra.app

app: build
	swift build -c release --package-path apps/menubar
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS $(APP)/Contents/Resources
	cp apps/menubar/.build/release/Umbra $(APP)/Contents/MacOS/Umbra
	cp $(BIN)/umbra $(APP)/Contents/MacOS/umbra          # bundle the CLI (§2b resolution order #1)
	cp apps/menubar/Resources/Info.plist $(APP)/Contents/Info.plist
	cp apps/menubar/Resources/AppIcon.icns $(APP)/Contents/Resources/AppIcon.icns
	codesign --force --deep --sign - $(APP)
```

Key points, matching how the daemon is already ad-hoc signed
(`docs/runbooks/entitlements-and-codesigning.md`):
- `codesign --sign -` (ad-hoc, no Apple Developer Program) is enough for local use, same posture as
  `umbrad`. The menu bar app needs **no entitlements file** — it doesn't touch Virtualization.framework,
  it just runs `Process` and AppleScript, neither of which needs `com.apple.security.virtualization`
  or any other entitlement, provided the app is **not** sandboxed (§5).
- `--deep` (or explicit per-component signing) matters once the bundled `umbra` CLI binary is
  inside `Contents/MacOS/` too — both binaries need valid ad-hoc signatures.
- This whole `app` target is additive to the existing `Makefile` — `make build` (Go daemon+CLI)
  stays untouched; `make app` is new and depends on it (bundles the just-built `umbra`).

**When an `.xcodeproj` would still make sense:** SPM-built menu bar apps are common in the OSS
ecosystem specifically to avoid Xcode project files (`FluidMenuBarExtra`, cited above, ships as a
pure SPM library consumed by SPM executables) — there's no *functional* blocker to staying
SPM-only for M5's scope. The main reason to reach for Xcode later (M6+, not M5) would be code
signing with a real Developer ID + notarization for public distribution outside this repo's
`make`/local-install flow — `scriptingosx.com`'s companion notarization post covers that path when
it's actually needed, but Ahmad's own build/run loop doesn't need it now (same reasoning M6's "OSS
release polish" milestone already defers signed release artifacts to itself, per
`docs/superpowers/specs/2026-07-11-umbra-design.md:107`).

---

## 7. Icons / status

- **Menu bar icon**: an SF Symbol via `MenuBarExtra("Umbra", systemImage: "cube.fill")` (or
  `"server.rack"`/`"shippingbox.fill"` as alternates — pick one that reads clearly at menu-bar size).
  SF Symbols rendered in the menu bar are **template images by default** when supplied via
  `systemImage:` — SwiftUI/AppKit renders template images as monochrome, auto-adapting to light/dark
  menu bar backgrounds and to the "reduce transparency"/dark-menu-bar-on-light-desktop cases,
  matching how every other native menu bar icon behaves. If you instead load a custom asset (not an
  SF Symbol), mark it as a template explicitly: `NSImage.isTemplate = true` (AppKit) or, in SwiftUI,
  `Image(...).renderingMode(.template)`.
- **Status dot**: not achievable via the menu bar icon itself with a single-color template image —
  render it as a small colored `Circle()` overlay *inside* the `.window`-style popover content
  (e.g. next to "daemon: up"), not on the status-bar glyph. If a colored indicator on the menu bar
  icon itself is wanted later, that requires a non-template (full-color) `NSImage` composited from
  SF Symbol + color, which opts out of automatic light/dark adaptation — skip this for M5; the
  in-popover dot covers the "at a glance" need once the user has clicked in, and the always-visible
  glyph stays a plain template icon.

---

## 8. Pitfalls

- **`MenuBarExtra` open/close detection has no first-party API** — see §4; use `.onAppear`/
  `.onDisappear` on the popover root view as the pragmatic M5-scope signal, accept it's not a true
  `isInserted`/`scenePhase` hook without a third-party wrapper.
- **`.window` style sizing/positioning quirks** — the popover doesn't animate size changes and
  doesn't reliably reposition itself if it would extend past a screen edge on multi-monitor setups
  (`FluidMenuBarExtra` README, cited above). Keep the M5 content to a fixed/near-fixed size (a
  bounded-height `List` with, say, `.frame(width: 320)`) to sidestep the resizing-animation gap
  rather than building for dynamic height.
- **`Process` PATH/cwd/env is not your login shell's** — covered in §2b; a GUI app launched via
  Finder/Launch Services (or in dev, `swift run`/Xcode) does not inherit `~/.zshrc`'s `PATH`. Always
  resolve the `umbra` binary to an absolute path; never rely on bare `"umbra"` + inherited `PATH`
  resolution the way a Terminal-launched process could.
- **Finding the `umbra` binary** — bundle it into `Contents/MacOS/` at `make app` time (§6) as the
  primary path; this is the only option immune to `PATH` drift and to the dev repo directory moving.
- **Do not sandbox the app** — App Sandbox blocks or heavily restricts `Process` execution and
  Apple-events (`do script` to Terminal); this app needs both, plus filesystem read access to
  `~/.umbra/ssh/*` if it ever constructs an `ssh` command directly instead of delegating to
  `umbra shell` (§5 recommends delegating specifically to sidestep this). Same non-sandboxed,
  ad-hoc-signed posture as `umbrad`.
- **App doesn't appear until the `MenuBarExtra` scene renders** — for a menu-bar-only (`LSUIElement`)
  app there's no Dock icon to indicate "launching"; if `Info.plist`/bundle assembly is wrong (missing
  `LSUIElement`, bad bundle ID, unsigned in a way Gatekeeper rejects) the app can silently fail to
  register a menu bar item with no visible error. Test by launching from Terminal
  (`open bin/Umbra.app`) first, where launch failures print to the terminal, before relying on
  Finder double-click during development.
- **Refresh-while-closed battery drain** — covered in §4; gate the 2-3s rich-content poll strictly
  on window-open state. An always-on daemon-liveness ping (icon color) can run far less frequently
  (e.g. every 30s) than the full `status --json` + machine-list poll.
- **`umbra start`/`stop` block until confirmed** — a naive "fire `Process` call, immediately refresh"
  sequence works because the call itself doesn't return until the daemon confirms state (§5); don't
  add an artificial delay before refreshing, and do show per-row busy state for the (multi-second)
  duration of the call.

---

## Sources

- [MenuBarExtra — Apple Developer Documentation](https://developer.apple.com/documentation/SwiftUI/MenuBarExtra)
- [MenuBarExtraStyle — Apple Developer Documentation](https://developer.apple.com/documentation/swiftui/menubarextrastyle)
- [LSUIElement — Apple Developer Documentation](https://developer.apple.com/documentation/bundleresources/information-property-list/lsuielement)
- [Configuring the macOS App Sandbox — Apple Developer Documentation](https://developer.apple.com/documentation/xcode/configuring-the-macos-app-sandbox)
- [Build a macOS menu bar utility in SwiftUI — nilcoalescing.com](https://nilcoalescing.com/blog/BuildAMacOSMenuBarUtilityInSwiftUI/)
- [Create a mac menu bar app in SwiftUI with MenuBarExtra — sarunw.com](https://sarunw.com/posts/swiftui-menu-bar-app/)
- [OrbStack menu bar app docs](https://docs.orbstack.dev/menu-bar)
- [OrbStack architecture docs](https://docs.orbstack.dev/architecture)
- [FluidMenuBarExtra — wadetregaskis (GitHub)](https://github.com/wadetregaskis/FluidMenuBarExtra)
- [MenuBarExtraAccess — orchetect (GitHub)](https://github.com/orchetect/MenuBarExtraAccess)
- [Detecting when a MenuBarExtra window is opened — damian.fyi](https://damian.fyi/swift/2022/12/29/detecting-when-a-swiftui-menubarextra-with-window-style-is-opened.html)
- [Apple feedback FB13683950 — no open event for MenuBarExtra](https://github.com/feedback-assistant/reports/issues/475)
- [Apple feedback FB11984872 — no programmatic close for MenuBarExtra window style](https://github.com/feedback-assistant/reports/issues/383)
- [NWEndpoint.unix / Unix Domain Socket — Apple Developer Forums](https://developer.apple.com/forums/thread/756756)
- [Opening a socket with Network Framework — darrellroot.github.io](https://darrellroot.github.io/networking/network-socket/)
- [Running Terminal Programs from Swift — Adonis Gaitatzis (Medium)](https://gaitatzis.medium.com/running-terminal-programs-from-swift-680db09a02b4)
- [Lessons from Swift 2: Processes — Eclectic Light Co.](https://eclecticlight.co/2017/01/17/lessons-from-swift-2-processes-running-commands-predicates-playgrounds/)
- [How to open and run a terminal command — Apple Developer Forums](https://developer.apple.com/forums/thread/681647)
- [Sandbox Inheritance Tax — Indie Stack](https://indiestack.com/2017/09/sandbox-inheritance-tax/)
- [Adding an Info.plist file to a Swift executable — polpiella.dev](https://www.polpiella.dev/info-plist-swift-cli/)
- [App Bundles with a Makefile — Joseph Long](https://joseph-long.com/writing/app-bundles-with-a-makefile/)
- [Build a notarized package with a Swift Package Manager executable — scriptingosx.com](https://scriptingosx.com/2023/08/build-a-notarized-package-with-a-swift-package-manager-executable/)

## Repo grounding (not external, cited throughout above)

- `internal/paths/paths.go` — `~/.umbra/run/api.sock`, `~/.umbra/ssh/`
- `internal/client/client.go` + `client_test.go` — unix-socket HTTP dial, P10 retry/backoff, `MachineView`/`DockerStatus` shapes
- `cmd/umbra/status.go`, `machines.go`, `docker.go`, `shell.go` — exact CLI commands + JSON envelope + `ssh` invocation
- `docs/runbooks/entitlements-and-codesigning.md` — ad-hoc signing, CLI needs no entitlement
- `docs/superpowers/specs/2026-07-11-umbra-design.md` — architecture diagram, M5 scope, "no business logic in the app"
- `docs/PITFALLS-EXTERNAL.md` (P10) — client retry requirement for "both CLI and menu bar app"
- `docs/research/launchd-and-ci-cutover.md` — `PATH` issue precedent for `launchd`-launched processes
- `Makefile` — existing `make build` pattern this doc's `make app` target extends
