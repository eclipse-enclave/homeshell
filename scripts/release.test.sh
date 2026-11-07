#!/usr/bin/env bash
# Black-box tests for the release scripts: version.sh against throwaway git repos,
# github-asset-name.sh, and the artifact manifest check of verify-release-artifacts.sh.
# Run: bash scripts/release.test.sh
set -euo pipefail

scripts="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmproot=$(mktemp -d)
trap 'rm -rf "$tmproot"' EXIT
pass=0
fail=0
unset GITHUB_REF_TYPE GITHUB_REF_NAME GITHUB_RUN_NUMBER

ok() { echo "  ok: $1"; pass=$((pass + 1)); }
bad() {
  echo "  FAIL: $1"
  [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/        /'
  fail=$((fail + 1))
}

# expect <label> <want-output> <command>...: the command succeeds and prints exactly want.
expect() {
  local label="$1" want="$2" out
  shift 2
  if ! out=$("$@" 2>&1); then
    bad "$label (failed)" "$out"
  elif [[ "$out" != "$want" ]]; then
    bad "$label (want '$want')" "$out"
  else
    ok "$label"
  fi
}

# expect_fail <label> <output-substring> <command>...: the command fails mentioning substring.
expect_fail() {
  local label="$1" want="$2" out
  shift 2
  if out=$("$@" 2>&1); then
    bad "$label (succeeded)" "$out"
  elif [[ "$out" != *"$want"* ]]; then
    bad "$label (output lacks '$want')" "$out"
  else
    ok "$label"
  fi
}

# new_repo: a git repo with one commit and scripts/version.sh, cd into it.
new_repo() {
  repo="$tmproot/repo-$RANDOM$RANDOM"
  mkdir -p "$repo/scripts"
  cp "$scripts/version.sh" "$repo/scripts/"
  cd "$repo"
  git init -q
  git -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m one
}

commit() { git -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m "$1"; }
short() { git rev-parse --short HEAD; }
short8() { git rev-parse --short=8 HEAD; }
day() { TZ=UTC git show -s --format=%cd --date=format-local:%Y%m%d HEAD; }

echo "version.sh"

new_repo
expect "before any tag: baseline" "0.0.0-g$(short)" scripts/version.sh
expect "before any tag: package snapshot" "0.0.1~~git.$(day).$(short8)" scripts/version.sh --package

git tag v1.2.0
expect "on a release tag" "1.2.0" scripts/version.sh
expect "on a release tag: package" "1.2.0" scripts/version.sh --package

commit two
expect "after a release" "1.2.0-1-g$(short)" scripts/version.sh
expect "after a release: package sorts below 1.2.1" "1.2.1~~git.$(day).$(short8)" \
  scripts/version.sh --package
expect "run number is part of the snapshot" "1.2.1~~git.$(day).7.$(short8)" \
  env GITHUB_RUN_NUMBER=7 scripts/version.sh --package

git tag v1.3.0-rc.1
expect "on a pre-release tag" "1.3.0-rc.1" scripts/version.sh
expect "pre-release package uses ~" "1.3.0~rc.1" scripts/version.sh --package
commit three
expect "after a pre-release: package sorts above it" "1.3.0~rc.1.git.$(day).$(short8)" \
  scripts/version.sh --package

git tag v1.3.0
git tag v1.3.0-rc.2
expect "the triggering tag wins on a commit with two" "1.3.0" \
  env GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v1.3.0 scripts/version.sh
expect "the triggering pre-release tag wins too" "1.3.0-rc.2" \
  env GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v1.3.0-rc.2 scripts/version.sh

new_repo
git tag rolling
expect "the rolling tag is ignored" "0.0.0-g$(short)" scripts/version.sh
for tag in v1.2.3-beta v01.2.3 v1.2.3-preview.1 v1.2.3.4; do
  new_repo
  git tag "$tag"
  expect_fail "tag $tag is refused" "is not a release tag" scripts/version.sh
done
expect_fail "unknown flag is refused" "usage" scripts/version.sh --bogus

echo "github-asset-name.sh"

expect "versionless name is kept" "homeshell-linux-amd64.tar.gz" \
  "$scripts/github-asset-name.sh" homeshell-linux-amd64.tar.gz
expect "snapshot deb: ~~ becomes one dot" "homeshell_1.0.1.git.20261106.abcdef12-1_amd64.deb" \
  "$scripts/github-asset-name.sh" "homeshell_1.0.1~~git.20261106.abcdef12-1_amd64.deb"
expect "pre-release rpm" "homeshell-1.3.0.rc.1-1.x86_64.rpm" \
  "$scripts/github-asset-name.sh" "homeshell-1.3.0~rc.1-1.x86_64.rpm"
expect "leading and trailing dots are dropped" "x" "$scripts/github-asset-name.sh" ".x~"

echo "verify-release-artifacts.sh"

assets="$tmproot/assets"
mkdir -p "$assets"
printf 'a' > "$assets/homeshell-linux-amd64.tar.gz"
printf 'b' > "$assets/HomeShell-1.0.0-arm64.dmg"
(cd "$assets" && sha256sum -- * > checksums.txt)
sum=$(sha256sum "$assets/checksums.txt" | cut -d' ' -f1)
expect "complete manifest passes" "" "$scripts/verify-release-artifacts.sh" "$assets" "$sum"
expect_fail "another build's manifest fails" "differs from the original build manifest" \
  "$scripts/verify-release-artifacts.sh" "$assets" "$(printf '0%.0s' {1..64})"
printf 'x' > "$assets/extra"
expect_fail "an extra file fails" "release files differ" \
  "$scripts/verify-release-artifacts.sh" "$assets" "$sum"
rm "$assets/extra" "$assets/HomeShell-1.0.0-arm64.dmg"
expect_fail "a missing file fails" "release files differ" \
  "$scripts/verify-release-artifacts.sh" "$assets" "$sum"

echo "release-notes.sh"

expect_fail "unknown channel is refused" "unknown channel" \
  "$scripts/release-notes.sh" nightly abc https://github.com/o/r/releases/download/rolling
notes=$("$scripts/release-notes.sh" stable 1.0.0 https://github.com/o/r/releases/download/v1.0.0 abcdef0)
if [[ "$notes" == *"https://download.eclipse.org/enclave/homeshell/releases/1.0.0/"* ]]; then
  ok "stable notes link the release directory"
else
  bad "stable notes link the release directory" "$notes"
fi
if [[ "$notes" == *"Open Anyway"* && "$notes" == *'`xattr -dr com.apple.quarantine'* ]]; then
  ok "notes explain opening the unsigned app on current macOS"
else
  bad "notes explain opening the unsigned app on current macOS" "$notes"
fi

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
