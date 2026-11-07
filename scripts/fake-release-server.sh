#!/usr/bin/env bash
# Serves a fake stable release so you can test the self-update flow locally.
#
# Usage (via make):
#   make dev-update                    # builds 0.0.1 + 0.0.2, starts everything
#   make dev-update UPDATE_VERSION=1.0.0
#
# Manual:
#   1. Build the "new" binary:  scripts/fake-release-server.sh build 0.0.2
#   2. Start the server:        scripts/fake-release-server.sh serve 0.0.2
#   3. In another terminal, run a stable-stamped daemon:
#        make build BUILD_CHANNEL=stable VERSION=0.0.1
#        HOMESHELL_UPDATE_BASE_URL=http://127.0.0.1:9876 ./bin/homeshell serve
#
# The server mirrors the download.eclipse.org layout the updater reads
# (docs/ARCHITECTURE.md, "Self-update"): releases/latest names the version, and
# releases/<version>/ holds homeshell-<os>-<arch>.tar.gz and checksums.txt.

set -euo pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

RELEASE_DIR="scripts/fake-release"

cmd_build() {
  local version="${1:?usage: $0 build <version>}"
  local binary="$RELEASE_DIR/homeshell"

  echo "Building fake release binary ${version}…"
  mkdir -p "$RELEASE_DIR"
  make embed >/dev/null 2>&1
  # Stamped like a release build, so the installed binary can update again.
  cd daemon && go build \
    -ldflags "-X main.version=${version} -X main.commit=$(git rev-parse HEAD) -X main.channel=stable" \
    -o "../${binary}" ./cmd/homeshell
  cd "$ROOT"
  echo "  → ${binary} ($(${binary} --version))"
}

cmd_serve() {
  local version="${1:?usage: $0 serve <version>}"
  local port="${2:-9876}"
  local binary="$RELEASE_DIR/homeshell"
  local asset_name release

  if [[ ! -f "$binary" ]]; then
    echo "No binary at $binary — run '$0 build $version' first." >&2
    exit 1
  fi

  asset_name="homeshell-$(go env GOOS)-$(go env GOARCH).tar.gz"
  release="$RELEASE_DIR/releases/$version"
  rm -rf "$RELEASE_DIR/releases"
  mkdir -p "$release"
  tar czf "$release/$asset_name" -C "$RELEASE_DIR" homeshell
  (cd "$release" && sha256sum "$asset_name" > checksums.txt)
  echo "$version" > "$RELEASE_DIR/releases/latest"

  echo "Serving fake stable release ${version} on http://127.0.0.1:${port}"
  echo "  latest:  http://127.0.0.1:${port}/releases/latest"
  echo "  archive: http://127.0.0.1:${port}/releases/${version}/${asset_name}"
  echo ""
  echo "Press Ctrl-C to stop."

  cd "$RELEASE_DIR"
  python3 -m http.server "$port" --bind 127.0.0.1
}

case "${1:-}" in
  build) shift; cmd_build "$@" ;;
  serve) shift; cmd_serve "$@" ;;
  *)
    # Legacy single-command mode: build + serve.
    version="${1:-0.0.2}"
    port="${2:-9876}"
    cmd_build "$version"
    cmd_serve "$version" "$port"
    ;;
esac
