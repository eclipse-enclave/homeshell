#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Derive the web UI's favicon from the app icon: crop the canvas to the tile and
# drop the tile's shadow, which a browser tab has no room for. Prints the SVG.
# Every edit must match exactly once, so a reworked icon fails here rather than
# producing a wrong crop.
# Usage: favicon.sh <icon.svg>
set -euo pipefail
svg="$1"
icon=$(<"$svg")

replace() {
  local from="$1" to="$2" count
  count=$(grep -cF -- "$from" <<<"$icon" || true)
  if [ "$count" != 1 ]; then
    printf 'favicon.sh: expected one line with %s in %s, found %s\n' "$from" "$svg" "$count" >&2
    exit 1
  fi
  icon=${icon/"$from"/"$to"}
}

replace '<svg xmlns="http://www.w3.org/2000/svg"' \
  $'<!-- Generated from packaging/homeshell.svg by `make icon`. -->\n<svg xmlns="http://www.w3.org/2000/svg"'
replace ' viewBox="0 0 1024 1024" width="1024" height="1024"' ' viewBox="100 100 824 824"'
replace ' filter="url(#shadow)"' ''
printf '%s\n' "$icon"
