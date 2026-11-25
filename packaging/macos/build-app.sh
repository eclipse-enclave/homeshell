#!/usr/bin/env bash
# Assemble HomeShell.app from natively-built binaries.
# Usage: MACOSX_DEPLOYMENT_TARGET=<min-macos> \
#   build-app.sh <version> <homeshell-bin> <desktop-bin> <icns> <app-path>
set -euo pipefail
version="$1"; hs_bin="$2"; desktop_bin="$3"; icns="$4"; app="$5"
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
sed -e "s/__VERSION__/$version/" -e "s/__MIN_MACOS__/${MACOSX_DEPLOYMENT_TARGET:?set by the Makefile}/" \
  packaging/macos/Info.plist > "$app/Contents/Info.plist"
cp "$hs_bin" "$app/Contents/MacOS/homeshell"
cp "$desktop_bin" "$app/Contents/MacOS/homeshell-desktop"
for bin in "$app"/Contents/MacOS/*; do
  minos=$(otool -l "$bin" | awk '/LC_BUILD_VERSION/{f=1} f && $1=="minos"{print $2; exit}')
  [ "$minos" = "$MACOSX_DEPLOYMENT_TARGET" ] || {
    echo "$bin targets macOS $minos, want $MACOSX_DEPLOYMENT_TARGET" \
      "(if Go raised its minimum, bump MACOS_MIN in the Makefile)" >&2; exit 1; }
done
cp "$icns" "$app/Contents/Resources/homeshell.icns"
cp LICENSE NOTICE.md "$app/Contents/Resources/"
# The daemon embeds herdr's detection manifests; Apache-2.0 requires its license.
cp packaging/THIRD-PARTY-NOTICES.txt "$app/Contents/Resources/"
mkdir -p "$app/Contents/Resources/licenses/herdr"
cp daemon/internal/detect/manifests/herdr/LICENSE "$app/Contents/Resources/licenses/herdr/LICENSE"
# TODO(signing): codesign + notarize via the Eclipse Foundation signing service
# here once project credentials are wired (see docs/DEV.md, "Releases").
