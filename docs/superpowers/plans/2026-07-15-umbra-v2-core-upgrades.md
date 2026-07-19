# Umbra v2 Core Upgrades Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the 9 deterministic Umbra feature upgrades motivated by the 2026-07-15 CI incidents and the coming MacBook→Mac Studio migration: `exec`, `set`, snapshot/restore, export/import, ci-runner role hardening (swap + reboot-safe docker), `runner add/list/harden`, `prune`, `stats`.

**Architecture:** Every state-mutating operation goes through the daemon REST API (`internal/api/server.go` mux → `internal/registry` / `internal/vm`), mirroring existing endpoints; the CLI (`cmd/umbra/*.go`, cobra) only talks to `apiClient` (`internal/client`). Read-only guest introspection (`stats`, `prune`, `runner list`) reuses the `shell` command's ssh idiom directly (loopback `SSHPort` + `paths.SSH()` key). Snapshots use APFS `clonefile(2)` for instant, space-shared copies.

**Tech Stack:** Go 1.22+ (`net/http` PathValue mux, cobra, `golang.org/x/sys/unix`), cloud-init YAML in `internal/cloudinit/seed.go`, systemd units in guests, `gh` CLI on host for runner registration tokens.

## Global Constraints

- **Local gate is THE gate:** umbra is a PUBLIC repo — org self-hosted runners cannot serve it and GitHub-hosted runners are billing-blocked, so its Actions CI cannot run. Before every push: `go test ./...` && `go vet ./...` && `make build` must pass locally.
- Repo: `~/Desktop/projects/_worktrees/umbra__feat-v2-upgrades` (branch `feat/v2-upgrades`). NEVER touch `~/Desktop/projects/umbra` (root checkout, stays on main).
- Module path `github.com/ForceAI-KW/umbra`. Match existing file style: small files, one responsibility, package comment on new packages.
- All new endpoints follow the existing handler idiom exactly: `writeErr(w, 404, err)` on `reg.Load` failure first, state checks via `s.lc.Info(name)` returning 409 for running/zombie conflicts, `writeJSON` for success.
- Stopped-only file operations (snapshot, restore, import, disk-grow) MUST refuse `vm.StateRunning` AND `info.Zombie` with 409, copying the DELETE handler's exact guard.
- Machine names validate with `registry.ValidName`; `registry.IsReserved(name)` guards the docker VM from destructive ops.
- Commit style: conventional, all-lowercase subject (`feat(cli): …`), one commit per task.
- vz/Virtualization framework code is NOT touched by any task in this plan (no entitlement re-sign risk).

## Deferred to a follow-up plan (spike-gated — NOT silently dropped)

Remote daemon mode (TCP+auth), LaunchDaemon (system-context vz needs an entitlement/session spike), ssh-forward backpressure root-cause, and menu-bar UI surfacing of snapshots/stats. Each needs investigation before TDD tasks can be written honestly.

---

### Task 1: `umbra exec` alias

**Files:**
- Modify: `cmd/umbra/shell.go` (append new command at end)
- Modify: `cmd/umbra/root.go:26` (register)

**Interfaces:**
- Consumes: existing `shellCmd.RunE` closure logic (ssh exec into guest).
- Produces: `execCmd` cobra command — `umbra exec <name> <command...>`.

- [x] **Step 1: Extract the shell RunE body into a named function**

In `cmd/umbra/shell.go`, change `shellCmd`'s `RunE:` to `RunE: runShell,` and declare the existing closure body as:

```go
func runShell(cmd *cobra.Command, args []string) error {
	// (existing body of the former closure, unchanged)
}
```

- [x] **Step 2: Add execCmd**

Append to `cmd/umbra/shell.go`:

```go
// execCmd is sugar for `umbra shell <name> -- <command...>` — every
// automation script guesses `umbra exec` exists (docker/kubectl muscle
// memory), so make it exist.
var execCmd = &cobra.Command{
	Use:   "exec <name> <command...>",
	Short: "Run a command in a machine (alias for shell <name> -- ...)",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runShell,
}
```

`runShell` already treats `args[1:]` as the remote command (the `--` separator is stripped by cobra before RunE), so no body change is needed.

- [x] **Step 3: Register it**

In `cmd/umbra/root.go:26` add `execCmd` after `shellCmd` in the `AddCommand` list.

- [x] **Step 4: Build + smoke**

Run: `make build && ./bin/umbra exec --help`
Expected: usage line `exec <name> <command...>` prints; `go test ./...` passes.

- [x] **Step 5: Commit**

```bash
git add cmd/umbra/shell.go cmd/umbra/root.go
git commit -m "feat(cli): umbra exec alias for shell -- command"
```

---

### Task 2: `umbra set` — mutable machine config

**Files:**
- Modify: `internal/registry/registry.go` (no change needed — `Save` already overwrites; task adds nothing here, listed for orientation)
- Modify: `internal/api/server.go` (new `PATCH /v1/machines/{name}` handler + `UpdateRequest` type)
- Modify: `internal/client/client.go` (add `UpdateMachine`)
- Create: `cmd/umbra/set.go`
- Modify: `cmd/umbra/root.go:26` (register `setCmd`)
- Test: `internal/api/server_test.go` (append tests)

**Interfaces:**
- Consumes: `registry.Machine`, `s.lc.Info(name)` state, `writeErr/writeJSON`.
- Produces: `type UpdateRequest struct { CPUs *uint "json:\"cpus\""; MemoryMiB *uint64 "json:\"memory_mib\""; DiskGiB *uint64 "json:\"disk_gib\""; Autostart *bool "json:\"autostart\"" }` (pointer fields = "not provided"); client method `UpdateMachine(ctx, name string, req UpdateRequest) (*MachineView, error)`.

- [x] **Step 1: Write the failing tests** (append to `internal/api/server_test.go`, following the file's existing `httptest` + fake-Lifecycle pattern; reuse its existing test server constructor helper):

```go
func TestPatchMachineAutostartWhileRunning(t *testing.T) {
	// autostart is mutable even while running
	srv, reg := newTestServer(t) // match the helper name used in this file
	reg.Save(&registry.Machine{Name: "ci", CPUs: 2, MemoryMiB: 1024, DiskGiB: 10})
	setState(t, "ci", vm.StateRunning) // match the file's state-stubbing helper
	body := `{"autostart":true}`
	rec := doReq(t, srv, "PATCH", "/v1/machines/ci", body)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	m, _ := reg.Load("ci")
	if !m.Autostart {
		t.Fatal("autostart not persisted")
	}
}

func TestPatchMachineResizeRefusedWhileRunning(t *testing.T) {
	srv, reg := newTestServer(t)
	reg.Save(&registry.Machine{Name: "ci", CPUs: 2, MemoryMiB: 1024, DiskGiB: 10})
	setState(t, "ci", vm.StateRunning)
	rec := doReq(t, srv, "PATCH", "/v1/machines/ci", `{"memory_mib":4096}`)
	if rec.Code != 409 {
		t.Fatalf("want 409, got %d", rec.Code)
	}
}

func TestPatchMachineDiskShrinkRefused(t *testing.T) {
	srv, reg := newTestServer(t)
	reg.Save(&registry.Machine{Name: "ci", CPUs: 2, MemoryMiB: 1024, DiskGiB: 60})
	rec := doReq(t, srv, "PATCH", "/v1/machines/ci", `{"disk_gib":30}`)
	if rec.Code != 400 {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}
```

NOTE for implementer: `newTestServer` / `setState` / `doReq` are placeholders for whatever helpers `server_test.go` ALREADY defines (read the file first and reuse its exact helpers — do not invent parallel ones).

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/ -run TestPatchMachine -v`
Expected: FAIL (404 from mux — no PATCH route).

- [x] **Step 3: Implement the handler** (in `Handler()`, after the DELETE handler):

```go
mux.HandleFunc("PATCH /v1/machines/{name}", func(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, err := s.reg.Load(name)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	info := s.lc.Info(name)
	resize := req.CPUs != nil || req.MemoryMiB != nil || req.DiskGiB != nil
	if resize && (info.State == vm.StateRunning || info.Zombie) {
		writeErr(w, 409, fmt.Errorf("machine %q must be stopped to change cpu/memory/disk", name))
		return
	}
	if req.DiskGiB != nil && *req.DiskGiB < m.DiskGiB {
		writeErr(w, 400, fmt.Errorf("disk can only grow (current %d GiB)", m.DiskGiB))
		return
	}
	if req.CPUs != nil {
		m.CPUs = *req.CPUs
	}
	if req.MemoryMiB != nil {
		m.MemoryMiB = *req.MemoryMiB
	}
	if req.DiskGiB != nil && *req.DiskGiB > m.DiskGiB {
		img := filepath.Join(paths.MachineDir(name), "disk.img")
		if err := os.Truncate(img, int64(*req.DiskGiB)<<30); err != nil {
			writeErr(w, 500, err)
			return
		}
		m.DiskGiB = *req.DiskGiB
	}
	if req.Autostart != nil {
		m.Autostart = *req.Autostart
	}
	if err := s.reg.Save(m); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, s.view(m))
})
```

Add near `CreateRequest`:

```go
// UpdateRequest mutates machine config. Pointer fields distinguish
// "not provided" from zero values. cpu/memory/disk require the machine
// stopped; disk only grows (the guest filesystem must then be grown
// inside the guest: sudo growpart /dev/vda 1 && sudo resize2fs /dev/vda1).
type UpdateRequest struct {
	CPUs      *uint   `json:"cpus"`
	MemoryMiB *uint64 `json:"memory_mib"`
	DiskGiB   *uint64 `json:"disk_gib"`
	Autostart *bool   `json:"autostart"`
}
```

(`paths` and `os` may need importing in server.go — check existing imports.)

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -run TestPatchMachine -v` → PASS; then `go test ./...` → PASS.

- [x] **Step 5: Client method** (in `internal/client/client.go`, after `GetMachine`):

```go
func (c *Client) UpdateMachine(ctx context.Context, name string, req api.UpdateRequest) (*MachineView, error) {
	var mv MachineView
	err := c.do(ctx, "PATCH", "/v1/machines/"+name, req, &mv)
	return &mv, err
}
```

(Match how this file references request types — if it re-declares local request structs instead of importing `api`, follow that pattern.)

- [x] **Step 6: CLI command** — Create `cmd/umbra/set.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/api"
)

var (
	setCPUs      uint
	setMemGiB    uint64
	setDiskGiB   uint64
	setAutostart string // "", "true", "false" — tri-state
)

var setCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Change a machine's cpus/memory/disk/autostart (resize requires it stopped; disk only grows)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var req api.UpdateRequest
		if cmd.Flags().Changed("cpus") {
			req.CPUs = &setCPUs
		}
		if cmd.Flags().Changed("memory-gib") {
			mib := setMemGiB * 1024
			req.MemoryMiB = &mib
		}
		if cmd.Flags().Changed("disk-gib") {
			req.DiskGiB = &setDiskGiB
		}
		if cmd.Flags().Changed("autostart") {
			v := setAutostart == "true"
			if setAutostart != "true" && setAutostart != "false" {
				return fmt.Errorf("--autostart must be true or false")
			}
			req.Autostart = &v
		}
		mv, err := apiClient.UpdateMachine(cmd.Context(), args[0], req)
		if err != nil {
			return err
		}
		fmt.Printf("%s: cpus=%d mem=%dGiB disk=%dGiB autostart=%v\n",
			mv.Name, mv.CPUs, mv.MemoryMiB/1024, mv.DiskGiB, mv.Autostart)
		if req.DiskGiB != nil {
			fmt.Println("disk grown on the host — inside the guest run: sudo growpart /dev/vda 1 && sudo resize2fs /dev/vda1")
		}
		return nil
	},
}

func init() {
	setCmd.Flags().UintVar(&setCPUs, "cpus", 0, "vCPU count")
	setCmd.Flags().Uint64Var(&setMemGiB, "memory-gib", 0, "memory in GiB")
	setCmd.Flags().Uint64Var(&setDiskGiB, "disk-gib", 0, "disk size in GiB (grow only)")
	setCmd.Flags().StringVar(&setAutostart, "autostart", "", "true|false — start with the daemon")
}
```

Register `setCmd` in `root.go`.

- [x] **Step 7: Build + full test + commit**

```bash
go test ./... && make build
git add internal/api/server.go internal/api/server_test.go internal/client/client.go cmd/umbra/set.go cmd/umbra/root.go
git commit -m "feat: umbra set - mutate cpus/memory/disk/autostart post-create"
```

---

### Task 3: Snapshots — `umbra snapshot` / `snapshots` / `restore`

**Files:**
- Create: `internal/snapshot/snapshot.go`
- Create: `internal/snapshot/snapshot_test.go`
- Modify: `internal/api/server.go` (3 endpoints)
- Modify: `internal/client/client.go` (3 methods)
- Create: `cmd/umbra/snapshot.go`
- Modify: `cmd/umbra/root.go`
- Modify: `internal/paths/paths.go` (add `Snapshots(name)`)

**Interfaces:**
- Produces: package `snapshot` with
  `Take(machineDir, snapDir, snapName string) error` (clonefile disk.img + copy config.json),
  `List(snapDir string) ([]Info, error)` where `type Info struct { Name string "json:\"name\""; CreatedAt time.Time "json:\"created_at\""; SizeBytes int64 "json:\"size_bytes\"" }`,
  `Restore(machineDir, snapDir, snapName string) error`.
- API: `POST /v1/machines/{name}/snapshots {"name":"pre-upgrade"}`, `GET /v1/machines/{name}/snapshots`, `POST /v1/machines/{name}/restore {"name":"pre-upgrade"}` — all snapshot ops 409 unless stopped (same guard as DELETE).
- Client: `TakeSnapshot(ctx, machine, snap string) error`, `ListSnapshots(ctx, machine string) ([]snapshot.Info, error)`, `RestoreSnapshot(ctx, machine, snap string) error`.
- Paths: `paths.Snapshots(name)` = `filepath.Join(MachineDir(name), "snapshots")`.

- [x] **Step 1: Failing tests for the snapshot package** — `internal/snapshot/snapshot_test.go`:

```go
package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTakeListRestoreRoundTrip(t *testing.T) {
	mdir := t.TempDir()
	sdir := filepath.Join(mdir, "snapshots")
	writeFile(t, filepath.Join(mdir, "disk.img"), "DISK-V1")
	writeFile(t, filepath.Join(mdir, "config.json"), `{"name":"x"}`)

	if err := Take(mdir, sdir, "s1"); err != nil {
		t.Fatal(err)
	}
	infos, err := List(sdir)
	if err != nil || len(infos) != 1 || infos[0].Name != "s1" {
		t.Fatalf("list=%v err=%v", infos, err)
	}

	writeFile(t, filepath.Join(mdir, "disk.img"), "DISK-V2-CORRUPT")
	if err := Restore(mdir, sdir, "s1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(mdir, "disk.img"))
	if string(b) != "DISK-V1" {
		t.Fatalf("restore did not bring back v1, got %q", b)
	}
}

func TestTakeDuplicateNameFails(t *testing.T) {
	mdir := t.TempDir()
	sdir := filepath.Join(mdir, "snapshots")
	writeFile(t, filepath.Join(mdir, "disk.img"), "D")
	writeFile(t, filepath.Join(mdir, "config.json"), "{}")
	if err := Take(mdir, sdir, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := Take(mdir, sdir, "s1"); err == nil {
		t.Fatal("duplicate snapshot name must fail")
	}
}

func TestRestoreMissingSnapshotFails(t *testing.T) {
	mdir := t.TempDir()
	if err := Restore(mdir, filepath.Join(mdir, "snapshots"), "nope"); err == nil {
		t.Fatal("want error for missing snapshot")
	}
}
```

- [x] **Step 2: Run to verify failure** — `go test ./internal/snapshot/ -v` → FAIL (package missing).

- [x] **Step 3: Implement `internal/snapshot/snapshot.go`:**

```go
// Package snapshot takes and restores point-in-time copies of a machine's
// disk image. On APFS the copy is clonefile(2) — instant and space-shared —
// with a plain copy fallback for non-APFS filesystems (or cross-volume).
// Snapshot layout: <machineDir>/snapshots/<snapName>/{disk.img,config.json}.
// Callers (the daemon) must ensure the machine is STOPPED first.
package snapshot

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type Info struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	SizeBytes int64     `json:"size_bytes"`
}

// cloneOrCopy clonefiles src to dst, falling back to a streamed copy when
// the filesystem refuses (ENOTSUP: non-APFS; EXDEV: cross-volume).
func cloneOrCopy(src, dst string) error {
	err := unix.Clonefile(src, dst, 0)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EXDEV) {
		return fmt.Errorf("clonefile %s: %w", src, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func Take(machineDir, snapDir, snapName string) error {
	dir := filepath.Join(snapDir, snapName)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("snapshot %q already exists", snapName)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := cloneOrCopy(filepath.Join(machineDir, "disk.img"), filepath.Join(dir, "disk.img")); err != nil {
		os.RemoveAll(dir) // don't leave a half-snapshot behind
		return err
	}
	if err := cloneOrCopy(filepath.Join(machineDir, "config.json"), filepath.Join(dir, "config.json")); err != nil {
		os.RemoveAll(dir)
		return err
	}
	return nil
}

func List(snapDir string) ([]Info, error) {
	entries, err := os.ReadDir(snapDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st, err := os.Stat(filepath.Join(snapDir, e.Name(), "disk.img"))
		if err != nil {
			continue // half-snapshot; Take cleans these up, ignore
		}
		out = append(out, Info{Name: e.Name(), CreatedAt: st.ModTime(), SizeBytes: st.Size()})
	}
	return out, nil
}

// Restore replaces machineDir/disk.img with the snapshot's copy. The
// current image is cloned aside to disk.img.pre-restore first so a failed
// restore never destroys the only copy; it is removed on success.
func Restore(machineDir, snapDir, snapName string) error {
	src := filepath.Join(snapDir, snapName, "disk.img")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("snapshot %q not found", snapName)
	}
	live := filepath.Join(machineDir, "disk.img")
	backup := live + ".pre-restore"
	os.Remove(backup)
	if err := cloneOrCopy(live, backup); err != nil {
		return err
	}
	if err := os.Remove(live); err != nil {
		return err
	}
	if err := cloneOrCopy(src, live); err != nil {
		// bring the original back — never leave the machine diskless
		os.Rename(backup, live)
		return err
	}
	os.Remove(backup)
	return nil
}
```

- [x] **Step 4: Run** — `go test ./internal/snapshot/ -v` → PASS.

- [x] **Step 5: Paths helper** — add to `internal/paths/paths.go`:

```go
func Snapshots(name string) string { return filepath.Join(MachineDir(name), "snapshots") }
```

- [x] **Step 6: API endpoints** — in `Handler()`:

```go
mux.HandleFunc("POST /v1/machines/{name}/snapshots", func(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.reg.Load(name); err != nil {
		writeErr(w, 404, err)
		return
	}
	if info := s.lc.Info(name); info.State == vm.StateRunning || info.Zombie {
		writeErr(w, 409, fmt.Errorf("machine %q must be stopped to snapshot", name))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, 400, fmt.Errorf("snapshot name required"))
		return
	}
	if !registry.ValidName(req.Name) {
		writeErr(w, 400, fmt.Errorf("invalid snapshot name"))
		return
	}
	if err := snapshot.Take(paths.MachineDir(name), paths.Snapshots(name), req.Name); err != nil {
		writeErr(w, 500, err)
		return
	}
	w.WriteHeader(201)
})

mux.HandleFunc("GET /v1/machines/{name}/snapshots", func(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.reg.Load(name); err != nil {
		writeErr(w, 404, err)
		return
	}
	infos, err := snapshot.List(paths.Snapshots(name))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, infos)
})

mux.HandleFunc("POST /v1/machines/{name}/restore", func(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.reg.Load(name); err != nil {
		writeErr(w, 404, err)
		return
	}
	if info := s.lc.Info(name); info.State == vm.StateRunning || info.Zombie {
		writeErr(w, 409, fmt.Errorf("machine %q must be stopped to restore", name))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, 400, fmt.Errorf("snapshot name required"))
		return
	}
	if err := snapshot.Restore(paths.MachineDir(name), paths.Snapshots(name), req.Name); err != nil {
		writeErr(w, 500, err)
		return
	}
	w.WriteHeader(204)
})
```

Append API tests to `server_test.go` (same helper reuse rule as Task 2): snapshot-while-running → 409; snapshot then list → 1 entry; restore missing → 500. Use the test registry's temp dir with a dummy `disk.img` (write one after `reg.Save` — `paths` honors the test env override, see `paths_test.go`).

- [x] **Step 7: Client methods**

```go
func (c *Client) TakeSnapshot(ctx context.Context, machine, snap string) error {
	return c.do(ctx, "POST", "/v1/machines/"+machine+"/snapshots", map[string]string{"name": snap}, nil)
}
func (c *Client) ListSnapshots(ctx context.Context, machine string) ([]snapshot.Info, error) {
	var out []snapshot.Info
	err := c.do(ctx, "GET", "/v1/machines/"+machine+"/snapshots", nil, &out)
	return out, err
}
func (c *Client) RestoreSnapshot(ctx context.Context, machine, snap string) error {
	return c.do(ctx, "POST", "/v1/machines/"+machine+"/restore", map[string]string{"name": snap}, nil)
}
```

- [x] **Step 8: CLI** — Create `cmd/umbra/snapshot.go`:

```go
package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot <machine> <snapshot-name>",
	Short: "Take an instant point-in-time snapshot of a stopped machine (APFS clone)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.TakeSnapshot(cmd.Context(), args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("snapshot %q taken for %s\n", args[1], args[0])
		return nil
	},
}

var snapshotsCmd = &cobra.Command{
	Use:   "snapshots <machine>",
	Short: "List a machine's snapshots",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		infos, err := apiClient.ListSnapshots(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if len(infos) == 0 {
			fmt.Println("no snapshots")
			return nil
		}
		for _, i := range infos {
			fmt.Printf("%-24s %s  %.1f GiB\n", i.Name, i.CreatedAt.Format(time.DateTime), float64(i.SizeBytes)/(1<<30))
		}
		return nil
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore <machine> <snapshot-name>",
	Short: "Restore a stopped machine's disk from a snapshot",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.RestoreSnapshot(cmd.Context(), args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("%s restored from %q\n", args[0], args[1])
		return nil
	},
}
```

Register all three in `root.go`.

- [x] **Step 9: Full gate + live verify + commit**

```bash
go test ./... && make build
# live: snapshot the stopped spare guest
./bin/umbra snapshot fwb-ci2 baseline && ./bin/umbra snapshots fwb-ci2
git add internal/snapshot internal/paths/paths.go internal/api/server.go internal/api/server_test.go internal/client/client.go cmd/umbra/snapshot.go cmd/umbra/root.go
git commit -m "feat: instant apfs snapshots - snapshot/snapshots/restore"
```

---

### Task 4: `umbra export` / `umbra import`

**Files:**
- Create: `internal/export/export.go`
- Create: `internal/export/export_test.go`
- Modify: `internal/api/server.go` (import endpoint)
- Modify: `internal/client/client.go` (`ImportMachine`)
- Create: `cmd/umbra/export.go`
- Modify: `cmd/umbra/root.go`

**Interfaces:**
- Produces: package `export` with
  `Write(machineDir, outFile string) error` (tar.gz of `config.json` + `disk.img`, paths stored flat),
  `Read(inFile, destDir string) (*registry.Machine, error)` (extracts into destDir, returns the parsed embedded config).
- API: `POST /v1/machines/import {"name":"fwb-ci5","staging_dir":"/path/from/cli"}` — daemon takes ownership of the extracted dir: validates name free, allocates fresh MAC (`randomMAC()`), clears IP, stamps `HostBuild`/`CreatedAt`, moves dir into `paths.Machines()`, saves config. 409 if name exists.
- Client: `ImportMachine(ctx, name, stagingDir string) (*MachineView, error)`.
- CLI: `umbra export <machine> [-o file.tar.gz]` (stopped-only, checked via `GetMachine`), `umbra import <file.tar.gz> [--name newname]` (extracts to a temp dir under `paths.Run()`, then calls the API).

- [x] **Step 1: Failing tests** — `internal/export/export_test.go`:

```go
package export

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "disk.img"), []byte("DISKDATA"), 0o600)
	os.WriteFile(filepath.Join(src, "config.json"),
		[]byte(`{"name":"orig","cpus":3,"memory_mib":3072,"disk_gib":60,"image":"ubuntu:noble","mac":"aa:bb:cc:dd:ee:ff","autostart":true}`), 0o600)

	tarball := filepath.Join(t.TempDir(), "m.tar.gz")
	if err := Write(src, tarball); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	m, err := Read(tarball, dest)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "orig" || m.CPUs != 3 || !m.Autostart {
		t.Fatalf("config mangled: %+v", m)
	}
	b, err := os.ReadFile(filepath.Join(dest, "disk.img"))
	if err != nil || string(b) != "DISKDATA" {
		t.Fatalf("disk mangled: %q %v", b, err)
	}
}

func TestReadRejectsTraversal(t *testing.T) {
	// a tarball containing ../evil must not escape destDir
	// (build it by hand with archive/tar in the test)
	evil := buildEvilTar(t) // helper writing an entry named "../evil"
	if _, err := Read(evil, t.TempDir()); err == nil {
		t.Fatal("path traversal must be rejected")
	}
}
```

(Include the `buildEvilTar` helper in the test file — `archive/tar` writer with one `../evil` header entry, gzip-wrapped.)

- [x] **Step 2: Verify fail** — `go test ./internal/export/ -v` → FAIL.

- [x] **Step 3: Implement** `internal/export/export.go` — `archive/tar` + `compress/gzip`; `Write` streams the two files with flat names; `Read` extracts ONLY `config.json`/`disk.img` entries (reject any other name → covers traversal), parses config with `encoding/json` into `registry.Machine`. ~90 lines; no external deps.

- [x] **Step 4: PASS** — `go test ./internal/export/ -v`.

- [x] **Step 5: API import endpoint** (in `Handler()`):

```go
mux.HandleFunc("POST /v1/machines/import", func(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		StagingDir string `json:"staging_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if !registry.ValidName(req.Name) || registry.IsReserved(req.Name) {
		writeErr(w, 400, fmt.Errorf("invalid machine name %q", req.Name))
		return
	}
	if _, err := s.reg.Load(req.Name); err == nil {
		writeErr(w, 409, fmt.Errorf("machine %q already exists", req.Name))
		return
	}
	m, err := export.Read(filepath.Join(req.StagingDir, "machine.tar.gz"), req.StagingDir)
	_ = m // config re-read below after move; see note
	// NOTE to implementer: the CLI already extracted the tarball into
	// StagingDir (config.json + disk.img). Simplify: read config.json from
	// StagingDir directly instead of re-extracting; then:
	//   m.Name = req.Name; m.MAC = randomMAC(); m.IP = ""
	//   m.HostBuild = hostBuild(); m.CreatedAt = time.Now().UTC()
	//   os.Rename(StagingDir -> paths.MachineDir(req.Name)) then s.reg.Save(m)
	// os.Rename is same-volume (both under paths.Root()) so it's atomic.
	writeJSON(w, 201, s.view(m))
})
```

(The final code follows the NOTE — the sketch above locks the request/response contract and validation order; write the clean version.)
Append server_test: import with taken name → 409; happy path creates dir + fresh MAC ≠ tarball MAC.

- [x] **Step 6: CLI** — `cmd/umbra/export.go` with both commands: `export` calls `GetMachine` (must be `stopped`, else error), then `export.Write(paths.MachineDir(name), out)` — export is read-only so CLI-side direct file access matches how `shell` reads `paths.SSH()`. `import` extracts to `os.MkdirTemp(paths.Run(), "import-*")` then `apiClient.ImportMachine`. Register both.

- [x] **Step 7: Gate + commit**

```bash
go test ./... && make build
git add internal/export internal/api internal/client cmd/umbra/export.go cmd/umbra/root.go
git commit -m "feat: export/import machines as tarballs for host migration"
```

---

### Task 5: ci-runner role hardening — default swap + reboot-safe docker

**Files:**
- Modify: `internal/cloudinit/seed.go` (ciRunner write_files + runcmd)
- Test: `internal/cloudinit/seed_test.go` (extend existing ci-runner rendering tests)

**Interfaces:**
- Consumes: `ciRunnerRuncmdLines()`, `runcmdSection`, the write_files assembly (read `seed.go` first — mirror how docker's write_files entry is added).
- Produces: rendered user-data for role `ci-runner` gains (a) a 4 GiB swapfile provisioned idempotently, (b) a systemd `ensure-docker.service` oneshot that (re)installs docker if missing on every boot — closing the "reboot mid-cloud-init leaves docker missing forever" gap.

- [x] **Step 1: Failing tests** — extend `seed_test.go`:

```go
func TestCIRunnerUserDataProvisionsSwap(t *testing.T) {
	ud := renderForTest(t, registry.RoleCIRunner) // reuse the file's existing render helper
	for _, want := range []string{"fallocate -l 4G /swapfile", "mkswap /swapfile", "swapon /swapfile", "/swapfile none swap sw 0 0"} {
		if !strings.Contains(ud, want) {
			t.Fatalf("user-data missing %q", want)
		}
	}
}

func TestCIRunnerUserDataHasEnsureDockerUnit(t *testing.T) {
	ud := renderForTest(t, registry.RoleCIRunner)
	for _, want := range []string{"ensure-docker.service", "ConditionPathExists=!/usr/bin/docker", "systemctl enable ensure-docker.service"} {
		if !strings.Contains(ud, want) {
			t.Fatalf("user-data missing %q", want)
		}
	}
}
```

- [x] **Step 2: FAIL** — `go test ./internal/cloudinit/ -run TestCIRunner -v`.

- [x] **Step 3: Implement.** In the ci-runner write_files block add the unit (following the existing write_files idiom — "write_files runs before runcmd" comment at seed.go:35):

```yaml
- path: /etc/systemd/system/ensure-docker.service
  permissions: '0644'
  content: |
    # Reprovisions docker if a mid-cloud-init reboot interrupted
    # get.docker.com (cloud-init runcmd is once-per-instance and won't
    # retry). ConditionPathExists makes healthy boots a no-op.
    [Unit]
    Description=Ensure docker engine is installed
    Wants=network-online.target
    After=network-online.target
    ConditionPathExists=!/usr/bin/docker
    [Service]
    Type=oneshot
    ExecStart=/bin/sh -c 'curl -fsSL https://get.docker.com | sh && usermod -aG docker umbra'
    TimeoutStartSec=600
    [Install]
    WantedBy=multi-user.target
```

And to `ciRunnerRuncmdLines()` append (idempotent — safe on re-run):

```go
// 4 GiB swap: a 3 GiB CI guest OOM-kills heavy jobs (eslint/next build,
// exit 137) AND takes the runner service down with them (2026-07-15
// incident). Swap makes peak jobs slow instead of dead.
`test -f /swapfile || (fallocate -l 4G /swapfile && chmod 600 /swapfile && mkswap /swapfile)`,
`swapon /swapfile || true`,
`grep -q '/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab`,
`systemctl daemon-reload`,
`systemctl enable ensure-docker.service`,
```

- [x] **Step 4: PASS** — `go test ./internal/cloudinit/ -v` (whole package — the golden/rendering tests must still pass).

- [x] **Step 5: Commit**

```bash
git add internal/cloudinit/seed.go internal/cloudinit/seed_test.go
git commit -m "feat(ci-runner): default 4gib swap + reboot-safe docker provisioning"
```

---

### Task 6: `umbra runner add` / `runner list` / `runner harden`

**Files:**
- Create: `internal/runner/script.go`
- Create: `internal/runner/script_test.go`
- Create: `cmd/umbra/runner.go`
- Modify: `cmd/umbra/root.go`

**Interfaces:**
- Produces: package `runner` with
  `InstallScript(p InstallParams) string` where `type InstallParams struct { RepoURL, Token, RunnerName, DirName, Labels, Version string }` — returns the full bash script to run inside the guest (download pinned tarball if dir missing, `./config.sh --unattended --replace`, systemd `svc.sh install` + drop-in, start);
  `HardenScript() string` — bash that writes an override drop-in for every installed `actions.runner.*` service and daemon-reloads;
  both scripts embed the watchdog drop-in: `[Service]\nRestart=always\nRestartSec=10`.
- CLI:
  `umbra runner add <machine> --repo <org/repo> [--labels wsl2,umbra-ci] [--name <auto>] [--count 1]` — fetches a repo registration token via host `gh api --method POST repos/<org/repo>/actions/runners/registration-token --jq .token`, then streams the script over the same ssh invocation `shell` uses (`ssh ... umbra@127.0.0.1 'bash -s'` with the script on stdin);
  `umbra runner list <machine> [--repo org/repo]` — guest `systemctl list-units 'actions.runner.*' --no-legend` always; when `--repo` given, also `gh api repos/<org/repo>/actions/runners` and prints GitHub-side status;
  `umbra runner harden <machine>` — streams `HardenScript()`.
- Default `Version`: `2.328.0` (matches `scripts/install-runner.sh`); default runner name `<machine>-<repo-basename>-N`.

- [x] **Step 1: Failing script-generation tests** — `internal/runner/script_test.go`:

```go
package runner

import (
	"strings"
	"testing"
)

func TestInstallScriptContainsContract(t *testing.T) {
	s := InstallScript(InstallParams{
		RepoURL: "https://github.com/ForceAI-KW/force-website-builder",
		Token:   "REGTOK", RunnerName: "fwb-ci5-fwb-1",
		DirName: "actions-runner-fwb-1", Labels: "wsl2,umbra-ci", Version: "2.328.0",
	})
	for _, want := range []string{
		"actions-runner-linux-arm64-2.328.0.tar.gz",
		`--url "https://github.com/ForceAI-KW/force-website-builder"`,
		`--token "REGTOK"`,
		`--name "fwb-ci5-fwb-1"`,
		`--labels "wsl2,umbra-ci"`,
		"--unattended --replace",
		"svc.sh install",
		"Restart=always",
		"RestartSec=10",
		"systemctl daemon-reload",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("script missing %q", want)
		}
	}
	if strings.Contains(s, "$REG_TOKEN") {
		t.Fatal("script must not depend on caller env — token is inlined")
	}
}

func TestHardenScriptCoversAllRunnerUnits(t *testing.T) {
	s := HardenScript()
	for _, want := range []string{"actions.runner.", "override.conf", "Restart=always", "systemctl daemon-reload"} {
		if !strings.Contains(s, want) {
			t.Fatalf("harden script missing %q", want)
		}
	}
}
```

- [x] **Step 2: FAIL** — `go test ./internal/runner/ -v`.

- [x] **Step 3: Implement `internal/runner/script.go`** — template the exact working recipe from `/home/umbra/crm-install.sh` (documented in scripts/install-runner.sh) with the drop-in added after `svc.sh install`:

```bash
SVC=$(sudo ./svc.sh status | grep -o 'actions\.runner\.[^ ]*\.service' | head -1)
sudo mkdir -p "/etc/systemd/system/${SVC}.d"
printf '[Service]\nRestart=always\nRestartSec=10\n' | sudo tee "/etc/systemd/system/${SVC}.d/override.conf" >/dev/null
sudo systemctl daemon-reload
```

`HardenScript()` loops `systemctl list-units --all 'actions.runner.*' --no-legend | awk '{print $1}'` applying the same drop-in per unit, then `daemon-reload` + restarts any unit in `failed` state.

- [x] **Step 4: PASS**, then **Step 5: CLI** — `cmd/umbra/runner.go`: token fetch via `exec.Command("gh", "api", "--method", "POST", "repos/"+repo+"/actions/runners/registration-token", "--jq", ".token")` (clear error if `gh` missing: "runner add needs the GitHub CLI (brew install gh) authenticated with repo admin"); ssh streaming reuses the exact ssh arg construction from `runShell` — extract that arg-builder into a shared helper `sshArgs(mv *client.MachineView, remoteCmd []string) []string` in `shell.go` so both call sites share it.

- [x] **Step 6: Live verify + commit** — `./bin/umbra runner harden fwb-ci5` against the real guest; confirm `systemctl show actions.runner.ForceAI-KW-force-website-builder.fwb-ci5-fwb-1.service -p Restart` → `Restart=always`.

```bash
git add internal/runner cmd/umbra/runner.go cmd/umbra/shell.go cmd/umbra/root.go
git commit -m "feat: umbra runner add/list/harden - ci-in-a-box runner management"
```

---

### Task 7: `umbra prune`

**Files:**
- Create: `cmd/umbra/prune.go`
- Modify: `cmd/umbra/root.go`

**Interfaces:**
- Consumes: `apiClient.ListMachines`, the shared `sshArgs` helper from Task 6.
- Produces: `umbra prune [machine...]` — for each RUNNING machine (all running when no args): runs the guest cleanup script; prints per-guest freed bytes (df / before/after). No daemon change; guests without docker skip that step (`|| true`).

- [x] **Step 1: Implement** (thin CLI orchestration — the testable logic is the script constant; add `cmd/umbra/prune_test.go` asserting the script contains `apt-get clean`, `docker system prune -af`, `journalctl --vacuum-size`, `fstrim -av`, and does NOT contain `--volumes`):

Guest script:

```bash
BEFORE=$(df -B1 --output=avail / | tail -1)
sudo apt-get clean 2>/dev/null || true
docker system prune -af 2>/dev/null || true   # images/containers/build cache; NEVER volumes
sudo journalctl --vacuum-size=100M 2>/dev/null || true
sudo rm -rf /tmp/* /var/tmp/* 2>/dev/null || true
sudo fstrim -av 2>/dev/null || true
AFTER=$(df -B1 --output=avail / | tail -1)
echo "PRUNE_FREED $((AFTER - BEFORE))"
```

CLI parses the `PRUNE_FREED` line and prints `fwb-ci5: freed 3.2 GiB`.

- [x] **Step 2: Gate + live verify + commit**

```bash
go test ./... && make build && ./bin/umbra prune fwb-ci5
git add cmd/umbra/prune.go cmd/umbra/prune_test.go cmd/umbra/root.go
git commit -m "feat: umbra prune - reclaim guest disk (caches, docker, journal, fstrim)"
```

---

### Task 8: `umbra stats`

**Files:**
- Create: `cmd/umbra/stats.go`
- Create: `cmd/umbra/stats_test.go`
- Modify: `cmd/umbra/root.go`

**Interfaces:**
- Consumes: `apiClient.ListMachines`, `sshArgs` helper, `paths.MachineDir` (host disk.img size via `os.Stat`).
- Produces: `umbra stats [machine...]` table: NAME · STATE · LOAD · MEM used/total · SWAP used/total · DISK used/total (guest /) · IMG (host file size). Guest probe = one ssh exec of `cat /proc/loadavg; free -b | sed -n '2p;3p'; df -B1 --output=used,size / | tail -1`; parsing lives in a pure function `parseGuestStats(out string) (GuestStats, error)` with a unit test feeding canned output.

- [x] **Step 1: Failing parse test** — canned 4-line output → assert fields (load "0.42", mem used/total, swap, disk).
- [x] **Step 2: FAIL → implement `parseGuestStats` + table printing → PASS.**
- [x] **Step 3: Gate + live verify + commit**

```bash
go test ./... && make build && ./bin/umbra stats
git add cmd/umbra/stats.go cmd/umbra/stats_test.go cmd/umbra/root.go
git commit -m "feat: umbra stats - live guest cpu/mem/swap/disk table"
```

---

### Task 9: Docs + release polish

**Files:**
- Modify: `README.md` (new commands in the usage section, one line each)
- Modify: `VERSION` (bump minor)
- Modify: `docs/superpowers/plans/2026-07-15-umbra-v2-core-upgrades.md` (tick boxes — the executor does this as it goes)

**Steps:**
- [x] README: add `set / exec / snapshot / snapshots / restore / export / import / runner add|list|harden / prune / stats` to the command table with one-line descriptions (match existing table format).
- [x] `VERSION`: bump (e.g. `0.6.0` → `0.7.0`) — `make app` reads it.
- [x] Full local gate: `go test ./... && go vet ./... && make build`.
- [x] Commit: `docs: v0.7.0 command reference for v2 core upgrades`.

---

## Self-Review (done at write time)

- **Spec coverage:** 12-item roadmap → Tasks 1–8 cover exec, set, snapshot/restore, export/import, role swap + reboot-safe cloud-init, runner add/list + watchdog (add-time drop-in AND retrofit via `harden`), prune, stats. Remote daemon / LaunchDaemon / ssh-forward root-cause explicitly deferred with reasons (Global Constraints section).
- **Placeholder scan:** Task 4 Step 5 contains an implementer NOTE by design (contract locked, clean implementation described concretely); no TBD/TODO elsewhere.
- **Type consistency:** `UpdateRequest` (T2) matches client method; `snapshot.Info` used by client + CLI; `InstallParams`/`HardenScript` names consistent across T6 steps; `sshArgs` helper introduced in T6 and consumed by T7/T8.
