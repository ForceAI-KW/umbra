# Umbra M1 — Core VM Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `umbrad` boots a persistent Ubuntu arm64 machine via Virtualization.framework; `umbra shell <name>` drops into it; the Mac home dir is visible at `/mnt/mac` (VirtioFS).

**Architecture:** Go daemon (`umbrad`) owns VMs via Code-Hex/vz v3 (EFI boot, Ubuntu cloud image + cloud-init NoCloud seed ISO, VZNATNetworkDeviceAttachment for M1 networking, VirtioFS home share). JSON API over a unix socket; `umbra` CLI is a thin client. Every VM runs behind a per-VM state-machine goroutine with panic-recovery boundaries (PITFALLS P1), graceful-then-hard stop (P8), verified teardown (P9), and staged bounded boot-readiness (P6). gvisor-tap-vsock networking, DNS, docker, GUI come in M2+.

**Tech Stack:** Go 1.23+, github.com/Code-Hex/vz/v3 v3.7.1 (pinned), github.com/lima-vm/go-qcow2reader, github.com/kdomanski/iso9660, github.com/spf13/cobra, golang.org/x/crypto/ssh.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-11-umbra-design.md` · Pitfalls: `docs/PITFALLS-EXTERNAL.md` (P-numbers below refer to it)
- Naming (verbatim from spec): daemon `umbrad`, CLI `umbra`, state dir `~/.umbra/`, socket `~/.umbra/run/api.sock`, machine name regex `^[a-z0-9][a-z0-9-]{0,31}$`
- Platform: macOS 13+ arm64 only (dev machine is macOS 26 arm64). All vz-touching files get `//go:build darwin && arm64`.
- Dependencies EXACTLY: Code-Hex/vz/v3 **v3.7.1 pinned** (low upstream velocity — do not chase master), lima-vm/go-qcow2reader, kdomanski/iso9660, spf13/cobra, golang.org/x/crypto. No others without flagging. kdomanski/iso9660 is >12mo since release — **intentional exception to dep-freshness rule**: ISO9660 is a frozen format, the lib is the only maintained pure-Go writer, and it's used by kubevirt. Flag stays in this plan.
- `umbrad` MUST be codesigned with `com.apple.security.virtualization` after EVERY build (ad-hoc `-s -`), or vz calls fail at runtime. The Makefile is the only supported build entrypoint.
- OrbStack keeps running untouched throughout M1 (cutover is M4). Default subnet OrbStack uses doesn't conflict: vz NAT hands out 192.168.64.0/24 via macOS bootpd.
- Commits: conventional messages, specific paths (never `git add -A`), Co-Authored-By + Claude-Session trailers per user config.
- TDD every task: failing test → verify fail → implement → verify pass → commit.
- Integration/E2E tests carry `//go:build integration` and run ONLY on this Mac (GitHub-hosted runners can't create VZ VMs; CI builds + unit-tests only).

## File Structure (locked)

```
umbra/
├── go.mod  go.sum  LICENSE  README.md  Makefile  .gitignore
├── build/vz.entitlements
├── .github/workflows/ci.yml
├── cmd/
│   ├── umbrad/main.go          # daemon entrypoint
│   └── umbra/                  # CLI (cobra)
│       ├── main.go  root.go  create.go  machines.go  shell.go  status.go
├── internal/
│   ├── paths/paths.go          # ~/.umbra layout (+_test)
│   ├── registry/registry.go    # machine configs CRUD (+_test)
│   ├── sshkey/sshkey.go        # ed25519 keypair mgmt (+_test)
│   ├── cloudinit/seed.go       # NoCloud seed ISO builder (+_test)
│   ├── image/image.go          # cloud image download+verify+qcow2→raw (+_test)
│   ├── vmnet/leases.go         # /var/db/dhcpd_leases parser (+_test)
│   ├── vm/
│   │   ├── manager.go          # Manager + per-VM instance state machine (P1)
│   │   ├── guard.go            # recover boundary (+guard_test.go)
│   │   ├── stop.go             # graceful→hard stop escalation (P8/P9) (+stop_test.go)
│   │   ├── config_darwin.go    # vz.VirtualMachineConfiguration builder
│   │   └── readiness.go        # staged bounded boot wait (P6) (+readiness_test.go)
│   ├── api/server.go           # unix-socket JSON API (+server_test.go)
│   └── client/client.go        # CLI-side client w/ retry (P10) (+client_test.go)
├── scripts/e2e-smoke.sh
└── docs/runbooks/entitlements-and-codesigning.md
```

---

### Task 1: Repo scaffold, Makefile + codesign, CI, GitHub repo

**Files:**
- Create: `go.mod`, `.gitignore`, `LICENSE` (Apache-2.0), `Makefile`, `build/vz.entitlements`, `.github/workflows/ci.yml`, `README.md`, `docs/runbooks/entitlements-and-codesigning.md`

**Interfaces:**
- Produces: `make build` (builds+signs `bin/umbrad`, builds `bin/umbra`), `make test` (unit), `make test-integration`. Module path `github.com/ForceAI-KW/umbra`.

- [ ] **Step 1: Init module + hygiene files**

```bash
cd ~/Desktop/projects/umbra
go mod init github.com/ForceAI-KW/umbra
curl -s https://www.apache.org/licenses/LICENSE-2.0.txt > LICENSE
```

`.gitignore`:
```
bin/
*.img
*.iso
.DS_Store
coverage.out
```

- [ ] **Step 2: Entitlements + Makefile**

`build/vz.entitlements`:
```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>com.apple.security.virtualization</key>
    <true/>
</dict>
</plist>
```

`Makefile`:
```make
BIN := bin
GOFLAGS := -trimpath

.PHONY: build test test-integration lint clean run-daemon

build:
	mkdir -p $(BIN)
	go build $(GOFLAGS) -o $(BIN)/umbrad ./cmd/umbrad
	codesign --force --entitlements build/vz.entitlements --sign - $(BIN)/umbrad
	go build $(GOFLAGS) -o $(BIN)/umbra ./cmd/umbra

test:
	go test ./... -count=1

test-integration: build
	go test -tags=integration ./... -count=1 -timeout 15m

lint:
	gofmt -l . && test -z "$$(gofmt -l .)"
	go vet ./...

run-daemon: build
	$(BIN)/umbrad

clean:
	rm -rf $(BIN)
```

- [ ] **Step 3: CI workflow (Force AI standard: concurrency, per-job timeouts, notify-failure)**

`.github/workflows/ci.yml`:
```yaml
name: ci
on:
  push: { branches: [main] }
  pull_request:
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true
jobs:
  lint:
    runs-on: macos-14
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - run: make lint
  unit:
    runs-on: macos-14
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - run: go test ./... -count=1
  build:
    runs-on: macos-14
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - run: make build
  vuln:
    runs-on: macos-14
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - run: go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
  gitleaks:
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: gitleaks/gitleaks-action@dcedce43c6f43de0b836d1fe38946645c9c638dc # v2
        env: { GITHUB_TOKEN: "${{ secrets.GITHUB_TOKEN }}" }
  notify-failure:
    needs: [lint, unit, build, vuln, gitleaks]
    if: failure() && github.ref == 'refs/heads/main' && github.event_name == 'push'
    runs-on: ubuntu-latest
    timeout-minutes: 2
    steps:
      - run: |
          curl -s "https://api.telegram.org/bot${{ secrets.TELEGRAM_BOT_TOKEN }}/sendMessage" \
            -d chat_id="${{ secrets.TELEGRAM_ALERT_CHAT_ID }}" \
            -d text="🔴 umbra CI failed on main: ${{ github.event.head_commit.message }} — https://github.com/${{ github.repository }}/actions/runs/${{ github.run_id }}"
```

- [ ] **Step 4: README stub + entitlements runbook (P12 content)**

`README.md`: name, one-paragraph description (open-source OrbStack-style Linux machines + Docker for Apple Silicon), status table (M1 in progress), build instructions (`make build`, requires macOS 13+ arm64 + Xcode CLT), spec/pitfalls links.

`docs/runbooks/entitlements-and-codesigning.md`:
```markdown
# Entitlements & codesigning

- `umbrad` requires `com.apple.security.virtualization` — applied via ad-hoc signing in `make build`. An unsigned/re-linked binary fails at VM creation with a VZErrorDomain error. Always build via make.
- The CLI (`umbra`) creates no VMs → needs no entitlement.
- **Never request `com.apple.vm.networking`** (bridged networking). Apple gates it to vetted vendors (PITFALLS P12, Code-Hex/vz#180). Umbra uses userspace NAT (M1: VZNATNetworkDeviceAttachment; M2: gvisor-tap-vsock) which needs no entitlement. If bridged mode is ever demanded: separately-signed root helper via SMAppService (lima socket_vmnet pattern) — design doc first.
```

- [ ] **Step 5: Create private GitHub repo + push**

```bash
cd ~/Desktop/projects/umbra
git add go.mod .gitignore LICENSE Makefile build/vz.entitlements .github/workflows/ci.yml README.md docs/runbooks/entitlements-and-codesigning.md
git commit -m "chore: scaffold — module, Makefile+codesign, CI, entitlements runbook"
gh repo create ForceAI-KW/umbra --private --source . --push
```
Expected: repo created, CI runs (lint/unit pass trivially, build passes with empty cmd dirs skipped — note: `make build` fails until Task 10 adds cmd/umbrad; acceptable, CI build job will go green at Task 10. Do NOT mark CI required until then).

### Task 2: `internal/paths` — state-dir layout

**Files:**
- Create: `internal/paths/paths.go`, `internal/paths/paths_test.go`

**Interfaces:**
- Produces: `paths.Root() string` (honors `$UMBRA_ROOT`, default `~/.umbra`), `Machines()`, `MachineDir(name string)`, `Images()`, `Run()`, `Logs()`, `SSH()`, `APISocket()` (= `Run()/api.sock`), `EnsureTree() error` (mkdir -p all, 0700).

- [ ] **Step 1: Write failing test** — `paths_test.go`:

```go
package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootHonorsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UMBRA_ROOT", dir)
	if Root() != dir {
		t.Fatalf("Root() = %q, want %q", Root(), dir)
	}
	if got, want := APISocket(), filepath.Join(dir, "run", "api.sock"); got != want {
		t.Fatalf("APISocket() = %q, want %q", got, want)
	}
}

func TestEnsureTreeCreatesAllDirs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UMBRA_ROOT", dir)
	if err := EnsureTree(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{Machines(), Images(), Run(), Logs(), SSH()} {
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() {
			t.Fatalf("missing dir %s: %v", d, err)
		}
		if st.Mode().Perm() != 0o700 {
			t.Fatalf("%s perm = %v, want 0700", d, st.Mode().Perm())
		}
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/paths/ -v` — expect FAIL (undefined: Root)
- [ ] **Step 3: Implement** `paths.go`:

```go
// Package paths defines the ~/.umbra state-directory layout.
package paths

import (
	"os"
	"path/filepath"
)

func Root() string {
	if v := os.Getenv("UMBRA_ROOT"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".umbra")
}

func Machines() string             { return filepath.Join(Root(), "machines") }
func MachineDir(name string) string { return filepath.Join(Machines(), name) }
func Images() string               { return filepath.Join(Root(), "images") }
func Run() string                  { return filepath.Join(Root(), "run") }
func Logs() string                 { return filepath.Join(Root(), "log") }
func SSH() string                  { return filepath.Join(Root(), "ssh") }
func APISocket() string            { return filepath.Join(Run(), "api.sock") }

func EnsureTree() error {
	for _, d := range []string{Machines(), Images(), Run(), Logs(), SSH()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run** `go test ./internal/paths/ -v` — expect PASS
- [ ] **Step 5: Commit** `git add internal/paths && git commit -m "feat(paths): ~/.umbra state-dir layout"`

### Task 3: `internal/registry` — machine config CRUD

**Files:**
- Create: `internal/registry/registry.go`, `internal/registry/registry_test.go`

**Interfaces:**
- Produces:
```go
type Machine struct {
	Name      string    `json:"name"`
	CPUs      uint      `json:"cpus"`
	MemoryMiB uint64    `json:"memory_mib"`
	DiskGiB   uint64    `json:"disk_gib"`
	Image     string    `json:"image"`      // e.g. "ubuntu:noble"
	MAC       string    `json:"mac"`        // locally-administered, assigned at create
	Autostart bool      `json:"autostart"`
	HostBuild string    `json:"host_build"` // sw_vers -buildVersion at creation (P6)
	CreatedAt time.Time `json:"created_at"`
}
func New(dir string) *Registry
func (r *Registry) Save(m *Machine) error            // atomic tmp+rename to <dir>/<name>/config.json
func (r *Registry) Load(name string) (*Machine, error) // ErrNotFound
func (r *Registry) List() ([]*Machine, error)          // sorted by name
func (r *Registry) Delete(name string) error           // os.RemoveAll machine dir
func ValidName(name string) bool                       // ^[a-z0-9][a-z0-9-]{0,31}$
var ErrNotFound = errors.New("machine not found")
```

- [ ] **Step 1: Write failing test** — `registry_test.go`:

```go
package registry

import (
	"errors"
	"testing"
	"time"
)

func newTestRegistry(t *testing.T) *Registry { return New(t.TempDir()) }

func TestSaveLoadRoundtrip(t *testing.T) {
	r := newTestRegistry(t)
	m := &Machine{Name: "fwb-ci", CPUs: 4, MemoryMiB: 8192, DiskGiB: 60,
		Image: "ubuntu:noble", MAC: "a6:5e:00:11:22:33", Autostart: true,
		HostBuild: "25F84", CreatedAt: time.Now().UTC().Truncate(time.Second)}
	if err := r.Save(m); err != nil {
		t.Fatal(err)
	}
	got, err := r.Load("fwb-ci")
	if err != nil {
		t.Fatal(err)
	}
	if got.MAC != m.MAC || !got.Autostart || got.MemoryMiB != 8192 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestLoadMissingReturnsErrNotFound(t *testing.T) {
	if _, err := newTestRegistry(t).Load("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestListSortedAndDelete(t *testing.T) {
	r := newTestRegistry(t)
	for _, n := range []string{"bbb", "aaa"} {
		if err := r.Save(&Machine{Name: n, CPUs: 1, MemoryMiB: 1024, DiskGiB: 10, Image: "ubuntu:noble"}); err != nil {
			t.Fatal(err)
		}
	}
	l, _ := r.List()
	if len(l) != 2 || l[0].Name != "aaa" {
		t.Fatalf("list = %+v", l)
	}
	if err := r.Delete("aaa"); err != nil {
		t.Fatal(err)
	}
	if l, _ = r.List(); len(l) != 1 {
		t.Fatalf("after delete: %+v", l)
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"fwb-ci": true, "a": true, "UPPER": false, "-lead": false,
		"": false, "has space": false, "0123456789012345678901234567890123": false,
	} {
		if ValidName(name) != want {
			t.Fatalf("ValidName(%q) != %v", name, want)
		}
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/registry/ -v` — expect FAIL (undefined)
- [ ] **Step 3: Implement** `registry.go`:

```go
// Package registry persists machine configurations as JSON under the machines dir.
package registry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var ErrNotFound = errors.New("machine not found")
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func ValidName(name string) bool { return nameRe.MatchString(name) }

type Machine struct {
	Name      string    `json:"name"`
	CPUs      uint      `json:"cpus"`
	MemoryMiB uint64    `json:"memory_mib"`
	DiskGiB   uint64    `json:"disk_gib"`
	Image     string    `json:"image"`
	MAC       string    `json:"mac"`
	Autostart bool      `json:"autostart"`
	HostBuild string    `json:"host_build"`
	CreatedAt time.Time `json:"created_at"`
}

type Registry struct{ dir string }

func New(dir string) *Registry { return &Registry{dir: dir} }

func (r *Registry) configPath(name string) string {
	return filepath.Join(r.dir, name, "config.json")
}

func (r *Registry) Save(m *Machine) error {
	if !ValidName(m.Name) {
		return errors.New("invalid machine name: must match ^[a-z0-9][a-z0-9-]{0,31}$")
	}
	dir := filepath.Join(r.dir, m.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.configPath(m.Name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.configPath(m.Name))
}

func (r *Registry) Load(name string) (*Machine, error) {
	b, err := os.ReadFile(r.configPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var m Machine
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *Registry) List() ([]*Machine, error) {
	entries, err := os.ReadDir(r.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Machine
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := r.Load(e.Name())
		if err != nil {
			continue // dir without config.json is not a machine
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *Registry) Delete(name string) error {
	if _, err := r.Load(name); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(r.dir, name))
}
```

- [ ] **Step 4: Run** `go test ./internal/registry/ -v` — expect PASS
- [ ] **Step 5: Commit** `git add internal/registry && git commit -m "feat(registry): machine config CRUD with atomic writes"`

### Task 4: `internal/sshkey` — managed ed25519 keypair

**Files:**
- Create: `internal/sshkey/sshkey.go`, `internal/sshkey/sshkey_test.go`

**Interfaces:**
- Produces: `sshkey.Ensure(dir string) (pubLine string, privPath string, err error)` — idempotent; writes `id_ed25519` (0600, OpenSSH PEM) + `id_ed25519.pub` in dir; returns the authorized_keys line `ssh-ed25519 AAAA... umbra`.
- Consumes: `golang.org/x/crypto/ssh` (`ssh.MarshalPrivateKey`, `ssh.NewPublicKey`, `ssh.MarshalAuthorizedKey`).

- [ ] **Step 1: Write failing test** — `sshkey_test.go`:

```go
package sshkey

import (
	"os"
	"strings"
	"testing"
)

func TestEnsureCreatesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	pub1, priv, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub1, "ssh-ed25519 ") {
		t.Fatalf("pub line: %q", pub1)
	}
	st, err := os.Stat(priv)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("priv perm %v", st.Mode().Perm())
	}
	pub2, _, err := Ensure(dir) // second call must not regenerate
	if err != nil || pub2 != pub1 {
		t.Fatalf("not idempotent: %v / %q vs %q", err, pub2, pub1)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/sshkey/ -v` — expect FAIL
- [ ] **Step 3: Implement** `sshkey.go`:

```go
// Package sshkey manages umbra's dedicated ed25519 keypair for machine access.
package sshkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

func Ensure(dir string) (pubLine, privPath string, err error) {
	privPath = filepath.Join(dir, "id_ed25519")
	pubPath := privPath + ".pub"
	if b, rerr := os.ReadFile(pubPath); rerr == nil {
		return strings.TrimSpace(string(b)), privPath, nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "umbra")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(privPath, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " umbra"
	if err := os.WriteFile(pubPath, []byte(line+"\n"), 0o644); err != nil {
		return "", "", err
	}
	return line, privPath, nil
}
```

- [ ] **Step 4: Run** `go test ./internal/sshkey/ -v` — expect PASS (run `go get golang.org/x/crypto` first)
- [ ] **Step 5: Commit** `git add internal/sshkey go.mod go.sum && git commit -m "feat(sshkey): managed ed25519 keypair"`

### Task 5: `internal/cloudinit` — NoCloud seed ISO

**Files:**
- Create: `internal/cloudinit/seed.go`, `internal/cloudinit/seed_test.go`

**Interfaces:**
- Consumes: `registry.Machine`, ssh pub line from Task 4, `github.com/kdomanski/iso9660`.
- Produces: `cloudinit.BuildSeed(m *registry.Machine, machineDir, sshPub string) (isoPath string, err error)` — writes `<machineDir>/seed.iso`, volume label `cidata`, containing `user-data` + `meta-data`. Guest user is `umbra`, VirtioFS tag `home` mounted at `/mnt/mac`, chrony installed (clock-drift near-miss, lima#850).

- [ ] **Step 1: Write failing test** — `seed_test.go`:

```go
package cloudinit

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kdomanski/iso9660"
	"github.com/ForceAI-KW/umbra/internal/registry"
)

func TestBuildSeedProducesCidataISO(t *testing.T) {
	dir := t.TempDir()
	m := &registry.Machine{Name: "t1", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20, Image: "ubuntu:noble"}
	iso, err := BuildSeed(m, dir, "ssh-ed25519 AAAATEST umbra")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(iso)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := iso9660.OpenImage(f)
	if err != nil {
		t.Fatal(err)
	}
	root, err := img.RootDir()
	if err != nil {
		t.Fatal(err)
	}
	children, err := root.GetChildren()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, c := range children {
		b, _ := io.ReadAll(c.Reader())
		found[c.Name()] = string(b)
	}
	ud, ok := found["user-data"]
	if !ok {
		t.Fatalf("no user-data in ISO; got %v", keys(found))
	}
	for _, want := range []string{"#cloud-config", "ssh-ed25519 AAAATEST umbra", "name: umbra", "/mnt/mac", "virtiofs", "chrony", "local-hostname: t1"} {
		joined := ud + found["meta-data"]
		if !strings.Contains(joined, want) {
			t.Fatalf("seed missing %q", want)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 2: Run** `go test ./internal/cloudinit/ -v` — expect FAIL
- [ ] **Step 3: Implement** `seed.go`:

```go
// Package cloudinit builds NoCloud seed ISOs (volume label "cidata").
package cloudinit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kdomanski/iso9660"

	"github.com/ForceAI-KW/umbra/internal/registry"
)

const userDataTmpl = `#cloud-config
users:
  - name: umbra
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - %s
packages:
  - chrony
package_update: false
growpart:
  mode: auto
  devices: ["/"]
mounts:
  - [home, /mnt/mac, virtiofs, "defaults,nofail", "0", "0"]
ssh_pwauth: false
`

const metaDataTmpl = `instance-id: umbra-%s
local-hostname: %s
`

func BuildSeed(m *registry.Machine, machineDir, sshPub string) (string, error) {
	w, err := iso9660.NewWriter()
	if err != nil {
		return "", err
	}
	defer w.Cleanup()

	userData := fmt.Sprintf(userDataTmpl, sshPub)
	metaData := fmt.Sprintf(metaDataTmpl, m.Name, m.Name)
	if err := w.AddFile(strings.NewReader(userData), "user-data"); err != nil {
		return "", err
	}
	if err := w.AddFile(strings.NewReader(metaData), "meta-data"); err != nil {
		return "", err
	}

	isoPath := filepath.Join(machineDir, "seed.iso")
	f, err := os.OpenFile(isoPath+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if err := w.WriteTo(f, "cidata"); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return isoPath, os.Rename(isoPath+".tmp", isoPath)
}
```

- [ ] **Step 4: Run** `go test ./internal/cloudinit/ -v` — expect PASS (`go get github.com/kdomanski/iso9660` first)
- [ ] **Step 5: Commit** `git add internal/cloudinit go.mod go.sum && git commit -m "feat(cloudinit): NoCloud cidata seed ISO builder"`

### Task 6: `internal/image` — Ubuntu cloud image download + qcow2→raw

**Files:**
- Create: `internal/image/image.go`, `internal/image/image_test.go`

**Interfaces:**
- Produces:
```go
const DefaultImage = "ubuntu:noble"
func Resolve(ref string) (url, sumsURL, fileName string, err error) // only ubuntu:noble in M1
func Ensure(ctx context.Context, imagesDir, ref string) (rawPath string, err error)
// download qcow2 → verify sha256 against SHA256SUMS → convert to raw via go-qcow2reader → cache as <imagesDir>/ubuntu-noble-arm64.raw (atomic). Cached hit returns immediately.
func CloneDisk(rawBase, dst string, sizeGiB uint64) error
// copyfile.CloneFile (APFS clone via os specific) fallback io.Copy; then os.Truncate(dst, sizeGiB<<30). Guest growpart expands.
func parseSHA256SUMS(sums []byte, fileName string) (string, error)
```
- Consumes: `github.com/lima-vm/go-qcow2reader` (`qcow2reader.Open`, then `io.Copy` to raw file via `image.NewImageReader`-style sequential read).

- [ ] **Step 1: Write failing unit tests** (network-free) — `image_test.go`:

```go
package image

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveNoble(t *testing.T) {
	url, sums, name, err := Resolve("ubuntu:noble")
	if err != nil {
		t.Fatal(err)
	}
	if name != "ubuntu-24.04-server-cloudimg-arm64.img" {
		t.Fatalf("name = %q", name)
	}
	if url != "https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-arm64.img" {
		t.Fatalf("url = %q", url)
	}
	if sums != "https://cloud-images.ubuntu.com/releases/noble/release/SHA256SUMS" {
		t.Fatalf("sums = %q", sums)
	}
}

func TestResolveUnknownRefErrors(t *testing.T) {
	if _, _, _, err := Resolve("arch:latest"); err == nil {
		t.Fatal("want error for unsupported ref")
	}
}

func TestParseSHA256SUMS(t *testing.T) {
	sums := []byte("abc123 *ubuntu-24.04-server-cloudimg-arm64.img\ndef456 *other.img\n")
	got, err := parseSHA256SUMS(sums, "ubuntu-24.04-server-cloudimg-arm64.img")
	if err != nil || got != "abc123" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseSHA256SUMS(sums, "missing.img"); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestCloneDiskTruncatesToSize(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.raw")
	if err := os.WriteFile(base, []byte("rawdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "disk.img")
	if err := CloneDisk(base, dst, 1); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(dst)
	if st.Size() != 1<<30 {
		t.Fatalf("size = %d, want %d", st.Size(), 1<<30)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/image/ -v` — expect FAIL
- [ ] **Step 3: Implement** `image.go`:

```go
// Package image downloads, verifies, and converts guest base images.
package image

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lima-vm/go-qcow2reader"
)

const DefaultImage = "ubuntu:noble"

const nobleBase = "https://cloud-images.ubuntu.com/releases/noble/release/"

func Resolve(ref string) (url, sumsURL, fileName string, err error) {
	if ref != "ubuntu:noble" {
		return "", "", "", fmt.Errorf("unsupported image ref %q (M1 supports ubuntu:noble only)", ref)
	}
	fileName = "ubuntu-24.04-server-cloudimg-arm64.img"
	return nobleBase + fileName, nobleBase + "SHA256SUMS", fileName, nil
}

func rawCachePath(imagesDir, ref string) string {
	return filepath.Join(imagesDir, strings.ReplaceAll(ref, ":", "-")+"-arm64.raw")
}

func Ensure(ctx context.Context, imagesDir, ref string) (string, error) {
	rawPath := rawCachePath(imagesDir, ref)
	if _, err := os.Stat(rawPath); err == nil {
		return rawPath, nil
	}
	url, sumsURL, fileName, err := Resolve(ref)
	if err != nil {
		return "", err
	}
	qcowTmp := rawPath + ".qcow2.tmp"
	sum, err := download(ctx, url, qcowTmp)
	if err != nil {
		return "", err
	}
	defer os.Remove(qcowTmp)
	sums, err := fetch(ctx, sumsURL)
	if err != nil {
		return "", err
	}
	want, err := parseSHA256SUMS(sums, fileName)
	if err != nil {
		return "", err
	}
	if sum != want {
		return "", fmt.Errorf("sha256 mismatch for %s: got %s want %s", fileName, sum, want)
	}
	if err := convertToRaw(qcowTmp, rawPath+".tmp"); err != nil {
		return "", err
	}
	return rawPath, os.Rename(rawPath+".tmp", rawPath)
}

func download(ctx context.Context, url, dst string) (sha string, err error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func parseSHA256SUMS(sums []byte, fileName string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == fileName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s not found in SHA256SUMS", fileName)
}

func convertToRaw(qcowPath, dst string) error {
	f, err := os.Open(qcowPath)
	if err != nil {
		return err
	}
	defer f.Close()
	img, err := qcow2reader.Open(f)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// sequential copy of the virtual disk content
	if _, err := io.Copy(out, io.NewSectionReader(img, 0, img.Size())); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func CloneDisk(rawBase, dst string, sizeGiB uint64) error {
	src, err := os.Open(rawBase)
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Truncate(dst, int64(sizeGiB)<<30)
}
```
Note for implementer: `qcow2reader.Open` returns an `image.Image` which implements `io.ReaderAt` + `Size()`; if the pinned version's API differs, adapt inside `convertToRaw` only — the exported surface stays fixed.

- [ ] **Step 4: Run** `go test ./internal/image/ -v` — expect PASS (`go get github.com/lima-vm/go-qcow2reader` first)
- [ ] **Step 5: Commit** `git add internal/image go.mod go.sum && git commit -m "feat(image): ubuntu cloud image download, sha256 verify, qcow2→raw"`

### Task 7: `internal/vmnet` — dhcpd_leases IP lookup

**Files:**
- Create: `internal/vmnet/leases.go`, `internal/vmnet/leases_test.go`

**Interfaces:**
- Produces: `vmnet.LookupIP(leasesContent []byte, mac string) (string, bool)` (pure function) and `vmnet.LookupIPFromFile(mac string) (string, bool, error)` reading `/var/db/dhcpd_leases`.
- **Trap this task exists for:** macOS bootpd strips leading zeros in `hw_address` octets (`0a:1b` stored as `a:1b`) and prefixes `1,`. Both sides must be normalized octet-by-octet.

- [ ] **Step 1: Write failing test** — `leases_test.go`:

```go
package vmnet

import "testing"

const fixture = `{
	name=t1
	ip_address=192.168.64.5
	hw_address=1,a6:5e:0:11:2:33
	identifier=1,a6:5e:0:11:2:33
	lease=0x66b2c1de
}
{
	name=other
	ip_address=192.168.64.9
	hw_address=1,de:ad:be:ef:0:1
}`

func TestLookupIPNormalizesLeadingZeros(t *testing.T) {
	// config stores canonical form with leading zeros; leases file has them stripped
	ip, ok := LookupIP([]byte(fixture), "a6:5e:00:11:02:33")
	if !ok || ip != "192.168.64.5" {
		t.Fatalf("got %q %v", ip, ok)
	}
}

func TestLookupIPMiss(t *testing.T) {
	if _, ok := LookupIP([]byte(fixture), "aa:bb:cc:dd:ee:ff"); ok {
		t.Fatal("want miss")
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/vmnet/ -v` — expect FAIL
- [ ] **Step 3: Implement** `leases.go`:

```go
// Package vmnet resolves guest IPs from macOS bootpd's lease database.
package vmnet

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

const leasesFile = "/var/db/dhcpd_leases"

// normalizeMAC parses each octet as hex so "a6:5e:0:11:2:33" == "a6:5e:00:11:02:33".
func normalizeMAC(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "1,")
	parts := strings.Split(s, ":")
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return ""
		}
		parts[i] = strconv.FormatUint(v, 16)
	}
	return strings.Join(parts, ":")
}

func LookupIP(leases []byte, mac string) (string, bool) {
	want := normalizeMAC(mac)
	if want == "" {
		return "", false
	}
	var ip string
	sc := bufio.NewScanner(strings.NewReader(string(leases)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "{":
			ip = ""
		case strings.HasPrefix(line, "ip_address="):
			ip = strings.TrimPrefix(line, "ip_address=")
		case strings.HasPrefix(line, "hw_address="):
			if normalizeMAC(strings.TrimPrefix(line, "hw_address=")) == want && ip != "" {
				return ip, true
			}
		}
	}
	return "", false
}

func LookupIPFromFile(mac string) (string, bool, error) {
	b, err := os.ReadFile(leasesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	ip, ok := LookupIP(b, mac)
	return ip, ok, nil
}
```

- [ ] **Step 4: Run** `go test ./internal/vmnet/ -v` — expect PASS
- [ ] **Step 5: Commit** `git add internal/vmnet && git commit -m "feat(vmnet): dhcpd_leases parser with MAC leading-zero normalization"`

### Task 8: `internal/vm` — guard, stop escalation, state machine, vz config

**Files:**
- Create: `internal/vm/guard.go`, `internal/vm/guard_test.go`, `internal/vm/stop.go`, `internal/vm/stop_test.go`, `internal/vm/manager.go`, `internal/vm/config_darwin.go`

**Interfaces:**
- Produces:
```go
type State string
const (StateStopped State = "stopped"; StateStarting State = "starting"; StateRunning State = "running"; StateStopping State = "stopping"; StateCrashed State = "crashed")
type Info struct { Name string `json:"name"`; State State `json:"state"`; IP string `json:"ip,omitempty"` }
func NewManager(reg *registry.Registry, machinesDir string) *Manager
func (m *Manager) Start(ctx context.Context, name string) error   // idempotent if running
func (m *Manager) Stop(ctx context.Context, name string) error
func (m *Manager) StopAll(ctx context.Context)
func (m *Manager) Info(name string) Info
func (m *Manager) List() []Info
func (m *Manager) SetIP(name, ip string)   // called by readiness
```
- Internal seam for tests (vz never runs in CI): `type vzHandle interface { RequestStop() (bool, error); Stop() error; State() vzState; Start() error }` with `vzState` = `int` mirroring vz states. Real impl wraps `*vz.VirtualMachine` (darwin file); tests use fakes.
- P1: every call into a vzHandle happens inside `guarded(...)`. P8/P9: `stopWithEscalation` = RequestStop → wait `gracefulTimeout` (30s, injectable) → hard Stop() → poll state to confirmed-stopped with `hardTimeout` (60s ceiling) → error if still not gone (caller marks Crashed, never reports clean stop).

- [ ] **Step 1: Write failing guard test** — `guard_test.go`:

```go
package vm

import (
	"errors"
	"testing"
)

func TestGuardedConvertsPanicToError(t *testing.T) {
	err := guarded("stop", func() error {
		panic("runtime/cgo: misuse of an invalid Handle")
	})
	if err == nil || !errors.Is(err, ErrVZPanic) {
		t.Fatalf("want ErrVZPanic, got %v", err)
	}
}

func TestGuardedPassesThroughError(t *testing.T) {
	want := errors.New("boom")
	if err := guarded("start", func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/vm/ -run TestGuarded -v` — FAIL
- [ ] **Step 3: Implement** `guard.go`:

```go
// Package vm owns VM lifecycle. All vz calls are guarded: a panic in the
// cgo/Objective-C boundary (Code-Hex/vz#124) must crash ONE VM's state, never
// the daemon (PITFALLS P1).
package vm

import (
	"errors"
	"fmt"
)

var ErrVZPanic = errors.New("vz panicked")

func guarded(op string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w during %s: %v", ErrVZPanic, op, r)
		}
	}()
	return fn()
}
```

- [ ] **Step 4: Run** — PASS. Commit: `git add internal/vm/guard.go internal/vm/guard_test.go && git commit -m "feat(vm): recover guard converting vz cgo panics to errors (P1)"`

- [ ] **Step 5: Write failing stop-escalation test** — `stop_test.go`:

```go
package vm

import (
	"context"
	"testing"
	"time"
)

type fakeVZ struct {
	state           vzState
	requestStopped  bool
	hardStopped     bool
	honorGraceful   bool // if true, transition to stopped after RequestStop
	honorHard       bool
}

func (f *fakeVZ) Start() error { f.state = vzRunning; return nil }
func (f *fakeVZ) RequestStop() (bool, error) {
	f.requestStopped = true
	if f.honorGraceful {
		f.state = vzStopped
	}
	return true, nil
}
func (f *fakeVZ) Stop() error {
	f.hardStopped = true
	if f.honorHard {
		f.state = vzStopped
	}
	return nil
}
func (f *fakeVZ) State() vzState { return f.state }

func TestStopGracefulPath(t *testing.T) {
	f := &fakeVZ{state: vzRunning, honorGraceful: true}
	err := stopWithEscalation(context.Background(), f, 50*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if f.hardStopped {
		t.Fatal("hard stop should not fire when graceful works")
	}
}

func TestStopEscalatesToHardKill(t *testing.T) {
	// panicked guest: RequestStop never lands (P8)
	f := &fakeVZ{state: vzRunning, honorGraceful: false, honorHard: true}
	err := stopWithEscalation(context.Background(), f, 20*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !f.hardStopped {
		t.Fatal("expected escalation to hard stop")
	}
}

func TestStopReportsFailureWhenNeverConfirmed(t *testing.T) {
	// zombie: even hard kill doesn't confirm (P9) — must NOT report clean stop
	f := &fakeVZ{state: vzRunning}
	if err := stopWithEscalation(context.Background(), f, 20*time.Millisecond, 50*time.Millisecond); err == nil {
		t.Fatal("want error when stop never confirmed")
	}
}
```

- [ ] **Step 6: Run** `go test ./internal/vm/ -run TestStop -v` — FAIL
- [ ] **Step 7: Implement** `stop.go`:

```go
package vm

import (
	"context"
	"fmt"
	"time"
)

type vzState int

const (
	vzStopped vzState = iota
	vzRunning
	vzOther
)

// vzHandle is the minimal seam over *vz.VirtualMachine so escalation logic
// is unit-testable off-mac.
type vzHandle interface {
	Start() error
	RequestStop() (bool, error)
	Stop() error
	State() vzState
}

// stopWithEscalation: graceful ACPI RequestStop → gracefulTimeout → hard
// Stop() → poll until confirmed stopped within hardTimeout (P8, P9). Never
// trust a stop call on send — only on observed state.
func stopWithEscalation(ctx context.Context, h vzHandle, gracefulTimeout, hardTimeout time.Duration) error {
	_ = guarded("request-stop", func() error {
		_, err := h.RequestStop()
		return err
	}) // errors fall through to hard path
	if waitState(ctx, h, vzStopped, gracefulTimeout) {
		return nil
	}
	if err := guarded("hard-stop", h.Stop); err != nil {
		return fmt.Errorf("hard stop failed: %w", err)
	}
	if waitState(ctx, h, vzStopped, hardTimeout) {
		return nil
	}
	return fmt.Errorf("vm did not reach stopped state within %s after hard kill (zombie — P9)", hardTimeout)
}

func waitState(ctx context.Context, h vzHandle, want vzState, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h.State() == want {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return h.State() == want
}
```

- [ ] **Step 8: Run** — PASS. Commit: `git add internal/vm/stop.go internal/vm/stop_test.go && git commit -m "feat(vm): graceful→hard stop escalation with confirmed teardown (P8, P9)"`

- [ ] **Step 9: Implement manager + darwin vz wiring** (no new unit tests — covered by fakes above + integration in Task 12; the manager is thin orchestration):

`manager.go`:
```go
package vm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ForceAI-KW/umbra/internal/registry"
)

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateCrashed  State = "crashed"
)

type Info struct {
	Name  string `json:"name"`
	State State  `json:"state"`
	IP    string `json:"ip,omitempty"`
}

type instance struct {
	mu     sync.Mutex
	state  State
	ip     string
	handle vzHandle
	stopFn func() // releases run loop resources (darwin)
}

type Manager struct {
	reg         *registry.Registry
	machinesDir string
	mu          sync.Mutex
	instances   map[string]*instance
}

func NewManager(reg *registry.Registry, machinesDir string) *Manager {
	return &Manager{reg: reg, machinesDir: machinesDir, instances: map[string]*instance{}}
}

func (m *Manager) inst(name string) *instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i, ok := m.instances[name]; ok {
		return i
	}
	i := &instance{state: StateStopped}
	m.instances[name] = i
	return i
}

func (m *Manager) Start(ctx context.Context, name string) error {
	cfg, err := m.reg.Load(name)
	if err != nil {
		return err
	}
	i := m.inst(name)
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state == StateRunning || i.state == StateStarting {
		return nil
	}
	i.state = StateStarting
	h, stopFn, err := launchVZ(cfg, m.machinesDir) // darwin-only; guarded inside
	if err != nil {
		i.state = StateCrashed
		return fmt.Errorf("launch %s: %w", name, err)
	}
	i.handle, i.stopFn = h, stopFn
	i.state = StateRunning
	return nil
}

func (m *Manager) Stop(ctx context.Context, name string) error {
	i := m.inst(name)
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state != StateRunning && i.state != StateCrashed {
		return nil
	}
	i.state = StateStopping
	err := stopWithEscalation(ctx, i.handle, 30*time.Second, 60*time.Second)
	if i.stopFn != nil {
		i.stopFn()
	}
	if err != nil {
		i.state = StateCrashed
		return err
	}
	i.state = StateStopped
	i.ip = ""
	return nil
}

func (m *Manager) StopAll(ctx context.Context) {
	m.mu.Lock()
	names := make([]string, 0, len(m.instances))
	for n := range m.instances {
		names = append(names, n)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func(n string) { defer wg.Done(); _ = m.Stop(ctx, n) }(n)
	}
	wg.Wait()
}

func (m *Manager) SetIP(name, ip string) {
	i := m.inst(name)
	i.mu.Lock()
	i.ip = ip
	i.mu.Unlock()
}

func (m *Manager) Info(name string) Info {
	i := m.inst(name)
	i.mu.Lock()
	defer i.mu.Unlock()
	return Info{Name: name, State: i.state, IP: i.ip}
}

func (m *Manager) List() []Info {
	machines, _ := m.reg.List()
	out := make([]Info, 0, len(machines))
	for _, mc := range machines {
		out = append(out, m.Info(mc.Name))
	}
	return out
}
```

`config_darwin.go` (build tag `//go:build darwin && arm64`) — the only file importing vz:
```go
//go:build darwin && arm64

package vm

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/Code-Hex/vz/v3"

	"github.com/ForceAI-KW/umbra/internal/registry"
)

// realVZ adapts *vz.VirtualMachine to vzHandle.
type realVZ struct{ vm *vz.VirtualMachine }

func (r *realVZ) Start() error              { return r.vm.Start() }
func (r *realVZ) RequestStop() (bool, error) { return r.vm.RequestStop() }
func (r *realVZ) Stop() error               { return r.vm.Stop() }
func (r *realVZ) State() vzState {
	switch r.vm.State() {
	case vz.VirtualMachineStateStopped, vz.VirtualMachineStateError:
		return vzStopped
	case vz.VirtualMachineStateRunning:
		return vzRunning
	default:
		return vzOther
	}
}

// launchVZ builds the configuration and starts the VM. Every vz call is
// inside guarded() — a cgo panic marks this VM crashed, not the daemon (P1).
func launchVZ(m *registry.Machine, machinesDir string) (vzHandle, func(), error) {
	mdir := filepath.Join(machinesDir, m.Name)
	var handle *realVZ
	err := guarded("launch", func() error {
		bootLoader, err := efiBootLoader(mdir)
		if err != nil {
			return err
		}
		platform, err := genericPlatform(mdir)
		if err != nil {
			return err
		}
		cfg, err := vz.NewVirtualMachineConfiguration(bootLoader, m.CPUs, m.MemoryMiB*1024*1024)
		if err != nil {
			return err
		}
		cfg.SetPlatformVirtualMachineConfiguration(platform)

		// storage: root disk + cloud-init seed
		var storages []vz.StorageDeviceConfiguration
		for _, img := range []string{filepath.Join(mdir, "disk.img"), filepath.Join(mdir, "seed.iso")} {
			att, err := vz.NewDiskImageStorageDeviceAttachment(img, false)
			if err != nil {
				return fmt.Errorf("attach %s: %w", img, err)
			}
			blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
			if err != nil {
				return err
			}
			storages = append(storages, blk)
		}
		cfg.SetStorageDevicesVirtualMachineConfiguration(storages)

		// network: NAT with the machine's persistent MAC (IP found via dhcpd_leases)
		natAtt, err := vz.NewNATNetworkDeviceAttachment()
		if err != nil {
			return err
		}
		netCfg, err := vz.NewVirtioNetworkDeviceConfiguration(natAtt)
		if err != nil {
			return err
		}
		hw, err := net.ParseMAC(m.MAC)
		if err != nil {
			return err
		}
		mac, err := vz.NewMACAddress(hw)
		if err != nil {
			return err
		}
		netCfg.SetMACAddress(mac)
		cfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netCfg})

		// virtiofs: share $HOME as tag "home" (mounted at /mnt/mac by cloud-init)
		home, _ := os.UserHomeDir()
		fsCfg, err := vz.NewVirtioFileSystemDeviceConfiguration("home")
		if err != nil {
			return err
		}
		shared, err := vz.NewSharedDirectory(home, false)
		if err != nil {
			return err
		}
		single, err := vz.NewSingleDirectoryShare(shared)
		if err != nil {
			return err
		}
		fsCfg.SetDirectoryShare(single)
		cfg.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{fsCfg})

		// serial console → log file
		serialAtt, err := vz.NewFileSerialPortAttachment(filepath.Join(mdir, "console.log"), false)
		if err != nil {
			return err
		}
		serial, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAtt)
		if err != nil {
			return err
		}
		cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serial})

		// entropy
		entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
		if err != nil {
			return err
		}
		cfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

		if ok, err := cfg.Validate(); !ok || err != nil {
			return fmt.Errorf("vz config invalid: %w", err)
		}
		machine, err := vz.NewVirtualMachine(cfg)
		if err != nil {
			return err
		}
		if err := machine.Start(); err != nil {
			return err
		}
		handle = &realVZ{vm: machine}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return handle, func() {}, nil
}

func efiBootLoader(mdir string) (vz.BootLoader, error) {
	storePath := filepath.Join(mdir, "efi-vars.fd")
	if _, err := os.Stat(storePath); os.IsNotExist(err) {
		if _, err := vz.NewEFIVariableStore(storePath, vz.WithCreatingEFIVariableStore()); err != nil {
			return nil, err
		}
	}
	store, err := vz.NewEFIVariableStore(storePath)
	if err != nil {
		return nil, err
	}
	return vz.NewEFIBootLoader(vz.WithEFIVariableStore(store))
}

func genericPlatform(mdir string) (vz.PlatformConfiguration, error) {
	idPath := filepath.Join(mdir, "machine-id.bin")
	var mid *vz.GenericMachineIdentifier
	if b, err := os.ReadFile(idPath); err == nil {
		mid, err = vz.NewGenericMachineIdentifierWithData(b)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		mid, err = vz.NewGenericMachineIdentifier()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(idPath, mid.DataRepresentation(), 0o600); err != nil {
			return nil, err
		}
	}
	return vz.NewGenericPlatformConfiguration(vz.WithGenericMachineIdentifier(mid))
}
```
Note for implementer: verify exact vz v3.7.1 API names against the pinned module source (`go doc github.com/Code-Hex/vz/v3 | less`) — if a constructor differs (e.g. `NewGenericMachineIdentifierWithDataRepresentation`), adapt inside this file only. The vzHandle seam and guarded() usage are non-negotiable.

- [ ] **Step 10: Verify build compiles + unit suite green**: `make build || go build ./...` then `go test ./... -count=1` — expect PASS
- [ ] **Step 11: Commit** `git add internal/vm go.mod go.sum && git commit -m "feat(vm): manager, per-VM guarded lifecycle, vz EFI/NAT/virtiofs config"`

### Task 9: `internal/vm/readiness.go` — staged bounded boot wait (P6)

**Files:**
- Create: `internal/vm/readiness.go`, `internal/vm/readiness_test.go`

**Interfaces:**
- Produces:
```go
type stageError struct{ Stage, Detail string } // Error(): `readiness stage "ip" timed out after 90s: ...`
func waitReady(ctx context.Context, lookupIP func() (string, bool, error), dial func(addr string) error, timeout time.Duration) (ip string, err error)
// Stage 1 "ip": poll lookupIP every 1s. Stage 2 "ssh": poll dial(ip:22) every 1s.
// On timeout returns *stageError naming the stage that failed — never a bare deadline error.
```
- Consumes: `vmnet.LookupIPFromFile` (wrapped in a closure by the caller in Task 10), `net.DialTimeout`.

- [ ] **Step 1: Write failing test** — `readiness_test.go`:

```go
package vm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitReadyHappyPath(t *testing.T) {
	ip, err := waitReady(context.Background(),
		func() (string, bool, error) { return "192.168.64.5", true, nil },
		func(addr string) error { return nil },
		2*time.Second)
	if err != nil || ip != "192.168.64.5" {
		t.Fatalf("got %q, %v", ip, err)
	}
}

func TestWaitReadyNamesIPStageOnTimeout(t *testing.T) {
	_, err := waitReady(context.Background(),
		func() (string, bool, error) { return "", false, nil },
		func(addr string) error { return nil },
		150*time.Millisecond)
	var se *stageError
	if !errors.As(err, &se) || se.Stage != "ip" {
		t.Fatalf("want ip stageError, got %v", err)
	}
}

func TestWaitReadyNamesSSHStageOnTimeout(t *testing.T) {
	_, err := waitReady(context.Background(),
		func() (string, bool, error) { return "192.168.64.5", true, nil },
		func(addr string) error { return errors.New("refused") },
		150*time.Millisecond)
	var se *stageError
	if !errors.As(err, &se) || se.Stage != "ssh" {
		t.Fatalf("want ssh stageError, got %v", err)
	}
	if !strings.Contains(err.Error(), `stage "ssh"`) {
		t.Fatalf("error text: %v", err)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/vm/ -run TestWaitReady -v` — FAIL
- [ ] **Step 3: Implement** `readiness.go`:

```go
package vm

import (
	"context"
	"fmt"
	"time"
)

// DefaultReadyTimeout bounds the whole boot-readiness wait (P6 — colima#629:
// unbounded waits hide the failing stage; 90s then a stage-named error).
const DefaultReadyTimeout = 90 * time.Second

type stageError struct {
	Stage  string
	Detail string
}

func (e *stageError) Error() string {
	return fmt.Sprintf("readiness stage %q timed out: %s", e.Stage, e.Detail)
}

func waitReady(ctx context.Context, lookupIP func() (string, bool, error), dial func(addr string) error, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	tick := func() { time.Sleep(1 * time.Second) }

	var ip string
	for {
		if time.Now().After(deadline) {
			return "", &stageError{Stage: "ip", Detail: "no DHCP lease appeared for machine MAC (check /var/db/dhcpd_leases and console.log)"}
		}
		got, ok, err := lookupIP()
		if err != nil {
			return "", fmt.Errorf("lease lookup: %w", err)
		}
		if ok {
			ip = got
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			tick()
		}
	}

	for {
		if time.Now().After(deadline) {
			return "", &stageError{Stage: "ssh", Detail: fmt.Sprintf("port 22 on %s never accepted (guest booted but sshd/cloud-init not ready — check console.log)", ip)}
		}
		if dial(ip+":22") == nil {
			return ip, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			tick()
		}
	}
}
```

- [ ] **Step 4: Run** — PASS (note: tests take ~300ms due to 1s tick short-circuit; if flaky, make tick injectable — keep signature).
- [ ] **Step 5: Commit** `git add internal/vm/readiness.go internal/vm/readiness_test.go && git commit -m "feat(vm): staged bounded boot readiness with stage-named errors (P6)"`

### Task 10: `internal/api` server + `internal/client` with retry, `cmd/umbrad`

**Files:**
- Create: `internal/api/server.go`, `internal/api/server_test.go`, `internal/client/client.go`, `internal/client/client_test.go`, `cmd/umbrad/main.go`

**Interfaces:**
- Produces (HTTP over unix socket, all JSON):
  - `GET /v1/ping` → `{"ok":true}`
  - `GET /v1/machines` → `[{name,state,ip,cpus,memory_mib,disk_gib,image,autostart}]`
  - `POST /v1/machines` body `{"name","cpus","memory_mib","disk_gib","image","autostart"}` → 201 + machine JSON (creates: registry entry w/ random MAC + HostBuild, image ensure, disk clone, ssh key, seed ISO)
  - `POST /v1/machines/{name}/start` → 200 `{name,state,ip}` (launch + waitReady + SetIP)
  - `POST /v1/machines/{name}/stop` → 200
  - `DELETE /v1/machines/{name}` → 204 (409 if running — spec: no cascade)
  - `GET /v1/machines/{name}` → machine JSON + state + ip
  - Errors: `{"error":"..."}` with 4xx/5xx.
- Server constructor takes seams so tests never touch vz/network:
```go
type Lifecycle interface {
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Info(name string) vm.Info
	List() []vm.Info
}
type Provisioner func(ctx context.Context, m *registry.Machine) error // image+disk+sshkey+seed
func NewServer(reg *registry.Registry, lc Lifecycle, prov Provisioner, ready func(ctx context.Context, m *registry.Machine) (string, error)) *Server
func (s *Server) Handler() http.Handler
```
- Client (P10 — retry only on dial errors: 5 attempts, 200ms→2s backoff):
```go
func New(socketPath string) *Client
func (c *Client) Ping(ctx) error
func (c *Client) CreateMachine(ctx, req CreateRequest) (*MachineView, error)
func (c *Client) StartMachine(ctx, name string) (*vm.Info, error)
func (c *Client) StopMachine(ctx, name string) error
func (c *Client) DeleteMachine(ctx, name string) error
func (c *Client) ListMachines(ctx) ([]MachineView, error)
func (c *Client) GetMachine(ctx, name string) (*MachineView, error)
type MachineView struct { registry.Machine; State vm.State `json:"state"`; IP string `json:"ip,omitempty"` }
type CreateRequest struct { Name string; CPUs uint; MemoryMiB uint64; DiskGiB uint64; Image string; Autostart bool }
```

- [ ] **Step 1: Write failing server test** — `server_test.go` (fake Lifecycle/Provisioner; httptest.NewServer over Handler(); exercises create→409-on-delete-running→stop→delete, invalid name 400, start returns ip):

```go
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

type fakeLC struct{ states map[string]vm.State }

func (f *fakeLC) Start(ctx context.Context, n string) error { f.states[n] = vm.StateRunning; return nil }
func (f *fakeLC) Stop(ctx context.Context, n string) error  { f.states[n] = vm.StateStopped; return nil }
func (f *fakeLC) Info(n string) vm.Info {
	s, ok := f.states[n]
	if !ok {
		s = vm.StateStopped
	}
	return vm.Info{Name: n, State: s}
}
func (f *fakeLC) List() []vm.Info { return nil }

func newTestServer(t *testing.T) (*httptest.Server, *fakeLC) {
	reg := registry.New(t.TempDir())
	lc := &fakeLC{states: map[string]vm.State{}}
	s := NewServer(reg, lc,
		func(ctx context.Context, m *registry.Machine) error { return nil },
		func(ctx context.Context, m *registry.Machine) (string, error) { return "192.168.64.7", nil })
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, lc
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCreateStartStopDeleteFlow(t *testing.T) {
	ts, _ := newTestServer(t)

	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{
		"name": "t1", "cpus": 2, "memory_mib": 2048, "disk_gib": 20})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}

	resp = postJSON(t, ts.URL+"/v1/machines/t1/start", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("start: %d", resp.StatusCode)
	}
	var info vm.Info
	json.NewDecoder(resp.Body).Decode(&info)
	if info.IP != "192.168.64.7" || info.State != vm.StateRunning {
		t.Fatalf("start info: %+v", info)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/machines/t1", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 409 {
		t.Fatalf("delete-while-running: %d, want 409", resp.StatusCode)
	}

	resp = postJSON(t, ts.URL+"/v1/machines/t1/stop", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

func TestCreateRejectsInvalidName(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "Bad Name"})
	if resp.StatusCode != 400 {
		t.Fatalf("got %d, want 400", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/api/ -v` — FAIL
- [ ] **Step 3: Implement** `server.go`:

```go
// Package api exposes umbrad's JSON API over a unix socket.
package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

type Lifecycle interface {
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Info(name string) vm.Info
	List() []vm.Info
}

type Provisioner func(ctx context.Context, m *registry.Machine) error

type Server struct {
	reg   *registry.Registry
	lc    Lifecycle
	prov  Provisioner
	ready func(ctx context.Context, m *registry.Machine) (string, error)
}

func NewServer(reg *registry.Registry, lc Lifecycle, prov Provisioner, ready func(ctx context.Context, m *registry.Machine) (string, error)) *Server {
	return &Server{reg: reg, lc: lc, prov: prov, ready: ready}
}

type MachineView struct {
	registry.Machine
	State vm.State `json:"state"`
	IP    string   `json:"ip,omitempty"`
}

type CreateRequest struct {
	Name      string `json:"name"`
	CPUs      uint   `json:"cpus"`
	MemoryMiB uint64 `json:"memory_mib"`
	DiskGiB   uint64 `json:"disk_gib"`
	Image     string `json:"image"`
	Autostart bool   `json:"autostart"`
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func randomMAC() string {
	b := make([]byte, 6)
	rand.Read(b)
	b[0] = (b[0] | 0x02) &^ 0x01 // locally administered, unicast
	parts := make([]string, 6)
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02x", x)
	}
	return strings.Join(parts, ":")
}

func hostBuild() string {
	out, err := exec.Command("/usr/bin/sw_vers", "-buildVersion").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s *Server) view(m *registry.Machine) MachineView {
	info := s.lc.Info(m.Name)
	return MachineView{Machine: *m, State: info.State, IP: info.IP}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	mux.HandleFunc("GET /v1/machines", func(w http.ResponseWriter, r *http.Request) {
		machines, err := s.reg.List()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		out := make([]MachineView, 0, len(machines))
		for _, m := range machines {
			out = append(out, s.view(m))
		}
		writeJSON(w, 200, out)
	})

	mux.HandleFunc("POST /v1/machines", func(w http.ResponseWriter, r *http.Request) {
		var req CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err)
			return
		}
		if !registry.ValidName(req.Name) {
			writeErr(w, 400, fmt.Errorf("invalid machine name %q", req.Name))
			return
		}
		if _, err := s.reg.Load(req.Name); err == nil {
			writeErr(w, 409, fmt.Errorf("machine %q already exists", req.Name))
			return
		}
		if req.CPUs == 0 {
			req.CPUs = 4
		}
		if req.MemoryMiB == 0 {
			req.MemoryMiB = 8192
		}
		if req.DiskGiB == 0 {
			req.DiskGiB = 60
		}
		if req.Image == "" {
			req.Image = "ubuntu:noble"
		}
		m := &registry.Machine{Name: req.Name, CPUs: req.CPUs, MemoryMiB: req.MemoryMiB,
			DiskGiB: req.DiskGiB, Image: req.Image, MAC: randomMAC(),
			Autostart: req.Autostart, HostBuild: hostBuild(), CreatedAt: time.Now().UTC()}
		if err := s.reg.Save(m); err != nil {
			writeErr(w, 500, err)
			return
		}
		if err := s.prov(r.Context(), m); err != nil {
			_ = s.reg.Delete(m.Name) // don't leave half-provisioned machines
			writeErr(w, 500, fmt.Errorf("provision: %w", err))
			return
		}
		writeJSON(w, 201, s.view(m))
	})

	mux.HandleFunc("GET /v1/machines/{name}", func(w http.ResponseWriter, r *http.Request) {
		m, err := s.reg.Load(r.PathValue("name"))
		if err != nil {
			writeErr(w, 404, err)
			return
		}
		writeJSON(w, 200, s.view(m))
	})

	mux.HandleFunc("POST /v1/machines/{name}/start", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		m, err := s.reg.Load(name)
		if err != nil {
			writeErr(w, 404, err)
			return
		}
		if err := s.lc.Start(r.Context(), name); err != nil {
			writeErr(w, 500, err)
			return
		}
		if _, err := s.ready(r.Context(), m); err != nil {
			writeErr(w, 500, err) // stage-named error from readiness (P6)
			return
		}
		writeJSON(w, 200, s.lc.Info(name))
	})

	mux.HandleFunc("POST /v1/machines/{name}/stop", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if _, err := s.reg.Load(name); err != nil {
			writeErr(w, 404, err)
			return
		}
		if err := s.lc.Stop(r.Context(), name); err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, s.lc.Info(name))
	})

	mux.HandleFunc("DELETE /v1/machines/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if _, err := s.reg.Load(name); err != nil {
			writeErr(w, 404, err)
			return
		}
		if s.lc.Info(name).State == vm.StateRunning {
			writeErr(w, 409, fmt.Errorf("machine %q is running; stop it first", name))
			return
		}
		if err := s.reg.Delete(name); err != nil {
			writeErr(w, 500, err)
			return
		}
		w.WriteHeader(204)
	})

	return mux
}
```
Note: the fake `ready` in tests must also call `lc.Info` consistency — the server returns `s.lc.Info(name)` after ready; in production `ready` wraps `waitReady` + `mgr.SetIP` (wired in cmd/umbrad below), so Info carries the IP. In the test the fake Lifecycle returns no IP — adjust the test's start assertion to read the ip from a fakeLC that stores it: simplest is to have the fake `ready` function ALSO set state on fakeLC — implementer: make the test's `ready` closure call `lc.states["t1"] = vm.StateRunning` and have `fakeLC.Info` return `vm.Info{..., IP: "192.168.64.7"}` when running. Keep the assertion as written.

- [ ] **Step 4: Run** `go test ./internal/api/ -v` — PASS
- [ ] **Step 5: Commit** `git add internal/api && git commit -m "feat(api): unix-socket JSON API for machine CRUD + lifecycle"`

- [ ] **Step 6: Write failing client retry test** — `client_test.go`:

```go
package client

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// Daemon socket appears 400ms after the client's first attempt — retry must
// absorb the race (P10, apple/container#672).
func TestClientRetriesUntilSocketAppears(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "api.sock")
	go func() {
		time.Sleep(400 * time.Millisecond)
		l, err := net.Listen("unix", sock)
		if err != nil {
			t.Error(err)
			return
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"ok":true}`))
		})
		http.Serve(l, mux)
	}()
	c := New(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping after retries: %v", err)
	}
}

func TestClientGivesUpWhenNoDaemon(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "nope.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err == nil {
		t.Fatal("want error when daemon never appears")
	}
}
```

- [ ] **Step 7: Run** `go test ./internal/client/ -v` — FAIL
- [ ] **Step 8: Implement** `client.go`:

```go
// Package client is the CLI/GUI-side client for umbrad's unix-socket API.
// Dial errors are retried with backoff (P10 — first-connection races daemon
// socket registration); HTTP-level errors are never retried.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

type Client struct {
	http *http.Client
}

type MachineView struct {
	registry.Machine
	State vm.State `json:"state"`
	IP    string   `json:"ip,omitempty"`
}

type CreateRequest struct {
	Name      string `json:"name"`
	CPUs      uint   `json:"cpus"`
	MemoryMiB uint64 `json:"memory_mib"`
	DiskGiB   uint64 `json:"disk_gib"`
	Image     string `json:"image"`
	Autostart bool   `json:"autostart"`
}

func New(socketPath string) *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
	}}
}

var backoffs = []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 2 * time.Second}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	var lastErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, "http://umbra"+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			var opErr *net.OpError
			if errors.As(err, &opErr) && attempt < len(backoffs) { // dial error → retry
				lastErr = err
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoffs[attempt]):
				}
				continue
			}
			return fmt.Errorf("umbrad unreachable (is the daemon running? `make run-daemon`): %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			var e struct {
				Error string `json:"error"`
			}
			b, _ := io.ReadAll(resp.Body)
			if json.Unmarshal(b, &e) == nil && e.Error != "" {
				return errors.New(e.Error)
			}
			return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
		}
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	return fmt.Errorf("umbrad unreachable after %d attempts: %w", len(backoffs)+1, lastErr)
}

func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/ping", nil, nil)
}
func (c *Client) CreateMachine(ctx context.Context, req CreateRequest) (*MachineView, error) {
	var mv MachineView
	return &mv, c.do(ctx, http.MethodPost, "/v1/machines", req, &mv)
}
func (c *Client) StartMachine(ctx context.Context, name string) (*vm.Info, error) {
	var info vm.Info
	return &info, c.do(ctx, http.MethodPost, "/v1/machines/"+name+"/start", nil, &info)
}
func (c *Client) StopMachine(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/v1/machines/"+name+"/stop", nil, nil)
}
func (c *Client) DeleteMachine(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/machines/"+name, nil, nil)
}
func (c *Client) ListMachines(ctx context.Context) ([]MachineView, error) {
	var out []MachineView
	return out, c.do(ctx, http.MethodGet, "/v1/machines", nil, &out)
}
func (c *Client) GetMachine(ctx context.Context, name string) (*MachineView, error) {
	var mv MachineView
	return &mv, c.do(ctx, http.MethodGet, "/v1/machines/"+name, nil, &mv)
}
```

- [ ] **Step 9: Run** `go test ./internal/client/ -v` — PASS
- [ ] **Step 10: Implement `cmd/umbrad/main.go`** (wires everything; darwin-only via the vm package's build tags on config_darwin.go — main itself is portable but useless off-mac):

```go
// umbrad is the Umbra daemon: owns all VMs, serves the unix-socket API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ForceAI-KW/umbra/internal/api"
	"github.com/ForceAI-KW/umbra/internal/cloudinit"
	"github.com/ForceAI-KW/umbra/internal/image"
	"github.com/ForceAI-KW/umbra/internal/paths"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/sshkey"
	"github.com/ForceAI-KW/umbra/internal/vm"
	"github.com/ForceAI-KW/umbra/internal/vmnet"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("umbrad exiting", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if err := paths.EnsureTree(); err != nil {
		return err
	}
	reg := registry.New(paths.Machines())
	mgr := vm.NewManager(reg, paths.Machines())

	provision := func(ctx context.Context, m *registry.Machine) error {
		rawBase, err := image.Ensure(ctx, paths.Images(), m.Image)
		if err != nil {
			return err
		}
		mdir := paths.MachineDir(m.Name)
		if err := image.CloneDisk(rawBase, filepath.Join(mdir, "disk.img"), m.DiskGiB); err != nil {
			return err
		}
		pub, _, err := sshkey.Ensure(paths.SSH())
		if err != nil {
			return err
		}
		_, err = cloudinit.BuildSeed(m, mdir, pub)
		return err
	}

	ready := func(ctx context.Context, m *registry.Machine) (string, error) {
		ip, err := vm.WaitReady(ctx,
			func() (string, bool, error) { return vmnet.LookupIPFromFile(m.MAC) },
			func(addr string) error {
				c, err := net.DialTimeout("tcp", addr, 2*time.Second)
				if err == nil {
					c.Close()
				}
				return err
			},
			vm.DefaultReadyTimeout)
		if err != nil {
			return "", err
		}
		mgr.SetIP(m.Name, ip)
		return ip, nil
	}

	srv := api.NewServer(reg, mgr, provision, ready)

	sock := paths.APISocket()
	_ = os.Remove(sock) // stale socket from a previous run
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}

	httpSrv := &http.Server{Handler: srv.Handler()}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(l) }()
	logger.Info("umbrad listening", "socket", sock)

	// autostart-flagged machines (fwb-ci pattern; launchd wiring lands in M4)
	if machines, err := reg.List(); err == nil {
		for _, m := range machines {
			if m.Autostart {
				go func(name string) {
					logger.Info("autostarting", "machine", name)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					if err := mgr.Start(ctx, name); err != nil {
						logger.Error("autostart failed", "machine", name, "err", err)
						return
					}
					if mc, err := reg.Load(name); err == nil {
						if _, err := ready(ctx, mc); err != nil {
							logger.Error("autostart readiness failed", "machine", name, "err", err)
						}
					}
				}(m.Name)
			}
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case s := <-sig:
		logger.Info("shutting down", "signal", s.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	mgr.StopAll(shutdownCtx) // graceful → hard per VM (P8)
	_ = httpSrv.Shutdown(shutdownCtx)
	return nil
}
```
**Required rename:** `waitReady`/`DefaultReadyTimeout` from Task 9 must be exported as `vm.WaitReady`/`vm.DefaultReadyTimeout` — do the rename in Task 9's files when this task wires them (tests update mechanically).

- [ ] **Step 11: Build + full unit suite**: `make build && go test ./... -count=1` — expect binaries in `bin/`, all PASS
- [ ] **Step 12: Commit** `git add internal/client cmd/umbrad internal/vm && git commit -m "feat(daemon): umbrad wiring — provision, readiness, autostart, graceful shutdown"`

### Task 11: `cmd/umbra` CLI (cobra)

**Files:**
- Create: `cmd/umbra/main.go`, `cmd/umbra/root.go`, `cmd/umbra/create.go`, `cmd/umbra/machines.go`, `cmd/umbra/shell.go`, `cmd/umbra/status.go`

**Interfaces:**
- Consumes: `client.New(paths.APISocket())`, all client methods, `sshkey.Ensure` (for shell key path), `paths`.
- Produces commands: `umbra create <name> [--cpus 4 --memory-gib 8 --disk-gib 60 --image ubuntu:noble --autostart]`, `umbra list`, `umbra start|stop|rm <name>`, `umbra shell <name> [-- cmd...]`, `umbra status`.

- [ ] **Step 1: Implement** (CLI is glue over the tested client — no unit tests; E2E covers it in Task 12):

`main.go`:
```go
package main

import "os"

func main() { os.Exit(execute()) }
```

`root.go`:
```go
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/paths"
)

var apiClient *client.Client

var rootCmd = &cobra.Command{
	Use:   "umbra",
	Short: "Umbra — Linux machines and Docker on Apple Silicon, invisibly",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		apiClient = client.New(paths.APISocket())
	},
	SilenceUsage: true,
}

func execute() int {
	rootCmd.AddCommand(createCmd, listCmd, startCmd, stopCmd, rmCmd, shellCmd, statusCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
```

`create.go`:
```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
)

var (
	flagCPUs      uint
	flagMemoryGiB uint64
	flagDiskGiB   uint64
	flagImage     string
	flagAutostart bool
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new Linux machine",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("creating %s (first run downloads the Ubuntu image, ~600MB)...\n", args[0])
		mv, err := apiClient.CreateMachine(cmd.Context(), client.CreateRequest{
			Name: args[0], CPUs: flagCPUs, MemoryMiB: flagMemoryGiB * 1024,
			DiskGiB: flagDiskGiB, Image: flagImage, Autostart: flagAutostart,
		})
		if err != nil {
			return err
		}
		fmt.Printf("created %s (%d cpu, %d GiB mem, %d GiB disk)\n", mv.Name, mv.CPUs, mv.MemoryMiB/1024, mv.DiskGiB)
		return nil
	},
}

func init() {
	createCmd.Flags().UintVar(&flagCPUs, "cpus", 4, "vCPUs")
	createCmd.Flags().Uint64Var(&flagMemoryGiB, "memory-gib", 8, "memory (GiB)")
	createCmd.Flags().Uint64Var(&flagDiskGiB, "disk-gib", 60, "disk size (GiB)")
	createCmd.Flags().StringVar(&flagImage, "image", "ubuntu:noble", "guest image")
	createCmd.Flags().BoolVar(&flagAutostart, "autostart", false, "start with the daemon")
}
```

`machines.go`:
```go
package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List machines",
	RunE: func(cmd *cobra.Command, args []string) error {
		machines, err := apiClient.ListMachines(cmd.Context())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE\tIP\tCPUS\tMEM(GiB)\tDISK(GiB)\tAUTOSTART")
		for _, m := range machines {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%v\n",
				m.Name, m.State, m.IP, m.CPUs, m.MemoryMiB/1024, m.DiskGiB, m.Autostart)
		}
		return w.Flush()
	},
}

var startCmd = &cobra.Command{
	Use: "start <name>", Short: "Start a machine", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := apiClient.StartMachine(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		fmt.Printf("%s running at %s\n", info.Name, info.IP)
		return nil
	},
}

var stopCmd = &cobra.Command{
	Use: "stop <name>", Short: "Stop a machine", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.StopMachine(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Printf("%s stopped\n", args[0])
		return nil
	},
}

var rmCmd = &cobra.Command{
	Use: "rm <name>", Short: "Delete a machine (must be stopped)", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.DeleteMachine(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Printf("%s deleted\n", args[0])
		return nil
	},
}
```

`shell.go`:
```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/paths"
)

var shellCmd = &cobra.Command{
	Use:   "shell <name> [-- command...]",
	Short: "Open a shell (or run a command) in a machine",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mv, err := apiClient.GetMachine(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if mv.IP == "" {
			return fmt.Errorf("machine %q has no IP (state: %s) — start it first", mv.Name, mv.State)
		}
		sshPath, err := exec.LookPath("ssh")
		if err != nil {
			return err
		}
		sshArgs := []string{"ssh",
			"-i", filepath.Join(paths.SSH(), "id_ed25519"),
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "UserKnownHostsFile=" + filepath.Join(paths.SSH(), "known_hosts"),
			"umbra@" + mv.IP,
		}
		if len(args) > 1 {
			sshArgs = append(sshArgs, args[1:]...)
		}
		return syscall.Exec(sshPath, sshArgs, os.Environ())
	},
}
```

`status.go`:
```go
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var statusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Daemon + machines status",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.Ping(cmd.Context()); err != nil {
			if statusJSON {
				json.NewEncoder(os.Stdout).Encode(map[string]any{"daemon": "down", "error": err.Error()})
				return nil
			}
			return fmt.Errorf("daemon: DOWN (%w)", err)
		}
		machines, err := apiClient.ListMachines(cmd.Context())
		if err != nil {
			return err
		}
		if statusJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"daemon": "up", "machines": machines})
		}
		fmt.Println("daemon: up")
		for _, m := range machines {
			fmt.Printf("  %s: %s %s\n", m.Name, m.State, m.IP)
		}
		return nil
	},
}

func init() { statusCmd.Flags().BoolVar(&statusJSON, "json", false, "JSON output (watchdog probe)") }
```

- [ ] **Step 2: Build + vet**: `make build && make lint` — PASS (`go get github.com/spf13/cobra` first)
- [ ] **Step 3: Commit** `git add cmd/umbra go.mod go.sum && git commit -m "feat(cli): umbra create/list/start/stop/rm/shell/status"`

### Task 12: Integration test + E2E smoke (runs on this Mac only)

**Files:**
- Create: `internal/vm/integration_test.go` (`//go:build integration`), `scripts/e2e-smoke.sh`

**Interfaces:**
- Consumes: everything. This is the M1 acceptance gate.

- [ ] **Step 1: E2E script** — `scripts/e2e-smoke.sh`:

```bash
#!/usr/bin/env bash
# Umbra M1 E2E smoke. Run on the Mac (never CI): boots a real VM.
set -euo pipefail
cd "$(dirname "$0")/.."

export UMBRA_ROOT="${UMBRA_ROOT:-$(mktemp -d /tmp/umbra-e2e.XXXXXX)}"
echo "UMBRA_ROOT=$UMBRA_ROOT"
make build

./bin/umbrad &
DAEMON_PID=$!
trap 'kill $DAEMON_PID 2>/dev/null || true; rm -rf "$UMBRA_ROOT"' EXIT

./bin/umbra status            # exercises client retry until socket is up (P10)

./bin/umbra create e2e --cpus 2 --memory-gib 2 --disk-gib 20
./bin/umbra start e2e         # bounded readiness — fails loud with stage name (P6)

# guest is arm64 ubuntu
ARCH=$(./bin/umbra shell e2e -- uname -m)
[ "$ARCH" = "aarch64" ] || { echo "FAIL: arch=$ARCH"; exit 1; }

# virtiofs home mount visible
./bin/umbra shell e2e -- ls /mnt/mac >/dev/null || { echo "FAIL: /mnt/mac not mounted"; exit 1; }

# stop is verified, not fire-and-forget (P8/P9)
./bin/umbra stop e2e
STATE=$(./bin/umbra status --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["machines"][0]["state"])')
[ "$STATE" = "stopped" ] || { echo "FAIL: state=$STATE"; exit 1; }

./bin/umbra rm e2e
kill $DAEMON_PID; wait $DAEMON_PID 2>/dev/null || true
echo "E2E SMOKE: PASS"
```
`chmod +x scripts/e2e-smoke.sh`

- [ ] **Step 2: Integration test** — `internal/vm/integration_test.go`:

```go
//go:build integration

package vm_test

// Boots a real VM via the full daemon stack. Requires: arm64 mac, signed
// umbrad NOT needed here (test binary needs the entitlement instead):
// run via scripts/e2e-smoke.sh normally; this test validates the Go-level
// API without the CLI. Sign the test binary first:
//   go test -tags=integration -c ./internal/vm && codesign --force \
//     --entitlements build/vz.entitlements --sign - vm.test && ./vm.test
// The Makefile target `test-integration` automates exactly that.

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/ForceAI-KW/umbra/internal/cloudinit"
	"github.com/ForceAI-KW/umbra/internal/image"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/sshkey"
	"github.com/ForceAI-KW/umbra/internal/vm"
	"github.com/ForceAI-KW/umbra/internal/vmnet"
)

func TestBootShellStopCycle(t *testing.T) {
	root := t.TempDir()
	machinesDir := filepath.Join(root, "machines")
	reg := registry.New(machinesDir)
	m := &registry.Machine{Name: "itest", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20,
		Image: "ubuntu:noble", MAC: "a6:5e:00:aa:bb:01", CreatedAt: time.Now()}
	if err := reg.Save(m); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	rawBase, err := image.Ensure(ctx, filepath.Join(root, "images"), m.Image)
	if err != nil {
		t.Fatal(err)
	}
	mdir := filepath.Join(machinesDir, m.Name)
	if err := image.CloneDisk(rawBase, filepath.Join(mdir, "disk.img"), m.DiskGiB); err != nil {
		t.Fatal(err)
	}
	pub, _, err := sshkey.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloudinit.BuildSeed(m, mdir, pub); err != nil {
		t.Fatal(err)
	}

	mgr := vm.NewManager(reg, machinesDir)
	if err := mgr.Start(ctx, m.Name); err != nil {
		t.Fatal(err)
	}
	ip, err := vm.WaitReady(ctx,
		func() (string, bool, error) { return vmnet.LookupIPFromFile(m.MAC) },
		func(addr string) error {
			c, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err == nil {
				c.Close()
			}
			return err
		}, vm.DefaultReadyTimeout)
	if err != nil {
		t.Fatalf("readiness: %v", err)
	}
	t.Logf("machine up at %s", ip)

	if err := mgr.Stop(ctx, m.Name); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := mgr.Info(m.Name).State; got != vm.StateStopped {
		t.Fatalf("state after stop: %s", got)
	}
}
```

Update Makefile `test-integration` target to sign the test binary:
```make
test-integration: build
	go test -tags=integration -c -o bin/vm.test ./internal/vm
	codesign --force --entitlements build/vz.entitlements --sign - bin/vm.test
	./bin/vm.test -test.v -test.timeout 15m
```

- [ ] **Step 3: Run integration on the Mac**: `make test-integration` — expect PASS (first run downloads ~600MB image)
- [ ] **Step 4: Run E2E**: `./scripts/e2e-smoke.sh` — expect final line `E2E SMOKE: PASS`
- [ ] **Step 5: Commit** `git add internal/vm/integration_test.go scripts/e2e-smoke.sh Makefile && git commit -m "test: integration boot cycle + E2E smoke script"`

### Task 13: Docs parity + close M1

**Files:**
- Modify: `README.md` (status table: M1 ✅, usage section with real commands), `docs/superpowers/specs/2026-07-11-umbra-design.md` (mark M1 done, note any deviations)

- [ ] **Step 1:** Update README usage:

````markdown
## Usage (M1)

```sh
make build && make run-daemon     # terminal 1 (launchd autostart lands in M4)
bin/umbra create dev --cpus 4 --memory-gib 8
bin/umbra start dev
bin/umbra shell dev               # you're in Ubuntu; your Mac home is at /mnt/mac
bin/umbra status --json           # watchdog probe surface
```
````

- [ ] **Step 2:** Blast-radius sweep (Rule D-GATE) — run and record result in the commit/PR body:

```bash
cd ~/Desktop/projects/umbra
grep -rn --exclude-dir=.git -iE 'forgebox|fbox|whisky-lunix|wlx' . && echo "STALE NAMES FOUND — fix" || echo "clean"
grep -rn --exclude-dir=.git -E 'ubuntu-24\.04|noble' --include='*.go' . | grep -v _test # image ref consistency
```

- [ ] **Step 3:** Full suite: `make lint && make test && make build` — all green
- [ ] **Step 4:** Commit + push: `git add README.md docs/ && git commit -m "docs: M1 usage + status" && git push`

---

## Self-Review (done at plan-write time)

1. **Spec coverage (M1 scope):** daemon ✅ (T10), machines via cloud-init ✅ (T5/T6), persistent raw disk ✅ (T6), VirtioFS ✅ (T8 config + T5 mount), shell over SSH ✅ (T4/T11), staged readiness P6 ✅ (T9), guarded lifecycle P1 ✅ (T8), stop escalation P8/P9 ✅ (T8), client retry P10 ✅ (T10), entitlements runbook P12 ✅ (T1), autostart flag ✅ (T10; launchd itself is M4 per spec), CI standard ✅ (T1). Out of M1 scope by design: gvisor networking/DNS (M2), docker (M3), launchd+cutover (M4), GUI (M5), Rosetta (M6).
2. **Placeholder scan:** none — every code step has complete code; the two "note for implementer" blocks are API-verification instructions against the pinned vz version, not TBDs.
3. **Type consistency:** `vm.Info{Name,State,IP}` used by api/client/CLI ✅; `registry.Machine` embedded in both `api.MachineView` and `client.MachineView` (duplicated intentionally — client must not import api) ✅; `WaitReady` export rename called out explicitly in T10 ✅; `stopWithEscalation(ctx, handle, graceful, hard)` signature matches tests ✅.
```
