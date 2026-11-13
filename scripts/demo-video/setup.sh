#!/usr/bin/env bash
# Prepares a throwaway home for recording the README demo: a demo HOME where Claude Code runs on
# ANTHROPIC_API_KEY with its first-run screens already seen, an Enclave config with only its
# built-in features plus playwright-mcp, and four small git projects. Writes <demo-root>/env.sh,
# the environment every demo process runs in (without the key, which run.sh passes on). Starts
# nothing.
#
# Usage: ANTHROPIC_API_KEY=... scripts/demo-video/setup.sh <demo-root> <projects-dir>
# Both are wiped. Nothing outside them is written; ~/.claude.json and ~/.config/enclave are read.
# Why an API key: a copy of the user's Claude login shares its rotating refresh token.
set -euo pipefail

root=${1:?demo root}
projects=${2:?projects dir}
here=$(cd "$(dirname "$0")" && pwd -P)
repo=$(cd "$here/../.." && pwd -P)
real_home=$HOME
real_config=${XDG_CONFIG_HOME:-$HOME/.config}
real_runtime=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}
home=$root/home

case "$projects" in "$real_home"/*) echo "projects dir must be outside $real_home" >&2; exit 1 ;; esac
: "${ANTHROPIC_API_KEY:?the demo agents run on ANTHROPIC_API_KEY}"
command -v podman >/dev/null || { echo "the demo's Enclave sessions need podman" >&2; exit 1; }

# Both folders are wiped, so only ones this script made (marked) or empty ones are accepted.
MARK=.homeshell-demo
for dir in "$root" "$projects"; do
  if [ -e "$dir" ] && [ ! -e "$dir/$MARK" ] && [ -n "$(ls -A "$dir" 2>/dev/null)" ]; then
    echo "refusing to wipe $dir: not made by setup.sh (no $MARK in it)" >&2
    exit 1
  fi
done
for dir in "$projects"/*/; do
  [ -d "$dir/.git" ] || continue
  git -C "$dir" worktree list --porcelain | sed -n "s/^worktree //p" | tail -n +2 |
    while IFS= read -r tree; do
      case "$tree" in "$projects"/*) rm -rf "$tree" ;; esac
    done
done
rm -rf "$root" "$projects"
mkdir -p "$home/.claude" "$home/.config/enclave/extensions/features" "$root/bin" "$root/run" "$root/data" \
  "$projects"
touch "$root/$MARK" "$projects/$MARK"
chmod 700 "$root/run"

# -- Claude Code: the first-run screens already seen, and the API key approved -----------------
python3 - "$real_home/.claude.json" "$home/.claude.json" "$projects" "$here/layout.json" <<'EOF'
import json, os, re, sys
real, out, projects, layout = sys.argv[1:]
src = json.load(open(real))
# Only flags that say what was seen or dismissed; nothing that identifies the account.
keep = re.compile(r"^(has[A-Z]|.*(Seen|Shown|Count|Counts|Dismissed|Complete|Impressions)$|tips|lastOnboardingVersion$"
                  r"|lastReleaseNotesSeen$|seenNotifications$|installMethod$|autoUpdates|numStartups$|firstStartTime$"
                  r"|claudeCodeFirstTokenDate$|migrationVersion$)")
drop = {"oauthAccount", "userID", "machineID", "projects", "githubRepoPaths", "customApiKeyResponses"}
cfg = {k: v for k, v in src.items() if keep.match(k) and k not in drop}
# Claude Code asks to trust each folder it starts in: every project and worktree of the layout.
folders = []
for name, p in json.load(open(layout))["projects"].items():
    folders += [name] + [f"{name}-{w}" for w in p["worktrees"] + ([p["onCamera"]] if "onCamera" in p else [])]
trusted = {f"{projects}/{f}": {"hasTrustDialogAccepted": True} for f in folders}
# autoModeEnvSetup: "Teach auto mode about your environment?" after a turn, which reads as waiting.
# customApiKeyResponses: Claude asks once whether to use a key from the environment, showing part
# of it. It records the answer by the key's last 20 characters.
key = os.environ["ANTHROPIC_API_KEY"]
cfg.update(hasCompletedOnboarding=True, theme="dark", projects=trusted, autoModeEnvSetup={"dismissed": True},
           customApiKeyResponses={"approved": [key[-20:]], "rejected": []})
os.umask(0o077)
json.dump(cfg, open(out, "w"), indent=1)
EOF

# -- Enclave: its built-in features plus playwright-mcp; it passes ANTHROPIC_API_KEY on ----------
python3 - "$real_config/enclave/config.json" "$home/.config/enclave/config.json" <<'EOF'
import json, sys
src = json.load(open(sys.argv[1]))
cfg = {k: src[k] for k in ("backend", "tool", "host_config") if k in src}
cfg["tool_overrides"] = {"claude": {"host_config_paths": ["default", "-settings.json"]}}
json.dump(cfg, open(sys.argv[2], "w"), indent=1)
EOF
cp -r "$real_config/enclave/extensions/features/playwright-mcp" "$home/.config/enclave/extensions/features/"
# Claude Code in the container keeps its settings in Enclave's auth store: the same first-run
# answers and approved key as on the host (projects are mounted at the same paths).
auth=$home/.local/state/enclave/tools/claude/auth/default
mkdir -p "$auth"
chmod 700 "$auth"
cp -p "$home/.claude.json" "$auth/.claude.json"
cp -p "$home/.claude.json" "$auth/config.json"

# Rootless podman keeps its storage and runtime state under the real user's home and runtime
# dir, and creates containers through the user's systemd on the real session bus; the demo uses
# those, so the images built for the real Enclave are reused and containers start.
cat >"$root/bin/podman" <<EOF
#!/bin/sh
exec env HOME="$real_home" XDG_CONFIG_HOME="$real_config" XDG_RUNTIME_DIR="$real_runtime" \\
  DBUS_SESSION_BUS_ADDRESS="unix:path=$real_runtime/bus" \\
  "$(command -v podman)" "\$@"
EOF
chmod +x "$root/bin/podman"

# -- Shell: a neutral prompt in every terminal the demo opens ----------------------------------
cat >"$home/.bashrc" <<'EOF'
PS1='\[\e[1;34m\]\W\[\e[0m\] $ '
HISTFILE=
alias ls='ls --color=auto'
EOF
cp "$home/.bashrc" "$home/.bash_profile"
touch "$home/.sudo_as_admin_successful" # hides Ubuntu's "To run a command as administrator" notice
printf '[user]\n\tname = Alex Doe\n\temail = alex@example.com\n[init]\n\tdefaultBranch = main\n' >"$home/.gitconfig"

# -- Projects -----------------------------------------------------------------------------------
for src in "$here"/projects/*/; do
  dir=$projects/$(basename "$src")
  cp -r "$src" "$dir"
  # Only the demo's git identity and config, whatever the caller's environment says.
  env -i PATH="$PATH" HOME="$home" bash -c 'git init -q -b main "$1" && git -C "$1" add -A &&
    git -C "$1" commit -qm "Initial commit"' _ "$dir"
done

{
  echo "# Sourced by run.sh: the environment every demo process runs in."
  printf 'export HOME=%q XDG_CONFIG_HOME=%q XDG_RUNTIME_DIR=%q\n' "$home" "$home/.config" "$root/run"
  printf 'export PATH=%q\n' "$root/bin:$PATH"
  echo "export SHELL=/bin/bash LANG=C.UTF-8 TERM=xterm-256color GTK_USE_PORTAL=0 NO_AT_BRIDGE=1"
  # A self-update would show in the agents' footers and is not what the demo is about.
  echo "export DISABLE_AUTOUPDATER=1"
  echo "export HOMESHELL_API_TOKEN=demo-token"
  # The recorder's Chromium is the one Playwright installed for the user.
  printf 'export PLAYWRIGHT_BROWSERS_PATH=%q\n' "${PLAYWRIGHT_BROWSERS_PATH:-$real_home/.cache/ms-playwright}"
  printf 'export DEMO_REPO=%q DEMO_PROJECTS=%q DEMO_ROOT=%q\n' "$repo" "$projects" "$root"
} >"$root/env.sh"
echo "demo home in $root, projects in $projects"
