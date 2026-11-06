#!/bin/sh
# Checks an extracted .deb or .rpm: both binaries, the launcher entry and icon,
# the project license and notices, and herdr's license, which Apache-2.0
# requires with every copy of the daemon.
#
#   verify-package.sh <extracted-package-root>

set -eu

root=${1:?usage: verify-package.sh <extracted-package-root>}
for path in \
    usr/bin/homeshell \
    usr/bin/homeshell-desktop \
    usr/share/applications/homeshell.desktop \
    usr/share/icons/hicolor/512x512/apps/homeshell.png \
    usr/share/doc/homeshell/LICENSE \
    usr/share/doc/homeshell/NOTICE.md \
    usr/share/doc/homeshell/THIRD-PARTY-NOTICES.txt \
    usr/share/doc/homeshell/licenses/herdr/LICENSE
do
    [ -f "$root/$path" ] || { echo "package is missing /$path" >&2; exit 1; }
done
for bin in homeshell homeshell-desktop; do
    [ -x "$root/usr/bin/$bin" ] || { echo "package /usr/bin/$bin is not executable" >&2; exit 1; }
done
