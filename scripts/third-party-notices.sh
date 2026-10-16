#!/usr/bin/env bash
# Writes THIRD-PARTY-NOTICES.txt, the licences and notices of everything
# third-party the app links or carries, which the app bundle ships (mygo.json
# resources) and Settings → Diagnostics and about opens. Run it after a
# dependency changes (go.mod, engine/Package.resolved, the mlx-audio-swift pin
# in fetch-engine-deps.sh, MyGo, RapiDoc, the voice previews) and before
# tagging a release; TestNoticesCurrent fails until you do.
#
#   scripts/third-party-notices.sh           regenerate the file
#   scripts/third-party-notices.sh --check   exit 1 if it is out of date
#   scripts/third-party-notices.sh --fetch   first fetch the Swift packages'
#                                            licence texts at their pins into
#                                            third_party/licenses/swift (git,
#                                            network; after a Swift pin moved)
#
# The generator is internal/tools/thirdparty (its doc comment says how it
# finds the components); third_party/licenses/README.md describes the
# committed texts.
set -euo pipefail
cd "$(dirname "$0")/.."
args=()
for a in "$@"; do
	case "$a" in
	--check) args+=(-check) ;;
	--fetch) args+=(-fetch) ;;
	*) echo "usage: $0 [--check | --fetch]" >&2; exit 2 ;;
	esac
done
exec go run ./internal/tools/thirdparty ${args[@]+"${args[@]}"}
