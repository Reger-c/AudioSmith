#!/usr/bin/env bash
# Builds a release on this Mac the way .github/workflows/release.yml does on
# GitHub's: the engine, then `go tool mygo build` signed with your Developer
# ID, notarized, in a disk image, with the signed update files, then
# scripts/verify-release.sh. The keys come from this Mac (docs/release.md).
#
#   scripts/release-local.sh                 # signed + notarized + updates, in build/darwin-arm64
#   scripts/release-local.sh --upload        # and upload to the draft GitHub release v<version> (gh)
#   scripts/release-local.sh --skip-notarize # signed, not notarized (quicker, for checking the signing)
#   scripts/release-local.sh --dry-run       # ad hoc, no notarization, no updates: what CI's dry run makes
#   add --skip-engine to reuse resources/darwin-arm64/engine
#
# From the environment, each with a default:
#   MACOS_SIGNING_IDENTITY   default: the first "Developer ID Application" identity
#                            of the keychains (security find-identity)
#   NOTARY_PROFILE           default "notary" (xcrun notarytool store-credentials notary)
#   MYGO_UPDATER_PRIVATE_KEY default: the contents of mygo-update.key in KEYS_DIR
#   MYGO_UPDATER_PUBLIC_KEY  default: the contents of mygo-update.pub in KEYS_DIR
#   KEYS_DIR                 default: where mygo keygen writes them,
#                            ~/Library/Application Support/mygo/update-keys
#
# mygo.json is edited for the build (scripts/release-config.sh) and put back
# afterwards, whatever happens.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
dry=false upload=false notarize=true engine=true
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry=true ;;
    --upload) upload=true ;;
    --skip-notarize) notarize=false ;;
    --skip-engine) engine=false ;;
    *) echo "unknown option $arg (see the header of $0)" >&2; exit 2 ;;
  esac
done
fail() { echo "release-local: $*" >&2; exit 1; }

[[ "$(uname -s)/$(uname -m)" == Darwin/arm64 ]] || fail "builds on a Mac with Apple silicon only"
export PATH="/opt/homebrew/bin:$PATH" GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
command -v jq > /dev/null || fail "needs jq"
version="$(jq -r .version mygo.json)"
grep -Eq "^## \[?$version([^0-9.]|$)" CHANGELOG.md || fail "CHANGELOG.md has no \"## $version\" section: the update window shows it"
if ! git diff --quiet HEAD 2> /dev/null; then
  echo "release-local: note: the working tree has changes; the build includes them" >&2
fi

if $dry; then
  unset MACOS_SIGNING_IDENTITY NOTARY_PROFILE MYGO_UPDATER_PRIVATE_KEY MYGO_UPDATER_PUBLIC_KEY
  $upload && fail "--dry-run does not upload"
else
  if [[ -z "${MACOS_SIGNING_IDENTITY:-}" ]]; then
    MACOS_SIGNING_IDENTITY="$(security find-identity -v -p codesigning | sed -n 's/.*"\(Developer ID Application: .*\)"/\1/p' | head -1)"
    [[ -n "$MACOS_SIGNING_IDENTITY" ]] || fail "no Developer ID Application identity in the keychains (docs/release.md, step 1)"
  fi
  export MACOS_SIGNING_IDENTITY
  if $notarize; then
    export NOTARY_PROFILE="${NOTARY_PROFILE:-notary}"
  else
    unset NOTARY_PROFILE
  fi
  keys="${KEYS_DIR:-$HOME/Library/Application Support/mygo/update-keys}"
  if [[ -z "${MYGO_UPDATER_PRIVATE_KEY:-}" && -f "$keys/mygo-update.key" ]]; then
    MYGO_UPDATER_PRIVATE_KEY="$(cat "$keys/mygo-update.key")"
  fi
  if [[ -z "${MYGO_UPDATER_PUBLIC_KEY:-}" && -f "$keys/mygo-update.pub" ]]; then
    MYGO_UPDATER_PUBLIC_KEY="$(cat "$keys/mygo-update.pub")"
  fi
  [[ -n "${MYGO_UPDATER_PRIVATE_KEY:-}" && -n "${MYGO_UPDATER_PUBLIC_KEY:-}" ]] ||
    fail "no update keys: run go tool mygo keygen (docs/release.md, step 3), or set MYGO_UPDATER_PRIVATE_KEY and MYGO_UPDATER_PUBLIC_KEY"
  export MYGO_UPDATER_PRIVATE_KEY MYGO_UPDATER_PUBLIC_KEY
fi

if $engine || [[ ! -x resources/darwin-arm64/engine/audiosmith-engine ]]; then
  scripts/build-engine.sh Release
fi

backup="$(mktemp)"
cp mygo.json "$backup"
trap 'cp "$backup" mygo.json; rm -f "$backup"' EXIT
scripts/release-config.sh

flags=()
$upload && flags+=(-upload)
$notarize || flags+=(-skip-notarize)
rm -rf build/darwin-arm64
go tool mygo build -platform darwin/arm64 ${flags[@]+"${flags[@]}"}

if $dry || ! $notarize; then
  scripts/verify-release.sh
else
  scripts/verify-release.sh --release
fi
$upload && echo "Uploaded to the draft release v$version; publish it on GitHub, or: gh release edit v$version --draft=false"
exit 0
