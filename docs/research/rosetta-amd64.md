# Rosetta amd64 support (M6) — verified vz v3.7.1 API + guest binfmt setup

Verified against the actually-pinned module in this repo (`go.sum`: `github.com/Code-Hex/vz/v3 v3.7.1`) via `go doc` run inside `/Users/ahmadsharaf/Desktop/projects/umbra`, plus [lima-vm/lima](https://github.com/lima-vm/lima)'s real, shipping Rosetta implementation — which (confirmed by reading its source) uses the **exact same `Code-Hex/vz` package and the exact same symbol names** this repo already depends on, so its wiring is a directly-transferable reference, not just an analogous project.

## 1. Availability check — verified symbols

```
$ go doc github.com/Code-Hex/vz/v3 LinuxRosettaAvailability
type LinuxRosettaAvailability int
    LinuxRosettaAvailability represents an availability of Rosetta support for
    Linux binaries.

const LinuxRosettaAvailabilityNotSupported LinuxRosettaAvailability = iota ...
func LinuxRosettaDirectoryShareAvailability() LinuxRosettaAvailability
func (i LinuxRosettaAvailability) String() string
```

Full const block (`go doc -all`):

```go
const (
	// Rosetta support for Linux binaries is not available on the host system.
	LinuxRosettaAvailabilityNotSupported LinuxRosettaAvailability = iota
	// Rosetta support for Linux binaries is not installed on the host system.
	LinuxRosettaAvailabilityNotInstalled
	// Rosetta support for Linux is installed on the host system.
	LinuxRosettaAvailabilityInstalled
)
```

```
$ go doc github.com/Code-Hex/vz/v3 LinuxRosettaDirectoryShareAvailability
func LinuxRosettaDirectoryShareAvailability() LinuxRosettaAvailability
    LinuxRosettaDirectoryShareAvailability checks the availability of Rosetta
    support for the directory share.

    This is only supported on macOS 13 and newer,
    LinuxRosettaAvailabilityNotSupported will be returned on older versions.
```

This is the Go binding for Apple's `+[VZLinuxRosettaDirectoryShare availability]` ([Apple docs](https://developer.apple.com/documentation/virtualization/vzlinuxrosettadirectoryshare)). No completion handler, no error — a plain synchronous enum read.

## 2. Install — verified symbol

```
$ go doc github.com/Code-Hex/vz/v3 LinuxRosettaDirectoryShareInstallRosetta
func LinuxRosettaDirectoryShareInstallRosetta() error
    LinuxRosettaDirectoryShareInstallRosetta download and install Rosetta
    support for Linux binaries if necessary.

    This is only supported on macOS 13 and newer, error will be returned on
    older versions.
```

Binds Apple's `+[VZLinuxRosettaDirectoryShare installRosetta:]`. The Go wrapper is **synchronous/blocking** (no completion handler in the signature — it returns `error` directly), unlike the Swift/ObjC completion-handler form. Lima calls it inline and logs before/after:

```go
// lima-vm/lima pkg/driver/vz/rosetta_directory_share_arm64.go
case vz.LinuxRosettaAvailabilityNotInstalled:
    logrus.Info("Installing rosetta...")
    logrus.Info("Hint: try `softwareupdate --install-rosetta` if Lima gets stuck here")
    if err := vz.LinuxRosettaDirectoryShareInstallRosetta(); err != nil {
        return nil, fmt.Errorf("failed to install rosetta: %w", err)
    }
    logrus.Info("Rosetta installation complete.")
```

Treat this as a **potentially long-running, network-fetching call** (it downloads the Rosetta runtime if missing) — call it off the request-handling goroutine / before `machine.Start()`, same as any other slow provisioning step already inside `guarded()` in `config_darwin.go`, and log before/after exactly like Lima does so a stuck install is diagnosable instead of silent.

## 3. Building + attaching the Rosetta share — verified symbols

```
$ go doc github.com/Code-Hex/vz/v3 NewLinuxRosettaDirectoryShare
func NewLinuxRosettaDirectoryShare() (*LinuxRosettaDirectoryShare, error)
    NewLinuxRosettaDirectoryShare creates a new Rosetta directory share if
    Rosetta support for Linux binaries is installed.

    This is only supported on macOS 13 and newer, error will be returned on
    older versions.

$ go doc -all github.com/Code-Hex/vz/v3 LinuxRosettaDirectoryShare
type LinuxRosettaDirectoryShare struct {
	// Has unexported fields.
}
    LinuxRosettaDirectoryShare directory share to enable Rosetta support for
    Linux binaries.

func (ds *LinuxRosettaDirectoryShare) SetOptions(options LinuxRosettaCachingOptions)
    SetOptions enables translation caching and configure the socket
    communication type for Rosetta.
    This is only supported on macOS 14 and newer. Older versions do nothing.
```

`LinuxRosettaDirectoryShare` implements the `DirectoryShare` interface (same interface `SingleDirectoryShare` implements for the existing `"home"` mount), so it composes with `VirtioFileSystemDeviceConfiguration` exactly the way `config_darwin.go` already wires the home share:

```
$ go doc github.com/Code-Hex/vz/v3 NewVirtioFileSystemDeviceConfiguration
func NewVirtioFileSystemDeviceConfiguration(tag string) (*VirtioFileSystemDeviceConfiguration, error)

$ go doc -all github.com/Code-Hex/vz/v3 VirtioFileSystemDeviceConfiguration
func (c *VirtioFileSystemDeviceConfiguration) SetDirectoryShare(share DirectoryShare)
```

**Tag convention**: Lima (same `Code-Hex/vz` package, verified in `pkg/driver/vz/rosetta_directory_share_arm64.go`) uses tag **`"vz-rosetta"`**:

```go
// lima-vm/lima pkg/driver/vz/rosetta_directory_share_arm64.go — verified source, same vz package
func createRosettaDirectoryShareConfiguration() (*vz.VirtioFileSystemDeviceConfiguration, error) {
	config, err := vz.NewVirtioFileSystemDeviceConfiguration("vz-rosetta")
	if err != nil {
		return nil, fmt.Errorf("failed to create a new virtio file system configuration: %w", err)
	}
	availability := vz.LinuxRosettaDirectoryShareAvailability()
	switch availability {
	case vz.LinuxRosettaAvailabilityNotSupported:
		return nil, errRosettaUnsupported
	case vz.LinuxRosettaAvailabilityNotInstalled:
		logrus.Info("Installing rosetta...")
		if err := vz.LinuxRosettaDirectoryShareInstallRosetta(); err != nil {
			return nil, fmt.Errorf("failed to install rosetta: %w", err)
		}
	case vz.LinuxRosettaAvailabilityInstalled:
		// nothing to do
	}

	rosettaShare, err := vz.NewLinuxRosettaDirectoryShare()
	if err != nil {
		return nil, fmt.Errorf("failed to create a new rosetta directory share: %w", err)
	}
	// macOS 14+: enable AOT translation caching over a unix socket
	macOSProductVersion, err := osutil.ProductVersion()
	if err != nil {
		return nil, fmt.Errorf("failed to get macOS product version: %w", err)
	}
	if !macOSProductVersion.LessThan(*semver.New("14.0.0")) {
		cachingOption, err := vz.NewLinuxRosettaUnixSocketCachingOptions("/run/rosettad/rosetta.sock")
		if err != nil {
			return nil, fmt.Errorf("failed to create a new rosetta directory share caching option: %w", err)
		}
		rosettaShare.SetOptions(cachingOption)
	}
	config.SetDirectoryShare(rosettaShare)
	return config, nil
}
```

Then wired into the VM config alongside every other directory-sharing device (`pkg/driver/vz/vm_darwin.go`, verified):

```go
if vzOpts.Rosetta.Enabled != nil && *vzOpts.Rosetta.Enabled {
	directorySharingDeviceConfig, err := createRosettaDirectoryShareConfiguration()
	if err != nil {
		logrus.Warnf("Unable to configure Rosetta: %s", err)
	} else {
		mounts = append(mounts, directorySharingDeviceConfig)
	}
}
if len(mounts) > 0 {
	vmConfig.SetDirectorySharingDevicesVirtualMachineConfiguration(mounts)
}
```

Note Lima's failure mode on `createRosettaDirectoryShareConfiguration` error: **log + boot without Rosetta**, not a hard launch failure — worth matching so a Rosetta hiccup doesn't take down a VM that doesn't need amd64 at all (only opt-in machines/roles should even call this).

### Umbra integration (`internal/vm/config_darwin.go`)

Sibling to the existing `"home"` VirtioFS share (lines 89–107), added only when the machine's role calls for amd64 support (e.g. the reserved `docker` machine, mirroring how `dockerWriteFiles`/`ciRunnerRuncmdLines` are role-gated in `internal/cloudinit/seed.go`):

```go
// virtiofs: Rosetta share (M6) — enables `docker run --platform linux/amd64`.
// Tag "vz-rosetta" matches lima-vm/lima's convention (same Code-Hex/vz API);
// no reason to diverge, and it makes cross-referencing lima's cidata scripts
// trivial if guest-side issues ever need triage against their source.
if needsRosetta(m) { // e.g. m.Role == registry.ReservedDockerName
	switch avail := vz.LinuxRosettaDirectoryShareAvailability(); avail {
	case vz.LinuxRosettaAvailabilityNotSupported:
		log.Printf("vm: rosetta not supported on this host (macOS <13); skipping amd64 support for %s", m.Name)
	default:
		if avail == vz.LinuxRosettaAvailabilityNotInstalled {
			log.Printf("vm: installing rosetta for %s (first use)...", m.Name)
			if err := vz.LinuxRosettaDirectoryShareInstallRosetta(); err != nil {
				return fmt.Errorf("install rosetta: %w", err)
			}
		}
		rosettaFsCfg, err := vz.NewVirtioFileSystemDeviceConfiguration("vz-rosetta")
		if err != nil {
			return err
		}
		rosettaShare, err := vz.NewLinuxRosettaDirectoryShare()
		if err != nil {
			return err
		}
		rosettaFsCfg.SetDirectoryShare(rosettaShare)
		// append to the SAME slice already holding fsCfg ("home"), then
		// pass both to SetDirectorySharingDevicesVirtualMachineConfiguration
	}
}
```

`SetDirectorySharingDevicesVirtualMachineConfiguration` takes a `[]vz.DirectorySharingDeviceConfiguration` — both the home share and the Rosetta share are the same `*VirtioFileSystemDeviceConfiguration` type carrying different `DirectoryShare` implementations (`SingleDirectoryShare` vs `LinuxRosettaDirectoryShare`), so they compose into one slice with no config-object changes needed elsewhere.

## 4. P5 re-validation hook — where it plugs in

`docs/PITFALLS-EXTERNAL.md` **P5**: *"Rosetta breaks after macOS point updates (SIGSEGV / 'not installed')... Check `VZLinuxRosettaDirectoryShare.availability` before attach; trigger `installRosetta()` when missing; re-validate on every daemon boot against `sw_vers -buildVersion` cached at VM creation, re-provision share on change."*

Grounding already in the codebase (verified, not yet consumed for revalidation — this is the "hook point only" `docs/research/dockerd-in-vm.md` §5 flags):

- `internal/registry/registry.go:39` — `Machine.HostBuild string` field, persisted in `config.json`.
- `cmd/umbrad/docker.go:70-73` and `internal/api/server.go:132` — both already shell out to `/usr/bin/sw_vers -buildVersion` and store the result as `HostBuild` **at machine-creation time** (`cmd/umbrad/docker.go:105`, `internal/api/server.go:203`).
- Nothing currently *reads* `HostBuild` back for comparison — this is the M6 gap.

**M6 hook**: at the top of `launchVZ` (or a new `checkRosettaBuild(m *registry.Machine) error` called from it, before the Rosetta share is built), re-run `sw_vers -buildVersion`, compare to `m.HostBuild`:
- If equal → proceed as normal (availability check in §3 above is still cheap enough to run every boot regardless).
- If different → the cached share may be stale (P5's failure mode). Force a fresh `LinuxRosettaDirectoryShareAvailability()` read (this call is stateless/live, so it self-corrects), re-run `installRosetta()` if it now reports `NotInstalled`, and update `m.HostBuild` in the registry to the new value via `Registry.Save` so future boots compare against the current baseline. This mirrors P6's mitigation ("detect host build change since VM creation and re-provision network config preemptively") for the identical trigger condition, applied to Rosetta instead of network config — worth doing both checks in the same boot-time "host build changed" branch rather than two separate diffs against `m.HostBuild`.

## 5. Guest-side binfmt registration — exact bytes, verified against lima-vm/lima's shipping source

Lima's guest boot script (`pkg/driver/vz/boot.Linux/05-rosetta-volume.sh`, fetched live from `lima-vm/lima` on GitHub) is the canonical reference — same vz mount mechanism, same problem:

```bash
binfmt_entry=/proc/sys/fs/binfmt_misc/rosetta
binfmtd_conf=/usr/lib/binfmt.d/rosetta.conf

rosetta_binfmt=":rosetta:M::\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00:\xff\xff\xff\xff\xff\xfe\xfe\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff:/mnt/lima-rosetta/rosetta:OCF"

# If rosetta is not registered in binfmt_misc, register it.
[ -f "$binfmt_entry" ] || echo "$rosetta_binfmt" >/proc/sys/fs/binfmt_misc/register

# Prioritize rosetta even if qemu-user-static is also installed (systemd-binfmt.service picks up /usr/lib/binfmt.d/*.conf on every boot)
[ ! -d "$(dirname "$binfmtd_conf")" ] || [ -f "$binfmtd_conf" ] || echo "$rosetta_binfmt" >"$binfmtd_conf"
```

Decoded registration string (`man 5 binfmt_misc` / kernel `Documentation/admin-guide/binfmt-misc.rst` format: `:name:type:offset:magic:mask:interpreter:flags`):
- **name**: `rosetta`
- **type**: `M` (magic match)
- **offset**: `` (0, i.e. from byte 0)
- **magic**: `\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00` — ELF magic + `EI_CLASS=ELFCLASS64` + `EI_DATA=ELFDATA2LSB` + `e_type=ET_EXEC(2)` + `e_machine=EM_X86_64(0x3e)` — this is the standard "64-bit little-endian x86-64 executable" magic.
- **mask**: `\xff\xff\xff\xff\xff\xfe\xfe\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff` — masks off `e_type`'s LSB (matches both `ET_EXEC` and `ET_DYN`, i.e. static and PIE/dynamic binaries) and one byte in `EI_VERSION`.
- **interpreter**: `/mnt/lima-rosetta/rosetta` — the Rosetta binary's path **inside the virtiofs mount**, not a copied/local path.
- **flags**: `OCF`
  - **O** (open-binary) — keep an fd open to the interpreter, avoiding a re-open (and re-permission-check) per exec.
  - **C** (credentials) — preserve the calling binary's credentials/security context when invoking the interpreter (needed for setuid-ish correctness).
  - **F** (fix binary) — **the flag the task called out**: loads/pins the interpreter at *registration* time rather than resolving its path at every exec. This is what makes the handler work from inside a `chroot`/container/mount-namespace (Docker's `containerd`-managed rootfs) where the interpreter's `/mnt/lima-rosetta/rosetta` path wouldn't otherwise be resolvable from the container's mount namespace. Without `F`, `docker run --platform linux/amd64` would fail because the container can't see the host mount path.

Confirms the exact failure mode named in `docs/research/dockerd-in-vm.md` §5 (`can't create /proc/sys/fs/binfmt_misc/register: nonexistent directory`, [lima-vm/lima#1088](https://github.com/lima-vm/lima/issues/1088)): the `/proc/sys/fs/binfmt_misc/register` write fails if `binfmt_misc` isn't mounted yet. On Ubuntu 22.04+/24.04 (Umbra's guest, per `internal/cloudinit/seed.go`'s `ubuntu:noble` default) `systemd-binfmt.service` + the kernel module normally auto-mount it at boot, but registration inside a `runcmd:` step can race that — Lima's script is defensive (`rc-service procfs start --ifnotstarted` on Alpine; on Ubuntu, `mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc 2>/dev/null || true` before the register line is the safe belt-and-suspenders addition).

### Umbra's cloud-init runcmd (new `rosettaRuncmdLines` in `internal/cloudinit/seed.go`)

Following the existing `mounts:`/`runcmd:` pattern (the `"home"` mount at line 31-32, `dockerRuncmdLines`/`ciRunnerRuncmdLines` role-gating at lines 130-137):

```yaml
mounts:
  - [home, /mnt/mac, virtiofs, "defaults,nofail", "0", "0"]
  - [vz-rosetta, /mnt/rosetta, virtiofs, "defaults,nofail", "0", "0"]   # role == docker only
```

```go
// rosettaRuncmdLines renders the binfmt_misc registration for the Rosetta
// x86-64 ELF handler, mounted at "vz-rosetta" (config_darwin.go) → /mnt/rosetta.
// The F flag is required: without it the handler can't resolve the
// interpreter path from inside a container's mount namespace, so
// `docker run --platform linux/amd64` would fail even though a bare host
// exec of an amd64 binary would work. Magic/mask/flags verified against
// lima-vm/lima's shipping boot.Linux/05-rosetta-volume.sh (same Code-Hex/vz
// mount mechanism) — see docs/research/rosetta-amd64.md.
func rosettaRuncmdLines() []string {
	return []string{
		`mountpoint -q /proc/sys/fs/binfmt_misc || mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc`,
		`test -f /proc/sys/fs/binfmt_misc/rosetta || printf ':rosetta:M::\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00:\xff\xff\xff\xff\xff\xfe\xfe\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff:/mnt/rosetta/rosetta:OCF' > /proc/sys/fs/binfmt_misc/register`,
	}
}
```

Two notes carried over from the existing `hostsRuncmdLines` doc comment (same file): (a) use `printf`, not `echo -e` — cloud-init's `runcmd` executes via `dash`, whose builtin `echo` doesn't interpret `\x` escapes the way bash's does, so the magic/mask bytes must go through `printf`'s `%b`-equivalent octal/hex handling — verify this in-guest before landing (Lima's script runs under `#!/bin/bash` directly, not through cloud-init's dash `runcmd`, so its `\x`-escaped literal works as-is there but needs the `printf` treatment here, same as the existing hosts-line gotcha already documented in `seed.go`); (b) this is **not idempotent across every possible reboot ordering** if `/mnt/rosetta` isn't mounted yet when `runcmd` fires — the `mounts:` stanza runs before `runcmd:` in cloud-init's phase ordering (same guarantee the existing `home` mount already relies on), so this is fine on first boot, but a persisted machine that mounts-then-crashes mid-`runcmd` could need the same "not idempotent across reboots" caveat already flagged for `hostsRuncmdLines`.

## 6. Does docker need anything beyond binfmt?

**No — once the guest has the `F`-flagged binfmt_misc handler registered, `docker run --platform linux/amd64 ...` works with no docker/containerd-specific configuration.** This is confirmed by both Lima's and the general community's setup: Rosetta *replaces* the qemu-user-static path entirely — they're mutually-exclusive registrations for the same `binfmt_misc` slot family (x86-64 ELF), not layered. Two production references:

- **Lima** ([`website/content/en/docs/config/multi-arch.md`](https://github.com/lima-vm/lima/blob/master/website/content/en/docs/config/multi-arch.md), fetched live): "Fast mode" (QEMU user-mode emulation) requires manually installing `tonistiigi/binfmt:qemu-*` via a privileged container (`nerdctl run --privileged --rm tonistiigi/binfmt ... --install all`) — i.e. QEMU's path genuinely does need an extra container-installed registration step. **"Fast mode 2 (Rosetta)"** by contrast only needs `rosetta.enabled: true` + `rosetta.binfmt: true` in the VM config; the doc's own container example (`docker run --platform=amd64 --rm alpine uname -m` → `x86_64`) runs with zero additional docker-side setup once binfmt is registered guest-side. Optional bonus: Lima's "Rosetta AOT Caching" (macOS 14+, `LinuxRosettaUnixSocketCachingOptions`, §3 above) adds a CDI device (`/etc/cdi/rosetta.yaml`) that `docker run --device=lima-vm.io/rosetta=cached` can opt into for translation caching — that's a performance optimization on top, not a requirement for `--platform linux/amd64` to work at all.
- **Colima** (referenced in `docs/PITFALLS-EXTERNAL.md` P5's own sourcing, colima#926/#1069) ships the equivalent binfmt-only Rosetta path for its VZ+Rosetta profile — same shape, no separate containerd registration.

Practical implication for Umbra: once §5's `rosettaRuncmdLines()` runs on the reserved `docker` machine's first boot, `docker run --platform linux/amd64 ...` should "just work" through the existing dockerd (installed per `docs/research/dockerd-in-vm.md` §1-§3) with no changes to the dockerd systemd override, no `qemu-user-static` package, and no containerd config — matching what `docs/research/dockerd-in-vm.md` §5 already asserted ("no docker-specific config beyond the binfmt registration").

## Sources

- `go doc github.com/Code-Hex/vz/v3 <Symbol>` output above — run directly against this repo's pinned `v3.7.1` (`go.sum`), 2026-07-12.
- Apple Developer docs — [`VZLinuxRosettaDirectoryShare`](https://developer.apple.com/documentation/virtualization/vzlinuxrosettadirectoryshare), [Running Intel binaries in Linux VMs with Rosetta](https://developer.apple.com/documentation/virtualization/running_intel_binaries_in_linux_vms_with_rosetta)
- [lima-vm/lima `pkg/driver/vz/rosetta_directory_share_arm64.go`](https://github.com/lima-vm/lima/blob/master/pkg/driver/vz/rosetta_directory_share_arm64.go) — verified live source, same `Code-Hex/vz` package/symbols this repo pins
- [lima-vm/lima `pkg/driver/vz/vm_darwin.go`](https://github.com/lima-vm/lima/blob/master/pkg/driver/vz/vm_darwin.go) — directory-sharing device composition
- [lima-vm/lima `pkg/driver/vz/boot.Linux/05-rosetta-volume.sh`](https://github.com/lima-vm/lima/blob/master/pkg/driver/vz/boot.Linux/05-rosetta-volume.sh) — verified live source, exact binfmt magic/mask/flags
- [lima-vm/lima `pkg/cidata/cidata.TEMPLATE.d/boot.Linux/05-lima-mounts.sh`](https://github.com/lima-vm/lima/blob/master/pkg/cidata/cidata.TEMPLATE.d/boot.Linux/05-lima-mounts.sh) — virtiofs mount handling for rosetta-tagged shares
- [lima-vm/lima `website/content/en/docs/config/multi-arch.md`](https://github.com/lima-vm/lima/blob/master/website/content/en/docs/config/multi-arch.md) — Rosetta vs QEMU-user-mode setup comparison, confirms binfmt-only requirement for docker
- [lima-vm/lima#1088](https://github.com/lima-vm/lima/issues/1088), [lima-vm/lima#1443](https://github.com/lima-vm/lima/issues/1443) — `binfmt_misc` mount-race failure mode (already cited in `docs/research/dockerd-in-vm.md` §5)
- `man 5 binfmt_misc` / kernel `Documentation/admin-guide/binfmt-misc.rst` — registration string format and flag semantics (O/C/F)
- This repo: `docs/PITFALLS-EXTERNAL.md` P5/P6, `docs/research/dockerd-in-vm.md` §5, `internal/vm/config_darwin.go`, `internal/cloudinit/seed.go`, `internal/registry/registry.go`, `cmd/umbrad/docker.go`, `internal/api/server.go`
