#!/usr/bin/env bash
# Umbra uninstaller. Removes the LaunchAgent, the CLI/daemon binaries, and the
# menu bar app. Does NOT delete ~/.umbra (your machines + disks) unless you
# pass --purge.
#
#   ./scripts/uninstall.sh            # remove the app, keep ~/.umbra
#   ./scripts/uninstall.sh --purge    # also delete ~/.umbra (machines + disks!)
set -euo pipefail

BIN_DIR="${UMBRA_BIN_DIR:-/usr/local/bin}"
APP_DIR="${UMBRA_APP_DIR:-/Applications}"
PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

say()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }

# Stop any running docker VM + daemon, best-effort, before removing binaries.
if command -v "$BIN_DIR/umbra" >/dev/null 2>&1; then
  say "Unloading the LaunchAgent…"
  "$BIN_DIR/umbra" daemon uninstall 2>/dev/null || warn "daemon uninstall failed (may not have been installed)."
fi
pkill -f "$BIN_DIR/umbrad" 2>/dev/null || true

SUDO=""; [ -w "$BIN_DIR" ] || SUDO="sudo"
say "Removing binaries from $BIN_DIR"
$SUDO rm -f "$BIN_DIR/umbrad" "$BIN_DIR/umbra"

if [ -d "$APP_DIR/Umbra.app" ]; then
  say "Removing $APP_DIR/Umbra.app"
  rm -rf "$APP_DIR/Umbra.app"
  pkill -f "Umbra.app/Contents/MacOS/UmbraMenuBar" 2>/dev/null || true
fi

if [ "$PURGE" = 1 ]; then
  say "Purging ~/.umbra (machines, disks, keys)…"
  rm -rf "$HOME/.umbra"
else
  say "Kept ~/.umbra (your machines + disks). Pass --purge to remove it."
fi

# The /etc/resolver/umbra.local entry (if the daemon ever managed to write it)
# needs root to remove; mention rather than sudo-remove silently.
[ -f /etc/resolver/umbra.local ] && warn "Left /etc/resolver/umbra.local — remove with: sudo rm /etc/resolver/umbra.local"

say "Umbra uninstalled."
