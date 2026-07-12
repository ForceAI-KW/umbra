#!/usr/bin/env bash
# Umbra installer. Works two ways:
#   • from a release tarball  — installs the bundled umbrad/umbra/Umbra.app
#   • from a source checkout   — runs `make build && make app` first, then installs
#
# Installs the CLI + daemon onto PATH, the menu bar app into /Applications,
# and loads the umbrad LaunchAgent (auto-start at login). Re-runnable.
#
#   curl -fsSL https://raw.githubusercontent.com/ForceAI-KW/umbra/main/scripts/install.sh | bash
# or, from a clone / unpacked tarball:
#   ./scripts/install.sh          # or:  ./install.sh
set -euo pipefail

BIN_DIR="${UMBRA_BIN_DIR:-/usr/local/bin}"
APP_DIR="${UMBRA_APP_DIR:-/Applications}"
REPO="https://github.com/ForceAI-KW/umbra"

say()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# --- preflight -------------------------------------------------------------
[ "$(uname -s)" = "Darwin" ] || die "Umbra is macOS-only."
[ "$(uname -m)" = "arm64" ] || die "Umbra requires Apple Silicon (arm64)."
osver=$(sw_vers -productVersion | cut -d. -f1)
[ "$osver" -ge 13 ] 2>/dev/null || die "Umbra requires macOS 13 or newer (found $(sw_vers -productVersion))."

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- locate the artifacts --------------------------------------------------
# Layout A: source checkout — this script sits in <repo>/scripts/, sources build.
# Layout B: release tarball — umbrad/umbra/Umbra.app sit next to this script.
UMBRAD="" UMBRA="" APP=""
if [ -f "$here/umbrad" ] && [ -f "$here/umbra" ]; then          # unpacked tarball
  UMBRAD="$here/umbrad"; UMBRA="$here/umbra"
  [ -d "$here/Umbra.app" ] && APP="$here/Umbra.app"
elif [ -f "$here/../Makefile" ]; then                          # source checkout
  repo="$(cd "$here/.." && pwd)"
  say "Building from source (make build && make app)…"
  make -C "$repo" build
  make -C "$repo" app || warn "make app failed (Swift toolchain?); installing CLI + daemon only"
  UMBRAD="$repo/bin/umbrad"; UMBRA="$repo/bin/umbra"
  [ -d "$repo/bin/Umbra.app" ] && APP="$repo/bin/Umbra.app"
else
  die "Can't find umbra binaries. Run from a source checkout or an unpacked release tarball."
fi
[ -f "$UMBRAD" ] && [ -f "$UMBRA" ] || die "umbrad/umbra not found after build."

# --- sudo helper: only elevate when the install dir genuinely needs it -----
# Writable dir → no sudo. Missing dir but writable parent → mkdir, no sudo.
# Otherwise → sudo (and create it if missing).
SUDO=""
if [ -d "$BIN_DIR" ] && [ -w "$BIN_DIR" ]; then
  :
elif [ ! -d "$BIN_DIR" ] && [ -w "$(dirname "$BIN_DIR")" ]; then
  say "Creating $BIN_DIR"; mkdir -p "$BIN_DIR"
else
  SUDO="sudo"
  say "$BIN_DIR needs elevated permissions — you may be prompted for your password."
  [ -d "$BIN_DIR" ] || sudo mkdir -p "$BIN_DIR"
fi

# --- install CLI + daemon --------------------------------------------------
say "Installing umbrad + umbra → $BIN_DIR"
$SUDO cp "$UMBRAD" "$BIN_DIR/umbrad"
$SUDO cp "$UMBRA"  "$BIN_DIR/umbra"
# Re-sign in place: copying can invalidate the ad-hoc signature, and umbrad
# needs its com.apple.security.virtualization entitlement to create VMs.
# Look for vz.entitlements in either layout: release tarball ships it next to
# this script (Layout B); a source checkout has it under build/ (Layout A).
ENT=""
if [ -f "$here/vz.entitlements" ]; then
  ENT="$here/vz.entitlements"
elif [ -f "$here/../build/vz.entitlements" ]; then
  ENT="$here/../build/vz.entitlements"
fi
if [ -n "$ENT" ]; then
  $SUDO codesign --force --entitlements "$ENT" --sign - "$BIN_DIR/umbrad" 2>/dev/null || true
else
  warn "vz.entitlements not found — umbrad will lack the virtualization entitlement; VMs will fail to boot. Re-run from a full checkout or tarball."
  $SUDO codesign --force --sign - "$BIN_DIR/umbrad" 2>/dev/null || true
fi
$SUDO codesign --force --sign - "$BIN_DIR/umbra" 2>/dev/null || true

# --- install the menu bar app ---------------------------------------------
if [ -n "$APP" ]; then
  say "Installing Umbra.app → $APP_DIR"
  rm -rf "$APP_DIR/Umbra.app"
  cp -R "$APP" "$APP_DIR/Umbra.app"
  # Outer-only re-sign, matching `make app`: --deep would re-sign the nested
  # umbrad with the app's (empty) entitlements, stripping its virtualization
  # entitlement and breaking VM boot.
  codesign --force --sign - "$APP_DIR/Umbra.app" 2>/dev/null || true
else
  warn "Umbra.app not built — skipping the menu bar app (install the Swift toolchain / Xcode CLT and re-run for it)."
fi

# --- load the LaunchAgent --------------------------------------------------
say "Installing the umbrad LaunchAgent (auto-start at login)…"
"$BIN_DIR/umbra" daemon install --bin "$BIN_DIR/umbrad" || warn "daemon install failed — run 'umbra daemon install' manually."

# --- done ------------------------------------------------------------------
cat <<EOF

$(say "Umbra installed.")
  CLI:        $BIN_DIR/umbra          (try: umbra status)
  Daemon:     running via launchd     (umbra daemon status)
$( [ -n "$APP" ] && echo "  Menu bar:   $APP_DIR/Umbra.app        (open -a Umbra)" )

First run: the first time umbrad boots a VM, macOS shows a one-time
Virtualization permission prompt — approve it. If /mnt/mac (your home share)
ever shows 'operation not permitted', grant the binary Full Disk Access in
System Settings › Privacy & Security.

Quickstart:
  umbra create dev --cpus 4 --memory-gib 8
  umbra start dev
  umbra shell dev

Docs: $REPO
Uninstall: ./scripts/uninstall.sh
EOF
