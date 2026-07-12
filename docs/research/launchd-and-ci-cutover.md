# launchd autostart + GitHub Actions runner cutover — cheat-sheet (Umbra M4)

**Purpose**: design + procedure reference the M4 plan (`docs/superpowers/plans/...`) gets written
from. Covers (a) `umbrad` as a login-time LaunchAgent with a single-instance guard, (b) a fresh
Umbra Ubuntu machine running GitHub Actions self-hosted runners for `ForceAI-KW`, registered in
**parallel** with the existing OrbStack `fwb-ci` runners, verified green, with the actual
deregister/uninstall step left as a human-gated runbook.

Grounded in the actual M1–M3 code: `paths.Root()` = `~/.umbra`, `paths.APISocket()` =
`~/.umbra/run/api.sock`, `paths.Logs()` = `~/.umbra/log` (`internal/paths/paths.go`); autostart is
already wired in `cmd/umbrad/main.go` (`if m.Autostart { ... mgr.Start ... }`, comment: *"launchd
wiring lands in M4"*); `registry.Machine.Role` already exists and drives `cloudinit.BuildSeed`'s
extra `runcmd` block (`internal/cloudinit/seed.go`) for the reserved `docker` role — the CI-runner
machine reuses this exact mechanism with a new role string, not a new provisioning path. Design
spec commitment (`docs/superpowers/specs/2026-07-11-umbra-design.md:105`): fresh **`fwb-ci2`**
machine, parallel registration, verify green CI, *then* deregister old + retire OrbStack.

---

## 1. launchd LaunchAgent for `umbrad`

### plist structure — `~/Library/LaunchAgents/com.forceai.umbrad.plist`

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.forceai.umbrad</string>

    <key>ProgramArguments</key>
    <array>
        <string>/Users/ahmadsharaf/Desktop/projects/umbra/bin/umbrad</string>
    </array>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>

    <key>ThrottleInterval</key>
    <integer>5</integer>

    <key>StandardOutPath</key>
    <string>/Users/ahmadsharaf/.umbra/log/umbrad.out.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/ahmadsharaf/.umbra/log/umbrad.err.log</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>

    <key>ProcessType</key>
    <string>Interactive</string>

    <key>WorkingDirectory</key>
    <string>/Users/ahmadsharaf/Desktop/projects/umbra</string>
</dict>
</plist>
```

Key-by-key reasoning:

- **`Label`** — required, unique, reverse-DNS. `com.forceai.umbrad` matches the `com.forceai.*`
  convention already used for `com.forceai.neon-backup` / `com.forceai.mac-maintenance` (per
  memory).
- **`ProgramArguments`** — absolute path to the *codesigned* `bin/umbrad`, no shell wrapper (a
  wrapper would break the entitlement chain — see below). Use `umbra daemon install` (§3) to embed
  the real repo path at install time, not a hardcoded path baked into source.
- **`RunAtLoad: true`** — launchd's own docs call speculative `RunAtLoad` launches an
  "adverse effect" for boot/login-storm scenarios in general, but for a single always-wanted user
  agent this is the standard/expected pattern (every `brew services` plist sets it) — the guidance
  is about *not overusing* it across many jobs, not against using it here. [launchd.info](https://www.launchd.info/)
- **`KeepAlive: {SuccessfulExit: false}`**, not bare `KeepAlive: true` — this is the exact "restart
  on crash, not on clean stop" semantics the task asked for: *"the job will be restarted until it
  succeeds... if the app crashes (exit code ≠ 0) it will be restarted"* but a clean `exit 0` (e.g.
  from `umbra daemon stop`'s SIGTERM handling in `main.go`'s `signal.Notify(... SIGTERM)` path,
  which returns `nil` → `os.Exit(0)`) does **not** trigger a relaunch. [launchd.info](https://www.launchd.info/) confirms this SuccessfulExit=false→"restart until succeeds" behavior; cross-checked against the [`launchd.plist(5)` man page](https://keith.github.io/xcode-man-pages/launchd.plist.5.html) and the [tjluoma/launchd-keepalive](https://github.com/tjluoma/launchd-keepalive) worked examples repo.
  - Caveat: `SIGKILL`/crash and `SIGTERM`-then-clean-exit both look like "exited" to launchd; only
    the **exit code** distinguishes them. `main.go`'s signal handler already returns `nil` (exit 0)
    on graceful SIGTERM — this is *already* correct for the LaunchAgent contract, no code change
    needed there. A panic or `os.Exit(1)` (the `logger.Error("umbrad exiting"...); os.Exit(1)` path
    in `main()`) *will* restart, which is the desired self-heal behavior.
- **`ThrottleInterval: 5`** — floor between relaunches so a crash-loop (e.g. corrupted registry
  JSON) doesn't spin launchd; default is 10s if unset, 5s is a reasonable tighten given `umbrad`
  boots in ~1s (no VM boot on daemon start, only lazy `mgr.Start` for autostart machines).
- **`StandardOutPath`/`StandardErrorPath`** → `~/.umbra/log/` (existing `paths.Logs()` dir, already
  created 0700 by `paths.EnsureTree()`) — keeps LaunchAgent logs colocated with the daemon's own
  `slog` output today (`main.go` currently logs to `os.Stderr`, which under launchd *is*
  `StandardErrorPath` — no dual-logging system needed, just redirect the same stream to disk).
- **`EnvironmentVariables.PATH`** — **load-bearing, not cosmetic**. launchd agents get a minimal
  PATH (`/usr/bin:/bin:/usr/sbin:/sbin`), missing Homebrew's `/opt/homebrew/bin`. Concretely: this
  breaks `internal/dockerctx/dockerctx.go:27` (`lookPath("docker")` in `dockerctx.Ensure`, called
  transitively by `umbra docker install/start` → `DockerInstall`/`DockerStart` API handlers) since
  `brew install docker` (per README) puts the CLI at `/opt/homebrew/bin/docker` on Apple Silicon.
  Every other host-side `exec.Command` call in the codebase already uses an absolute path
  (`/usr/bin/sw_vers` in `internal/api/server.go:130` and `cmd/umbrad/docker.go:71`) so this is the
  **only** PATH-dependent host-side call site. [Confirmed pattern: Apple Developer Forums thread + Medium "Where is my PATH, launchd?"](https://developer.apple.com/forums/thread/681550)
- **`ProcessType: Interactive`** — tells launchd this is a foreground-adjacent, latency-sensitive
  job (vs. `Background`/`Standard`), so it isn't throttled under App Nap / low-priority QoS the way
  a batch job would be. Matches how GUI-adjacent long-running agents (not pure batch daemons) are
  classified in Apple's launchd docs.
- **`WorkingDirectory`** — not required by `umbrad` (all its paths are absolute via `paths.Root()`),
  but harmless to set for clarity/debuggability; omit if it complicates `umbra daemon install`'s
  templating.

### Prior art: how Colima/Lima/Docker Desktop do this

- **Colima**: no native login-autostart; the accepted pattern is `brew services start colima`,
  which Homebrew implements by copying a formula-provided plist to
  `~/Library/LaunchAgents/homebrew.mxcl.colima.plist` and running `launchctl load` on it. A typical
  Homebrew service plist sets `Label`, `ProgramArguments`, `RunAtLoad: true`, `KeepAlive: true`
  (bare, no `SuccessfulExit` split), plus stdout/stderr paths. [Colima autostart via `brew services`](https://github.com/abiosoft/colima/issues/96), [how `brew services` writes/loads the plist](https://dorokhovich.com/blog/homebrew-services). Umbra deliberately does **not** shell out through Homebrew — `umbra daemon install` (§3) writes and bootstraps the plist directly, which also lets it use the more precise `SuccessfulExit: false` KeepAlive form Homebrew's generic template doesn't bother with.
- **Docker Desktop**: not launchd-based at all — it's a full macOS `.app` with its own
  `LaunchServices`/login-item registration (`SMAppService` login item), a different model entirely
  since it has a menu-bar GUI process as the "real" entry point. Not directly transferable to a
  headless daemon; Umbra's M5 menu-bar app is the closer analog for *that* pattern, and would use
  `SMAppService.mainApp.register()` for its own auto-launch — orthogonal to `umbrad`'s LaunchAgent.
- **Lima**: no built-in autostart either; community docs point at the same `brew services`/manual
  LaunchAgent pattern as Colima.

Net: there's no "special" trick in prior art beyond what's above — Umbra's plan (agent, not daemon,
scope; `KeepAlive.SuccessfulExit=false`; explicit PATH) is *more* precise than the Homebrew
services baseline, not less.

### Codesign/entitlement survival under launchd

**Yes — launchd runs the signed binary directly, unmodified**, so `com.apple.security.virtualization`
(applied by `make build`'s `codesign --entitlements build/vz.entitlements --sign - bin/umbrad`,
per `docs/runbooks/entitlements-and-codesigning.md`) is preserved exactly as if invoked from a
Terminal. launchd's `ProgramArguments[0]` is `execve()`'d as-is — no re-linking, no wrapper shell
by default (a wrapper *would* need Team-ID matching or would strip the entitlement's meaning,
since Virtualization.framework checks the entitlement of the running process image, not a parent).
**Do not** wrap `ProgramArguments` in `/bin/sh -c "..."` for this reason — call the codesigned
binary directly, exactly like `make run-daemon` already does (`$(BIN)/umbrad`, no shell layer).

### TCC prompts under launchd

None expected for the `com.apple.security.virtualization` entitlement itself — that's an
entitlement check, not a TCC (user-consent) dialog, and VM creation via `Virtualization.framework`
does not itself gate on TCC. The one *possible* TCC surface is the VirtioFS home-directory share
(`/mnt/mac` in every guest, per README's "your macOS home mounted read-write") — VirtioFS host
shares of `$HOME` can intersect TCC-protected subdirectories (Desktop/Documents/Downloads) on some
macOS versions, and unlike a Terminal-launched process, **a LaunchAgent has no windowed session to
show a consent dialog to** — a first-run TCC prompt that would normally appear can silently fail
instead, surfacing as `EPERM`/"operation not permitted" from inside the guest instead of a visible
macOS dialog. [This exact virtiofs+home-share TCC failure mode is documented](https://github.com/anthropics/claude-code/issues/29119). **Action for M4**: before relying on the LaunchAgent path, do one interactive `make run-daemon` + `umbra shell <machine>` + `ls /mnt/mac/Desktop` (or whichever subdir) run first, from a real login session, so any one-time TCC grant happens with a UI present; after that grant is recorded (Full Disk Access / Files-and-Folders for the specific binary+TeamID), subsequent LaunchAgent-launched instances of the *same signed binary* should not re-prompt. Document this as a first-run runbook step, not something automatable.

---

## 2. Single-instance guard

### Why launchd's KeepAlive alone isn't sufficient

launchd guarantees at most one *instance-it-started*, but nothing stops a user from also typing
`make run-daemon` (`$(BIN)/umbrad` run directly) while the LaunchAgent copy is already up. Two
`umbrad` processes would both try to `net.Listen("unix", paths.APISocket())` (`main.go`) — the
second bind fails loudly (good), **but** if the first is later killed and cleans up
(`os.Remove(sock)`-on-next-boot logic only runs at *start*, not on the currently-running process),
a narrow window exists, and more importantly both processes independently manage the *same*
`~/.umbra/machines/<name>/disk.img` files and VZ VM instances if given the chance — silent disk
corruption if a race ever lets two `vm.Manager`s both call `Start` on the same machine directory.
The task's own framing (two processes fighting over the socket + VM disks) is correct — treat
"can't even get to the socket bind" as the target failure mode, with a *clear* message, not a
race-prone one.

### Recommended: `flock` on `~/.umbra/run/umbrad.lock`, acquired before anything else

Go's `syscall.Flock` (non-blocking `LOCK_EX|LOCK_NB`) is the standard single-instance-daemon
pattern: "Single-Instance Daemons ensure only one copy of your background worker is running" via
an exclusive non-blocking lock, auto-released on process exit/close. [Reference pattern](https://rednafi.com/misc/run-single-instance/); library alternative (cross-platform, not needed here since Umbra is macOS-only) is [gofrs/flock](https://github.com/gofrs/flock).

```go
// internal/paths/paths.go — add alongside APISocket()
func LockFile() string { return filepath.Join(Run(), "umbrad.lock") }
```

```go
// cmd/umbrad/main.go (or a new internal/singleton package) — acquire FIRST,
// before paths.EnsureTree()'s siblings are even touched for the socket.
package singleton

import (
	"fmt"
	"os"
	"syscall"
)

// Lock is held for the life of the process; call Close on clean shutdown
// (though process exit releases it anyway — flock is per-fd, not persistent).
type Lock struct{ f *os.File }

// Acquire takes an exclusive, non-blocking flock on path. Returns a
// human-readable error (never a bare syscall errno) when another umbrad
// instance already holds it, so `make run-daemon` against an already-running
// LaunchAgent copy fails fast and obviously instead of racing the socket bind.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf(
			"another umbrad is already running (lock held on %s) — "+
				"check `launchctl print gui/$(id -u)/com.forceai.umbrad` or `pgrep umbrad`", path)
	}
	return &Lock{f: f}, nil
}

func (l *Lock) Close() error { return l.f.Close() } // releases the flock
```

Wire it as the very first line of `run(logger)` in `cmd/umbrad/main.go`, before
`paths.EnsureTree()`:

```go
lock, err := singleton.Acquire(paths.LockFile())
if err != nil {
    return err // becomes the os.Exit(1) message in main() — clear, not a race
}
defer lock.Close()
```

This needs `paths.Run()` (i.e. `~/.umbra/run/`) to exist first — either call `os.MkdirAll` on just
that one directory ahead of the full `EnsureTree()`, or reorder `EnsureTree()` to run before the
lock (safe: `EnsureTree()` only creates directories, doesn't touch the socket/VMs, so it's fine for
two racing processes to both call it).

### flock vs. launchd socket activation — recommendation

launchd *can* do socket activation (the `Sockets` key: launchd pre-binds the listening socket and
hands the fd to whichever process is spawned on first connection — "launchd can turn any program
reading from standard input into a server"), which *would* give a stronger single-owner guarantee
for the API socket specifically. **Not recommended for M4**: (1) it only protects the Unix socket,
not the VM-disk-corruption risk from a second `make run-daemon` invocation, so the flock guard is
still needed regardless; (2) it requires `umbrad` to accept the fd launchd hands it (`LaunchSocket`
via `os.NewFile`) instead of calling `net.Listen` itself — a real code change to the listener setup
in `main.go` for marginal benefit once flock already exists; (3) it only activates through launchd,
so a bare `./bin/umbrad` run for local dev/tests would need a fallback path anyway, duplicating the
flock logic. **flock is simpler, covers the actual failure mode (VM disk races, not just the
socket), and works identically whether launched by launchd or by hand** — take it as the sole
guard.

---

## 3. `umbra daemon install|uninstall|status`

New `cmd/umbra/daemon.go`, added to `rootCmd.AddCommand(...)` in `root.go` alongside the existing
group.

```go
var daemonCmd = &cobra.Command{Use: "daemon", Short: "Manage the umbrad LaunchAgent"}

var daemonInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Write + load the umbrad LaunchAgent (auto-start at login)",
	RunE: func(cmd *cobra.Command, args []string) error {
		// 1. Locate bin/umbrad next to the running `umbra` binary (or via
		//    an explicit --bin flag / $UMBRA_BIN override for dev).
		// 2. Render the plist (§1) to ~/Library/LaunchAgents/com.forceai.umbrad.plist,
		//    0644, overwriting any prior version — idempotent by construction.
		// 3. `launchctl bootstrap gui/$(id -u) <plist>` (see below).
		// 4. `launchctl enable gui/$(id -u)/com.forceai.umbrad`
		// 5. `launchctl kickstart -k gui/$(id -u)/com.forceai.umbrad` (start now, don't wait for next login)
	},
}
```

### `launchctl bootstrap` vs `launchctl load` — use `bootstrap` on macOS 26

`launchctl load`/`unload` are deprecated since macOS 10.10 (Yosemite) and print a deprecation
warning today; the modern (10.11+, still current on macOS 26) `launchctl2` syntax is:

| Legacy (deprecated) | Modern |
|---|---|
| `launchctl load <plist>` | `launchctl bootstrap gui/<uid> <plist>` |
| `launchctl unload <plist>` | `launchctl bootout gui/<uid>/<label>` |
| `launchctl start <label>` | `launchctl kickstart gui/<uid>/<label>` |
| `launchctl stop <label>` | `launchctl kill SIGTERM gui/<uid>/<label>` |

[Source: launchctl2 syntax reference](https://babodee.wordpress.com/2016/04/09/launchctl-2-0-syntax/); [deprecation + bootstrap/bootout migration example](https://github.com/blamechris/chroxy/issues/743); ["these newer commands work correctly whether or not the service is already registered"](https://joelsenders.wordpress.com/2019/03/14/dear-launchctl-were-all-using-you-wrong/) — that last point matters for idempotency (below).

Full `install` sequence:
```sh
UID=$(id -u)
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/com.forceai.umbrad.plist
launchctl enable gui/$UID/com.forceai.umbrad
launchctl kickstart -k gui/$UID/com.forceai.umbrad   # -k = kill-and-restart if already running
```

Full `uninstall` sequence:
```sh
UID=$(id -u)
launchctl bootout gui/$UID/com.forceai.umbrad   # stops it, unregisters
rm -f ~/Library/LaunchAgents/com.forceai.umbrad.plist
```

`umbra daemon status`: `launchctl print gui/$(id -u)/com.forceai.umbrad` (parse for `state = running`)
or simpler — just hit the API socket the same way `umbra status` already does
(`apiClient.Ping`), since "is umbrad reachable" is the thing that actually matters, and it works
identically whether umbrad is running under launchd, under `make run-daemon`, or not at all.

### Idempotency

- `bootstrap` on an already-bootstrapped label errors (`Bootstrap failed: 5: Input/output error` or
  similar EEXIST-shaped error) — **`install` should `bootout` first (ignoring "not found" errors),
  then `bootstrap` fresh**, mirroring the exact idempotency pattern the codebase already uses for
  `dockerctx.Remove` (`internal/dockerctx/dockerctx.go:54`: *"a 'not found' failure on the rm is
  swallowed... safe to run even when install never completed or ran twice"*, P15 in
  `PITFALLS-EXTERNAL.md`). Same shape here: `uninstall` swallows "not loaded" from `bootout`.
- Writing the plist file is naturally idempotent (overwrite).
- `enable` is idempotent (repeated calls are no-ops).

---

## 4. GitHub Actions self-hosted runner in an Umbra Ubuntu guest

### Registration token

- **Org-level, not repo-level** — matches the existing `fwb-ci` OrbStack setup (org id `287515696`,
  `ForceAI-KW`), so one runner pool serves FWB/WBS/Force_media_CRM/etc. without per-repo
  duplication.
- `POST /orgs/{org}/actions/runners/registration-token` — requires **admin access to the org**
  (Ahmad's `voidengineer-911` is org owner) and the token-bearer needs **`admin:org` OAuth/PAT
  scope**. [REST API reference](https://docs.github.com/en/rest/actions/self-hosted-runners). Per user memory, this is the exact scope `gh auth refresh -h github.com -s admin:org` was already used to add — reuse that auth for the runner-install script rather than minting a new PAT.
  ```sh
  gh api --method POST -H "Accept: application/vnd.github+json" \
    /orgs/ForceAI-KW/actions/runners/registration-token | jq -r .token
  ```
- **Token expires in 1 hour** — confirmed both by the REST API docs and independently by the
  community/Orchestra write-ups. This is the single biggest procedural constraint: the token must
  be fetched *just before* `config.sh` runs inside the guest, not baked into a cloud-init template
  that might sit unused if VM creation/boot is slow. **Design implication**: do NOT put the
  registration token in `cloudinit.BuildSeed`'s static `user-data` template the way the docker
  role's provisioning is baked in (`internal/cloudinit/seed.go`'s `dockerRuncmdLines`) — that
  template is rendered once at `umbra create` time, and disk/ISO build + VM boot could plausibly
  exceed an hour on a slow day, or the machine could be recreated later reusing a stale seed. Fetch
  the token live and push it via `umbra shell <machine> -- ...` (already-existing SSH-over-forward
  mechanism, README §Networking) at *install* time, not *create* time.

### Install steps (run via `umbra shell fwb-ci2 -- bash -s` or a `scripts/install-runner.sh` pushed and executed over the same channel)

```sh
# Inside the guest, as the `umbra` user (passwordless sudo, per cloud-init template):
RUNNER_VERSION=2.328.0   # pin; check https://github.com/actions/runner/releases for current
mkdir -p ~/actions-runner-1 && cd ~/actions-runner-1
curl -o actions-runner.tar.gz -L \
  https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-arm64-${RUNNER_VERSION}.tar.gz
tar xzf actions-runner.tar.gz

./config.sh --url https://github.com/ForceAI-KW \
  --token "$REG_TOKEN" \
  --name fwb-ci2-1 \
  --labels umbra-ci \
  --unattended --replace

sudo ./svc.sh install
sudo ./svc.sh start
```

Notes grounded in the docs:
- **arm64 tarball** — Umbra machines are Apple-Silicon-hosted Ubuntu guests (per M1: "aarch64"
  shown in README's `uname -m` example), so it's `actions-runner-linux-**arm64**-*.tar.gz`, not the
  `x64` asset most GH Actions tutorials default to.
- `--unattended --replace --name <n>`: [`--replace` "replaces any existing runner with the same
  name"](https://oneuptime.com/blog/post/2026-01-25-github-actions-self-hosted-runner/view) — makes
  re-running the install script (e.g. after a machine rebuild) idempotent by name.
- `sudo ./svc.sh install [username]` → installs a systemd unit; `start`/`stop`/`status`/`uninstall`
  round out the lifecycle. [GitHub Docs: configuring the runner application as a service](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/configure-the-application).
- **Multiple runner instances on one VM**: not documented by GitHub directly, but the mechanism is
  implicit in how `config.sh`/`svc.sh` work — each is scoped to its own directory (own `.runner`
  config, own systemd unit named after that directory's absolute path). Repeat the whole block
  above in `~/actions-runner-2`, `~/actions-runner-3`, etc., with distinct `--name` values
  (`fwb-ci2-2`, `fwb-ci2-3`) and a fresh registration token per instance (each `config.sh` run
  consumes/needs its own valid token — tokens aren't single-use across instances but are
  time-boxed, so fetch one per instance to avoid the 1-hour window biting during a multi-instance
  batch install).

### Multiple runner instances — how many

fwb-ci (OrbStack, 26GB) presumably runs more than one runner process already for parallelism
(concurrent PR CI across FWB/WBS/CRM). Size `fwb-ci2`'s CPU/mem/disk to match or exceed that
baseline (check `orb info fwb-ci` or the OrbStack UI for its allocated CPUs/RAM before sizing the
Umbra machine) and run the same runner-instance count. This isn't independently researchable
(depends on live OrbStack config) — **flag as a plan input to gather from Ahmad or `orb list -v`
before finalizing machine specs**, not a fixed number to hardcode into the design.

### Docker for the runner: Umbra's docker VM, or docker inside the runner VM?

**Recommendation: run docker *inside* the runner VM itself (its own dockerd), not the shared Umbra
`docker` machine.**

Reasoning:
1. **Isolation matches the existing fwb-ci topology.** OrbStack's `fwb-ci` is a single VM that
   presumably already has docker installed locally for container-based CI steps — it isn't proxying
   through a second shared docker host. Keeping `fwb-ci2` self-contained (own dockerd) reproduces
   that shape exactly, so behavior parity during the parallel-verify phase (§5) isn't confounded by
   a topology change on top of a runtime change.
2. **The shared Umbra `docker` VM is explicitly untrusted-guest-hostile by design.** M3's own
   provisioning (`internal/cloudinit/seed.go`'s `dockerRuncmdLines` comment) says the docker VM's
   TCP API is firewalled to the gateway specifically because *"every VM shares one L2 segment... an
   unauthenticated docker API must not be reachable by other guests (e.g. a CI runner)"* — that
   comment is *already* naming CI runners as the threat model the firewall exists for. A GitHub
   Actions runner executes arbitrary untrusted PR-branch code; pointing it at the shared docker
   daemon (used for Ahmad's own interactive `docker compose up` work) would mean any compromised CI
   job has root-equivalent access to the daemon backing local dev — unacceptable blast radius for
   a shared multi-purpose docker host.
3. **Reuse the exact `get.docker.com` runcmd recipe already proven in M3** — same install command
   (`dockerRuncmdLines()`'s `curl -fsSL https://get.docker.com | sh` line), just without the
   TCP-2375-exposure override (the runner VM's dockerd never needs to be reachable from outside
   itself — `docker` CLI and the Actions runner both run in-guest against the local Unix socket).
   This is a subset of the docker role's existing provisioning, not new work.
4. Runner concurrency: each of the N runner *instances* on `fwb-ci2` shares that VM's single
   dockerd — fine, since GitHub Actions jobs on self-hosted runners aren't expected to have
   cross-job docker isolation anyway (same as OrbStack's `fwb-ci` today, one VM, N runner
   processes, one shared dockerd).

**Design/code implication for M4**: add a `registry.Role` value (e.g. `"ci-runner"`) mirroring the
existing `ReservedDockerName = "docker"` const pattern in `internal/registry/registry.go`, and a
`ciRunnerRuncmdLines()` function in `internal/cloudinit/seed.go` alongside `dockerRuncmdLines()`
that installs plain (non-2375-exposed) docker + adds the `umbra` user to the `docker` group. The
GitHub-runner-specific `config.sh`/`svc.sh` steps stay *outside* cloud-init (per the token-freshness
constraint above) and are pushed via `umbra shell` post-boot.

---

## 5. Parallel registration + verification strategy

### Distinct label, not shared `self-hosted`

GitHub's own docs say: *"we recommend providing an array of labels that begins with `self-hosted`
... and then includes additional labels"*, and — critically — *"jobs will be queued on runners that
have **all** the labels you specify"* (AND semantics, not OR). [Source](https://github.com/orgs/community/discussions/50172) + [labels reference](https://docs.github.com/actions/using-jobs/choosing-the-runner-for-a-job). So:

- Register the new runners with labels `["self-hosted", "umbra-ci"]` (or similar) — they still
  carry `self-hosted` (required baseline label GH always attaches) but add a **second label that no
  existing FWB/WBS/CRM workflow currently requests**.
- **Do not** just rely on `runs-on: self-hosted` in real workflows picking up the new runners "by
  accident" during verification — with only `self-hosted` in common, GitHub *will* load-balance a
  real CI job onto whichever of `fwb-ci` or `fwb-ci2` is idle first (confirmed: *"if multiple
  runners have the same label... GitHub will assign the job to any online and idle runner that
  matches"*). That's exactly the "risk a real CI job lands on an unproven runner" scenario the task
  says to avoid.
- **Safe verification path**: add one **temporary test workflow**
  (`.github/workflows/umbra-ci-verify.yml`) to one low-stakes repo (or a scratch branch), triggered
  only by `workflow_dispatch`, with `runs-on: [self-hosted, umbra-ci]` — the AND-label semantics
  guarantee it can *only* land on `fwb-ci2`'s runners, never on `fwb-ci`'s (which don't carry
  `umbra-ci`). Real workflows keep targeting bare `self-hosted` (still exclusively `fwb-ci` during
  this phase) and are completely unaffected.

### Verify healthy + taking jobs

```sh
# 1. Confirm registration + online status
gh api /orgs/ForceAI-KW/actions/runners --jq \
  '.runners[] | select(.labels[].name=="umbra-ci") | {name, status, busy}'

# 2. Trigger the scratch verify workflow
gh workflow run umbra-ci-verify.yml --repo ForceAI-KW/<scratch-repo>

# 3. Watch it land + complete on the umbra-ci runner specifically
gh run list --repo ForceAI-KW/<scratch-repo> --workflow umbra-ci-verify.yml --limit 1
gh run watch --repo ForceAI-KW/<scratch-repo>
```

Do this for **each** of a representative subset of real job shapes fwb-ci actually runs (lint,
typecheck, unit, a docker-build step) — not just a trivial `echo hello` — since the goal is
verifying the *runner environment* (docker present, git present, correct PATH inside the systemd
service — see Pitfalls §8) works end-to-end, not just that registration succeeded.

Only once several consecutive verify-workflow runs are green should any real workflow's `runs-on`
be touched — and per the task's scope, that flip is **out of scope for M4's automated portion**
(see §6).

---

## 6. Cutover kill-switch — HUMAN GATE, Ahmad's hands only

**Do not automate any of this. The M4 implementation stops at "verified green on `umbra-ci`
label, `fwb-ci` untouched."** This section is the runbook for the day Ahmad decides to flip.

### (a) Point real workflows at the new runners (still reversible)

Change `runs-on: self-hosted` → `runs-on: [self-hosted, umbra-ci]` across the target repos, one
repo/PR at a time. Both runner pools stay registered during this step — a bad `fwb-ci2` run just
means a red CI job, not lost history, and is trivially revertable by reverting the `runs-on` change.

### (b) Deregister the OrbStack `fwb-ci` runners

Once satisfied real workflows are healthy on `fwb-ci2` for a real observation window (not one
green run — several days across normal PR volume), remove the OrbStack-side runners:

```sh
# Per runner instance, from inside the fwb-ci OrbStack VM:
cd ~/actions-runner-1 && sudo ./svc.sh stop && sudo ./svc.sh uninstall
REMOVE_TOKEN=$(gh api --method POST -H "Accept: application/vnd.github+json" \
  /orgs/ForceAI-KW/actions/runners/remove-token | jq -r .token)
./config.sh remove --token "$REMOVE_TOKEN"
# repeat for actions-runner-2, -3, ... on the same VM

# Or, faster, force-remove from the API side without touching the VM at all
# (works even if the VM is already gone/unreachable):
gh api /orgs/ForceAI-KW/actions/runners --jq '.runners[] | select(.labels[].name=="fwb-ci") | .id' | \
  xargs -I{} gh api --method DELETE /orgs/ForceAI-KW/actions/runners/{}
```
[`remove-token` endpoint](https://docs.github.com/en/rest/actions/self-hosted-runners) and [DELETE-by-id endpoint](https://docs.github.com/en/rest/actions/self-hosted-runners) both require the same `admin:org` scope as registration.

### (c) Stop/delete the OrbStack `fwb-ci` machine, uninstall OrbStack

```sh
orb stop fwb-ci
orb delete fwb-ci        # irreversible — confirm the 26GB disk has nothing else worth keeping first
# then, only once nothing else in the household depends on OrbStack:
brew uninstall --cask orbstack
```

### Rollback (if `fwb-ci2` misbehaves *after* cutover but *before* OrbStack is deleted)

As long as step (c) hasn't run, rollback is just reversing (a): flip `runs-on` back to bare
`self-hosted` (or explicitly `[self-hosted, fwb-ci]` if `fwb-ci`'s runners were also given a
distinguishing label before cutover — recommended, so rollback doesn't depend on `fwb-ci2` being
turned off manually to avoid load-balancing between them again). Once (c) has run, rollback means
re-provisioning OrbStack + `fwb-ci` from scratch — this is exactly why (c) must be the *last* step,
gated on real confidence, and why the task's zero-downtime requirement forces (a)/(b)/(c) to be
sequential, human-approved gates rather than one script.

---

## 7. Watchdog probe integration

- `umbra status --json` already exists (`cmd/umbra/status.go`) and is explicitly named in the
  design spec as *"the probe surface for the self-healing OS watchdog"*
  (`docs/superpowers/specs/2026-07-11-umbra-design.md:72`) — matches the `fbox status`-style
  contract the task references. Current shape: `{"daemon":"up","machines":[...]}` with each machine
  carrying `state`/`ip` (per `status.go`'s non-JSON branch, `m.State`/`m.IP`).
- **M4 gap to close**: the docker VM's health (`umbra docker status`'s
  `Installed/Running/IP/Socket/ContextCurrent` fields, per `cmd/umbra/docker.go`) is **not**
  currently folded into `umbra status --json`'s top-level output — a watchdog polling only
  `umbra status --json` today would miss "docker VM is down" as a distinct alert condition from
  "some machine is down." Fold a `docker` key into the JSON status payload
  (`{"daemon":"up","machines":[...],"docker":{"installed":true,"running":true,...}}`) so
  `services-watch.sh`-style scripts (per user memory: 3-channel alerting pattern) get one probe
  call for daemon+machines+docker instead of two.
- Register `com.forceai.umbrad`'s `KeepAlive` restart behavior itself as one layer of self-healing
  (matches the design spec's own framing: *"launchd KeepAlive, autostart machines re-boot in
  seconds, watchdog probes `umbra status --json`"* — three complementary layers, not redundant
  ones: launchd restarts the *process*, autostart re-boots *VMs* after a fresh process start,
  the watchdog probe is the *external* health signal for alerting when either of the first two
  isn't enough).
- Follow the existing repo-wide alerting convention (per global memory: 3-channel alert pattern +
  Telegram bridge already used by `services-watch.sh`/`cost-guard.sh` for other Force AI infra) —
  wire `umbra status --json` into that same probe script rather than inventing a new monitoring
  path specific to Umbra.

---

## 8. Pitfalls (continuing the PITFALLS-EXTERNAL.md numbering from P19)

## P19 — launchd's minimal PATH breaks `docker` CLI lookups from `umbrad`
Concretely traced: `internal/dockerctx/dockerctx.go:27`'s `lookPath("docker")` (called from
`DockerInstall`/`DockerStart` API handlers) fails under a bare launchd PATH
(`/usr/bin:/bin:/usr/sbin:/sbin`) since Homebrew's `docker` CLI lives at `/opt/homebrew/bin/docker`.
**Every other host-side `exec.Command`** in the codebase (`internal/api/server.go:130`,
`cmd/umbrad/docker.go:71`) already uses an absolute path (`/usr/bin/sw_vers`) and is unaffected —
this is the *only* PATH-dependent call site. **Mitigation**: `EnvironmentVariables.PATH` in the
plist (§1), verified by re-running `umbra docker status` after `umbra daemon install`.

## P20 — the registration token is a ticking clock across a multi-instance install
GitHub's org registration token expires in 1 hour. A cloud-init-baked token (the pattern M3 uses
for static docker provisioning) risks going stale before boot+provisioning completes, and installing
N runner instances sequentially against one token risks the same on a slow guest. **Mitigation**:
fetch a fresh token per `config.sh` invocation, live, over `umbra shell` — never bake it into the
seed ISO (§4).

## P21 — runner VM loses network on host sleep; the M2 supervisor is necessary but not sufficient
`internal/netstack/supervisor.go`'s sleep/wake probe (per `main.go`'s wiring) detects a wake gap and
probes SSH health, logging loudly — but it does not restart the GitHub Actions runner *service*
inside the guest if the runner's own long-lived HTTPS connection to GitHub's Actions backend
(separate from the SSH-based probe path) needs re-establishing after a host sleep. `svc.sh`'s
systemd unit should already auto-reconnect (the runner binary itself retries), but this should be
explicitly verified during §5's verification phase (put the host to sleep/wake mid-verification,
confirm the runner still picks up a dispatched job afterward) rather than assumed.

## P22 — CI cache/build-artifact disk growth silently fills the guest disk
Docker image layers, npm/pnpm caches, and build artifacts accumulate on `fwb-ci2`'s guest disk over
weeks of CI churn — this is P9 from `PITFALLS-EXTERNAL.md` ("zombie... disk fills") applied to a
*healthy*-but-growing disk rather than a crashed one. **Mitigation**: size the guest disk with
headroom over whatever `fwb-ci`'s OrbStack 26GB has proven sufficient for, and add a periodic
`docker system prune -af --volumes` (cron or a runner post-job hook) — not solved by M1–M3's
`growpart`-on-first-boot alone, since that only grows the filesystem to fill the *disk image*, it
doesn't reclaim space once CI churn fills it.

## P23 — codesign/entitlement survives launchd, but *rebuilds* silently break it if the LaunchAgent isn't reloaded
Running `make build` while the old `bin/umbrad` is still the one loaded under launchd is fine (the
running process keeps its own already-checked entitlement in memory) — but developers should know
that `umbra daemon install`/`kickstart -k` after a rebuild is required to pick up the new signed
binary; there's no launchd "auto-reload on file change" behavior. Document as a one-line note in
the `umbra daemon` command help text, not a bug — just a discoverability gap.

## P24 — TCC prompts have no UI to answer under a LaunchAgent (see §1)
Restated as a pitfall for visibility: a first-run VirtioFS home-share TCC prompt cannot be answered
by a LaunchAgent (no windowed session). If the interactive-first-run step in §1 is skipped, the
guest's `/mnt/mac` mount could silently fail for TCC-protected subdirectories the *first* time
`umbrad` ever runs as a LaunchAgent, with no visible dialog — only an `EPERM` buried in guest logs.

---

## Sources cited

- [launchd.info — plist key tutorial](https://www.launchd.info/)
- [`launchd.plist(5)` man page](https://keith.github.io/xcode-man-pages/launchd.plist.5.html)
- [tjluoma/launchd-keepalive — worked KeepAlive examples](https://github.com/tjluoma/launchd-keepalive)
- [Apple: Creating Launch Daemons and Agents](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)
- [launchctl2 syntax reference](https://babodee.wordpress.com/2016/04/09/launchctl-2-0-syntax/)
- [launchctl bootstrap/bootout migration example (chroxy#743)](https://github.com/blamechris/chroxy/issues/743)
- [Dear Launchctl, We're All Using You Wrong](https://joelsenders.wordpress.com/2019/03/14/dear-launchctl-were-all-using-you-wrong/)
- [Homebrew Services internals (writes/loads the plist)](https://dorokhovich.com/blog/homebrew-services)
- [Colima autostart via `brew services` (issue #96)](https://github.com/abiosoft/colima/issues/96)
- [Where is my PATH, launchd? (Medium)](https://lucaspin.medium.com/where-is-my-path-launchd-fc3fc5449864)
- [Apple Developer Forums — launchd PATH thread](https://developer.apple.com/forums/thread/681550)
- [GitHub REST API — self-hosted runners](https://docs.github.com/en/rest/actions/self-hosted-runners)
- [GitHub Docs — configure the runner application as a service](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/configure-the-application)
- [GitHub Docs — choosing the runner for a job (label AND-semantics)](https://docs.github.com/actions/using-jobs/choosing-the-runner-for-a-job)
- [Community discussion — labels AND-semantics confirmed](https://github.com/orgs/community/discussions/50172)
- [OneUptime — self-hosted runner config walkthrough](https://oneuptime.com/blog/post/2026-01-25-github-actions-self-hosted-runner/view)
- [Running only a single instance of a process (flock pattern)](https://rednafi.com/misc/run-single-instance/)
- [gofrs/flock — cross-platform flock library](https://github.com/gofrs/flock)
- [claude-code#29119 — VirtioFS home-share TCC/EPERM failure mode](https://github.com/anthropics/claude-code/issues/29119)

Internal grounding (this repo, read at research time): `internal/paths/paths.go`,
`cmd/umbrad/main.go`, `cmd/umbra/status.go`, `cmd/umbra/docker.go`, `internal/dockerctx/dockerctx.go`,
`internal/cloudinit/seed.go`, `internal/registry/registry.go`, `docs/PITFALLS-EXTERNAL.md`,
`docs/runbooks/entitlements-and-codesigning.md`, `docs/superpowers/specs/2026-07-11-umbra-design.md`, `README.md`.
