#!/usr/bin/env bash
# One take of the README demo, run on the virtual desktop by run.sh (via xsession.sh): starts the
# demo daemon, seeds it and plays the story, which records <out-dir>/raw.mkv and timeline.json.
#
# Usage: scripts/demo-video/take.sh <port> <out-dir>   (in the demo env, with DISPLAY set)
set -euo pipefail

port=${1:?port}
out=${2:?out dir}
here=$(cd "$(dirname "$0")" && pwd -P)
url=http://127.0.0.1:$port

"$DEMO_REPO/bin/homeshell" serve -addr "127.0.0.1:$port" -home-dir "$DEMO_ROOT/data" >"$DEMO_ROOT/daemon.log" 2>&1 &
for i in $(seq 100); do
  curl -sf -o /dev/null -H "Authorization: Bearer $HOMESHELL_API_TOKEN" "$url/api/hello" && break
  [ "$i" = 100 ] && { echo "the daemon did not start; see $DEMO_ROOT/daemon.log" >&2; exit 1; }
  sleep 0.2
done

# Every window the daemon opens: a GNOME Terminal at a fixed place, on the server xsession.sh runs.
export DEMO_TERMINAL='gnome-terminal --app-id org.homeshell.DemoTerminal --geometry=100x30+250+110 -- {command}'
echo "seeding (real agents; takes a few minutes)"
node "$here/seed.mjs" "$url" "$DEMO_PROJECTS"
echo "recording"
node "$here/story.mjs" "$url" "$out"
