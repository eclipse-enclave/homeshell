#!/usr/bin/env bash
# Convert the shared PNG icon to an .icns (macOS only: sips, iconutil).
# No .icns is committed because Linux dev machines cannot produce one.
# Usage: build-icns.sh <png> <out.icns>
set -euo pipefail
png="$1"; out="$2"
iconset="$(mktemp -d)/homeshell.iconset"
trap 'rm -rf "$(dirname "$iconset")"' EXIT
mkdir -p "$iconset"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" "$png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
  double=$((size * 2))
  sips -z "$double" "$double" "$png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$out"
