# Umbra

Umbra is an open-source, OrbStack-style VM manager for macOS: fast Linux
machines and Docker containers on Apple Silicon, built on Apple's
[Virtualization.framework](https://developer.apple.com/documentation/virtualization)
via a lightweight Go daemon (`umbrad`) and CLI (`umbra`).

## Status

| Milestone | Scope | State |
|---|---|---|
| M1 | Core VM lifecycle (create/start/stop/delete, VZ integration, userspace NAT) | In progress |
| M2 | Docker container support (gvisor-tap-vsock networking) | Not started |

## Build

Requirements: macOS 13+ on Apple Silicon (arm64), Xcode Command Line Tools.

```bash
make build   # builds + ad-hoc codesigns bin/umbrad, builds bin/umbra
make test    # unit tests
make test-integration  # integration tests (builds first)
```

`umbrad` must always be built via `make build` — it requires the
`com.apple.security.virtualization` entitlement, applied via ad-hoc signing
in the build step. See
[docs/runbooks/entitlements-and-codesigning.md](docs/runbooks/entitlements-and-codesigning.md).

## Docs

- [docs/PITFALLS-EXTERNAL.md](docs/PITFALLS-EXTERNAL.md) — 12 verified production pitfalls (vz / gvisor-tap-vsock / VirtioFS / Rosetta)
- [docs/superpowers/specs/2026-07-11-umbra-design.md](docs/superpowers/specs/2026-07-11-umbra-design.md) — design spec
- [docs/superpowers/plans/2026-07-11-m1-core-vm-lifecycle.md](docs/superpowers/plans/2026-07-11-m1-core-vm-lifecycle.md) — M1 implementation plan (spec-driven development, TDD)
- [docs/runbooks/entitlements-and-codesigning.md](docs/runbooks/entitlements-and-codesigning.md) — entitlements & codesigning runbook

## License

Apache-2.0 — see [LICENSE](LICENSE).
