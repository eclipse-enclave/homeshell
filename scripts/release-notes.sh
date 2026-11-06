#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT
#
# Prints the GitHub release notes for a build.
#
#   release-notes.sh rolling <commit> <assets-url>
#   release-notes.sh stable <version> <assets-url> <commit>
#
# assets-url is the GitHub download URL of the release's assets; the same
# files are published on download.eclipse.org (see docs/DEV.md, Releases).
# The Commit line records the source of a release; release.yml refuses to
# republish a version from another commit.

set -eu

channel=${1:?channel}
build=${2:?commit or version}
assets_url=${3:?assets url}
repo_url=${assets_url%/releases/download/*}
downloads_url=https://download.eclipse.org/enclave/homeshell

case "$channel" in
    rolling)
        short=$(printf '%s' "$build" | cut -c1-7)
        cat <<EOT
**Rolling release from \`main\`**

Commit: [\`$short\`]($repo_url/commit/$build)
Date: $(date -u +%Y-%m-%d)

Rolling builds are rebuilt from every push to \`main\` and replaced in place; the
same files are mirrored at [$downloads_url/rolling/]($downloads_url/rolling/).
Official releases are listed under [$downloads_url/releases/]($downloads_url/releases/).

EOT
        ;;
    stable)
        commit=${4:?commit}
        short=$(printf '%s' "$commit" | cut -c1-7)
        cat <<EOT
**HomeShell $build**

Commit: [\`$short\`]($repo_url/commit/$commit)

These files are also published at
[$downloads_url/releases/$build/]($downloads_url/releases/$build/).

EOT
        ;;
    *)
        echo "unknown channel: $channel" >&2
        exit 2
        ;;
esac

cat <<EOT
| File | Platform | Notes |
|------|----------|-------|
| \`homeshell_*_amd64.deb\` | Ubuntu 24.04 (x86-64) | Daemon and desktop app |
| \`homeshell-*.x86_64.rpm\` | Fedora Linux (x86-64) | Daemon and desktop app |
| \`HomeShell-*-arm64.dmg\` | macOS (Apple Silicon) | Desktop app, unsigned |
| \`HomeShell-*-amd64.dmg\` | macOS (Intel) | Desktop app, unsigned |
| \`homeshell-linux-amd64.tar.gz\` | Linux (x86-64) | Daemon only, used with a browser; updates itself |
| \`homeshell-linux-arm64.tar.gz\` | Linux (arm64) | Daemon only, used with a browser; updates itself |
| \`homeshell-darwin-arm64.tar.gz\` | macOS (Apple Silicon) | Daemon only, used with a browser; updates itself |
| \`homeshell-darwin-amd64.tar.gz\` | macOS (Intel) | Daemon only, used with a browser; updates itself |

\`\`\`sh
# Ubuntu 24.04 (resolves dependencies automatically)
sudo apt install ./homeshell_*_amd64.deb

# Fedora Linux (resolves dependencies automatically)
sudo dnf install ./homeshell-*.x86_64.rpm

# Any Linux or macOS, daemon only (pick the archive for your platform)
sha256sum --check --ignore-missing checksums.txt   # macOS: shasum -a 256 --check --ignore-missing checksums.txt
tar -xzf homeshell-linux-amd64.tar.gz homeshell
install -d ~/.local/bin
install homeshell ~/.local/bin/homeshell
homeshell serve
\`\`\`

The daemon from an archive updates itself from the \`$channel\` channel (\`homeshell update\`, or the update button in the UI) when installed into a directory you own, such as \`~/.local/bin\`. The packages and the macOS app are updated by installing a newer one.

The macOS builds are unsigned and not notarized, so Gatekeeper warns on first open; right-click the app and choose Open. Windows users run the Linux build inside WSL.
EOT
