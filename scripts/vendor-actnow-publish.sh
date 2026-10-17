#!/usr/bin/env bash
# scripts/actnow-publish.sh is actnow.dev's release script, as the site
# serves it (https://actnow.dev/actnow-publish.sh; its source is
# scripts/actnow-publish.sh of hehehai/actnow-www, its contract
# docs/release-pipeline.md there), committed here byte for byte so that a
# release runs the copy that was reviewed, offline in tests and the same in
# CI and on a Mac (thread #71). Never edit it: refresh it with this script
# and review the diff.
#
#   scripts/vendor-actnow-publish.sh          # download it anew into scripts/
#   scripts/vendor-actnow-publish.sh --check  # fail (exit 1, with the diff) when the site's differs
#
# The release workflow runs --check before the build and only warns: a
# changed script on the site is not an error by itself (the site keeps the
# API the copy here speaks, or tells why it refuses).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
URL="${ACTNOW_PUBLISH_URL:-https://actnow.dev/actnow-publish.sh}"
DEST="$ROOT/scripts/actnow-publish.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
curl -fsSL --retry 3 "$URL" -o "$work/actnow-publish.sh" || { echo "cannot download $URL" >&2; exit 2; }
head -1 "$work/actnow-publish.sh" | grep -q '^#!/usr/bin/env bash' || { echo "$URL is not a bash script" >&2; exit 2; }

sum() { if command -v sha256sum > /dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'; }
if [[ "${1:-}" == "--check" ]]; then
  if cmp -s "$work/actnow-publish.sh" "$DEST"; then
    echo "scripts/actnow-publish.sh is the site's ($(sum "$DEST" | cut -c1-12))"
    exit 0
  fi
  echo "scripts/actnow-publish.sh ($(sum "$DEST" | cut -c1-12)) differs from $URL ($(sum "$work/actnow-publish.sh" | cut -c1-12)):" >&2
  diff -u "$DEST" "$work/actnow-publish.sh" | head -60 >&2 || true
  echo "refresh it with scripts/vendor-actnow-publish.sh and review the diff" >&2
  exit 1
fi
cp "$work/actnow-publish.sh" "$DEST"
chmod 755 "$DEST"
echo "scripts/actnow-publish.sh: $(sum "$DEST") from $URL"
