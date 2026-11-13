#!/usr/bin/env bash
# Records the README demo: docs/assets/homeshell-demo.gif, and an MP4 of the same cut next to it.
# Builds a throwaway demo home and projects (setup.sh), records a take on a virtual desktop with
# real Claude Code agents (xsession.sh, take.sh), encodes it (encode.sh) and cleans up.
#
# Usage: ANTHROPIC_API_KEY=... scripts/demo-video/run.sh [output.gif]
# Env: DEMO_ROOT (default /tmp/homeshell-demo), DEMO_PROJECTS (projects shown in the video,
# default /tmp/code; must be outside $HOME), DEMO_PORT (default 8431), DEMO_DISPLAY (default 97),
# DEMO_KEEP=1 keeps the daemon's data and the raw take (never the demo home, which holds the key);
# encode.sh's variables apply too.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd -P)
repo=$(cd "$here/../.." && pwd -P)
gif=${1:-$repo/docs/assets/homeshell-demo.gif}
root=${DEMO_ROOT:-/tmp/homeshell-demo}
projects=${DEMO_PROJECTS:-/tmp/code}
port=${DEMO_PORT:-8431}
display=${DEMO_DISPLAY:-97}
: "${ANTHROPIC_API_KEY:?the demo agents run on ANTHROPIC_API_KEY}"

[ -x "$repo/bin/homeshell" ] || { echo "build first: make build" >&2; exit 1; }
"$here/setup.sh" "$root" "$projects"

cleanup() {
  set +e # every step runs, whatever fails before it
  # Enclave keeps the key it injected in its env store, and Claude Code backups of .claude.json
  # hold its tail: the demo home goes first. DEMO_KEEP keeps the take and the daemon's data only.
  rm -rf "$root/home"
  # Only what this demo started: processes of the repo's homeshell binary (daemon, bridges) and
  # the virtual desktop, and the Enclave containers of the demo projects. Each by exact match.
  ps -eo pid=,args= | awk -v bin="$repo/bin/homeshell" -v root="$root/" -v d=":$display" \
    '($2 == bin && index($0, root)) || ($2 ~ /Xvfb$/ && $3 == d) {print $1}' | xargs -r kill 2>/dev/null
  podman ps -a --format '{{.Names}} {{index .Labels "enclave.project_dir"}}' 2>/dev/null |
    awk -v p="$projects/" 'index($2, p) == 1 && $1 !~ /-gateway$/ {print $1}' |
    while read -r name; do
      [ -n "$name" ] && podman stop -t 5 "$name" "$name-gateway" >/dev/null 2>&1
      [ -n "$name" ] && podman rm -f "$name" "$name-gateway" >/dev/null 2>&1
    done
  rm -rf /tmp/hs-demo-chromium-* 2>/dev/null
  [ "${DEMO_KEEP:-}" = 1 ] || rm -rf "$root/take" "$root/data"
}
trap cleanup EXIT

take=$root/take
env -i ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" bash -c \
  'source "$1/env.sh"; exec "$2/xsession.sh" "$3" "$2/take.sh" "$4" "$5"' \
  _ "$root" "$here" "$display" "$port" "$take"

"$here/encode.sh" "$take/raw.mkv" "$take/timeline.json" "$gif" "${gif%.gif}.mp4"
