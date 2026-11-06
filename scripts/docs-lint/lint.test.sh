#!/usr/bin/env bash
# Black-box tests for lint.sh (the docs lint). Builds throwaway git repos, runs the lint in
# each, and asserts the exit code and the file:line it reports.
# Run: bash scripts/docs-lint/lint.test.sh
set -euo pipefail

lint="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lint.sh"
tmproot=$(mktemp -d)
trap 'rm -rf "$tmproot"' EXIT
pass=0
fail=0

long=$(printf 'x%.0s' {1..121})   # 121 characters: one over the limit
max=$(printf 'x%.0s' {1..120})    # 120 characters: exactly at the limit
# 117 ASCII characters + 3 em-dashes: 120 characters but 126 bytes.
dashes="$(printf 'x%.0s' {1..117})———"

# new_repo: create an empty git repo with a clean docs/index.md, cd into it.
new_repo() {
  repo="$tmproot/repo-$RANDOM$RANDOM"
  mkdir -p "$repo"
  cd "$repo"
  git init -q
  add docs/index.md "# Index"
}

# add <path> <line>...: write the lines to <path> and stage it.
add() {
  local path="$1"
  shift
  mkdir -p "$(dirname "$path")"
  printf '%s\n' "$@" >"$path"
  git add "$path"
}

# check <expected-exit> <label> [expected-output-substring]: run the lint in $PWD.
check() {
  local want="$1" label="$2" expect="${3:-}" got=0 out
  out=$("$lint" 2>&1) || got=$?
  if [[ "$got" != "$want" ]]; then
    echo "  FAIL: $label (want exit $want, got $got)"
    printf '%s\n' "$out" | sed 's/^/        /'
    fail=$((fail + 1))
  elif [[ -n "$expect" && "$out" != *"$expect"* ]]; then
    echo "  FAIL: $label (output lacks '$expect')"
    printf '%s\n' "$out" | sed 's/^/        /'
    fail=$((fail + 1))
  else
    echo "  ok: $label"
    pass=$((pass + 1))
  fi
}

echo "docs lint"

new_repo
add README.md "# Title" "" "$max" "$dashes" "[index](docs/index.md)"
add main.go "package main" "// See docs/index.md." "func main() {}"
check 0 "clean repo passes"

new_repo
add README.md "# Title" "" "$long"
check 1 "overlong prose line fails" "README.md:3"

new_repo
add README.md "# Title" "" "| a | $long |"
check 1 "overlong table row fails" "README.md:3"

new_repo
add README.md "# Title" '```sh' "$long" '```' "" "~~~~" '```' "$long" "~~~~"
check 0 "lines in fenced code blocks are exempt"

new_repo
add README.md "# Title" '```sh' "fine" '```' "$long"
check 1 "a line after a closed code block is checked" "README.md:5"

new_repo
add README.md "# Title" "" "https://example.com/$long" "- <https://example.com/$long>"
check 0 "bare-URL lines are exempt"

new_repo
add README.md "# Title" "" "See https://example.com/$long"
check 1 "a URL inside prose is not exempt" "README.md:3"

new_repo
add .github/skills/tool/SKILL.md "$long" "[x](../../../docs/missing.md)"
check 0 ".github/skills is excluded from both checks"

new_repo
printf '%s\n' "$long" >NOTES.md
check 0 "untracked files are ignored"

new_repo
add AGENTS.md "# Agents" "" "Start at [the index](docs/missing.md#top)."
check 1 "dangling Markdown link fails" "AGENTS.md:3: dangling reference to docs/missing.md"

new_repo
add web/README.md "See [the index](../docs/index.md) and [x](../docs/gone.md)."
check 1 "relative Markdown links resolve from the file's directory" \
  "web/README.md:1: dangling reference to docs/gone.md"

new_repo
add docs/guide.md "Back to [the index](index.md), on to [next](next.md)."
check 1 "links between docs resolve within docs/" "docs/guide.md:1: dangling reference to docs/next.md"

new_repo
add README.md "Here: [this dir](.), [up](./), [index](./docs/index.md)."
check 0 "links that normalize to the repo root or an existing doc pass"

new_repo
add README.md "Footnote.[^1]" "" "[^1]: docs/api.md is a planned page."
check 0 "footnote definitions are not links"

new_repo
add docs/guide.md "See [the notes](../lib/NOTES.md)."
check 1 "a dangling link to a file outside docs/ fails" "docs/guide.md:1: dangling reference to lib/NOTES.md"

new_repo
add lib/NOTES.md "# Notes"
add lib/main.go "package lib"
add docs/guide.md "See [the notes](../lib/NOTES.md), [the code](../lib/main.go) and [lib](../lib/)."
check 0 "links to existing files and directories outside docs/ pass"

new_repo
add docs/guide.md "See [outside](../../../../../../../../tmp)."
check 1 "a link above the repo root fails even if the path exists" "docs/guide.md:1: dangling reference to ../"

new_repo
add README.md "[api]: docs/api.md"
check 1 "dangling reference-style link fails" "README.md:1: dangling reference to docs/api.md"

new_repo
add README.md "Mentioning \`docs/future.md\` in prose is not a link." '```' "[x](docs/nope.md)" '```' \
  "Write links as \`[text](docs/nope.md)\`."
check 0 "Markdown prose mentions and links in code are not checked"

new_repo
add daemon/main.go "package main" "" "// Why: see docs/missing.md." "func main() {}"
check 1 "dangling reference in a code comment fails" \
  "daemon/main.go:3: dangling reference to docs/missing.md"

new_repo
add run.sh "# Why: ./docs/gone.md."
check 1 "a ./docs/ comment reference is checked" "run.sh:1: dangling reference to docs/gone.md"

new_repo
add Makefile "# See ../../docs/gone.md and https://example.com/docs/remote.md." "all:"
check 1 "config comment references resolve from the repo root; URLs are skipped" \
  "Makefile:1: dangling reference to docs/gone.md"

echo
echo "passed: $pass, failed: $fail"
[[ "$fail" == 0 ]]
