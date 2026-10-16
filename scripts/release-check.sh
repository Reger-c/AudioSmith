#!/usr/bin/env bash
# Checks a release before it builds, for .github/workflows/release.yml and
# scripts/release-local.sh: the version of mygo.json has its own section in
# CHANGELOG.md, whose heading starts with exactly that version ("## 0.4.0",
# "## 0.4.0-beta.1 — 2026-10-16", "## [0.4.0]"), as mygo build needs for the
# release notes the update window shows; and with a tag, the tag is
# v<version>. It prints the version and whether it is a beta, as
# VERSION=… and PRERELEASE=true|false lines (for $GITHUB_ENV).
#
# A version with a prerelease part (0.4.0-beta.1, 0.4.0-rc.1) is a beta: a
# GitHub prerelease, which only copies with Receive beta updates on take
# (docs/release.md, "Betas and releases").
#
#   scripts/release-check.sh            # the version and the changelog
#   scripts/release-check.sh v0.4.0     # and the tag
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
fail() { echo "release-check: $*" >&2; exit 1; }
command -v jq > /dev/null || fail "needs jq"

version="$(jq -r .version mygo.json)"
# Semantic versioning: three numbers, then optionally -<prerelease>.
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || fail "mygo.json's version \"$version\" is not like 0.4.0 or 0.4.0-beta.1"
if [[ $# -gt 0 && "$1" != "v$version" ]]; then
  fail "the tag $1 is not v$version, the version in mygo.json"
fi
# The first word of a "## " heading, without Keep a Changelog's brackets,
# is the version, exactly: "## 0.4.0-beta.1" is not 0.4.0's section.
if ! awk -v v="$version" '
  $1 == "##" { h = $2; sub(/^\[/, "", h); sub(/\].*$/, "", h); sub(/^v/, "", h); if (h == v) found = 1 }
  END { exit !found }' CHANGELOG.md; then
  fail "CHANGELOG.md has no \"## $version\" section, which the update window shows"
fi
prerelease=false
[[ "$version" == *-* ]] && prerelease=true
echo "VERSION=$version"
echo "PRERELEASE=$prerelease"
