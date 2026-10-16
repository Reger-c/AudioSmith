#!/usr/bin/env bash
# Writes a release's settings into mygo.json, in place, for `mygo build`:
# the repository keeps none of them, so builds made without them (go run,
# mygo dev, scripts/mac.sh, CI's dry runs) are signed ad hoc and never
# update themselves (mygo.Updater.Enabled() is false). The release
# workflow (.github/workflows/release.yml) and scripts/release-local.sh
# run it on a copy they throw away or restore; never commit its output.
#
# Updates come from actnow.dev (thread #69, scripts/actnow.sh): the app
# reads https://actnow.dev/updates/audiosmith/update-darwin-arm64.json, and
# with Receive beta updates on the beta/ one beside it too (MyGo patch
# 0008); `mygo publish` uploads to the R2 bucket that the site serves
# (updates.s3; credentials in AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY at
# upload time, release-publish.sh). Up to 0.4.0-beta.4 the app read GitHub
# releases (UPDATES_FEED=github).
#
# From the environment, each optional:
#   MYGO_UPDATER_PUBLIC_KEY  contents of mygo-update.pub (mygo keygen): adds
#                            "updates": {publicKey, url, s3} (the website)
#   UPDATES_FEED             website (default), or github: {publicKey, github:
#                            UPDATES_GITHUB}, the feed of 0.4.0-beta.1 … beta.4
#   UPDATES_GITHUB           default hehehai/AudioSmith
#   MACOS_SIGNING_IDENTITY   "Developer ID Application: Name (TEAMID)" or
#                            its SHA-1: macos.signingIdentity
#   NOTARY_PROFILE           a notarytool keychain profile: macos.notarize
#   NOTARY_KEYCHAIN          the keychain holding it (CI's temporary one)
#
# GITHUB_FEED (release.yml's transition toggle) changes nothing here: the
# app always reads the website; release-publish.sh puts a copy of the
# update on GitHub for the copies that still read it.
#
#   scripts/release-config.sh            # edit mygo.json
#   scripts/release-config.sh --print    # print the result, change nothing
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CONFIG="$ROOT/mygo.json"
# shellcheck source=actnow.sh
source "$ROOT/scripts/actnow.sh"
command -v jq > /dev/null || { echo "release-config.sh needs jq" >&2; exit 1; }

key="$(printf '%s' "${MYGO_UPDATER_PUBLIC_KEY:-}" | tr -d '[:space:]')"
if [[ -n "$key" ]]; then
  # An Ed25519 public key: 32 bytes in base64, as mygo keygen writes it.
  len="$(printf '%s' "$key" | base64 -d 2> /dev/null | wc -c | tr -d ' ')" || len=0
  if [[ "$len" != 32 ]]; then
    echo "MYGO_UPDATER_PUBLIC_KEY is not the contents of mygo-update.pub (an Ed25519 public key in base64)" >&2
    exit 1
  fi
fi

feed="${UPDATES_FEED:-website}"
case "$feed" in
  website | github) ;;
  *) echo "UPDATES_FEED is \"$feed\", not website or github" >&2; exit 1 ;;
esac

result="$(jq \
  --arg key "$key" \
  --arg feed "$feed" \
  --arg github "${UPDATES_GITHUB:-hehehai/AudioSmith}" \
  --arg url "$ACTNOW_UPDATES_URL" \
  --arg bucket "$ACTNOW_R2_BUCKET" \
  --arg prefix "$ACTNOW_R2_PREFIX" \
  --arg endpoint "$ACTNOW_R2_ENDPOINT" \
  --arg identity "${MACOS_SIGNING_IDENTITY:-}" \
  --arg profile "${NOTARY_PROFILE:-}" \
  --arg keychain "${NOTARY_KEYCHAIN:-}" '
  (if $key == "" then .
    elif $feed == "github" then .updates = ((.updates // {}) | del(.url, .s3)) + {publicKey: $key, github: $github}
    else .updates = ((.updates // {}) | del(.github, .tagPrefix)) + {publicKey: $key, url: $url,
      s3: {bucket: $bucket, prefix: $prefix, endpoint: $endpoint, region: "auto"}}
    end)
  | (if $identity != "" then .macos.signingIdentity = $identity else . end)
  | (if $profile != "" then .macos.notarize = ({keychainProfile: $profile}
      + (if $keychain != "" then {keychain: $keychain} else {} end)) else . end)
' "$CONFIG")"

if [[ "${1:-}" == "--print" ]]; then
  printf '%s\n' "$result"
  exit 0
fi
printf '%s\n' "$result" > "$CONFIG"
echo "mygo.json: updates $([[ -n "$key" ]] && echo "on ($feed)" || echo off)," \
  "signing ${MACOS_SIGNING_IDENTITY:-ad hoc}," \
  "notarization $([[ -n "${NOTARY_PROFILE:-}" ]] && echo on || echo off)"
