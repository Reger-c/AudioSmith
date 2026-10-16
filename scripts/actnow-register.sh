#!/usr/bin/env bash
# Registers a version of AudioSmith on actnow.dev (thread #69), after its
# disk image and update files are uploaded (scripts/release-publish.sh):
# the site's download and changelog pages read what this sends. The
# release workflow runs it; it can be run by hand too.
#
#   POST $ACTNOW_API (https://actnow.dev/api/releases)
#   Authorization: Bearer $ACTNOW_INGEST_TOKEN
#   {app, version, channel, date, notes, dmgKey, dmgSize, sha256,
#    minMacOS, arch, published}
#
# 201 created and 200 updated (the same app, channel and version again) are
# success; anything else fails with the site's answer (never the token).
#
#   scripts/actnow-register.sh --dmg build/darwin-arm64/AudioSmith-0.4.0-beta.5.dmg
#   scripts/actnow-register.sh --dmg … --dry-run   # print the request's body, send nothing
#
# Options (defaults in brackets):
#   --dmg FILE        the disk image as uploaded: its size and SHA-256 (required)
#   --version V       [mygo.json's version]
#   --channel C       beta or stable [beta for a version with a prerelease part]
#   --date YYYY-MM-DD [today, UTC]
#   --dmg-key KEY     its R2 key [audiosmith[/beta]/AudioSmith-<version>.dmg]
#   --unpublished     register it hidden (published: false)
#   --dry-run
# Environment: ACTNOW_INGEST_TOKEN (the site's release-ingest token for
# audiosmith, the GitHub secret of the same name), ACTNOW_API.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=actnow.sh
source "$ROOT/scripts/actnow.sh"
fail() { echo "actnow-register: $*" >&2; exit 1; }
command -v jq > /dev/null || fail "needs jq"
command -v curl > /dev/null || fail "needs curl"

dmg="" version="" channel="" date="" key="" published=true dry=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dmg | --version | --channel | --date | --dmg-key) [[ $# -ge 2 ]] || fail "$1 needs a value" ;;
  esac
  case "$1" in
    --dmg) dmg="${2:-}"; shift ;;
    --version) version="${2:-}"; shift ;;
    --channel) channel="${2:-}"; shift ;;
    --date) date="${2:-}"; shift ;;
    --dmg-key) key="${2:-}"; shift ;;
    --unpublished) published=false ;;
    --dry-run) dry=true ;;
    *) fail "unknown option $1 (see the header of $0)" ;;
  esac
  shift
done

[[ -n "$dmg" ]] || fail "--dmg FILE is required: the disk image that was uploaded"
[[ -f "$dmg" ]] || fail "no disk image $dmg"
version="${version:-$(jq -r .version "$ROOT/mygo.json")}"
channel="${channel:-$(actnow_channel "$version")}"
[[ "$channel" == beta || "$channel" == stable ]] || fail "--channel is \"$channel\", not beta or stable"
date="${date:-$(date -u +%Y-%m-%d)}"
[[ "$date" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || fail "--date \"$date\" is not YYYY-MM-DD"
key="${key:-$(actnow_dmg_key "$version")}"
notes="$(changelog_section "$ROOT/CHANGELOG.md" "$version")"
[[ -n "$notes" ]] || fail "CHANGELOG.md has no \"## $version\" section (or it is empty)"
min_macos="$(jq -r '.macos.minimumSystemVersion // "15.0"' "$ROOT/mygo.json")"

payload="$(jq -n \
  --arg app "$ACTNOW_APP" --arg version "$version" --arg channel "$channel" --arg date "$date" \
  --arg notes "$notes" --arg key "$key" --argjson size "$(file_size "$dmg")" \
  --arg sha "$(file_sha256 "$dmg")" --arg min "$min_macos" --argjson published "$published" \
  '{app: $app, version: $version, channel: $channel, date: $date, notes: $notes,
    dmgKey: $key, dmgSize: $size, sha256: $sha, minMacOS: $min, arch: "arm64", published: $published}')"

if $dry; then
  echo "actnow-register: would POST to $ACTNOW_API:"
  printf '%s\n' "$payload"
  exit 0
fi
token="${ACTNOW_INGEST_TOKEN:-}"
[[ -n "$token" ]] || fail "ACTNOW_INGEST_TOKEN is not set (the site's release-ingest token for audiosmith)"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
# The token goes in a header file (printf is a builtin), never in a
# command line that ps or a log could show.
(umask 077 && printf 'Authorization: Bearer %s\n' "$token" > "$work/auth")
printf '%s' "$payload" > "$work/payload.json"
status=0
code="$(curl -sS --retry 3 --max-time 60 -o "$work/body" -w '%{http_code}' -X POST \
  -H @"$work/auth" -H 'Content-Type: application/json' \
  --data-binary @"$work/payload.json" "$ACTNOW_API")" || status=$?
body="$(cat "$work/body" 2> /dev/null || true)"
body="${body//"$token"/***}"
if [[ $status -ne 0 ]]; then
  fail "could not reach $ACTNOW_API (curl exit $status)"
fi
case "$code" in
  201) echo "actnow-register: registered AudioSmith $version ($channel, $key): created" ;;
  200) echo "actnow-register: registered AudioSmith $version ($channel, $key): updated" ;;
  *)
    why=""
    case "$code" in
      400) why=" (the site refused the body)" ;;
      401) why=" (ACTNOW_INGEST_TOKEN is wrong)" ;;
      403) why=" (ACTNOW_INGEST_TOKEN is not audiosmith's)" ;;
      422) why=" ($key is not in R2: upload the disk image first)" ;;
    esac
    echo "actnow-register: POST $ACTNOW_API answered HTTP $code$why:" >&2
    printf '%s\n' "$body" >&2
    exit 1
    ;;
esac
