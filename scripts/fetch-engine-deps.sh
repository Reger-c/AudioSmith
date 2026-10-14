#!/usr/bin/env bash
# Prepares AudioSmith's thin fork of mlx-audio-swift for the engine build:
# upstream Blaizzy/mlx-audio-swift at the pinned commit, plus the patch
# series in engine/patches/mlx-audio-swift/ (applied in name order), in
# engine/.build/deps/mlx-audio-swift, which engine/Package.swift uses as a
# local package. It does nothing when the checkout already matches the pin
# and the patches (a stamp file records both).
#
# build-engine.sh and test-engine.sh run it; run it by hand before opening
# engine/Package.swift in Xcode. To move the pin: change COMMIT, check the
# patches still apply (git apply --check), rebuild, run smoke-models.sh.
set -euo pipefail

UPSTREAM="https://github.com/Blaizzy/mlx-audio-swift.git"
COMMIT="244458d7b61e54ff135073ddc9ed76782bb20758" # 2026-10-10, "Fix parallel test runner. (#282)"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PATCHES="$ROOT/engine/patches/mlx-audio-swift"
DIR="$ROOT/engine/.build/deps/mlx-audio-swift"
STAMP="$DIR/.audiosmith-stamp"

want="$( (echo "$COMMIT"; cat "$PATCHES"/*.patch) | shasum -a 256 | cut -d' ' -f1)"
if [[ -f "$STAMP" && "$(cat "$STAMP")" == "$want" ]]; then
  exit 0
fi

echo "preparing mlx-audio-swift ${COMMIT:0:7} + $(ls "$PATCHES"/*.patch | wc -l | tr -d ' ') patches in ${DIR#$ROOT/}"
rm -rf "$DIR"
mkdir -p "$DIR"
cd "$DIR"
git init -q
git remote add origin "$UPSTREAM"
git fetch -q --depth 1 origin "$COMMIT"
git checkout -q --detach FETCH_HEAD
for p in "$PATCHES"/*.patch; do
  git apply --whitespace=nowarn "$p" || { echo "patch $(basename "$p") does not apply" >&2; exit 1; }
done
echo "$want" > "$STAMP"
