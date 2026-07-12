# Umbra M4 — launchd autostart + CI-runner cutover kit

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `umbrad` runs as a login LaunchAgent (single-instance-guarded, KeepAlive-restarted, autostarting flagged machines), and Umbra ships a complete, tested *cutover kit* — a CI-runner machine profile + runner-install script + parallel-verify workflow + human runbook — so the fwb-ci → Umbra CI migration can be executed with zero downtime.

**Architecture:** A `com.forceai.umbrad` LaunchAgent (`RunAtLoad`, `KeepAlive.SuccessfulExit=false`, explicit PATH, `~/.umbra/log` output) managed by a new `umbra daemon install|uninstall|status` group via `launchctl bootstrap/bootout/kickstart`. A `syscall.Flock` on `~/.umbra/run/umbrad.lock` is the single-instance guard (covers VM-disk races, not just the socket). A new `registry.Role == "ci-runner"` reuses the M3 role-driven cloud-init mechanism to provision a machine with plain docker (no tcp:2375 exposure — untrusted PR code must not reach the shared docker VM). The GitHub runner itself is installed post-boot via `scripts/install-runner.sh` pushed over `umbra shell` (the org registration token is 1-hour-lived, so never baked into the seed). Docker health folds into `umbra status --json` for the watchdog.

**STOPS AT THE HUMAN/PRODUCTION GATE.** M4's automated portion builds and unit/integration-tests all of the above. It does NOT: install the LaunchAgent on Ahmad's Mac (needs an interactive TCC first-run for the VirtioFS home share), register runners against the live `ForceAI-KW` org (needs `admin:org` auth + a live token + fwb-ci sizing input from Ahmad), boot `fwb-ci2`, add the verify workflow to a production repo, or run any cutover step. Those are the runbook in `docs/runbooks/ci-cutover.md`, executed by Ahmad.

**Tech Stack:** Go 1.25, `launchctl`, `syscall.Flock`, the existing netstack/vm/cloudinit/registry/dockerbridge packages, GitHub Actions self-hosted runner.

## Global Constraints

- Research cheat-sheet is authoritative: `docs/research/launchd-and-ci-cutover.md`. Symbols/commands/pitfalls (P19–P24) come from it.
- LaunchAgent label `com.forceai.umbrad` (matches the `com.forceai.*` convention). Plist at `~/Library/LaunchAgents/`. `KeepAlive.SuccessfulExit=false`, `RunAtLoad=true`, explicit `PATH` incl. `/opt/homebrew/bin` (P19 — else `dockerctx` `lookPath("docker")` fails under launchd), stdout/stderr → `paths.Logs()`. `ProgramArguments[0]` = the codesigned `bin/umbrad` directly, **no shell wrapper** (would break the entitlement chain).
- Use modern `launchctl bootstrap gui/<uid>`/`bootout`/`kickstart -k`, NOT deprecated `load`/`unload`. Idempotent: `install` does `bootout` (swallow not-found) then `bootstrap` fresh.
- flock guard acquired FIRST in `run()` (after `EnsureTree` creates `~/.umbra/run`), before the socket bind; clear human-readable error if held.
- CI-runner VM: `registry.Role == "ci-runner"`, plain docker via get.docker.com (reuse M3's recipe **minus** the tcp:2375 override + firewall — runner dockerd is local-socket-only), `umbra` user in the docker group. Runner registration (`config.sh`/`svc.sh`) stays OUT of cloud-init (token freshness, P20) — it's in `scripts/install-runner.sh`, pushed via `umbra shell` at install time.
- Verify workflow uses labels `[self-hosted, umbra-ci]`; the `umbra-ci` label is requested by NO existing workflow, so AND-semantics keep real jobs on fwb-ci until a human flips `runs-on` (the cutover).
- **Human gate, do NOT automate:** live-org runner registration, fwb-ci2 boot, the verify-workflow install into a prod repo, LaunchAgent install on the Mac, and the deregister/uninstall cutover. All go in `docs/runbooks/ci-cutover.md`.
- Build/test discipline identical to M1–M3: `//go:build darwin && arm64` where OS-specific; `gofmt`+`go mod tidy`+`make lint`+`go test ./... -count=1 -race` before each commit; conventional commits + trailers; specific `git add`; blast-radius sweep before "done".

## File Structure

```
internal/singleton/singleton.go      # flock single-instance guard (+_test)
internal/paths/paths.go              # + LockFile()
internal/launchagent/launchagent.go  # plist render + launchctl bootstrap/bootout/kickstart/status (+_test)
cmd/umbrad/main.go                   # acquire flock first
cmd/umbra/daemon.go                  # umbra daemon install|uninstall|status
cmd/umbra/root.go                    # register daemon cmd
internal/registry/registry.go        # + RoleCIRunner const
internal/cloudinit/seed.go           # ciRunnerRuncmdLines() (plain docker, no 2375)
internal/api/server.go               # fold docker health into status; (docker VM status already exists)
cmd/umbra/status.go                  # umbra status --json includes docker block
internal/client/client.go            # status view carries docker
scripts/install-runner.sh            # actions/runner install (pushed via umbra shell)
.github/workflow-templates/umbra-ci-verify.yml  # verify workflow TEMPLATE (not installed to prod)
docs/runbooks/ci-cutover.md          # the human runbook (register/verify/cutover/rollback)
docs/PITFALLS-EXTERNAL.md            # P19–P24
README.md                            # M4 section
```

---

### Task 1: `internal/singleton` flock guard + `paths.LockFile`

**Files:** Create `internal/singleton/singleton.go`, `internal/singleton/singleton_test.go`; modify `internal/paths/paths.go`.

**Interfaces:**
- `paths.LockFile() string` → `Run()/umbrad.lock`.
- `singleton.Acquire(path string) (*Lock, error)` — `os.OpenFile(O_CREATE|O_RDWR, 0600)` + `syscall.Flock(LOCK_EX|LOCK_NB)`; on `EWOULDBLOCK` return a clear "another umbrad is already running (lock held on <path>) — check `pgrep umbrad`" error. `(*Lock).Close() error` releases.

- [ ] **Step 1: Failing test** — `singleton_test.go`: `Acquire` a temp path succeeds; a second `Acquire` on the SAME path (same process, different fd) fails with a non-nil error mentioning "already running"; after `Close()`, a fresh `Acquire` succeeds again.

```go
package singleton

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireIsExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "umbrad.lock")
	l1, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(p); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second acquire should fail clearly, got %v", err)
	}
	if err := l1.Close(); err != nil {
		t.Fatal(err)
	}
	l2, err := Acquire(p) // released → reacquirable
	if err != nil {
		t.Fatalf("reacquire after close: %v", err)
	}
	l2.Close()
}
```

- [ ] **Step 2: Run** `go test ./internal/singleton/ -v` — FAIL
- [ ] **Step 3: Implement** `singleton.go` (research §2 sketch) + `paths.LockFile()`.
- [ ] **Step 4: Run** — PASS
- [ ] **Step 5: Commit** `feat(singleton): flock single-instance guard for umbrad`

### Task 2: wire flock into `umbrad` main

**Files:** Modify `cmd/umbrad/main.go`.

- [ ] **Step 1:** In `run(logger)`, after `paths.EnsureTree()` (which creates `~/.umbra/run`), before the socket listen: `lock, err := singleton.Acquire(paths.LockFile())`; on err `return err`; `defer lock.Close()`. No unit test (main wiring; covered by the E2E in Task 8 — a second `umbrad` exits non-zero).
- [ ] **Step 2:** `make build` + `go test ./... -count=1` green.
- [ ] **Step 3: Commit** `feat(daemon): acquire single-instance flock before binding the API socket`

### Task 3: `internal/launchagent` — plist render + launchctl ops

**Files:** Create `internal/launchagent/launchagent.go`, `internal/launchagent/launchagent_test.go`.

**Interfaces:**
```go
const Label = "com.forceai.umbrad"
func PlistPath() string                    // ~/Library/LaunchAgents/com.forceai.umbrad.plist
func RenderPlist(binPath, logDir string) []byte  // the §1 plist, absolute bin + PATH incl /opt/homebrew/bin
func Install(binPath, logDir string) error // write plist 0644, bootout(swallow), bootstrap, enable, kickstart -k
func Uninstall() error                     // bootout (swallow not-found), rm plist
func Installed() bool                       // plist file exists
// launchctl calls go through a var execCommand = exec.Command seam for tests.
```

- [ ] **Step 1: Failing test** — `RenderPlist` output contains the Label, the absolute bin path, `<key>RunAtLoad</key>\n\t<true/>`, `SuccessfulExit`/`false`, `/opt/homebrew/bin` in PATH, the log dir; is valid XML (parse with `encoding/xml` into a generic struct or at least `xml.Unmarshal` a `plist` doctype-stripped body succeeds). `Install`/`Uninstall` exercised via `execCommand` override asserting the `bootout`→`bootstrap`→`enable`→`kickstart` argv sequence (no real launchctl).
- [ ] **Step 2: Run** — FAIL
- [ ] **Step 3: Implement.** Use `gui/<uid>` from `os.Getuid()`. `Install` bootout-then-bootstrap for idempotency (research §3).
- [ ] **Step 4: Run** — PASS
- [ ] **Step 5: Commit** `feat(launchagent): plist render + launchctl bootstrap/bootout/kickstart`

### Task 4: `umbra daemon install|uninstall|status`

**Files:** Create `cmd/umbra/daemon.go`; modify `cmd/umbra/root.go`.

**Interfaces:** `daemon install` locates `bin/umbrad` next to the running `umbra` (via `os.Executable()` dir; `--bin`/`$UMBRA_BIN` override) → `launchagent.Install`. `daemon uninstall` → `launchagent.Uninstall`. `daemon status` → prints whether the LaunchAgent plist is installed AND whether the API is reachable (`apiClient.Ping`). Help text notes P23 (rebuild → re-run install to pick up the new signed binary).

- [ ] **Step 1:** Implement (glue over launchagent + client; no unit test — E2E/manual). Include a doc note in help that this needs a first interactive `make run-daemon` for the TCC home-share grant (P24).
- [ ] **Step 2:** `make build && make lint` green.
- [ ] **Step 3: Commit** `feat(cli): umbra daemon install/uninstall/status`

### Task 5: CI-runner role + cloud-init profile

**Files:** Modify `internal/registry/registry.go` (add `RoleCIRunner = "ci-runner"` const), `internal/cloudinit/seed.go` (+_test).

**Interfaces:** `BuildSeed` branches on `m.Role == "ci-runner"` → append `ciRunnerRuncmdLines()`: install docker via get.docker.com (NO tcp:2375 override, NO iptables 2375 rule — runner dockerd is local-socket only), `usermod -aG docker umbra`, `systemctl enable --now docker`. Reuse the single-`runcmd:` merge from M3.

- [ ] **Step 1: Failing test** — `TestBuildCIRunnerSeed`: a `Role:"ci-runner"` machine's user-data contains `get.docker.com`, `usermod -aG docker umbra`, but does NOT contain `tcp://0.0.0.0:2375` or `--dport 2375` (those are docker-role only). Static netplan + ssh key still present. A non-ci-runner machine unaffected.
- [ ] **Step 2: Run** — FAIL
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/cloudinit/ -race` — PASS
- [ ] **Step 5: Commit** `feat(cloudinit): ci-runner role profile (plain docker, no 2375 exposure)`

### Task 6: docker health in `umbra status --json`

**Files:** Modify `internal/api/server.go` (status/machines already exist; add a `docker` block to a status payload), `cmd/umbra/status.go`, `internal/client/client.go`.

**Interfaces:** `umbra status --json` payload gains `"docker": {installed, running, ip, context_current}` from the docker controller's Status (via a `DockerStatuser` interface the server already has as `Docker`). If docker isn't installed, `installed:false`. The non-JSON branch prints a `docker: <state>` line. Watchdog polls one call for daemon+machines+docker (research §7).

- [ ] **Step 1: Failing test** — api `server_test.go`: `GET /v1/status` (or the existing status route) with a fake Docker returning installed+running → JSON has `docker.running == true`; with no docker → `docker.installed == false`.
- [ ] **Step 2: Run** — FAIL
- [ ] **Step 3: Implement.** (There's currently no `/v1/status` route — `umbra status` calls `/v1/ping` + `/v1/machines`. Add a `GET /v1/status` that returns `{daemon, machines, docker}` and switch the CLI to it, OR add `GET /v1/docker/status` consumption to the CLI's status. Simplest: CLI status calls `/v1/machines` + `/v1/docker/status` and merges. Pick the one fewer-moving-parts; keep the watchdog contract "one command → all health".)
- [ ] **Step 4: Run** `go test ./... -race` — PASS; `make build` green.
- [ ] **Step 5: Commit** `feat(status): fold docker health into umbra status --json (watchdog probe)`

### Task 7: runner install script + verify workflow template + runbook + pitfalls

**Files:** Create `scripts/install-runner.sh`, `.github/workflow-templates/umbra-ci-verify.yml`, `docs/runbooks/ci-cutover.md`; modify `docs/PITFALLS-EXTERNAL.md`, `README.md`.

- [ ] **Step 1:** `scripts/install-runner.sh` (runs INSIDE the guest via `umbra shell fwb-ci2 -- bash -s`, takes `REG_TOKEN`, `RUNNER_NAME`, `RUNNER_COUNT` as env/args): downloads actions-runner-linux-**arm64**, per-instance `config.sh --url https://github.com/ForceAI-KW --token $REG_TOKEN --name <name>-N --labels umbra-ci --unattended --replace` in `~/actions-runner-N`, `sudo ./svc.sh install && sudo ./svc.sh start`. Idempotent via `--replace`. Shellcheck-clean (`bash -n`).
- [ ] **Step 2:** `umbra-ci-verify.yml` TEMPLATE: `on: workflow_dispatch`, `runs-on: [self-hosted, umbra-ci]`, steps that exercise real job shapes (checkout, `docker run --rm hello-world`, a node/go build smoke) — proves the runner env, not just registration. It lives under `.github/workflow-templates/` (NOT `.github/workflows/`, so it never runs in the umbra repo's own CI).
- [ ] **Step 3:** `docs/runbooks/ci-cutover.md` — the human runbook from research §4–§6: fetch registration token (`gh api POST /orgs/ForceAI-KW/actions/runners/registration-token`, needs `admin:org` — `gh auth refresh -s admin:org`), size fwb-ci2 to match `orb info fwb-ci` (INPUT NEEDED), `umbra create fwb-ci2 --role ci-runner ...` (note: needs a `--role` flag or a reserved path — see below), push+run install-runner.sh, verify via the verify workflow, then the **human-gated** cutover (flip runs-on → deregister old → orb delete → uninstall orbstack) + rollback. Clearly marked "Ahmad's hands only" from the cutover section on.
- [ ] **Step 4:** Add P19–P24 to PITFALLS-EXTERNAL.md (verbatim from research §8). README M4 section (`umbra daemon`, the cutover-kit pointer, the human-gate note).
- [ ] **Step 5:** Decide the ci-runner creation path: unlike `docker` (reserved, single), ci-runner is a normal-ish machine the user names. Add a `--role ci-runner` flag to `umbra create` (validated: only `ci-runner` accepted from the CLI; `docker` still reserved/rejected) so `umbra create fwb-ci2 --role ci-runner --cpus N --memory-gib M --disk-gib D` works. Wire it: `create.go` flag → `CreateRequest.Role` → API create sets `m.Role` (reject `docker`, accept `ci-runner` or ""). Update the reserved-name/role guard tests.
- [ ] **Step 6: Commit** `feat(ci): runner install script + verify workflow template + cutover runbook; --role ci-runner`

### Task 8: integration/E2E for the buildable parts + blast-radius + close M4

**Files:** `internal/vm/cirunner_integration_test.go` (`//go:build integration`), modify `scripts/e2e-smoke.sh` or a small daemon-restart check.

- [ ] **Step 1:** Integration test (this Mac): boot a `Role:"ci-runner"` machine on the netstack, assert docker installed in-guest (`umbra shell -- docker version` works) and that dockerd is NOT reachable on tcp:2375 from the host (the ci-runner profile must NOT expose it) — a negative security assertion. (Reuse the M2/M3 integration harness.)
- [ ] **Step 2:** A quick guard check: start `umbrad`, start a SECOND `umbrad` with the same `UMBRA_ROOT`, assert the second exits non-zero with the "already running" message (the flock guard). Scriptable in `scripts/e2e-smoke.sh` or a standalone check.
- [ ] **Step 3:** Run the integration + guard checks live → green (diagnose+fix plumbing, max 2 attempts, else BLOCKED with diagnostics).
- [ ] **Step 4:** Blast-radius sweep (record result): `com.forceai.umbrad` label consistency, `LockFile`/lock path, `ci-runner` role consumers, launchctl subcommands, the `--role` flag → CreateRequest → API → registry chain, docker-in-status consumers.
- [ ] **Step 5: Commit** `test(m4): ci-runner boot + single-instance guard; docs: M4 done`

---

## Self-Review

1. **Spec coverage (M4 automated scope):** LaunchAgent ✅ (T3/T4); single-instance guard ✅ (T1/T2); autostart already wired (M2), now under launchd; ci-runner profile ✅ (T5); runner install kit + verify workflow + runbook ✅ (T7); watchdog docker health ✅ (T6). Deferred to Ahmad (documented, not code): live-org registration, fwb-ci2 boot/sizing, LaunchAgent install (TCC first-run), the cutover.
2. **Placeholder scan:** research-doc references are grounding; runbook TODOs that need Ahmad's input (fwb-ci sizing) are explicitly marked "INPUT NEEDED", not silent gaps.
3. **Type consistency:** `singleton.Acquire`/`Lock` (T1) used by main (T2); `launchagent.Install/Uninstall/Installed` (T3) used by CLI (T4); `registry.RoleCIRunner` (T5) → cloudinit branch (T5) → `--role` flag → CreateRequest → API (T7); docker status block (T6) flows api→client→CLI.
```
