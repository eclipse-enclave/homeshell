#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Black-box tests for favicon.sh against the real app icon and altered copies.
# Run: bash packaging/favicon.test.sh
set -euo pipefail

packaging="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
icon="$packaging/homeshell.svg"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
pass=0
fail=0

ok() { echo "  ok: $1"; pass=$((pass + 1)); }
bad() {
  echo "  FAIL: $1"
  [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/        /'
  fail=$((fail + 1))
}

# expect_fail <label> <output-substring> <svg>: favicon.sh fails on svg, mentioning substring.
expect_fail() {
  local label="$1" want="$2" out
  if out=$(bash "$packaging/favicon.sh" "$3" 2>&1); then
    bad "$label (succeeded)" "$out"
  elif [[ "$out" != *"$want"* ]]; then
    bad "$label (want '$want')" "$out"
  else
    ok "$label"
  fi
}

echo "favicon.sh"

favicon=$(bash "$packaging/favicon.sh" "$icon")

if [[ "$favicon" == *'viewBox="100 100 824 824"'* && "$favicon" != *'width="1024"'* ]]; then
  ok "crops the canvas to the tile"
else
  bad "crops the canvas to the tile" "$(grep -F '<svg ' <<<"$favicon")"
fi

if [[ "$favicon" != *'filter="url(#shadow)"'* ]]; then
  ok "drops the tile's shadow"
else
  bad "drops the tile's shadow"
fi

if [[ "$favicon" == *'<title>HomeShell</title>'* && "$favicon" == *'stroke-linejoin="round"'* ]]; then
  ok "keeps the artwork"
else
  bad "keeps the artwork"
fi

if out=$(printf '%s\n' "$favicon" | python3 -I -c 'import sys, xml.dom.minidom; xml.dom.minidom.parse(sys.stdin)' 2>&1); then
  ok "is well-formed XML"
else
  bad "is well-formed XML" "$out"
fi

sed 's/viewBox="0 0 1024 1024"/viewBox="0 0 512 512"/' "$icon" >"$tmp/resized.svg"
expect_fail "fails when the canvas changes" 'viewBox="0 0 1024 1024"' "$tmp/resized.svg"

sed 's/ filter="url(#shadow)"//' "$icon" >"$tmp/flat.svg"
expect_fail "fails when the tile has no shadow" 'filter="url(#shadow)"' "$tmp/flat.svg"

sed 's/<rect x="640"/<rect filter="url(#shadow)" x="640"/' "$icon" >"$tmp/two-shadows.svg"
expect_fail "fails when two shapes have a shadow" "found 2" "$tmp/two-shadows.svg"

echo
echo "passed: $pass, failed: $fail"
[ "$fail" -eq 0 ]
