#!/usr/bin/env bash
# Assemble HomeShell.app from natively-built binaries and wrap it as a .dmg.
# Usage: build-app.sh <version> <arch> <homeshell-bin> <desktop-bin> <icns> <out-dir>
set -euo pipefail
version="$1"; arch="$2"; hs_bin="$3"; desktop_bin="$4"; icns="$5"; out="$6"
# The .app is staged outside out-dir, so only the .dmg lands there.
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
app="$stage/HomeShell.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
sed "s/__VERSION__/$version/" packaging/macos/Info.plist > "$app/Contents/Info.plist"
cp "$hs_bin" "$app/Contents/MacOS/homeshell"
cp "$desktop_bin" "$app/Contents/MacOS/homeshell-desktop"
cp "$icns" "$app/Contents/Resources/homeshell.icns"
cp LICENSE NOTICE.md "$app/Contents/Resources/"
# The daemon embeds herdr's detection manifests; Apache-2.0 requires its license.
cp packaging/THIRD-PARTY-NOTICES.txt "$app/Contents/Resources/"
mkdir -p "$app/Contents/Resources/licenses/herdr"
cp daemon/internal/detect/manifests/herdr/LICENSE "$app/Contents/Resources/licenses/herdr/LICENSE"
# TODO(signing): codesign + notarize via the Eclipse Foundation signing service
# here once project credentials are wired (see docs/DEV.md, "Releases"). Unsigned .app
# still runs via right-click → Open.
hdiutil create -volname HomeShell -srcfolder "$app" -ov -format UDZO \
  "$out/HomeShell-$version-$arch.dmg"
