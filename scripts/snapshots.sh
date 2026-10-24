#!/usr/bin/env bash
# Renders the UI snapshots (every TestSnapshots* test) and compares them,
# for refactors that must not change a pixel (refactor batch B0). The
# snapshots are reproducible: two runs of one commit write the same bytes
# (snapshot_env_test.go pins the clock, the data directories, the free disk
# space and the ports).
#
#   scripts/snapshots.sh render DIR   render into DIR (emptied first)
#   scripts/snapshots.sh check [BASE] render BASE (default: the merge base
#                                     with main) in a temporary worktree and
#                                     the working tree, and compare them
#   scripts/snapshots.sh twice        render the working tree twice and
#                                     compare (is it reproducible?)
#
# check and twice exit 1 when a snapshot differs and leave the pictures of
# the differences (before | after | red pixels) in $SNAP_TMP/diff; they
# delete the renders (about 300 MB each) unless SNAP_KEEP=1. SNAP_TMP
# defaults to /tmp/audiosmith-snapcheck-$USER-<checkout>, one per worktree,
# so checks run at once in different worktrees do not share it. Rendering runs under
# nice -n 19 (the Linux host is shared) and takes about two minutes.
#
# The comparison is go run ./internal/tools/snapcmp BEFORE AFTER, with the
# few snapshots allowed to differ a little listed, with the reason, in
# scripts/snapshots-volatile.txt.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="${SNAP_TMP:-/tmp/audiosmith-snapcheck-${USER:-$(id -u)}-$(basename "$root")}"

# main_pkg SRC_DIR: the app's package in a checkout, ./app (refactor
# B13), or . in a commit from before it moved, so check can compare with
# those too.
main_pkg() {
	if [ -f "$1/app/main.go" ]; then echo ./app; else echo .; fi
}

render() { # render SRC_DIR OUT_DIR
	local src="$1" out="$2"
	rm -rf "$out"
	mkdir -p "$out"
	echo "rendering $(git -C "$src" rev-parse --short HEAD)$(git -C "$src" diff --quiet HEAD -- . 2>/dev/null || echo '+changes') into $out" >&2
	(cd "$src" && AUDIOSMITH_SNAPSHOTS="$out" nice -n 19 env GOMAXPROCS=4 CGO_ENABLED=0 \
		go test -count=1 -timeout 30m -run TestSnapshots "$(main_pkg "$src")" >&2)
	echo "$(find "$out" -type f | wc -l) files" >&2
}

compare() { # compare BEFORE AFTER
	rm -rf "$tmp/diff"
	local status=0
	(cd "$root" && go run ./internal/tools/snapcmp -volatile scripts/snapshots-volatile.txt -out "$tmp/diff" "$1" "$2") || status=$?
	if [ "$status" = 1 ]; then
		echo "differences: $tmp/diff" >&2
	fi
	return "$status"
}

cleanup() {
	if [ "${SNAP_KEEP:-}" != 1 ]; then
		rm -rf "$tmp/before" "$tmp/after"
	fi
	rmdir "$tmp/diff" "$tmp" 2>/dev/null || true
	if [ -n "${base_tree:-}" ]; then
		git -C "$root" worktree remove --force "$base_tree" || true
	fi
}

case "${1:-}" in
render)
	[ $# = 2 ] || { echo "usage: $0 render DIR" >&2; exit 2; }
	render "$root" "$(realpath -m "$2")"
	;;
check)
	base="${2:-$(git -C "$root" merge-base HEAD main)}"
	mkdir -p "$tmp"
	base_tree="$tmp/base"
	trap cleanup EXIT
	git -C "$root" worktree remove --force "$base_tree" 2>/dev/null || rm -rf "$base_tree"
	git -C "$root" worktree add --detach "$base_tree" "$base" >&2
	render "$base_tree" "$tmp/before"
	render "$root" "$tmp/after"
	compare "$tmp/before" "$tmp/after"
	;;
twice)
	mkdir -p "$tmp"
	trap cleanup EXIT
	render "$root" "$tmp/before"
	render "$root" "$tmp/after"
	compare "$tmp/before" "$tmp/after"
	;;
*)
	sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
	;;
esac
