#!/usr/bin/env bash
# Publishes the release that `go tool mygo build` made in build/darwin-arm64
# and scripts/verify-release.sh checked (thread #69): .github/workflows/
# release.yml runs it after both, and scripts/release-local.sh --upload.
#
#   1. Renames the disk image to AudioSmith-<version>.dmg, its name on the
#      website (scripts/actnow.sh).
#   2. Uploads to actnow.dev's R2 bucket with `go tool mygo publish` (MyGo
#      patch 0009): the disk image, the update archive and deltas, then the
#      manifest, which publishes the update. A beta goes under
#      audiosmith/beta/, a release under audiosmith/ and also writes
#      beta/'s manifest unless a newer beta is there (patch 0008).
#   3. Registers the version on the site (scripts/actnow-register.sh), which
#      lists it on its download and changelog pages.
#   4. GitHub: a draft release v<version> (a prerelease for a beta) with
#      the notes and links to the download page, plus update-darwin-arm64.json
#      for copies that still read GitHub (0.4.0-beta.1 … beta.4):
#      - GITHUB_FEED=true (the transition): the update archive and deltas
#        too, and the manifest points at them on GitHub;
#      - otherwise the website's manifest as it is, pointing at actnow.dev,
#        so a copy that missed the transition still finds the newest version.
#      Publish the draft on GitHub after checking it (copies that read
#      GitHub see it then; the website has it at once).
#
# Each step can run again: uploads replace, the registration updates.
#
#   scripts/release-publish.sh                # all four
#   scripts/release-publish.sh --dry-run      # 1, the GitHub files in build/github and the
#                                             # registration's body; uploads nothing
#   scripts/release-publish.sh --skip-github  # 1–3
#
# Environment:
#   R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY  the R2 API token (read/write on actnow-releases),
#                                           or AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
#   ACTNOW_INGEST_TOKEN                     the site's release-ingest token for audiosmith
#   GITHUB_FEED                             true during the transition (release.yml)
#   GITHUB_REPOSITORY                       default hehehai/AudioSmith; gh needs GH_TOKEN or gh auth
#   MYGO                                    default "go tool mygo" (tests replace it)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
# shellcheck source=actnow.sh
source "$ROOT/scripts/actnow.sh"
fail() { echo "release-publish: $*" >&2; exit 1; }
command -v jq > /dev/null || fail "needs jq"

dry=false github=true
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry=true ;;
    --skip-github) github=false ;;
    *) fail "unknown option $arg (see the header of $0)" ;;
  esac
done
github_feed="${GITHUB_FEED:-false}"
[[ "$github_feed" == true || "$github_feed" == false ]] || fail "GITHUB_FEED is \"$github_feed\", not true or false"
repo="${GITHUB_REPOSITORY:-hehehai/AudioSmith}"
read -r -a mygo <<< "${MYGO:-go tool mygo}"

OUT="build/darwin-arm64"
version="$(jq -r .version mygo.json)"
tag="v$version"
manifest="$OUT/update-darwin-arm64.json"

# 1. The disk image's name on the website.
dmg="$OUT/$(actnow_dmg_name "$version")"
if [[ -f "$OUT/AudioSmith $version arm64.dmg" ]]; then
  mv "$OUT/AudioSmith $version arm64.dmg" "$dmg"
fi
[[ -f "$dmg" ]] || fail "no disk image $OUT/AudioSmith $version arm64.dmg (build first: go tool mygo build -platform darwin/arm64)"
echo "release-publish: $dmg → R2 $(actnow_dmg_key "$version")"

if [[ -f "$manifest" ]]; then
  jq -e --arg v "$version" '.version == $v' "$manifest" > /dev/null || fail "$manifest is not of $version: build again"
  url="$(jq -r .url "$manifest")"
  [[ "$url" == "$ACTNOW_UPDATES_URL/"* ]] ||
    fail "$manifest points at $url, not at $ACTNOW_UPDATES_URL: build with scripts/release-config.sh (UPDATES_FEED=website)"
elif ! $dry; then
  fail "no $manifest: a release needs the update keys (MYGO_UPDATER_PRIVATE_KEY, MYGO_UPDATER_PUBLIC_KEY)"
fi

# 4's files, made first so that a dry run shows them: build/github.
notes_file="build/github-notes.md"
gh_dir="build/github"
rm -rf "$gh_dir"
gh_files=()
if $github; then
  mkdir -p "$gh_dir"
  {
    changelog_section CHANGELOG.md "$version"
    echo
    echo "---"
    echo
    echo "**Download:** [actnow.dev/download/audiosmith]($ACTNOW_DOWNLOAD_URL) (a free account) · [Changelog]($ACTNOW_CHANGELOG_URL) · or build it from this tag's source (AGPL-3.0)."
    if [[ -f "$manifest" ]]; then
      echo
      echo "Installed copies update themselves: AudioSmith → Check for Updates…"
    fi
  } > "$notes_file"
  if [[ -f "$manifest" ]]; then
    if [[ "$github_feed" == true ]]; then
      # The transition: copies that read GitHub get the archive (and the
      # deltas) from this release. Only the archives are signed, not the
      # manifest, so a second manifest of the same archive is as good.
      base="https://github.com/$repo/releases/download/$tag/"
      for f in "$(jq -r .url "$manifest")" $(jq -r '.deltas[]?.url' "$manifest"); do
        name="${f##*/}"
        [[ -f "$OUT/$name" ]] || fail "no $OUT/$name, which $manifest names"
        cp "$OUT/$name" "$gh_dir/$name"
        gh_files+=("$gh_dir/$name")
      done
      jq --arg base "$base" '.url = $base + (.url | split("/") | last)
        | if .deltas then .deltas |= map(.url = $base + (.url | split("/") | last)) else . end' \
        "$manifest" > "$gh_dir/update-darwin-arm64.json"
    else
      cp "$manifest" "$gh_dir/update-darwin-arm64.json"
    fi
  fi
fi

if $dry; then
  echo "release-publish: dry run; would upload to s3://$ACTNOW_R2_BUCKET/$ACTNOW_R2_PREFIX$(actnow_is_prerelease "$version" && echo /beta)/:"
  for f in "$OUT"/*; do
    case "${f##*/}" in
      *.dmg | *.tar.gz | *.delta | update-*.json) echo "  ${f##*/}" ;;
    esac
  done
  scripts/actnow-register.sh --dmg "$dmg" --version "$version" --dry-run
  if $github; then
    echo "release-publish: would create the draft GitHub release $tag of $repo (GITHUB_FEED=$github_feed) with:"
    for f in "$gh_dir"/*; do
      if [[ -e "$f" ]]; then echo "  ${f##*/}"; fi
    done
    if [[ -f "$gh_dir/update-darwin-arm64.json" ]]; then
      jq '{version, url, deltas: [.deltas[]?.url]}' "$gh_dir/update-darwin-arm64.json"
    else
      echo "  (no update manifest: this build has no update keys)"
    fi
  fi
  exit 0
fi

# 2. The website's bucket. MyGo reads the AWS names.
export AWS_ACCESS_KEY_ID="${R2_ACCESS_KEY_ID:-${AWS_ACCESS_KEY_ID:-}}"
export AWS_SECRET_ACCESS_KEY="${R2_SECRET_ACCESS_KEY:-${AWS_SECRET_ACCESS_KEY:-}}"
[[ -n "$AWS_ACCESS_KEY_ID" && -n "$AWS_SECRET_ACCESS_KEY" ]] ||
  fail "no R2 credentials: set R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY (an R2 API token with Object Read & Write on $ACTNOW_R2_BUCKET)"
[[ -n "${ACTNOW_INGEST_TOKEN:-}" ]] || fail "ACTNOW_INGEST_TOKEN is not set (the site's release-ingest token for audiosmith)"
jq -e --arg url "$ACTNOW_UPDATES_URL" '.updates.url == $url and .updates.s3.bucket != null' mygo.json > /dev/null ||
  fail "mygo.json has no website updates section: run scripts/release-config.sh first"
"${mygo[@]}" publish -platform darwin/arm64

# 3. The site's pages.
scripts/actnow-register.sh --dmg "$dmg" --version "$version"

# 4. GitHub.
if $github; then
  command -v gh > /dev/null || fail "needs the GitHub CLI (gh) for the GitHub release; or --skip-github"
  beta=()
  actnow_is_prerelease "$version" && beta=(--prerelease)
  if ! gh release view "$tag" --repo "$repo" > /dev/null 2>&1; then
    gh release create "$tag" --repo "$repo" --draft --title "$tag" --notes-file "$notes_file" ${beta[@]+"${beta[@]}"}
  fi
  if [[ ${#beta[@]} -gt 0 ]]; then
    # A beta stays a prerelease and never becomes GitHub's latest release.
    gh release edit "$tag" --repo "$repo" --prerelease --latest=false > /dev/null
  fi
  if [[ ${#gh_files[@]} -gt 0 ]]; then
    gh release upload "$tag" --repo "$repo" --clobber "${gh_files[@]}"
  fi
  # The manifest last, so it never names a file that is not there.
  if [[ -f "$gh_dir/update-darwin-arm64.json" ]]; then
    gh release upload "$tag" --repo "$repo" --clobber "$gh_dir/update-darwin-arm64.json"
  fi
  echo "release-publish: the draft GitHub release $tag of $repo is ready: check it and publish it (GITHUB_FEED=$github_feed)"
fi
echo "release-publish: AudioSmith $version is on actnow.dev ($(actnow_channel "$version"))"
