#!/usr/bin/env bash
# Runs a command on a virtual desktop for the README demo: Xvfb, the metacity window manager
# and a gnome-terminal server, all inside one D-Bus session. Expects the demo env (env.sh).
#
# Usage: scripts/demo-video/xsession.sh <display-number> <command> [args...]
# Env: DEMO_SCREEN (default 1280x800).
set -euo pipefail

display=${1:?display number}
shift
screen=${DEMO_SCREEN:-1280x800}
log=${DEMO_ROOT:?source env.sh first}/xsession.log

# Refuse a display in use: cleanup stops the Xvfb on it.
if [ -e "/tmp/.X11-unix/X$display" ] || [ -e "/tmp/.X$display-lock" ]; then
  echo "display :$display is in use; pick another with DEMO_DISPLAY" >&2
  exit 1
fi
Xvfb ":$display" -screen 0 "${screen}x24" -nolisten tcp >"$log" 2>&1 &
xvfb=$!
trap 'kill $xvfb 2>/dev/null || true' EXIT
for i in $(seq 50); do
  DISPLAY=":$display" xdotool getdisplaygeometry >/dev/null 2>&1 && break
  [ "$i" = 50 ] && { echo "Xvfb did not start on :$display" >&2; exit 1; }
  sleep 0.1
done
export DISPLAY=:$display

# The terminal server is started here: activated over D-Bus it never comes up under Xvfb.
# shellcheck disable=SC2016
dbus-run-session -- bash -c '
  metacity --replace >>"$0" 2>&1 &
  /usr/libexec/gnome-terminal-server --app-id org.homeshell.DemoTerminal >>"$0" 2>&1 &
  sleep 1.5
  "${@}"
' "$log" "$@"
