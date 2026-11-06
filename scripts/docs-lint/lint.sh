#!/usr/bin/env bash
# Docs lint. Checks the tracked files of the git repo it runs in and prints one
# "file:line: problem" per violation.
#
#   1. Markdown lines over 120 characters, table rows included. Fenced code blocks and lines
#      that are only a URL are exempt.
#   2. References to files that do not exist: every relative Markdown link target (inline and
#      reference-style, outside code; URLs and anchor-only links excluded), and any mention of
#      a docs/*.md file in a non-Markdown file, which in practice means code and config
#      comments. Only existence is checked, not anchors.
#
# Both skip the tool-formatted .github/skills/.
#
# Exit: 0 clean, 1 violations, 2 usage error (not in a git repo).
set -euo pipefail

MAX=120
# This script's directory, relative to the repo root. Its files are skipped by the reference
# check: the test is full of deliberately dangling references.
SELF_DIR=scripts/docs-lint

root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "docs lint: not inside a git repository" >&2
  exit 2
}
cd "$root"

violations=0
report() {
  echo "$1"
  violations=$((violations + 1))
}

# normalize <path>: collapse "." and ".." segments; a path escaping the root stays as "../x".
normalize() {
  local IFS=/ seg out=() n=0
  for seg in $1; do
    case "$seg" in
      "" | .) ;;
      ..)
        if ((n)) && [[ "${out[n - 1]}" != .. ]]; then
          n=$((n - 1))
          unset "out[$n]"
        else
          out[n]=..
          n=$((n + 1))
        fi
        ;;
      *)
        out[n]="$seg"
        n=$((n + 1))
        ;;
    esac
  done
  if ((n)); then echo "${out[*]}"; fi
}

# check_ref <file> <line> <repo-relative target>: an empty target is the repo root, and one
# above the root ("../x") never resolves inside the repo.
check_ref() {
  local target="$3"
  [[ "$target" != .. && "$target" != ../* && ( -z "$target" || -e "$target" ) ]] && return 0
  report "$1:$2: dangling reference to $target"
}

md_files() {
  git ls-files -- '*.md' ':!.github/skills/'
}

# One awk pass per file emits "L <line> <length>" for overlong lines and "R <line> <target>"
# for link targets, both outside fenced code. LC_ALL=C plus stripping UTF-8 continuation
# bytes counts characters the same way under mawk, gawk and BSD awk.
scan_md() {
  LC_ALL=C awk -v max="$MAX" '
    function fence_of(s) {
      sub(/^[ \t]*/, "", s)
      if (match(s, /^(```+|~~~+)/)) return substr(s, 1, RLENGTH)
      return ""
    }
    {
      f = fence_of($0)
      if (open != "") {
        # A closing fence: same character, at least as long, nothing after it.
        if (f != "" && substr(f, 1, 1) == substr(open, 1, 1) && length(f) >= length(open)) {
          rest = $0; sub(/^[ \t]*[`~]+[ \t]*$/, "", rest)
          if (rest == "") open = ""
        }
        next
      }
      if (f != "") { open = f; next }

      chars = $0; gsub(/[\200-\277]/, "", chars)
      if (length(chars) > max && $0 !~ /^[ \t]*([-*+>][ \t]+)?<?https?:\/\/[^ \t]*>?[ \t]*$/)
        print "L", NR, length(chars)

      s = $0; gsub(/`[^`]*`/, "", s)   # links inside inline code are examples
      while (match(s, /\]\([^)]*\)/)) {
        print "R", NR, substr(s, RSTART + 2, RLENGTH - 3)
        s = substr(s, RSTART + RLENGTH)
      }
      if (match($0, /^ ? ? ?\[[^]^][^]]*\]:[ \t]+[^ \t]+/)) {
        def = substr($0, RSTART, RLENGTH); sub(/^[^]]*\]:[ \t]+/, "", def)
        print "R", NR, def
      }
    }' "$1"
}

while IFS= read -r file; do
  [[ -f "$file" ]] || continue
  dir=$(dirname "$file")
  while read -r kind line value; do
    if [[ "$kind" == L ]]; then
      report "$file:$line: $value characters (max $MAX)"
      continue
    fi
    target="${value#<}"
    target="${target%%[ >]*}"
    target="${target%%#*}"
    target="${target%%\?*}"
    [[ -z "$target" || "$target" == *:* ]] && continue   # anchor-only, URL, mailto:
    if [[ "$target" == /* ]]; then
      target=$(normalize "$target")
    else
      target=$(normalize "$dir/$target")
    fi
    check_ref "$file" "$line" "$target"
  done < <(scan_md "$file")
done < <(md_files)

# Non-Markdown files: any docs/*.md path that starts a path (not "x/docs/", which covers
# URLs). Comments name docs from the repo root, so leading "./" and "../" are dropped.
while IFS= read -r hit; do
  file="${hit%%:*}"
  hit="${hit#*:}"
  line="${hit%%:*}"
  ref="${hit#*:}"
  ref="docs/${ref#*docs/}"
  check_ref "$file" "$line" "$ref"
done < <(git grep -n -I -o -E '(^|[^A-Za-z0-9_./-])(\.\.?/)*docs/[A-Za-z0-9_./-]+\.md' -- \
  ':!*.md' ':!.github/skills/' ":!$SELF_DIR/" || true)

if ((violations)); then
  echo "docs lint: $violations violation(s)" >&2
  exit 1
fi
