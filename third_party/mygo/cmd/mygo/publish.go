package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo/internal/update"
)

// checkUpload checks, before a build, that -upload has somewhere to upload
// to.
func checkUpload(c *Config) error {
	switch u := c.Updates; {
	case u != nil && u.S3 != nil:
		_, err := newS3Client(u.S3)
		return err
	case u == nil || u.GitHub == "":
		return fmt.Errorf("-upload needs updates.github or updates.s3 in %s", c.configName())
	}
	return nil
}

// publish uploads the artifacts of a build where updates point to: the
// bucket of updates.s3, else the GitHub release of this version.
func publish(c *Config, artifacts []string) error {
	if c.Updates != nil && c.Updates.S3 != nil {
		return publishS3(c, artifacts)
	}
	return publishGitHub(c, artifacts)
}

func runPublish(args []string) error {
	flags := newFlags("publish", "[flags] [dir]", `Uploads what mygo build made for each platform, in <out>/<goos>-<goarch>,
where updates point to, as mygo build -upload does: to the GitHub release of
this version (updates.github), as a draft, or to the bucket of updates.s3,
the manifests last. Build first, check the build, then publish it: with
updates.s3, apps see the update once its manifest is uploaded.

The installers, archives, deltas and manifests of the directory are
uploaded, under the names they have there, so a disk image renamed after
the build is uploaded under its new name. The manifests must be of the
version in mygo.json.`)
	platforms := flags.String("platform", runtime.GOOS+"/"+runtime.GOARCH, "comma separated GOOS/GOARCH targets that were built, e.g. darwin/universal,linux/amd64")
	out := flags.String("o", "", "output directory of the build (default: out from mygo.json or dist)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	c, err := loadConfig(dirArg(flags.Args()))
	if err != nil {
		return err
	}
	if *out != "" {
		c.Out = *out
	}
	if err := checkUpload(c); err != nil {
		return err
	}
	artifacts, err := builtArtifacts(c, splitList(*platforms))
	if err != nil {
		return err
	}
	return publish(c, artifacts)
}

// builtArtifacts returns the files that mygo build left for platforms, in
// <out>/<goos>-<goarch>, after checking that their update manifests are of
// this version.
func builtArtifacts(c *Config, platforms []string) ([]string, error) {
	var artifacts []string
	for _, p := range platforms {
		goos, goarch, ok := strings.Cut(p, "/")
		if !ok || isWebPlatform(p) {
			return nil, fmt.Errorf("cannot publish the platform %q: want GOOS/GOARCH of an app", p)
		}
		dir := filepath.Join(c.path(c.Out), goos+"-"+goarch)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("no build of %s: %w", p, err)
		}
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			if name := e.Name(); strings.HasPrefix(name, "update-") && strings.HasSuffix(name, ".json") {
				var m update.Manifest
				data, err := os.ReadFile(path)
				if err == nil {
					err = json.Unmarshal(data, &m)
				}
				switch {
				case err != nil:
					return nil, fmt.Errorf("%s: %w", relPathTo(c.root, path), err)
				case m.Version != c.Version:
					return nil, fmt.Errorf("%s is of version %s, not %s: build again", relPathTo(c.root, path), m.Version, c.Version)
				}
			}
			artifacts = append(artifacts, path)
		}
	}
	return artifacts, nil
}

// uploads returns the artifacts of a build to publish: the installers and
// update files, and apart the update manifests, which go last so that they
// never point at a file that is not there.
func uploads(artifacts []string) (files, manifests []string, err error) {
	for _, a := range artifacts {
		info, err := os.Stat(a)
		if err != nil || info.IsDir() {
			continue // the macOS bundle ships in the disk image and the update
		}
		name := filepath.Base(a)
		switch {
		case strings.HasPrefix(name, "update-") && strings.HasSuffix(name, ".json"):
			manifests = append(manifests, a)
		case name == installScriptName:
			// The same script for every Linux target.
			if !slices.ContainsFunc(files, func(f string) bool { return filepath.Base(f) == name }) {
				files = append(files, a)
			}
		case strings.HasSuffix(name, ".dmg"), strings.HasSuffix(name, ".exe") && strings.Contains(name, " Setup "),
			strings.HasSuffix(name, ".deb"), strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".delta"):
			files = append(files, a)
		}
	}
	if len(files)+len(manifests) == 0 {
		return nil, nil, errors.New("nothing to upload")
	}
	return files, manifests, nil
}

// publishGitHub uploads the artifacts of a build to the GitHub release of
// this version, a draft created if needed, with the gh CLI. Installed apps
// see the update once the release is published.
func publishGitHub(c *Config, artifacts []string) error {
	if c.Updates == nil || c.Updates.GitHub == "" {
		return fmt.Errorf("-upload needs updates.github in %s", c.configName())
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		return errors.New("-upload needs the GitHub CLI (https://cli.github.com), signed in with gh auth login or GH_TOKEN")
	}
	repo, tag := c.Updates.GitHub, c.Updates.TagPrefix+c.Version
	files, manifests, err := uploads(artifacts)
	if err != nil {
		return err
	}
	// With a tag prefix, the repository's latest release is another's, which
	// the app's releases leave alone. A prerelease version, such as
	// 1.3.0-beta.1, is a prerelease on GitHub, which never becomes the
	// latest: only apps that take prereleases see it
	// (mygo.Updater.SetPrereleases).
	latest := ""
	if c.Updates.tagged() {
		latest = " --latest=false"
	}
	prerelease := update.IsPrerelease(c.Version)
	if exec.Command(gh, "release", "view", tag, "--repo", repo).Run() != nil {
		notes, _ := c.releaseNotes()
		kind := "draft release "
		if prerelease {
			kind = "draft prerelease "
		}
		t := con.start("Creating the " + kind + tag + " of " + repo)
		args := []string{"release", "create", tag, "--repo", repo, "--draft", "--title", tag, "--notes", notes}
		if latest != "" {
			args = append(args, "--latest=false")
		}
		if prerelease {
			args = append(args, "--prerelease")
		}
		if out, err := exec.Command(gh, args...).CombinedOutput(); err != nil {
			t.fail()
			return fmt.Errorf("gh release create: %v\n%s", err, out)
		}
		t.done("Created the " + kind + tag + " of " + repo)
	}
	for _, batch := range [][]string{files, manifests} {
		if len(batch) == 0 {
			continue
		}
		t := con.start("Uploading " + uploadNames(batch))
		args := append([]string{"release", "upload", tag, "--repo", repo, "--clobber"}, batch...)
		if out, err := exec.Command(gh, args...).CombinedOutput(); err != nil {
			t.fail()
			return fmt.Errorf("gh release upload: %v\n%s", err, out)
		}
		t.done("Uploaded " + uploadNames(batch))
	}
	logf("Uploaded to the release %s of %s; publish it when every platform is there:\n  gh release edit %s --repo %s --draft=false%s", tag, repo, tag, repo, latest)
	return nil
}

// publishS3 uploads the artifacts of a build to the bucket of updates.s3,
// which updates.url serves. Installed apps see the update of a platform
// once its manifest is there.
//
// A prerelease, such as 1.3.0-beta.1, goes in the beta directory
// (update.PrereleaseDir), which only apps that take prereleases read
// (mygo.Updater.SetPrereleases). A release also writes its manifests
// there, last, unless a newer prerelease is published, so that the beta
// manifest names the newest version of either kind.
func publishS3(c *Config, artifacts []string) error {
	bucket, err := newS3Client(c.Updates.S3)
	if err != nil {
		return err
	}
	files, manifests, err := uploads(artifacts)
	if err != nil {
		return err
	}
	dir, where := "", c.Updates.URL
	prerelease := update.IsPrerelease(c.Version)
	if prerelease {
		dir = update.PrereleaseDir + "/"
		where = strings.TrimSuffix(where, "/") + "/" + update.PrereleaseDir
	}
	put := func(key, f string) error {
		name := filepath.Base(f)
		t := con.start("Uploading " + key)
		t.set(sizeOf(f))
		return t.end(bucket.put(bucket.key(key), f, uploadHeader(name)), "Uploaded "+key)
	}
	for _, f := range slices.Concat(files, manifests) {
		if err := put(dir+filepath.Base(f), f); err != nil {
			return err
		}
	}
	logf("Uploaded to s3://%s/%s%s, which %s serves", bucket.bucket, bucket.key(""), dir, where)
	if prerelease {
		return nil
	}
	for _, f := range manifests {
		name := filepath.Base(f)
		beta, _ := update.PrereleaseFeed(c.updateFile(name, true))
		switch m, err := fetchManifest(c.updateFile(name, true), true); {
		case err != nil && !errors.Is(err, errNotPublished):
			warnf("Not writing %s for prereleases (%s): %v; apps that take prereleases still find %s in the releases' manifest", name, beta, err, c.Version)
			continue
		case err == nil && update.Compare(m.Version, c.Version) > 0:
			logf("Left %s at the newer prerelease %s", beta, m.Version)
			continue
		}
		if err := put(update.PrereleaseDir+"/"+name, f); err != nil {
			return err
		}
	}
	return nil
}

// uploadHeader returns the headers of an uploaded file: its type and, for
// the files whose names stay from version to version, that caches must
// check them again.
func uploadHeader(name string) http.Header {
	typ, ok := contentTypes[filepath.Ext(name)]
	if !ok {
		typ = "application/octet-stream"
	}
	h := http.Header{"Content-Type": {typ}}
	if name == installScriptName || strings.HasPrefix(name, "update-") && strings.HasSuffix(name, ".json") {
		h.Set("Cache-Control", "no-cache")
	}
	return h
}

var contentTypes = map[string]string{
	".dmg":  "application/x-apple-diskimage",
	".exe":  "application/vnd.microsoft.portable-executable",
	".deb":  "application/vnd.debian.binary-package",
	".gz":   "application/gzip",
	".json": "application/json",
	".sh":   "text/plain; charset=utf-8", // readable in a browser before piping it to sh
}

// uploadNames names the files of an upload.
func uploadNames(files []string) string {
	if len(files) == 1 {
		return filepath.Base(files[0])
	}
	return fmt.Sprintf("%d files", len(files))
}
