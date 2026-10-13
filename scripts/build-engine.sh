#!/usr/bin/env bash
# Builds audiosmith-engine with xcodebuild (needed for MLX's Metal library)
# and stages it, with the resource bundles it loads at run time, into
# resources/darwin-arm64/engine/, which mygo dev and mygo build ship inside
# AudioSmith.app/Contents/Resources/engine/.
#
# Usage: scripts/build-engine.sh [Release|Debug]
set -euo pipefail

CONFIG="${1:-Release}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ENGINE="$ROOT/engine"
DERIVED="$ENGINE/.build/xcode"
STAGE="$ROOT/resources/darwin-arm64/engine"

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  echo "audiosmith-engine needs macOS on Apple silicon (MLX)." >&2
  exit 1
fi

PRODUCTS="$DERIVED/Build/Products/$CONFIG"
rm -f "$PRODUCTS/audiosmith-engine"
mkdir -p "$DERIVED"
LOG="$DERIVED/build-$CONFIG.log"

cd "$ENGINE"
echo "building audiosmith-engine ($CONFIG), full log: ${LOG#$ROOT/}"
xcodebuild build \
  -scheme AudioSmithEngine \
  -configuration "$CONFIG" \
  -destination 'platform=macOS,arch=arm64' \
  -derivedDataPath "$DERIVED" \
  -skipPackagePluginValidation \
  -skipMacroValidation \
  CODE_SIGNING_ALLOWED=NO \
  > "$LOG" 2>&1 || true
grep -E "error:|\*\* BUILD" "$LOG" | sort -u | head -40 || true

if [[ ! -x "$PRODUCTS/audiosmith-engine" ]]; then
  echo "build failed, see $LOG" >&2
  exit 1
fi

rm -rf "$STAGE"
mkdir -p "$STAGE"
cp "$PRODUCTS/audiosmith-engine" "$STAGE/"
# SwiftPM resource bundles (MLX's default.metallib lives in mlx-swift_Cmlx.bundle).
for bundle in "$PRODUCTS"/*.bundle; do
  [[ -e "$bundle" ]] && cp -R "$bundle" "$STAGE/"
done
# Dynamic frameworks, if a dependency produced any.
if compgen -G "$PRODUCTS/PackageFrameworks/*.framework" > /dev/null; then
  mkdir -p "$STAGE/PackageFrameworks"
  cp -R "$PRODUCTS"/PackageFrameworks/*.framework "$STAGE/PackageFrameworks/"
fi

echo "staged $(du -sh "$STAGE" | cut -f1) into ${STAGE#$ROOT/}:"
ls "$STAGE"
