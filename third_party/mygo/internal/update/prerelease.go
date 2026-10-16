package update

import (
	"net/url"
	"path"
	"strings"
)

// PrereleaseDir is the directory, beside the manifests of a feed served
// from a URL, that holds the manifests and files of prereleases, such as
// 1.3.0-beta.1: apps that take prereleases (mygo.Updater.SetPrereleases)
// read its manifest as well as the releases' one.
const PrereleaseDir = "beta"

// PrereleaseFeed returns the feed of the prereleases of a feed served from
// a URL: the manifest of the same name in its PrereleaseDir, such as
//
//	https://downloads.example.com/app/beta/update-darwin-arm64.json
//
// for https://downloads.example.com/app/update-darwin-arm64.json. Feeds of
// GitHub releases have none (ok is false): their prereleases are GitHub's
// (ResolvePrereleaseFeed).
func PrereleaseFeed(feed string) (string, bool) {
	if strings.Contains(feed, VersionPlaceholder) {
		return "", false
	}
	if _, _, ok := parseLatestFeed(feed); ok {
		return "", false
	}
	u, err := url.Parse(feed)
	if err != nil || u.Host == "" || u.Path == "" || strings.HasSuffix(u.Path, "/") {
		return "", false
	}
	dir, name := path.Split(u.Path)
	u.Path, u.RawPath = dir+PrereleaseDir+"/"+name, ""
	return u.String(), true
}
