#!/usr/bin/env bash
# Writes a release's settings into mygo.json, in place, for `mygo build`:
# the repository keeps none of them, so builds made without them (go run,
# mygo dev, scripts/mac.sh, CI's dry runs) are signed ad hoc and never
# update themselves (mygo.Updater.Enabled() is false). The release
# workflow (.github/workflows/release.yml) and scripts/release-local.sh
# run it on a copy they throw away or restore; never commit its output.
#
# From the environment, each optional:
#   MYGO_UPDATER_PUBLIC_KEY  contents of mygo-update.pub (mygo keygen): adds
#                            "updates": {publicKey, github: UPDATES_GITHUB}
#   UPDATES_GITHUB           default hehehai/AudioSmith
#   MACOS_SIGNING_IDENTITY   "Developer ID Application: Name (TEAMID)" or
#                            its SHA-1: macos.signingIdentity
#   NOTARY_PROFILE           a notarytool keychain profile: macos.notarize
#   NOTARY_KEYCHAIN          the keychain holding it (CI's temporary one)
#
#   scripts/release-config.sh            # edit mygo.json
#   scripts/release-config.sh --print    # print the result, change nothing
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CONFIG="$ROOT/mygo.json"
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

result="$(jq \
  --arg key "$key" \
  --arg github "${UPDATES_GITHUB:-hehehai/AudioSmith}" \
  --arg identity "${MACOS_SIGNING_IDENTITY:-}" \
  --arg profile "${NOTARY_PROFILE:-}" \
  --arg keychain "${NOTARY_KEYCHAIN:-}" '
  (if $key != "" then .updates = ((.updates // {}) + {publicKey: $key, github: $github}) else . end)
  | (if $identity != "" then .macos.signingIdentity = $identity else . end)
  | (if $profile != "" then .macos.notarize = ({keychainProfile: $profile}
      + (if $keychain != "" then {keychain: $keychain} else {} end)) else . end)
' "$CONFIG")"

if [[ "${1:-}" == "--print" ]]; then
  printf '%s\n' "$result"
  exit 0
fi
printf '%s\n' "$result" > "$CONFIG"
echo "mygo.json: updates $([[ -n "$key" ]] && echo on || echo off)," \
  "signing ${MACOS_SIGNING_IDENTITY:-ad hoc}," \
  "notarization $([[ -n "${NOTARY_PROFILE:-}" ]] && echo on || echo off)"
