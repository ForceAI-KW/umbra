# Full macOS app + .dmg — design + build cheat-sheet (Umbra M7)

**Purpose**: turn the M5 menu-bar-only thin client (`docs/research/menubar-app.md`) into a normal
macOS app — dock icon, main window dashboard, Settings (Cmd-,), first-run install flow — packaged
as a drag-to-Applications `.dmg`. Builds on the existing SPM + `make`-based, no-Xcode-GUI workflow
(`Makefile`'s `app` target) and the existing ad-hoc-signing posture
(`docs/runbooks/entitlements-and-codesigning.md`).

Grounded in the actual repo state: `apps/menubar/Sources/UmbraMenuBar/{UmbraApp,MenuBarView,
StatusModel,CLIClient,Models}.swift`, `apps/menubar/Resources/Info.plist` (`LSUIElement=YES`,
`CFBundleIdentifier co.forceai.umbra.menubar`), `Makefile`'s `app`/`release` targets, `scripts/
install.sh` (copies `umbrad`/`umbra` to `/usr/local/bin`, re-signs in place, then runs `umbra daemon
install --bin "$BIN_DIR/umbrad"`), `internal/launchagent/launchagent.go` (`RenderPlist` bakes
`binPath` verbatim into `ProgramArguments[0]` — whatever path you hand `daemon install --bin` is
what launchd execs forever after, until the next `daemon install`), `cmd/umbra/daemon.go`
(`daemon install --bin <path>`, defaults to `bin/umbrad` next to the running `umbra` binary or
`$UMBRA_BIN`).

---

## 1. Coexisting scenes — Window + Settings + MenuBarExtra in one `App`

**Yes — a single `App.body` can declare all three.** `Scene` is a `Result`/`TupleScene`-composable
protocol; `WindowGroup`, `Settings`, and `MenuBarExtra` are just three scene types you list in the
same `some Scene` body, same as combining `WindowGroup` + `Settings` in any ordinary Mac app
([Scenes types in a SwiftUI Mac app — nilcoalescing.com](https://nilcoalescing.com/blog/ScenesTypesInASwiftUIMacApp/);
[Understanding scenes for your macOS app — createwithswift.com](https://www.createwithswift.com/understanding-scenes-for-your-macos-app/)):

```swift
@main
struct UmbraApp: App {
    @StateObject private var model = StatusModel()

    var body: some Scene {
        Window("Umbra", id: "main") {
            DashboardView()
                .environmentObject(model)
        }

        Settings {
            SettingsView()
                .environmentObject(model)
        }

        MenuBarExtra("Umbra", systemImage: "cube.fill") {
            MenuBarView()
                .environmentObject(model)
        }
        .menuBarExtraStyle(.window)
    }
}
```

**`Window`, not `WindowGroup`, for the dashboard.** `Window(_:id:content:)` was introduced alongside
`openWindow`/`dismissWindow` in SwiftUI for **macOS 13 Ventura** (WWDC22 session 10061, "Bring
multiple windows to your SwiftUI app" —
[video](https://developer.apple.com/videos/play/wwdc2022/10061/)) — same deployment floor the repo
already commits to for Virtualization.framework, so no downgrade needed. `Window` creates exactly
**one instance** of a scene you control programmatically — the right fit for a dashboard that should
never have two copies open, versus `WindowGroup`, whose whole purpose is letting the user open
multiple independent instances (each with its own `@State`) of the same template
([WindowGroup docs](https://developer.apple.com/documentation/swiftui/windowgroup); [Window
management in SwiftUI — Swift with Majid](https://swiftwithmajid.com/2022/11/02/window-management-in-swiftui/)).
Trade-off: `Window` is more restricted than `WindowGroup` (e.g., you can't attach `.commands` to it
the way you can a `WindowGroup`) — irrelevant here since the app has one dashboard, not a document
model.

**Sharing `StatusModel` between the window and the menu bar extra.** Use the pattern already in
`UmbraApp.swift` — one `@StateObject private var model = StatusModel()` at the `App` level,
`.environmentObject(model)` injected into *both* the `Window`'s root view and the `MenuBarExtra`'s
content. SwiftUI's `@StateObject` at the `App` struct level is created once for the app's lifetime
and is exactly what makes this safe: both scenes read/write the same `ObservableObject` instance, so
starting a machine from the dashboard updates the menu bar popover's list on the next poll tick with
no extra plumbing. (A plain singleton — `StatusModel.shared` — would also work and is a reasonable
alternative if you want the daemon-liveness icon-color ping in §7 of the M5 doc to run independent of
either scene being open, but the `@StateObject`+`environmentObject` pattern is simpler and matches
what M5 already does.)

**Ordering / declaration-order gotcha (only matters for `LSUIElement`/accessory apps — see §2 for why
it does *not* apply to Umbra's target design):** Peter Steinberger's write-up of combining
`MenuBarExtra` + `Settings` documents that in an **accessory-policy** (`LSUIElement=YES`, no dock
icon) app, `SettingsLink`/`openSettings()` can fail silently because the app has no window context to
activate from, and the documented workaround needs a hidden 1×1 `Window` declared **before** the
`Settings` scene plus temporary `NSApp.setActivationPolicy(.regular)` toggling around the
`openSettings()` call
([Showing Settings from macOS Menu Bar Items: A 5-Hour Journey — steipete.me](https://steipete.me/posts/2025/showing-settings-from-macos-menu-bar-items)).
**This whole class of pain is specific to accessory/no-dock-icon apps.** Once Umbra is a regular
foreground app (§2), it has normal window/activation context, `Cmd-,` and `SettingsLink` work exactly
like they do in any ordinary Mac app, and none of Steinberger's workaround is needed — this citation
is here so you don't accidentally reach for that workaround, not because you need it.

---

## 2. Dock icon vs `LSUIElement`

**Set `LSUIElement` to `NO` (or delete the key entirely — its default is `NO`)** in
`apps/menubar/Resources/Info.plist`, replacing the M5-era `<key>LSUIElement</key><true/>`
([`LSUIElement` — Apple Developer Documentation](https://developer.apple.com/documentation/bundleresources/information-property-list/lsuielement)).
This makes Umbra a **regular** foreground app (`NSApplicationActivationPolicyRegular`): dock icon,
Cmd-Tab entry, Force Quit visibility, and — critically — full window/menu-bar/activation context so
Settings and the main window behave like any normal Mac app, sidestepping all of §1's accessory-app
gotchas.

**A regular app can still have a `MenuBarExtra`.** There's no exclusivity — `MenuBarExtra` is just
another scene; declaring it alongside `Window`/`Settings` in a regular-policy app gives you a bonus
menu-bar icon on top of the dock icon, same composition as VS Code, Docker Desktop, or OrbStack (cited
in the M5 doc), all of which are regular apps with dock icons that *also* have a menu bar item.

**No manual `NSApp.setActivationPolicy(.regular)` call needed** for the base case — `LSUIElement=NO`
in `Info.plist` is the declarative way to get regular policy from launch, and it's simpler than
setting it programmatically in `applicationDidFinishLaunching` (that imperative approach is what
accessory-app-turned-regular apps need when they want to *toggle* dock visibility at runtime — e.g. a
screenshot tool that shows a dock icon only while its editor window is open —
[Show/Hide dock icon on macOS App — Jie Zhang](https://medium.com/@jackymelb/show-hide-dock-icon-on-macos-app-3a59f7df282d),
[Fine-Tuning macOS App Activation Behavior — artlasovsky.com](https://artlasovsky.com/fine-tuning-macos-app-activation-behavior)).
Umbra doesn't need that toggle: it's a regular app, full stop.

**Window shows on launch automatically.** A `Window` scene declared in the `App` body opens by
default when the app launches (`SceneLaunchBehavior.automatic`, the default) — you don't need to call
`openWindow` yourself in `applicationDidFinishLaunching`. If you ever add a *second* auxiliary
`Window`/`WindowGroup` (e.g. a floating log viewer) that should *not* auto-reopen on relaunch, mark it
`.defaultLaunchBehavior(.suppressed)` — but that modifier is macOS 15+ only
([`defaultLaunchBehavior(_:)` docs](https://developer.apple.com/documentation/swiftui/scene/defaultlaunchbehavior(_:))),
so gate it behind `#available` if the app ever needs to run on macOS 13/14 for anything beyond the
main dashboard window. Not needed for M7's scope (one `Window`, always shown).

**Recommendation — simplest "normal app" behavior, do this:**
- `LSUIElement` = `NO` (or removed).
- One `Window` (the dashboard) — opens automatically on launch, no extra code.
- `Settings` scene reachable via `Cmd-,` / the app menu, works out of the box on a regular app.
- `MenuBarExtra` stays, as a convenience surface (matches the "menu bar extra as a bonus" framing) —
  keep it, don't remove the M5 work.
- **Quits on `Cmd-Q` / last-window-close is fine — don't add "close to menu bar" behavior.** A
  regular Mac app's default is: closing the last window does *not* quit the app (standard AppKit
  behavior, same as Xcode/Safari/Mail) — the dock icon and menu bar stay live with no windows open,
  and the user quits via `Cmd-Q` or dock-icon-right-click → Quit. This is already what SwiftUI gives
  you for free with a regular-policy app; you do **not** need
  `applicationShouldTerminateAfterLastWindowClosed` overridden to `false` via an
  `NSApplicationDelegateAdaptor` — that delegate method exists specifically for apps that need
  *document-style* "quit when last window closes" behavior turned *off* is already the default for
  non-document apps; only document-based/single-window-utility apps that *want* quit-on-last-close
  need to flip it *to* `true` explicitly
  ([`NSApplicationDelegateAdaptor` docs](https://developer.apple.com/documentation/swiftui/nsapplicationdelegateadaptor);
  [How to keep your macOS app's menu bar item running after quitting the app — polpiella.dev](https://www.polpiella.dev/keep-menu-bar-running-after-quitting-app)
  — relevant for the *inverse* case, an accessory app wanting the menu bar to survive dock-icon quit,
  which doesn't apply here). Net effect for Umbra: closing the dashboard window leaves the menu bar
  extra live (same as OrbStack/Docker Desktop's dock-app-plus-menu-bar-icon pattern) and the user has
  two ways back in — reopen the window from the dock icon, or from the menu bar extra — plus a normal
  `Cmd-Q` to fully quit. No custom window-close-hides-instead-of-closes hook required.

---

## 3. First-run onboarding / install flow

**Trigger**: on launch, if `umbra status --json` fails (CLI unreachable, or reachable but
`daemon: "down"` — the exact `StatusModel.refresh()` failure path that already exists in
`StatusModel.swift`), show a setup screen instead of (or on top of) the dashboard. This is the same
signal M5 already computes (`cliMissing`, `status?.daemon`), just routed to a different view instead
of the "down" badge.

**Bundling `umbrad` (with its entitlement) alongside `umbra` in `Contents/MacOS/`:** the `Makefile`'s
`app` target already copies `bin/umbra` into the bundle
(`cp $(BIN)/umbra $(APP)/Contents/MacOS/umbra`); extend it to also copy `bin/umbrad`
(`cp $(BIN)/umbrad $(APP)/Contents/MacOS/umbrad`) — both are auxiliary executables next to the main
`CFBundleExecutable`, which is exactly the layout `Bundle.main.url(forAuxiliaryExecutable:)` (already
used in `CLIClient.swift`'s `umbraCLIPath()`) is designed to resolve.

**The `--deep` entitlement-stripping trap — this is the one to get right.** `make build` already
signs `bin/umbrad` correctly: `codesign --force --entitlements build/vz.entitlements --sign -
$(BIN)/umbrad` (Makefile line 14) *before* `make app` copies it into the bundle. The bug to avoid is
in the **assembly step**: the current `app` target's final line is
`codesign --force --deep --sign - $(APP)` — Apple's own guidance is explicit that `--deep` is the
wrong tool here: *"Do not use the `--deep` argument. This feature is helpful in some specific
circumstances but it will cause problems when signing a complex program"* — because `--deep` applies
the **top-level bundle's** signing parameters (identity, entitlements) to every nested Mach-O it finds,
which for Umbra means it would **re-sign `umbrad` with the outer app's entitlements — i.e. none —
silently stripping `com.apple.security.virtualization` and breaking VM boot** the next time `make app`
runs after this bundling change goes in (`--deep` sourced from
[Apple Codesigning In Depth: Part I — Kayla McArthur](https://kayla.is/posts/codesigning-part-i/) and
the [codesign(1) man page](https://keith.github.io/xcode-man-pages/codesign.1.html); real-world
confirmation in
[macOS distribution — code signing, notarization… — rsms gist](https://gist.github.com/rsms/929c9c2fec231f0cf843a1a746a416f5)
and the [Apple Developer Forums thread on codesigning every binary](https://developer.apple.com/forums/thread/129678)).

**The fix — sign bottom-up, drop `--deep`:**
1. `umbrad` gets its entitlements from `make build` (already correct, unchanged).
2. `make app` copies both `umbra` and `umbrad` into `Contents/MacOS/` — **do not re-sign either of
   them** at this step; they arrive already signed (`umbrad` with the vz entitlement, `umbra` plain,
   both from `make build`).
3. Sign the **outer bundle only**, without `--deep`: `codesign --force --sign - $(APP)`. Apple's own
   procedure for nested content is "recursively sign all the helpers, tools, libraries… from the
   inside out" *manually*, then sign the container last, and a plain (non-`--deep`) `codesign
   --verify` on the top-level bundle is sufficient to verify the whole tree if every nested item was
   signed correctly on the way in
   ([Code Signing Tasks — Apple Developer Archive](https://developer.apple.com/library/archive/documentation/Security/Conceptual/CodeSigningGuide/Procedures/Procedures.html)).
4. **Verify in CI/the build script, every time**, not just once by hand:
   `codesign -d --entitlements :- $(APP)/Contents/MacOS/umbrad` and grep for
   `com.apple.security.virtualization` — fail the build target if it's missing. This is a cheap
   regression guard against a future edit reintroducing `--deep` or a `cp` that clobbers the signed
   binary after signing.

This directly matches the pattern `scripts/install.sh` **already uses** for the non-bundled path:
after `cp "$UMBRAD" "$BIN_DIR/umbrad"`, it explicitly **re-signs with the entitlements file** rather
than assuming the copy preserved the signature (`codesign --force --entitlements
"$here/../build/vz.entitlements" --sign - "$BIN_DIR/umbrad"`) — because a raw `cp` of a signed Mach-O
does **not** reliably survive as a valid signature once relocated/rewritten by some tools, so
re-signing explicitly (with the entitlements file, every time) is the safe default whether inside a
bundle or on `/usr/local/bin`. Apply the same discipline in `make app`: either avoid touching `umbrad`
post-`make build` (recommended — no need to re-sign what wasn't modified) or, if the bundling step
ever needs a `cp`/`ditto` that could disturb the signature, re-sign with `build/vz.entitlements`
immediately after, exactly like `install.sh` does.

**LaunchAgent target: point at `/usr/local/bin`, not the in-bundle path — recommended.** Two options:

| Option | How | Trade-off |
|---|---|---|
| **A — copy to `/usr/local/bin`, LaunchAgent points there** | Onboarding "Install" button: `cp` the bundled `umbrad`/`umbra` to `/usr/local/bin` (prompting for admin via the same writable-dir-check `scripts/install.sh` already does), re-sign in place with `build/vz.entitlements` (bundle the entitlements plist too, or re-derive it — see below), then run the bundled `umbra daemon install --bin /usr/local/bin/umbrad`. | App-bundle-independent: the LaunchAgent survives the user deleting/replacing/updating `Umbra.app` (a `.dmg`-driven "drag a new version over the old one" update — very likely for Umbra's actual distribution model — otherwise breaks the LaunchAgent, since `RenderPlist` bakes the exact `binPath` string into `ProgramArguments[0]` and nothing re-runs `daemon install` on app update unless you build that in). Needs write access to `/usr/local/bin` (handled: `scripts/install.sh`'s sudo-only-if-needed check). Matches the **existing, already-shipped** `scripts/install.sh` behavior exactly — one code path, one mental model, for both the CLI-tarball install and the new in-app onboarding install. |
| **B — LaunchAgent points at the path inside `Umbra.app`** (`/Applications/Umbra.app/Contents/MacOS/umbrad`) | Onboarding "Install" button just runs `umbra daemon install --bin <bundle-path-to-umbrad>` directly, no copy. | Self-contained, no `/usr/local/bin` write needed, no sudo prompt. **But breaks on every app update or move**: dragging a new `Umbra.app` from a new `.dmg` over `/Applications/Umbra.app` replaces the file at that exact path, so this *specific* failure mode is survivable *if* the path is always `/Applications/Umbra.app/...` — but it's **not** survivable if the user renames the app, moves it out of `/Applications`, or (worse) if a user runs the app once from `~/Downloads` before dragging it to `/Applications` (a genuinely common `.dmg` user-error path) — the LaunchAgent would then point at a path that vanishes the moment they delete the `.dmg`-mounted copy or the `~/Downloads` copy. `RenderPlist`'s `ProgramArguments[0]` is a static string baked in at `daemon install` time — there is no "resolve relative to running app" indirection in `launchagent.go` today, and adding one would be a real design change (either a wrapper shell script the LaunchAgent execs, or teaching `Install()` to re-resolve on every launchd start — neither exists now). |

**Recommend Option A (`/usr/local/bin` copy).** It's not just "safer in theory" — it's the **exact
pattern `scripts/install.sh` already ships and that `cmd/umbra/daemon.go`'s own `daemon status`
output already assumes** (`"note: after make build rebuilds umbrad, re-run umbra daemon install to
pick up the new signed binary (P23)"` — i.e. the codebase already treats "the LaunchAgent's bin path
is a stable, separately-managed install location, re-pointed explicitly on rebuild" as the norm, not
"tracks wherever the app happens to live"). Building the in-app onboarding flow around Option B would
introduce a *second*, divergent install code path with a strictly worse update story, for the sole
benefit of skipping one `cp` + one possible sudo prompt — not a good trade for a VM daemon whose
LaunchAgent needs to survive `Umbra.app` being replaced by drag-and-drop, which is the *entire point*
of shipping a `.dmg`.

**Onboarding UI shape** (First-run wizard, single view or a 2–3 step `TabView`/state machine):
1. **Welcome / status check** — "Umbra isn't installed yet" (or "daemon not running") with a short
   explanation.
2. **Install button** — runs, in order: (a) copy `umbrad`+`umbra` from the bundle to `/usr/local/bin`
   (prompt for admin only if needed, same writable-dir check as `install.sh`), (b) re-sign `umbrad`
   with `build/vz.entitlements` embedded as a bundled resource (`Contents/Resources/vz.entitlements`
   — add it to the `app` target's resource copy step) — needed because the copy destination is
   outside the bundle so you can't rely on the bundle's own signature covering it, (c) run
   `umbra daemon install --bin /usr/local/bin/umbrad` (bundled `umbra`, invoked via the same
   `Process`-based `runUmbra` helper `CLIClient.swift` already has — no new subprocess machinery
   needed, this is the same shape as every other CLI call the app makes).
3. **First VM-boot permission note** — the release notes already warn about this
   (`Makefile`'s `release` target `INSTALL.txt` text): *"umbrad ships ad-hoc codesigned with the
   com.apple.security.virtualization entitlement; macOS shows an interactive, one-time permission
   prompt the first time it boots a VM"* — surface that exact copy in the onboarding screen so it
   isn't a surprise later, and also surface `cmd/umbra/daemon.go`'s existing TCC note ("run `make
   run-daemon` once interactively first so the macOS VirtioFS home-share permission prompt can be
   granted with a UI present (P24)") as a "start your first machine now" nudge once install succeeds.
4. **Success → transition to the dashboard**, `StatusModel.refresh()` re-run, normal app from here on.

This mirrors, in-app, exactly what `scripts/install.sh` does out-of-app for the tarball/curl path —
the two flows should stay behaviorally identical (same target paths, same re-sign step, same `daemon
install --bin` call) so "install via .dmg" and "install via curl | bash" produce an indistinguishable
end state.

---

## 4. Building the `.dmg`

**Minimal reliable `hdiutil` recipe** — stage a folder with the `.app` + an `/Applications` symlink,
compress to `UDZO`:

```bash
STAGE=$(mktemp -d)
cp -R "$APP" "$STAGE/Umbra.app"
ln -s /Applications "$STAGE/Applications"
hdiutil create -volname "Umbra" -srcfolder "$STAGE" -ov -format UDZO "$BIN/Umbra-$(VERSION).dmg"
rm -rf "$STAGE"
```

This is the standard recipe — stage a folder, symlink `/Applications` into it so Finder shows the
familiar "drag app → Applications shortcut" affordance, then convert to compressed read-only
([How to create a "DMG Installer" for Mac OS X — gist.github.com/jadeatucker](https://gist.github.com/jadeatucker/5382343);
[Packaging a Mac OS X Application Using a DMG — asmaloney.com](https://asmaloney.com/2013/07/howto/packaging-a-mac-os-x-application-using-a-dmg/)).
The two-step form (`hdiutil create ... -format UDRW` then `hdiutil convert ... -format UDZO`) is only
needed if you're customizing window layout/icon positions via AppleScript on a mounted read-write
image first — for the "no background image, just app + Applications symlink" case, `hdiutil create
... -format UDZO` directly (single step, shown above) is sufficient and is what `create-dmg`'s own
plain fallback mode does internally.

**`create-dmg` — worth it for the nicer layout, and cheap to add.** Two different tools share this
name:
- **`create-dmg/create-dmg`** (shell script, `brew install create-dmg`) — wraps the AppleScript/
  `hdiutil` dance for you: `--background`, `--window-pos`, `--window-size`, `--icon-size`, and
  crucially **`--app-drop-link`** (positions the `/Applications` symlink for you, the exact
  affordance the recipe above builds by hand)
  ([create-dmg/create-dmg — GitHub](https://github.com/create-dmg/create-dmg)).
- **`sindresorhus/create-dmg`** (Node.js CLI, `npx create-dmg`) — different tool, same name; "does
  everything for you automatically," including an attempt at code-signing the `.dmg` itself (skip
  with `--no-code-sign` in CI to avoid failures on unsigned builds) — pulls in a Node dependency the
  repo doesn't otherwise have
  ([sindresorhus/create-dmg — GitHub](https://github.com/sindresorhus/create-dmg)).

**Recommendation: use `create-dmg/create-dmg` (the shell-script one) via Homebrew, not plain
`hdiutil`, and not the Node version.** Reasoning: it's a `brew install create-dmg` one-time dev-machine
dependency (not a repo dependency — nothing to `npm install`, keeping in the spirit of the repo's
Go+Swift-only, no-Node toolchain), it directly produces the "app icon + Applications shortcut,
arranged nicely" layout Ahmad wants without you hand-rolling the AppleScript Finder-window-styling
script the raw `hdiutil` path needs for anything beyond a bare unstyled window, and it degrades
gracefully — if `create-dmg` isn't installed on a given build machine, fall back to the plain
`hdiutil` one-liner above so `make dmg` never hard-fails a CI box that doesn't have it.

**`make dmg` target:**

```makefile
DMG := $(BIN)/Umbra-$(VERSION).dmg

dmg: app
	rm -f $(DMG)
	@if command -v create-dmg >/dev/null 2>&1; then \
		create-dmg \
			--volname "Umbra" \
			--window-size 540 380 \
			--icon-size 128 \
			--icon "Umbra.app" 140 190 \
			--app-drop-link 400 190 \
			"$(DMG)" "$(APP)" ; \
	else \
		echo "create-dmg not found (brew install create-dmg) — falling back to plain hdiutil" ; \
		stage=$$(mktemp -d) ; \
		cp -R "$(APP)" "$$stage/Umbra.app" ; \
		ln -s /Applications "$$stage/Applications" ; \
		hdiutil create -volname "Umbra" -srcfolder "$$stage" -ov -format UDZO "$(DMG)" ; \
		rm -rf "$$stage" ; \
	fi
	@echo "dmg: $(DMG)"
```

**Gatekeeper on a non-notarized `.dmg` — expected, document it, don't chase it.** Ad-hoc signing
(`codesign --sign -`, the posture the whole repo already uses for `umbrad`/`umbra`/`Umbra.app`) is
**not** a Developer ID signature and does **not** satisfy notarization — it proves nothing about
origin to Gatekeeper
([Living with(out) notarization — eclecticlight.co](https://eclecticlight.co/2024/10/01/living-without-notarization/)).
Two things changed recently and both matter for the `INSTALL.txt`/README copy:
1. **Since macOS Sequoia (15), right-click → Open no longer bypasses Gatekeeper** for an ad-hoc/
   unsigned app that carries the quarantine flag (set automatically on anything downloaded via a
   browser, including a `.dmg` fetched from a GitHub Release) — that override was removed; the user
   must instead try to open the app (it will refuse), then go to **System Settings → Privacy &
   Security → scroll down → "Open Anyway"**
   ([macOS 15.1 completely removes ability to launch unsigned applications — MacRumors
   forums](https://forums.macrumors.com/threads/macos-15-1-completely-removes-ability-to-launch-unsigned-applications.2441792/);
   [Running Unsigned Applications on macOS Sequoia — Christian Tietze](https://autodiscover.christiantietze.de/posts/2024/11/running-unsigned-applications-macos-sequoia/)).
2. `xattr -dr com.apple.quarantine /Applications/Umbra.app` still works as a manual/scripted bypass
   (what `curl | bash` + local `cp`-based installs never trigger in the first place, since quarantine
   is set by the *downloading* app — Safari/Chrome/`curl` with certain flags — not by `cp`/`make`) —
   keep this one-liner in the README/INSTALL.txt as the "if System Settings doesn't show Open Anyway"
   fallback, same caveat the search sources flag: it bypasses a real (if low-value, for an ad-hoc
   local tool) security check, document it as expected friction for a non-notarized `.dmg`, not a bug.

Update the `Makefile` `release` target's `INSTALL.txt` generation and the top-level README to mention
**both** the System Settings path (primary, works on current macOS) and the `xattr -dr` fallback —
the current `INSTALL.txt` text doesn't mention Gatekeeper at all, which will read as a broken/corrupt
app to a first-time `.dmg` user who's never hit this before.

---

## 5. Dashboard window content

**Recommendation: `NavigationSplitView`, not a flat `List`.** `NavigationSplitView` (macOS 13+,
introduced same WWDC22 wave as `Window`) is Apple's standard two/three-column pattern — sidebar +
detail — and is exactly the shape "list of machines, click one, see detail + actions" needs
([NavigationSplitView docs](https://developer.apple.com/documentation/swiftui/navigationsplitview);
[Mastering NavigationSplitView in SwiftUI — Swift with Majid](https://swiftwithmajid.com/2022/10/18/mastering-navigationsplitview-in-swiftui/)).
A flat `List` (what the M5 menu bar popover already uses, appropriately, for its cramped ~320pt-wide
window) doesn't scale to a real window: there's no natural place to put per-machine detail (state
history, IP, ssh port, cpu/mem/disk, start/stop/shell/delete) without either a disclosure-group mess
or a second sheet per machine, both worse than a persistent detail pane.

**Layout:**
- **Sidebar**: machine list (name, small state dot, matches `MenuBarView`'s existing status-dot
  convention), a "+ New Machine" toolbar button.
- **Detail** (selected machine): name/state header, IP + ssh_port, cpus/memory/disk as a small stat
  row, start/stop toggle button, "Open Shell" button (same `openShellScript`/`NSAppleScript` handoff
  `CLIClient.swift` already has — no new code, just called from the dashboard instead of only the
  popover), delete button (confirm via `.confirmationDialog`, then `umbra rm <name>`), zombie badge
  if `Machine.zombie == true` (same convention M5's Codable model already carries — don't drop it in
  the new UI either).
- **New Machine**: a `.sheet` with a form — name (`TextField`), cpus/memory/disk (`Stepper`s or
  `TextField`s pre-filled from the Settings-configured defaults, §6) — submit runs `umbra create
  <name> --cpus … --memory-gib … --disk-gib …` then `umbra start <name>`, matching the M5 doc's
  Actions pattern (mutate, then immediately re-`refresh()`, don't wait for the poll tick).
- **Docker section**: either its own sidebar entry/detail pane or a persistent strip at the bottom of
  the sidebar — install/start/stop/status, same `CLI.dockerStart()/dockerStop()` calls
  `StatusModel.toggleDocker()` already wraps.
- **Rosetta status line**: a small read-only row (`umbra rosetta status` — thin client, same pattern)
  somewhere visible but low-emphasis (footer of the sidebar or a Settings row — either works; a
  footer row keeps it visible without a dedicated section for a single status line).
- **Header**: daemon status (up/down dot, same computation `StatusModel.status?.daemon` already
  drives) + a Settings button (`SettingsLink()`, or a toolbar gear icon that calls `openSettings()` —
  both are standard once the app is regular-policy, §2).

**Keep it thin.** Every action above is a call into the existing `CLI` struct in `CLIClient.swift` —
the dashboard adds views and wires them to methods `StatusModel` already has (`toggleMachine`,
`toggleDocker`, `openShell`) or trivially analogous new ones (`createMachine`, `deleteMachine`), never
new business logic or a second source of truth. Same "no business logic in the app" commitment M5
already established (`docs/superpowers/specs/2026-07-11-umbra-design.md:74-75`).

---

## 6. Settings pane content

**`Settings` scene + `TabView`**, the standard macOS Settings-window shape (System Settings itself,
and most Mac apps, use a `TabView` with `.tabViewStyle` sidebar-icons row across the top):

```swift
Settings {
    TabView {
        DefaultsSettingsView()
            .tabItem { Label("Defaults", systemImage: "slider.horizontal.3") }
        DaemonSettingsView()
            .tabItem { Label("Daemon", systemImage: "bolt.horizontal.circle") }
        AdvancedSettingsView()
            .tabItem { Label("Advanced", systemImage: "gearshape.2") }
        AboutSettingsView()
            .tabItem { Label("About", systemImage: "info.circle") }
    }
    .environmentObject(model)
}
```

- **Defaults tab**: cpus/memory-gib/disk-gib defaults for the "New Machine" sheet (§5) —
  `@AppStorage` (UserDefaults-backed, the standard SwiftUI persistence for exactly this kind of small
  scalar preference) is enough, no need for a config file the daemon has to read.
- **Daemon tab**: LaunchAgent status (`launchagent.Installed()`/`daemon status` output, surfaced via
  the same CLI shell-out), Install/Uninstall/Restart buttons (`umbra daemon install --bin
  /usr/local/bin/umbrad` / `umbra daemon uninstall` / uninstall-then-install for "restart" — no
  restart subcommand exists in `cmd/umbra/daemon.go` today, compose it client-side from the two that
  do). This is where the onboarding install flow (§3) and ongoing daemon management converge — same
  underlying calls, reachable either from first-run or later from Settings if the user ever needs to
  reinstall (e.g. after `make build` rebuilds `umbrad`, per the existing P23 note in `daemon.go`).
- **Advanced tab**: CLI path override (`UMBRA_CLI_PATH` env var already supported by
  `umbraCLIPath()`'s resolution order in `CLIClient.swift` — expose it as a settable `@AppStorage`
  string the app checks *before* its built-in resolution order, for power users pointing at a
  non-standard build), an "install `/etc/resolver/umbra.local`" button if that's part of the daemon's
  DNS story (needs `sudo` — same writable-check-then-`sudo`-if-needed pattern as §3's install flow;
  shell out to a small privileged-helper script via `AuthorizationExecuteWithPrivileges`-successor
  patterns or, simpler for a local dev tool, just `Process` + `sudo -n` probe then prompt via
  `osascript "do shell script … with administrator privileges"` — the same AppleScript-driven
  privilege-elevation idiom already used for `do script` handoffs in `CLIClient.swift`, just with
  `with administrator privileges` appended).
- **About tab**: version (`CFBundleShortVersionString`, already `0.5.0` in `Info.plist` — read via
  `Bundle.main.infoDictionary`), GitHub link (`https://github.com/ForceAI-KW/umbra`, matches
  `scripts/install.sh`'s `$REPO` constant), license (`Apache-2.0`, matches
  `Info.plist`'s`NSHumanReadableCopyright`).

---

## 7. Pitfalls

- **`codesign --deep` silently strips `umbrad`'s virtualization entitlement** — the single biggest
  risk in this whole change (§3). Sign `umbrad` once in `make build` (already correct), never
  re-sign it in `make app`, sign only the outer bundle without `--deep`, and add a build-time
  `codesign -d --entitlements :- … | grep virtualization` check so a regression fails loudly instead
  of shipping a `.dmg` whose bundled daemon can't boot VMs.
- **Every bundled binary needs a valid signature, and a bare `cp` into the bundle can invalidate
  one** — mirror `scripts/install.sh`'s "always explicitly re-sign after any copy that could disturb
  the signature" discipline if the bundling step ever changes from a simple untouched-file copy.
- **Gatekeeper/quarantine on a non-notarized `.dmg`** — expected friction, not a bug; document the
  current (Sequoia+) System Settings → Privacy & Security → "Open Anyway" path plus the `xattr -dr
  com.apple.quarantine` fallback in the README/`INSTALL.txt`, since the old "just right-click → Open"
  folklore no longer works (§4).
- **Dashboard `StatusModel` polling even when the window is hidden** — the M5 doc's §4 "poll only
  while the window is open" rule still applies, now for *two* surfaces (dashboard window + menu bar
  popover) instead of one. Gate the rich 2-3s poll on `scenePhase`/window-visibility for the
  dashboard (macOS 13+: `@Environment(\.scenePhase)` on the `Window`'s root view, or the same
  `.onAppear`/`.onDisappear` pragmatic signal M5 already uses for the popover) independently from the
  menu bar popover's own open/closed gating — don't let one surface's "closed" state stop the other's
  poll, and don't double-poll (two independent 2s timers hitting `umbra status --json`
  simultaneously) if both are open at once; route both through the same shared `StatusModel.refresh()`
  call so concurrent opens coalesce onto one in-flight request rather than firing two.
- **Activation on launch** — with `LSUIElement=NO` and a `Window` scene, the dashboard comes to the
  front automatically on a fresh launch (regular-policy apps activate + focus their initial window by
  default) — no manual `NSApp.activate` call needed for the base "double-click Umbra.app" case. Only
  add explicit activation code if you later find the window opening *behind* other apps in some
  launch path (e.g. launched via a background LaunchAgent trigger rather than Finder/Dock) — not
  expected for M7's scope (user-initiated launch only).
- **LaunchAgent pointing at an in-bundle path that breaks on app update/move** — covered in depth in
  §3; this is the reason Option A (`/usr/local/bin` copy, LaunchAgent points there) is the
  recommendation over Option B (point at the path inside `Umbra.app`). If Option B is ever chosen
  instead for a future iteration, the onboarding/Settings "Daemon" tab (§6) must re-run `daemon
  install --bin <bundle-path>` **every time the app launches and the path differs from what's
  currently installed** — a real design addition `internal/launchagent` doesn't have today (`Install`
  is only ever called explicitly by `daemon install`, never automatically compared/re-synced on
  launch) — one more reason Option A is simpler to ship correctly.

---

## Sources

- [Scenes types in a SwiftUI Mac app — nilcoalescing.com](https://nilcoalescing.com/blog/ScenesTypesInASwiftUIMacApp/)
- [Understanding scenes for your macOS app — createwithswift.com](https://www.createwithswift.com/understanding-scenes-for-your-macos-app/)
- [Bring multiple windows to your SwiftUI app — WWDC22 session 10061](https://developer.apple.com/videos/play/wwdc2022/10061/)
- [WindowGroup — Apple Developer Documentation](https://developer.apple.com/documentation/swiftui/windowgroup)
- [Window management in SwiftUI — Swift with Majid](https://swiftwithmajid.com/2022/11/02/window-management-in-swiftui/)
- [Showing Settings from macOS Menu Bar Items: A 5-Hour Journey — Peter Steinberger](https://steipete.me/posts/2025/showing-settings-from-macos-menu-bar-items)
- [LSUIElement — Apple Developer Documentation](https://developer.apple.com/documentation/bundleresources/information-property-list/lsuielement)
- [Show/Hide dock icon on macOS App — Jie Zhang (Medium)](https://medium.com/@jackymelb/show-hide-dock-icon-on-macos-app-3a59f7df282d)
- [Fine-Tuning macOS App Activation Behavior — artlasovsky.com](https://artlasovsky.com/fine-tuning-macos-app-activation-behavior)
- [defaultLaunchBehavior(_:) — Apple Developer Documentation](https://developer.apple.com/documentation/swiftui/scene/defaultlaunchbehavior(_:))
- [NSApplicationDelegateAdaptor — Apple Developer Documentation](https://developer.apple.com/documentation/swiftui/nsapplicationdelegateadaptor)
- [How to keep your macOS app's menu bar item running after quitting the app — polpiella.dev](https://www.polpiella.dev/keep-menu-bar-running-after-quitting-app)
- [Apple Codesigning In Depth: Part I — Kayla McArthur](https://kayla.is/posts/codesigning-part-i/)
- [codesign(1) man page](https://keith.github.io/xcode-man-pages/codesign.1.html)
- [macOS distribution — code signing, notarization, quarantine, distribution vehicles — rsms (gist)](https://gist.github.com/rsms/929c9c2fec231f0cf843a1a746a416f5)
- [codesign every binary? — Apple Developer Forums](https://developer.apple.com/forums/thread/129678)
- [Code Signing Tasks — Apple Developer Documentation Archive](https://developer.apple.com/library/archive/documentation/Security/Conceptual/CodeSigningGuide/Procedures/Procedures.html)
- [How to create a "DMG Installer" for Mac OS X — jadeatucker (gist)](https://gist.github.com/jadeatucker/5382343)
- [Packaging a Mac OS X Application Using a DMG — asmaloney.com](https://asmaloney.com/2013/07/howto/packaging-a-mac-os-x-application-using-a-dmg/)
- [create-dmg/create-dmg — GitHub](https://github.com/create-dmg/create-dmg)
- [sindresorhus/create-dmg — GitHub](https://github.com/sindresorhus/create-dmg)
- [Living with(out) notarization — The Eclectic Light Company](https://eclecticlight.co/2024/10/01/living-without-notarization/)
- [macOS 15.1 completely removes ability to launch unsigned applications — MacRumors Forums](https://forums.macrumors.com/threads/macos-15-1-completely-removes-ability-to-launch-unsigned-applications.2441792/)
- [Running Unsigned Applications on macOS Sequoia — Christian Tietze](https://autodiscover.christiantietze.de/posts/2024/11/running-unsigned-applications-macos-sequoia/)
- [NavigationSplitView — Apple Developer Documentation](https://developer.apple.com/documentation/swiftui/navigationsplitview)
- [Mastering NavigationSplitView in SwiftUI — Swift with Majid](https://swiftwithmajid.com/2022/10/18/mastering-navigationsplitview-in-swiftui/)
- [OrbStack menu bar app docs](https://docs.orbstack.dev/menu-bar) (cited in `docs/research/menubar-app.md`, same real-world analog for "regular app + menu bar bonus")

## Repo grounding (not external, cited throughout above)

- `apps/menubar/Sources/UmbraMenuBar/UmbraApp.swift`, `StatusModel.swift`, `CLIClient.swift`,
  `MenuBarView.swift`, `Models.swift` — existing M5 app, thin-client pattern, `Process`-based CLI
  shell-out, `umbraCLIPath()` resolution order, `NSAppleScript` shell handoff
- `apps/menubar/Resources/Info.plist` — current `LSUIElement=YES`, bundle identity
- `Makefile` — `build`/`app`/`release`/`install` targets, existing `codesign --deep` call to fix
- `scripts/install.sh` — the existing out-of-app install flow M7's in-app onboarding should mirror
  (copy-to-`/usr/local/bin`, re-sign-with-entitlements, `daemon install --bin`)
- `internal/launchagent/launchagent.go` — `RenderPlist`/`Install` — confirms `ProgramArguments[0]` is
  a static baked-in path, no runtime re-resolution, the core reason Option A beats Option B in §3
- `cmd/umbra/daemon.go` — `daemon install --bin`, `daemon status`'s P23 rebuild-then-reinstall note
- `docs/runbooks/entitlements-and-codesigning.md` — ad-hoc signing posture, `com.apple.security.virtualization`
- `build/vz.entitlements` — the entitlements file at risk from `--deep`
- `docs/research/menubar-app.md` — M5's design doc this one extends; §1/§4/§5/§7 cross-referenced above
