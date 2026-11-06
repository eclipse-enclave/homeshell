#!/usr/bin/env bash
# Serves a fake release so you can test the self-update flow locally.
#
# Usage (via make):
#   make dev-update                    # builds v0.0.1 + v0.0.2, starts everything
#   make dev-update UPDATE_VERSION=1.0.0
#
# Manual:
#   1. Build the "new" binary:  scripts/fake-release-server.sh build 0.0.2
#   2. Start the server:        scripts/fake-release-server.sh serve 0.0.2
#   3. In another terminal:     HOMESHELL_UPDATE_BASE_URL=http://127.0.0.1:9876 ./bin/homeshell
#
# The "new" binary is compiled into scripts/fake-release/ with the specified
# version baked in via ldflags.  The server serves it as a go-selfupdate
# HttpSource release (manifest.yaml + tar.gz + checksums.txt).

set -euo pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

RELEASE_DIR="scripts/fake-release"

cmd_build() {
  local version="${1:?usage: $0 build <version>}"
  local binary="$RELEASE_DIR/homeshell"

  echo "Building fake release binary v${version}…"
  mkdir -p "$RELEASE_DIR"
  make embed >/dev/null 2>&1
  cd daemon && go build -ldflags "-X main.version=${version}" -o "../${binary}" ./cmd/homeshell
  cd "$ROOT"
  echo "  → ${binary} ($(${binary} -version))"
}

cmd_serve() {
  local version="${1:?usage: $0 serve <version>}"
  local port="${2:-9876}"
  local binary="$RELEASE_DIR/homeshell"
  local os arch asset_name

  if [[ ! -f "$binary" ]]; then
    echo "No binary at $binary — run '$0 build $version' first." >&2
    exit 1
  fi

  os="$(go env GOOS)"
  arch="$(go env GOARCH)"
  asset_name="homeshell_${os}_${arch}.tar.gz"

  # Package the binary into a tar.gz (go-selfupdate expects "homeshell" inside).
  tar czf "$RELEASE_DIR/$asset_name" -C "$RELEASE_DIR" homeshell

  # Checksum file matching ChecksumValidator{UniqueFilename: "checksums.txt"}.
  local checksum
  checksum="$(sha256sum "$RELEASE_DIR/$asset_name" | awk '{print $1}')"
  echo "$checksum  $asset_name" > "$RELEASE_DIR/checksums.txt"

  # go-selfupdate HttpSource manifest (YAML).
  cat > "$RELEASE_DIR/manifest.yaml" <<EOF
last_release_id: 1
last_asset_id: 2
releases:
  - id: 1
    name: v${version}
    tag_name: v${version}
    assets:
      - id: 1
        name: ${asset_name}
        url: ${asset_name}
      - id: 2
        name: checksums.txt
        url: checksums.txt
EOF

  # The HttpSource expects /{owner}/{repo}/manifest.yaml — create a symlink
  # so python's http.server resolves the path.
  local link_dir="$RELEASE_DIR/eclipsesource/homeshell"
  rm -rf "$RELEASE_DIR/eclipsesource"
  mkdir -p "$link_dir"
  for f in manifest.yaml "$asset_name" checksums.txt; do
    ln -sf "../../$f" "$link_dir/$f"
  done

  echo "Serving fake release v${version} for ${os}/${arch} on http://127.0.0.1:${port}"
  echo "  manifest: http://127.0.0.1:${port}/eclipsesource/homeshell/manifest.yaml"
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
