# Contributing to Umbra

Umbra is a macOS-only (Apple Silicon) VM manager built on Apple's
Virtualization.framework. Contributions welcome — this doc is the short,
accurate version of how the repo actually works.

## Build

```sh
make build    # go daemon (umbrad) + CLI (umbra), ad-hoc codesigns umbrad
              # with the com.apple.security.virtualization entitlement
make app      # SwiftUI menu bar app -> bin/Umbra.app (needs Xcode CLT)
make release  # tarball: bin/umbra-<version>-macos-arm64.tar.gz
```

`umbrad` must always be built via `make build` — a plain `go build` produces
a binary without the virtualization entitlement and it will fail to boot
VMs. See [docs/runbooks/entitlements-and-codesigning.md](docs/runbooks/entitlements-and-codesigning.md).

## Test

```sh
make test              # Go unit tests
make test-integration  # boots a real VM — needs an arm64 Mac + the vz
                        # entitlement; the Makefile codesigns the test
                        # binary before running it (~40s warm)
make app-test           # Swift/XCTest for the menu bar app
```

CI (`.github/workflows/`) runs `lint`, `unit` (`make test`), `build`,
`menubar` (Swift build + `make app-test`), `govulncheck`, and a full-history
`gitleaks` scan on every push/PR. `test-integration` is not run in CI —
GitHub-hosted macOS runners can't nest Virtualization.framework — so it's a
local-only gate; run it before shipping anything touching `internal/vm`.

## Repo structure

- `cmd/umbrad` — the daemon; `cmd/umbra` — the CLI
- `internal/` — `paths`, `registry`, `sshkey`, `cloudinit`, `image`,
  `ipalloc`, `netstack`, `vm`, `api`, `client`, `dockerbridge`, `dockerctx`,
  `launchagent`, `singleton`
- `apps/menubar/` — the SwiftUI menu bar app (Swift Package Manager, no
  `.xcodeproj`)
- `docs/` — `superpowers/specs` (design), `superpowers/plans` (per-milestone
  implementation plans, spec-driven development), `research` (ecosystem
  research cheat-sheets), `PITFALLS-EXTERNAL.md`, `runbooks/`

## Approach

Umbra is built pitfall-driven: [docs/PITFALLS-EXTERNAL.md](docs/PITFALLS-EXTERNAL.md)
documents 24 real production failures mined from Lima/Colima/apple-container/
vfkit issue trackers *before* the first line of code, and the implementation
is engineered against each one (panic-recovery boundaries around every vz
call, observed-state-confirmed stops, staged boot-readiness waits, etc.).
Each milestone is plan-before-code: a design spec
([docs/superpowers/specs/](docs/superpowers/specs/)) followed by a written
implementation plan ([docs/superpowers/plans/](docs/superpowers/plans/))
before any code changes, and each change gets a spec + quality review pass.

## Style

- `gofmt` — CI-gated (`make lint` runs `gofmt -l .` and fails on any diff)
- `go vet ./...` — also part of `make lint`
- Conventional commits (`feat:`, `fix:`, `docs:`, …)

## Security posture

Binaries are ad-hoc codesigned (`codesign --sign -`) with only the
`com.apple.security.virtualization` entitlement — there's no Apple Developer
Program membership behind this project, so there's no notarization and no
Developer ID signature. The app is not sandboxed. This is fine for
local/OSS-source distribution (build it yourself, or run the `make release`
tarball) but means Gatekeeper will flag an unsigned/unnotarized binary on
first run; that's expected, not a bug.

## How to contribute

1. Branch off `main`.
2. Open a PR — CI (lint, unit, build, menubar, vuln, gitleaks) must be green.
3. Keep changes scoped; if it touches `internal/vm` or networking, mention
   whether you ran `make test-integration` locally in the PR description.
