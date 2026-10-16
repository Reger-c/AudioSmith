# Sourced by the release scripts (release-config.sh, release-publish.sh,
# actnow-register.sh): where AudioSmith's releases go on the family
# website, actnow.dev (thread #69). Not a program of its own.
#
# The site serves the update files of the private R2 bucket actnow-releases
# publicly, and the disk images only through its sign-in download page
# (https://actnow.dev/download/audiosmith):
#
#   https://actnow.dev/updates/audiosmith/<file>       ⇔ R2 audiosmith/<file>       releases
#   https://actnow.dev/updates/audiosmith/beta/<file>  ⇔ R2 audiosmith/beta/<file>  betas (MyGo patch 0008)
#
# Disk images: audiosmith/AudioSmith-<version>.dmg, betas under beta/.
# After the uploads, POST /api/releases registers the version for the
# site's download and changelog pages (scripts/actnow-register.sh).

ACTNOW_APP=audiosmith
ACTNOW_UPDATES_URL="https://actnow.dev/updates/audiosmith"
ACTNOW_API="${ACTNOW_API:-https://actnow.dev/api/releases}"
ACTNOW_DOWNLOAD_URL="https://actnow.dev/download/audiosmith"
ACTNOW_CHANGELOG_URL="https://actnow.dev/audiosmith/changelog"
ACTNOW_R2_BUCKET=actnow-releases
ACTNOW_R2_PREFIX=audiosmith
# Cloudflare R2's S3 endpoint of the account (not a secret; the keys are).
ACTNOW_R2_ENDPOINT="https://8a8325619b834f43256fd77f030b11a2.r2.cloudflarestorage.com"

# actnow_is_prerelease VERSION: a version with a prerelease part, such as
# 0.4.0-beta.5, is a beta (semantic versioning, as MyGo compares them).
actnow_is_prerelease() {
  local v="${1%%+*}"
  [[ "$v" == *-* ]]
}

# actnow_channel VERSION: beta or stable, as the site names them.
actnow_channel() {
  if actnow_is_prerelease "$1"; then echo beta; else echo stable; fi
}

# actnow_dmg_name VERSION: the disk image's name in R2 (mygo build names it
# "AudioSmith <version> arm64.dmg"; release-publish.sh renames it).
actnow_dmg_name() {
  echo "AudioSmith-$1.dmg"
}

# actnow_dmg_key VERSION: the disk image's R2 key, as the site imported
# 0.4.0-beta.1 … beta.3: audiosmith/beta/AudioSmith-0.4.0-beta.1.dmg.
actnow_dmg_key() {
  local dir="$ACTNOW_R2_PREFIX"
  actnow_is_prerelease "$1" && dir="$dir/beta"
  echo "$dir/$(actnow_dmg_name "$1")"
}

# changelog_section FILE VERSION: the body of VERSION's "## " section of a
# Keep a Changelog file, without its heading and the blank lines around
# it, as MyGo's update.ReleaseNotes reads it for the update window: the
# heading's first word, without [ ] and a leading v, is the version,
# exactly ("## 0.4.0-beta.1" is not 0.4.0's section).
changelog_section() {
  awk -v v="$2" '
    /^## / {
      if (on) exit
      h = $2; sub(/^\[/, "", h); sub(/\].*$/, "", h); sub(/^v/, "", h)
      on = (h == v)
      next
    }
    on {
      line[++n] = $0
      if ($0 ~ /[^[:space:]]/) { if (!first) first = n; last = n }
    }
    END { for (i = first; first && i <= last; i++) print line[i] }
  ' "$1"
}

# file_sha256 FILE and file_size FILE, on macOS and Linux.
file_sha256() {
  if command -v sha256sum > /dev/null; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}
file_size() {
  wc -c < "$1" | tr -d '[:space:]'
}
