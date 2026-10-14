#!/usr/bin/env bash
# Runs the engine's Swift tests (FixtureTests: engine/Fixtures decoded and
# re-encoded with the Swift protocol types) with xcodebuild, sharing the
# derived data of scripts/build-engine.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DERIVED="$ROOT/engine/.build/xcode"
LOG="$DERIVED/test.log"
"$ROOT/scripts/fetch-engine-deps.sh"
mkdir -p "$DERIVED"
cd "$ROOT/engine"
status=0
xcodebuild test \
  -scheme AudioSmithEngine \
  -only-testing:AudioSmithEngineTests \
  -destination 'platform=macOS,arch=arm64' \
  -derivedDataPath "$DERIVED" \
  -skipPackagePluginValidation \
  -skipMacroValidation \
  -onlyUsePackageVersionsFromResolvedFile \
  CODE_SIGNING_ALLOWED=NO \
  > "$LOG" 2>&1 || status=$?
grep -E "error:|Test Case .*(passed|failed)|XCTAssert|Executed|\*\* TEST" "$LOG" | sort -u | head -60 || true
[[ $status -eq 0 ]] || echo "tests failed, see ${LOG#$ROOT/}" >&2
exit $status
