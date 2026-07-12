#!/usr/bin/env bash
# Regenerate AppIcon.icns from umbra-icon.svg. Needs rsvg-convert (brew install
# librsvg), sips + iconutil (macOS). The committed AppIcon.icns is the build
# output; run this only when the SVG changes.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

rsvg-convert -w 1024 -h 1024 umbra-icon.svg -o master-1024.png

ICONSET=Umbra.iconset
rm -rf "$ICONSET"; mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z "$s" "$s" master-1024.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  d=$((s * 2))
  sips -z "$d" "$d" master-1024.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
cp master-1024.png "$ICONSET/icon_512x512@2x.png"

iconutil -c icns "$ICONSET" -o AppIcon.icns
rm -rf "$ICONSET" master-1024.png
echo "wrote AppIcon.icns"
