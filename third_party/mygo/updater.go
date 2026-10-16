//go:build !js

package mygo

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/internal/update"
)

// Set by `mygo build` with -ldflags -X when mygo.config.ts configures updates:
// the manifest of this build's target, or with a tag prefix on GitHub its
// feed in the newest tagged release (update.TaggedFeed), and the public key
// updates must be signed with.
var (
	packageUpdateFeed string
	packageUpdateKey  string
)

// startExe is the executable the app started from. On Linux, os.Executable
// reports the old file once an update replaced it.
var startExe, _ = os.Executable()

// UpdaterModule installs new versions of the app, which `mygo build`
// publishes when mygo.config.ts configures updates: signed archives and a
// manifest per platform. Use the Updater singleton:
//
//	update, err := mygo.Updater.Check(ctx)
//	if err == nil && update != nil {
//		if err := update.Install(ctx, nil); err == nil {
//			mygo.App.Relaunch()
//		}
//	}
type UpdaterModule struct {
	prereleases atomic.Bool
}

// Updater installs new versions of the app.
var Updater = &UpdaterModule{}

// ErrUpdatesDisabled is returned by Check for builds without updates:
// development builds, and builds of apps without updates in mygo.config.ts.
var ErrUpdatesDisabled = errors.New("mygo: updates are not enabled for this build")

// Update is a newer version of the app, found by Updater.Check.
type Update struct {
	// Version of the update, and its release notes in Markdown.
	Version string
	Notes   string
	// Date is when the update was built.
	Date time.Time

	manifest update.Manifest
	key      string
	// current is the version that Check compared with, which delta updates
	// update.
	current string
}

// Enabled reports whether the app can update itself: it was built with
// updates and can write where it is installed. Apps installed by a package
// manager, such as a .deb in /opt, are updated by it instead.
func (u *UpdaterModule) Enabled() bool {
	if packageUpdateFeed == "" || packageUpdateKey == "" || IsDev() {
		return false
	}
	target, err := installTarget()
	if err != nil {
		return false
	}
	probe, err := os.MkdirTemp(filepath.Dir(target), ".mygo-update-")
	if err != nil {
		return false
	}
	os.Remove(probe)
	return true
}

// Check asks the update feed for a version newer than the running one and
// returns it, or nil when the app is up to date. With SetPrereleases(true),
// prereleases count too.
func (u *UpdaterModule) Check(ctx context.Context) (*Update, error) {
	if !u.Enabled() {
		return nil, ErrUpdatesDisabled
	}
	return checkUpdate(ctx, packageUpdateFeed, packageUpdateKey, App.Version(), u.Prereleases())
}

// SetPrereleases chooses whether Check offers prereleases, such as
// 1.3.0-beta.1, as well as releases: for a beta channel that users join in
// the app's settings. It is off until the app turns it on, and the app
// keeps the user's choice: call it at every launch, before the first
// check (the updater plugin's first automatic check is 10 seconds after
// launch).
//
// On GitHub, prereleases are the releases marked as such, which never
// become the latest release; Check then finds the newest version among
// all releases through the GitHub API, one request a check within its 60
// requests an hour for each address, and when the API fails it checks the
// latest release instead. Updates from a URL keep the prereleases' files in
// a beta directory beside the releases' (update.PrereleaseFeed), which
// `mygo build -upload` fills: Check reads both manifests and offers the
// newer version, and the releases' alone when the beta one is missing.
//
// Turning it off never offers an older version: an app running a
// prerelease is offered the next release newer than it.
func (u *UpdaterModule) SetPrereleases(on bool) { u.prereleases.Store(on) }

// Prereleases reports whether Check offers prereleases (SetPrereleases).
func (u *UpdaterModule) Prereleases() bool { return u.prereleases.Load() }

func checkUpdate(ctx context.Context, feed, key, current string, prereleases bool) (*Update, error) {
	m, err := fetchManifest(ctx, feed, prereleases)
	if err != nil {
		return nil, fmt.Errorf("mygo: checking for updates: %w", err)
	}
	if update.Compare(m.Version, current) <= 0 {
		return nil, nil
	}
	up := &Update{Version: m.Version, Notes: m.Notes, manifest: m, key: key, current: current}
	up.Date, _ = time.Parse(time.RFC3339, m.Date)
	return up, nil
}

// fetchManifest downloads the manifest of feed: of its newest version, or
// with prereleases of its newest prerelease or release. When prereleases
// cannot be listed, such as past the GitHub API's limit, it reads the
// manifest of the newest release.
func fetchManifest(ctx context.Context, feed string, prereleases bool) (update.Manifest, error) {
	if beta, ok := update.PrereleaseFeed(feed); ok && prereleases {
		return newerManifest(ctx, beta, feed)
	}
	if prereleases {
		u, err := update.ResolvePrereleaseFeed(ctx, feed, httpGet)
		if err == nil {
			return getManifest(ctx, u)
		}
		if ctx.Err() != nil {
			return update.Manifest{}, err
		}
		m, rerr := fetchManifest(ctx, feed, false)
		if rerr != nil {
			return update.Manifest{}, err
		}
		log.Printf("mygo: checked the newest release for updates, as prereleases could not be listed: %v", err)
		return m, nil
	}
	u, err := update.ResolveFeed(ctx, feed, httpGet)
	if err != nil {
		return update.Manifest{}, err
	}
	return getManifest(ctx, u)
}

// newerManifest downloads the manifests of the prereleases (beta) and of
// the releases (feed) of a feed served from a URL, and returns the newer.
// When one cannot be read, such as a beta manifest that was never
// published (404), it returns the other; when neither can, the releases'
// error, or the prereleases' when the releases have none published.
func newerManifest(ctx context.Context, beta, feed string) (update.Manifest, error) {
	pre, perr := getManifest(ctx, beta)
	if perr != nil && ctx.Err() != nil {
		return update.Manifest{}, perr
	}
	m, err := getManifest(ctx, feed)
	switch {
	case perr == nil && err == nil:
		if update.Compare(pre.Version, m.Version) > 0 {
			return pre, nil
		}
		return m, nil
	case perr == nil:
		if !isNotFound(err) {
			log.Printf("mygo: checked the prereleases for updates, as the releases' manifest could not be read: %v", err)
		}
		return pre, nil
	case err == nil:
		if !isNotFound(perr) {
			log.Printf("mygo: checked the releases for updates, as the prereleases' manifest could not be read: %v", perr)
		}
		return m, nil
	case isNotFound(err) && !isNotFound(perr):
		return update.Manifest{}, perr
	}
	return update.Manifest{}, err
}

// statusError is an answer other than the one a request of an update file
// asked for.
type statusError struct {
	url, status string
	code        int
}

func (e *statusError) Error() string { return "GET " + e.url + ": " + e.status }

// isNotFound reports whether err is a 404 Not Found: a file that is not
// published.
func isNotFound(err error) bool {
	var s *statusError
	return errors.As(err, &s) && s.code == http.StatusNotFound
}

// getManifest downloads the manifest at url.
func getManifest(ctx context.Context, url string) (update.Manifest, error) {
	var m update.Manifest
	body, err := httpGet(ctx, url)
	if err != nil {
		return m, err
	}
	defer body.Close()
	if err := json.UnmarshalRead(io.LimitReader(body, update.MaxManifestSize), &m); err != nil {
		return m, err
	}
	if m.Version == "" || m.URL == "" || m.Signature == "" {
		return m, errors.New("incomplete manifest")
	}
	return m, nil
}

// Install downloads the update, checks that it is signed with the app's
// key and replaces the app with it; progress, when not nil, is called with
// the bytes downloaded so far. When the update has a delta for the running
// version, only the delta is downloaded, unless it fails to make the new
// version: the whole update is downloaded then, and the progress starts
// over. The running app is not affected: the new version runs after
// App.Relaunch, or at the next launch.
//
// The app must be able to write where it is installed: a bundle in
// /Applications of an administrator, or an app directory the user owns on
// Linux and Windows. Apps installed by a package manager are updated by it.
func (up *Update) Install(ctx context.Context, progress func(downloaded, total int64)) error {
	target, err := installTarget()
	if err != nil {
		return err
	}
	if err := installUpdate(ctx, up.manifest, up.key, up.current, target, progress); err != nil {
		return fmt.Errorf("mygo: installing the update: %w", err)
	}
	if runtime.GOOS == "linux" {
		// install.sh registered the desktop entry of the version it installed.
		if err := update.RefreshDesktopEntry(target, filepath.Base(startExe)); err != nil {
			log.Printf("mygo: registering the desktop entry of the update: %v", err)
		}
	}
	return nil
}

// installTarget returns what an update replaces: the app bundle on macOS,
// the directory of the executable elsewhere.
func installTarget() (string, error) {
	exe := startExe
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if runtime.GOOS == "darwin" {
		bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
		if !strings.HasSuffix(bundle, ".app") {
			return "", errors.New("mygo: only app bundles can update themselves")
		}
		return bundle, nil
	}
	if os.Getenv("APPIMAGE") != "" {
		return "", errors.New("mygo: AppImages cannot update themselves")
	}
	return filepath.Dir(exe), nil
}

// installUpdate installs the version of m in place of target, the app of
// version current: from its delta for current when there is one, else, or
// when the delta fails, from its archive.
func installUpdate(ctx context.Context, m update.Manifest, key, current, target string, progress func(downloaded, total int64)) error {
	pub, err := update.ParsePublicKey(key)
	if err != nil {
		return err
	}
	// Next to the app, so that it can be renamed into place.
	work, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+".update-")
	if err != nil {
		return fmt.Errorf("cannot write next to the app: %w", err)
	}
	defer os.RemoveAll(work)
	unpacked := filepath.Join(work, "app")
	swap := swapFiles
	if runtime.GOOS == "darwin" {
		swap = swapBundle
	}

	if d := m.Delta(current); d != nil {
		err := applyDelta(ctx, *d, pub, m.Version, target, work, unpacked, progress)
		if err == nil {
			return swap(unpacked, target)
		}
		if ctx.Err() != nil {
			return err
		}
		log.Printf("mygo: downloading the whole update, as its delta update failed: %v", err)
		if err := os.RemoveAll(unpacked); err != nil {
			return err
		}
	}

	if m.Size <= 0 || m.Size > update.MaxArchiveSize {
		return fmt.Errorf("invalid size %d", m.Size)
	}
	archive := filepath.Join(work, "update.tar.gz")
	if err := download(ctx, m.URL, m.Size, m.Signature, pub, archive, progress); err != nil {
		return err
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	err = update.ExtractArchive(f, unpacked)
	f.Close()
	if err != nil {
		return err
	}
	return swap(unpacked, target)
}

// applyDelta downloads the delta d into work and makes the new version of
// the app at target from it, in dir: its bundle on macOS, in dir too, as
// the bundle of an archive is.
func applyDelta(ctx context.Context, d update.Delta, pub ed25519.PublicKey, version, target, work, dir string, progress func(downloaded, total int64)) error {
	if d.Size <= 0 || d.Size > update.MaxArchiveSize {
		return fmt.Errorf("invalid size %d", d.Size)
	}
	path := filepath.Join(work, "update.delta")
	if err := download(ctx, d.URL, d.Size, d.Signature, pub, path, progress); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if runtime.GOOS == "darwin" {
		if err := os.Mkdir(dir, 0o755); err != nil {
			return err
		}
		dir = filepath.Join(dir, filepath.Base(target))
	}
	return update.ApplyDelta(f, d.Size, d.From, version, target, dir)
}

// download fetches url into the file path and checks its size and
// signature.
func download(ctx context.Context, url string, size int64, signature string, pub ed25519.PublicKey, path string, progress func(downloaded, total int64)) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	sum := sha256.New()
	// A connection that drops part way (common through proxies and on
	// slow networks: "unexpected EOF") resumes where it stopped, a few
	// times, instead of failing the update.
	var n int64
	for try := 0; ; try++ {
		var body io.ReadCloser
		body, err = httpGetFrom(ctx, url, n)
		if err != nil {
			break
		}
		var got int64
		got, err = io.Copy(io.MultiWriter(f, sum), &progressReader{r: io.LimitReader(body, size+1-n), n: n, total: size, fn: progress})
		body.Close()
		n += got
		if err == nil || ctx.Err() != nil || try >= downloadRetries || n > size {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(downloadRetryDelay):
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("downloading: %w", err)
	}
	if n != size {
		return fmt.Errorf("downloaded %d bytes, want %d", n, size)
	}
	return update.Verify(pub, sum.Sum(nil), signature)
}

// swapBundle replaces the bundle at target with the one in dir.
func swapBundle(dir, target string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".app") {
		return errors.New("the update holds no app bundle")
	}
	old := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".old-"+strconv.Itoa(os.Getpid()))
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, entries[0].Name()), target); err != nil {
		_ = os.Rename(old, target)
		return err
	}
	// The running app keeps its open files.
	_ = os.RemoveAll(old)
	return nil
}

// swapFiles replaces the entries of the app directory target with those in
// dir. The running executable is renamed away, which Windows allows, and
// removed at the next launch (cleanUpdateLeftovers).
func swapFiles(dir, target string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("the update is empty")
	}
	suffix := ".old-" + strconv.Itoa(os.Getpid())
	var moved [][2]string // renamed away: old name, new name
	undo := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.RemoveAll(moved[i][0])
			_ = rename(moved[i][1], moved[i][0])
		}
	}
	for _, e := range entries {
		dst := filepath.Join(target, e.Name())
		if _, err := os.Lstat(dst); err == nil {
			aside := filepath.Join(target, "."+e.Name()+suffix)
			if err := rename(dst, aside); err != nil {
				undo()
				return err
			}
			moved = append(moved, [2]string{dst, aside})
		}
		if err := rename(filepath.Join(dir, e.Name()), dst); err != nil {
			undo()
			return err
		}
	}
	for _, m := range moved {
		_ = os.RemoveAll(m[1])
	}
	return nil
}

// relaunchNow starts the app again; it runs after the quit sequence.
func relaunchNow() {
	if launchedByDev() {
		os.Exit(devRelaunchCode)
	}
	// The executable the app started from, which an update may have
	// replaced.
	cmd := exec.Command(startExe, os.Args[1:]...)
	cmd.Dir = startDir
	if runtime.GOOS != "windows" {
		// GUI executables on Windows have no standard streams to pass on.
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// cleanUpdateLeftovers removes what an update could not remove while the
// previous version ran (Windows keeps running executables).
func cleanUpdateLeftovers() {
	if runtime.GOOS == "darwin" || startExe == "" {
		return
	}
	olds, _ := filepath.Glob(filepath.Join(filepath.Dir(startExe), ".*.old-*"))
	for _, p := range olds {
		_ = os.RemoveAll(p)
	}
}

// downloadRetries and downloadRetryDelay bound the resumes of a download
// whose connection dropped.
var (
	downloadRetries    = 4
	downloadRetryDelay = 2 * time.Second
)

// updateClient is the HTTP client of update checks and downloads (nil:
// http.DefaultClient). See SetHTTPClient.
var updateClient atomic.Pointer[http.Client]

// SetHTTPClient makes the updater check and download through c, nil for
// http.DefaultClient. Apps set it to honour a proxy: http.DefaultClient
// only reads the proxy environment variables, which apps started from the
// Finder do not get, and not the macOS system proxy.
func (u *UpdaterModule) SetHTTPClient(c *http.Client) { updateClient.Store(c) }

// httpGet fetches a URL over HTTPS (or plain HTTP from the loopback
// interface, for tests).
func httpGet(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	return httpGetFrom(ctx, rawURL, 0)
}

// httpGetFrom is httpGet from byte offset on (a Range request when
// offset > 0, which must be answered with 206 Partial Content).
func httpGetFrom(ctx context.Context, rawURL string, offset int64) (io.ReadCloser, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, fmt.Errorf("refusing to download %s: updates need HTTPS", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MyGo/"+Version+" "+App.Name()+"/"+App.Version())
	want := http.StatusOK
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		want = http.StatusPartialContent
	}
	client := updateClient.Load()
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != want {
		resp.Body.Close()
		return nil, &statusError{url: rawURL, status: resp.Status, code: resp.StatusCode}
	}
	return resp.Body, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// progressReader reports how much of a download was read.
type progressReader struct {
	r     io.Reader
	n     int64
	total int64
	fn    func(downloaded, total int64)
	last  time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.fn != nil && (err != nil || p.n == p.total || time.Since(p.last) > 100*time.Millisecond) {
		p.last = time.Now()
		p.fn(p.n, p.total)
	}
	return n, err
}
