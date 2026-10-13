package hub

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
)

const (
	testRepo   = "acme/tiny-tts"
	testCommit = "0123456789abcdef0123456789abcdef01234567"
	testToken  = "hf_secret"
)

// fakeHub serves one repo as the Hugging Face API and resolve endpoints do,
// LFS files through a redirect to a "CDN" path, with ranges.
type fakeHub struct {
	repo  string
	files map[string][]byte
	lfs   map[string]bool
	gated bool
	// Faults.
	apiDelay atomic.Int64 // ns
	apiFail  atomic.Bool
	corrupt  map[string]bool
	cutPath  string       // abort the next response of this file…
	cutAfter atomic.Int64 // …after this many bytes (0 = off)

	srv *httptest.Server

	mu       sync.Mutex
	served   int64
	ranges   []string
	apiCalls int
	auth     []string // Authorization headers seen, by path
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	big := make([]byte, 10_000+123)
	for i := range big {
		big[i] = byte(r.Uint32())
	}
	h := &fakeHub{
		repo: testRepo,
		files: map[string][]byte{
			"config.json":            []byte(`{"model_type":"tiny"}`),
			"model.safetensors":      big,
			"voices/af_heart.pt":     bytes.Repeat([]byte("voice"), 300),
			"tokenizer/vocab.txt":    []byte("a\nb\nc\n"),
			"empty.txt":              {},
			"weights/part 2.npz":     bytes.Repeat([]byte{7}, 4000),
			"README.md":              []byte("# tiny"),
			"nested/deeper/x.json":   []byte("{}"),
			"model.safetensors.json": []byte(`{"index":true}`),
		},
		lfs:     map[string]bool{"model.safetensors": true, "voices/af_heart.pt": true, "weights/part 2.npz": true},
		corrupt: map[string]bool{},
	}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func gitBlobID(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// count reads a counter while requests may run.
func (h *fakeHub) count(what string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if what == "api" {
		return int64(h.apiCalls)
	}
	return h.served
}

func (h *fakeHub) authSeen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.auth)
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.auth = append(h.auth, r.URL.Path+" "+r.Header.Get("Authorization"))
	h.mu.Unlock()
	authed := r.Header.Get("Authorization") == "Bearer "+testToken
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/models/"+h.repo+"/revision/"):
		h.mu.Lock()
		h.apiCalls++
		h.mu.Unlock()
		time.Sleep(time.Duration(h.apiDelay.Load()))
		if h.apiFail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		rev := strings.TrimPrefix(p, "/api/models/"+h.repo+"/revision/")
		if rev != "main" && rev != testCommit {
			http.NotFound(w, r)
			return
		}
		type lfs struct {
			SHA256 string `json:"sha256"`
			Size   int    `json:"size"`
		}
		type sib struct {
			Path   string `json:"rfilename"`
			BlobID string `json:"blobId"`
			Size   int    `json:"size"`
			LFS    *lfs   `json:"lfs,omitempty"`
		}
		var sibs []sib
		for _, path := range slices.Sorted(mapsKeys(h.files)) {
			b := h.files[path]
			s := sib{Path: path, BlobID: gitBlobID(b), Size: len(b)}
			if h.lfs[path] {
				s.LFS = &lfs{SHA256: sha(b), Size: len(b)}
				s.Size = len(b)
			}
			sibs = append(sibs, s)
		}
		gated := any(false)
		if h.gated {
			gated = "manual"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": h.repo, "sha": testCommit, "gated": gated, "siblings": sibs})
	case strings.HasPrefix(p, "/"+h.repo+"/resolve/"+testCommit+"/"):
		if h.gated && !authed {
			http.Error(w, "gated", http.StatusUnauthorized)
			return
		}
		path := strings.TrimPrefix(p, "/"+h.repo+"/resolve/"+testCommit+"/")
		if h.lfs[path] {
			http.Redirect(w, r, "/cdn/"+h.repo+"/"+path, http.StatusFound)
			return
		}
		h.content(w, r, path)
	case strings.HasPrefix(p, "/cdn/"+h.repo+"/"):
		h.content(w, r, strings.TrimPrefix(p, "/cdn/"+h.repo+"/"))
	default:
		http.NotFound(w, r)
	}
}

func mapsKeys(m map[string][]byte) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func (h *fakeHub) content(w http.ResponseWriter, r *http.Request, path string) {
	b, ok := h.files[path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if h.corrupt[path] {
		b = slices.Clone(b)
		b[len(b)/2] ^= 0xff
	}
	h.mu.Lock()
	if rg := r.Header.Get("Range"); rg != "" {
		h.ranges = append(h.ranges, path+" "+rg)
	}
	h.mu.Unlock()
	cw := &countingWriter{ResponseWriter: w, h: h}
	if path == h.cutPath {
		cw.cut = h.cutAfter.Swap(0)
	}
	w.Header().Set("ETag", `"`+gitBlobID(b)+`"`)
	http.ServeContent(cw, r, "", time.Time{}, bytes.NewReader(b))
}

type countingWriter struct {
	http.ResponseWriter
	h   *fakeHub
	n   int64
	cut int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.cut > 0 && c.n+int64(len(p)) > c.cut {
		p = p[:c.cut-c.n]
		c.ResponseWriter.Write(p)
		c.ResponseWriter.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	c.n += int64(len(p))
	c.h.mu.Lock()
	c.h.served += int64(len(p))
	c.h.mu.Unlock()
	return c.ResponseWriter.Write(p)
}

func (h *fakeHub) entry() catalog.Entry {
	return catalog.Entry{ID: "tiny", Label: "Tiny", Family: "tiny", Repo: testRepo, Tasks: []catalog.Task{catalog.TaskTTS},
		Licence: catalog.Licence{ID: "apache-2.0", Name: "Apache 2.0", Commercial: true}}
}

func (h *fakeHub) client(t *testing.T, mod func(*Options)) *Client {
	t.Helper()
	o := Options{CacheDir: t.TempDir(), Endpoints: []string{h.srv.URL}, Official: h.srv.URL, ChunkSize: 1024, ReserveBytes: -1}
	if mod != nil {
		mod(&o)
	}
	c := New(o)
	c.progressEvery = 0
	return c
}

func checkSnapshot(t *testing.T, c *Client, h *fakeHub) {
	t.Helper()
	l, err := c.Status(context.Background(), h.entry())
	if err != nil || l.State != Downloaded {
		t.Fatalf("status %+v %v", l, err)
	}
	for path, want := range h.files {
		got, err := os.ReadFile(filepath.Join(l.Snapshot, filepath.FromSlash(path)))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %d bytes, %v", path, len(got), err)
		}
		link, _ := os.Readlink(filepath.Join(l.Snapshot, filepath.FromSlash(path)))
		if !strings.HasPrefix(link, "../") || filepath.IsAbs(link) {
			t.Errorf("%s links to %q, want a relative link", path, link)
		}
	}
	ref, err := os.ReadFile(filepath.Join(RepoDir(c.opts.CacheDir, testRepo), "refs", "main"))
	if err != nil || string(ref) != testCommit {
		t.Errorf("refs/main %q %v", ref, err)
	}
	blobs, _ := os.ReadDir(filepath.Join(RepoDir(c.opts.CacheDir, testRepo), "blobs"))
	for _, b := range blobs {
		if strings.Contains(b.Name(), ".incomplete") {
			t.Errorf("left %s", b.Name())
		}
	}
	if total := sumSizes(h.files); l.Bytes != total {
		t.Errorf("status bytes %d, want %d", l.Bytes, total)
	}
}

func sumSizes(files map[string][]byte) int64 {
	var n int64
	for _, b := range files {
		n += int64(len(b))
	}
	return n
}

func TestDownload(t *testing.T) {
	h := newFakeHub(t)
	c := h.client(t, nil)
	ctx := context.Background()
	if l, _ := c.Status(ctx, h.entry()); l.State != Missing {
		t.Fatalf("before: %+v", l)
	}
	p, err := c.Plan(ctx, h.entry())
	if err != nil || p.Revision != testCommit || len(p.Files) != len(h.files) || p.Bytes != sumSizes(h.files) || p.Bytes != p.TotalBytes {
		t.Fatalf("plan %+v %v", p, err)
	}
	var last Progress
	calls := 0
	if err := c.Download(ctx, h.entry(), func(pr Progress) { last = pr; calls++ }); err != nil {
		t.Fatal(err)
	}
	if last.Bytes != last.TotalBytes || last.TotalBytes != sumSizes(h.files) || last.Mirror != h.srv.URL || calls < 2 {
		t.Errorf("progress %+v after %d calls", last, calls)
	}
	checkSnapshot(t, c, h)
	// The big file came in ranges.
	if n := len(h.ranges); n < 9 {
		t.Errorf("only %d ranged requests: %v", n, h.ranges)
	}
	// Again: nothing to fetch.
	served := h.count("served")
	if err := c.Download(ctx, h.entry(), nil); err != nil {
		t.Fatal(err)
	}
	if h.count("served") != served {
		t.Errorf("re-download fetched %d bytes", h.count("served")-served)
	}
	if p, _ := c.Plan(ctx, h.entry()); p.Bytes != 0 {
		t.Errorf("plan after download: %d bytes to go", p.Bytes)
	}
}

func TestResume(t *testing.T) {
	h := newFakeHub(t)
	ctx := context.Background()
	// Interrupted half way through, by cancelling.
	c := h.client(t, func(o *Options) { o.Parallel = 1 })
	cctx, cancel := context.WithCancel(ctx)
	err := c.Download(cctx, h.entry(), func(p Progress) {
		if p.Bytes > p.TotalBytes/2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled download: %v", err)
	}
	l, _ := c.Status(ctx, h.entry())
	if l.State != Partial || l.Bytes == 0 {
		t.Fatalf("after cancel: %+v", l)
	}
	p, _ := c.Plan(ctx, h.entry())
	if p.Bytes >= p.TotalBytes || p.Bytes == 0 {
		t.Fatalf("plan after cancel: %d of %d to go", p.Bytes, p.TotalBytes)
	}
	served := h.count("served")
	if err := c.Download(ctx, h.entry(), nil); err != nil {
		t.Fatal(err)
	}
	checkSnapshot(t, c, h)
	if again := h.count("served") - served; again > p.Bytes+1024 {
		t.Errorf("resumed download fetched %d bytes, plan said %d", again, p.Bytes)
	}
}

func TestConnectionDropsAreRetried(t *testing.T) {
	h := newFakeHub(t)
	c := h.client(t, func(o *Options) { o.Parallel = 1; o.ChunkSize = 1 << 20 }) // whole files
	h.cutPath = "model.safetensors"
	h.cutAfter.Store(3000)
	if err := c.Download(context.Background(), h.entry(), nil); err != nil {
		t.Fatal(err)
	}
	checkSnapshot(t, c, h)
	found := false
	for _, r := range h.ranges {
		found = found || strings.HasSuffix(r, "bytes=3000-10122") || strings.Contains(r, "bytes=3000-")
	}
	if !found {
		t.Errorf("no resumed range after the drop: %v", h.ranges)
	}
}

func TestSequentialPartialFromOtherTools(t *testing.T) {
	// huggingface_hub leaves blobs/<sha>.incomplete written in order.
	h := newFakeHub(t)
	c := h.client(t, nil)
	big := h.files["model.safetensors"]
	blobs := filepath.Join(RepoDir(c.opts.CacheDir, testRepo), "blobs")
	os.MkdirAll(blobs, 0o755)
	os.WriteFile(filepath.Join(blobs, sha(big)+".incomplete"), big[:5000], 0o644)
	p, _ := c.Plan(context.Background(), h.entry())
	if want := sumSizes(h.files) - 5000; p.Bytes != want {
		t.Errorf("plan %d, want %d", p.Bytes, want)
	}
	if err := c.Download(context.Background(), h.entry(), nil); err != nil {
		t.Fatal(err)
	}
	checkSnapshot(t, c, h)
	for _, r := range h.ranges {
		if strings.HasPrefix(r, "model.safetensors bytes=0-") {
			t.Errorf("refetched the first chunk: %v", h.ranges)
		}
	}
}

func TestChecksumMismatch(t *testing.T) {
	h := newFakeHub(t)
	h.corrupt["model.safetensors"] = true
	c := h.client(t, nil)
	err := c.Download(context.Background(), h.entry(), nil)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("corrupt download: %v", err)
	}
	repo := RepoDir(c.opts.CacheDir, testRepo)
	if _, err := os.Stat(filepath.Join(repo, "refs", "main")); err == nil {
		t.Error("a ref was written for a bad download")
	}
	if l, _ := c.Status(context.Background(), h.entry()); l.State == Downloaded {
		t.Errorf("status %+v", l)
	}
	// Fixed upstream: the next download completes.
	h.corrupt = map[string]bool{}
	if err := c.Download(context.Background(), h.entry(), nil); err != nil {
		t.Fatal(err)
	}
	checkSnapshot(t, c, h)
}

func TestVerifyAndRepair(t *testing.T) {
	h := newFakeHub(t)
	c := h.client(t, nil)
	ctx := context.Background()
	if err := c.Download(ctx, h.entry(), nil); err != nil {
		t.Fatal(err)
	}
	bad, err := c.Verify(ctx, h.entry())
	if err != nil || len(bad) != 0 {
		t.Fatalf("verify clean: %v %v", bad, err)
	}
	// Damage one LFS blob and one small file.
	l, _ := c.Status(ctx, h.entry())
	for _, p := range []string{"model.safetensors", "config.json"} {
		blob, _ := filepath.EvalSymlinks(filepath.Join(l.Snapshot, p))
		data, _ := os.ReadFile(blob)
		data[0] ^= 1
		os.WriteFile(blob, data, 0o644)
	}
	bad, err = c.Verify(ctx, h.entry())
	slices.Sort(bad)
	if err != nil || !slices.Equal(bad, []string{"config.json", "model.safetensors"}) {
		t.Fatalf("verify damaged: %v %v", bad, err)
	}
	if l, _ := c.Status(ctx, h.entry()); l.State != Partial || len(l.Missing) != 2 {
		t.Errorf("status after verify: %+v", l)
	}
	if err := c.Download(ctx, h.entry(), nil); err != nil {
		t.Fatal(err)
	}
	checkSnapshot(t, c, h)
}

func TestDelete(t *testing.T) {
	h := newFakeHub(t)
	c := h.client(t, nil)
	ctx := context.Background()
	if err := c.Download(ctx, h.entry(), nil); err != nil {
		t.Fatal(err)
	}
	repo := RepoDir(c.opts.CacheDir, testRepo)
	// Another snapshot (an older revision) shares config.json's blob and
	// keeps it.
	l, _ := c.Status(ctx, h.entry())
	shared, _ := os.Readlink(filepath.Join(l.Snapshot, "config.json"))
	old := filepath.Join(repo, "snapshots", strings.Repeat("f", 40))
	os.MkdirAll(old, 0o755)
	os.Symlink(shared, filepath.Join(old, "config.json"))
	if err := c.Delete(ctx, h.entry()); err != nil {
		t.Fatal(err)
	}
	blobs, _ := os.ReadDir(filepath.Join(repo, "blobs"))
	if len(blobs) != 1 || blobs[0].Name() != filepath.Base(shared) {
		t.Errorf("blobs left: %v", blobs)
	}
	if _, err := os.Stat(filepath.Join(repo, "refs", "main")); err == nil {
		t.Error("refs/main left")
	}
	if l, _ := c.Status(ctx, h.entry()); l.State != Partial {
		t.Errorf("with an older snapshot left: %+v", l)
	}
	// Without other snapshots the repo directory goes.
	os.RemoveAll(old)
	if err := c.Delete(ctx, h.entry()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Errorf("repo dir left: %v", err)
	}
	if l, _ := c.Status(ctx, h.entry()); l.State != Missing {
		t.Errorf("after delete: %+v", l)
	}
}

func TestPinnedRevision(t *testing.T) {
	h := newFakeHub(t)
	c := h.client(t, nil)
	e := h.entry()
	e.Revision = testCommit
	if err := c.Download(context.Background(), e, nil); err != nil {
		t.Fatal(err)
	}
	repo := RepoDir(c.opts.CacheDir, testRepo)
	if _, err := os.Stat(filepath.Join(repo, "refs")); err == nil {
		t.Error("a pinned commit wrote refs")
	}
	if l, _ := c.Status(context.Background(), e); l.State != Downloaded || filepath.Base(l.Snapshot) != testCommit {
		t.Errorf("status %+v", l)
	}
	// The catalogue's file list restricts the download and pins hashes.
	e2 := h.entry()
	e2.Repo = testRepo
	e2.Files = []catalog.File{{Path: "config.json"}, {Path: "model.safetensors", SHA256: strings.Repeat("0", 64)}}
	if _, err := c.Plan(context.Background(), e2); !errors.Is(err, ErrChecksum) {
		t.Errorf("a pinned hash that differs: %v", err)
	}
	e2.Files[1].SHA256 = sha(h.files["model.safetensors"])
	p, err := c.Plan(context.Background(), e2)
	if err != nil || len(p.Files) != 2 {
		t.Errorf("listed files: %+v %v", p, err)
	}
	e2.Files = append(e2.Files, catalog.File{Path: "nope.bin"})
	if _, err := c.Plan(context.Background(), e2); !errors.Is(err, ErrNotFound) {
		t.Errorf("a listed file the repo lacks: %v", err)
	}
}

func TestMirrorRace(t *testing.T) {
	official := newFakeHub(t)
	mirror := newFakeHub(t)
	official.apiDelay.Store(int64(300 * time.Millisecond))
	c := official.client(t, func(o *Options) {
		o.Endpoints = []string{official.srv.URL, mirror.srv.URL}
		o.Token = func() string { return testToken }
	})
	var last Progress
	if err := c.Download(context.Background(), official.entry(), func(p Progress) { last = p }); err != nil {
		t.Fatal(err)
	}
	if last.Mirror != mirror.srv.URL {
		t.Errorf("used %s, want the faster mirror", last.Mirror)
	}
	for _, a := range mirror.authSeen() {
		if strings.Contains(a, "Bearer") {
			t.Errorf("the mirror saw the token: %s", a)
		}
	}
	if official.count("served") != 0 || mirror.count("served") == 0 {
		t.Errorf("served: official %d mirror %d", official.count("served"), mirror.count("served"))
	}
	// The winner is remembered: the official is not asked again.
	calls := official.count("api")
	c.Plan(context.Background(), official.entry())
	if official.count("api") != calls {
		t.Error("raced again")
	}
	// When the winner goes down, race again.
	mirror.apiFail.Store(true)
	official.apiDelay.Store(int64(0))
	p, err := c.Plan(context.Background(), official.entry())
	if err != nil || p.Endpoint != official.srv.URL {
		t.Errorf("after the mirror failed: %+v %v", p, err)
	}
	// Every endpoint down.
	official.apiFail.Store(true)
	if _, err := c.Plan(context.Background(), official.entry()); !errors.Is(err, ErrOffline) {
		t.Errorf("all down: %v", err)
	}
}

func TestGated(t *testing.T) {
	official := newFakeHub(t)
	mirror := newFakeHub(t)
	official.gated, mirror.gated = true, true
	token := ""
	c := official.client(t, func(o *Options) {
		o.Endpoints = []string{mirror.srv.URL, official.srv.URL}
		o.Token = func() string { return token }
	})
	mirror.apiDelay.Store(int64(0))
	official.apiDelay.Store(int64(200 * time.Millisecond))
	if _, err := c.Plan(context.Background(), official.entry()); !errors.Is(err, ErrGated) {
		t.Fatalf("without a token: %v", err)
	}
	token = testToken
	p, err := c.Plan(context.Background(), official.entry())
	if err != nil || !p.Gated || p.Endpoint != official.srv.URL {
		t.Fatalf("with a token: %+v %v", p, err)
	}
	if err := c.Download(context.Background(), official.entry(), nil); err != nil {
		t.Fatal(err)
	}
	checkSnapshot(t, c, official)
	if mirror.count("served") != 0 {
		t.Error("a gated repo came from the mirror")
	}
	for _, a := range mirror.authSeen() {
		if strings.Contains(a, "Bearer") {
			t.Errorf("the mirror saw the token: %s", a)
		}
	}
}

func TestDiskPreflight(t *testing.T) {
	h := newFakeHub(t)
	c := h.client(t, func(o *Options) { o.ReserveBytes = 1 << 62 })
	_, err := c.Plan(context.Background(), h.entry())
	if !errors.Is(err, ErrDiskSpace) {
		t.Fatalf("plan: %v", err)
	}
	if err := c.Download(context.Background(), h.entry(), nil); !errors.Is(err, ErrDiskSpace) {
		t.Fatalf("download: %v", err)
	}
	if h.count("served") != 0 {
		t.Error("downloaded despite the preflight")
	}
	if freeBytes(t.TempDir()) <= 0 || freeBytes(filepath.Join(t.TempDir(), "not", "there")) <= 0 {
		t.Error("freeBytes")
	}
}

func TestNotFound(t *testing.T) {
	h := newFakeHub(t)
	c := h.client(t, nil)
	e := h.entry()
	e.Revision = "nope"
	if _, err := c.Plan(context.Background(), e); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown revision: %v", err)
	}
}

func TestStatusOfOtherToolsCache(t *testing.T) {
	// A snapshot with only config.json (a tool fetched metadata) is not a
	// model yet.
	dir := t.TempDir()
	e := catalog.Entry{Repo: "a/b"}
	repo := RepoDir(dir, "a/b")
	os.MkdirAll(filepath.Join(repo, "blobs"), 0o755)
	os.MkdirAll(filepath.Join(repo, "snapshots", testCommit), 0o755)
	os.MkdirAll(filepath.Join(repo, "refs"), 0o755)
	os.WriteFile(filepath.Join(repo, "refs", "main"), []byte(testCommit), 0o644)
	os.WriteFile(filepath.Join(repo, "blobs", "abc"), []byte("{}"), 0o644)
	os.Symlink("../../blobs/abc", filepath.Join(repo, "snapshots", testCommit, "config.json"))
	if l, _ := Status(dir, e); l.State != Partial {
		t.Errorf("config only: %+v", l)
	}
	os.WriteFile(filepath.Join(repo, "blobs", "w"), []byte("weights"), 0o644)
	os.Symlink("../../blobs/w", filepath.Join(repo, "snapshots", testCommit, "model.safetensors"))
	if l, _ := Status(dir, e); l.State != Downloaded || l.Bytes != 9 {
		t.Errorf("with weights: %+v", l)
	}
}

func TestToken(t *testing.T) {
	saved := security
	defer func() { security = saved }()
	var stdin string
	var args []string
	security = func(in string, a ...string) ([]byte, error) {
		stdin, args = in, a
		if a[0] == "find-generic-password" {
			return []byte("hf_fromkeychain\n"), nil
		}
		return nil, nil
	}
	k := Keychain{Service: "S", Account: "A"}
	if err := k.Set("hf_abc"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args, []string{"-i"}) || !strings.Contains(stdin, `-w "hf_abc"`) || strings.Contains(strings.Join(args, " "), "hf_abc") {
		t.Errorf("set: args %q stdin %q", args, stdin)
	}
	if err := k.Set(`bad"token`); err == nil {
		t.Error("accepted a quote")
	}
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HF_TOKEN_PATH", filepath.Join(t.TempDir(), "token"))
	if tok, src := Token(k); tok != "hf_fromkeychain" || src != TokenKeychain {
		t.Errorf("keychain: %q %q", tok, src)
	}
	t.Setenv("HF_TOKEN", "hf_env")
	if tok, src := Token(k); tok != "hf_env" || src != TokenEnv {
		t.Errorf("env: %q %q", tok, src)
	}
	t.Setenv("HF_TOKEN", "")
	security = func(string, ...string) ([]byte, error) { return nil, ErrNoKeychain }
	os.WriteFile(os.Getenv("HF_TOKEN_PATH"), []byte("hf_file\n"), 0o600)
	if tok, src := Token(k); tok != "hf_file" || src != TokenCLI {
		t.Errorf("file: %q %q", tok, src)
	}
	if MaskToken("hf_abcdefghijkl") != "hf_…ijkl" || MaskToken("short") != "•••••" {
		t.Error(MaskToken("hf_abcdefghijkl"))
	}
}

func TestAuxRepos(t *testing.T) {
	h := newFakeHub(t)
	g2p := newFakeHub(t)
	g2p.repo = "acme/g2p"
	g2p.files = map[string][]byte{"config.json": []byte(`{"g2p":1}`), "g2p.safetensors": bytes.Repeat([]byte{3}, 3000)}
	g2p.lfs = map[string]bool{"g2p.safetensors": true}
	both := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/acme/g2p/") {
			g2p.serve(w, r)
		} else {
			h.serve(w, r)
		}
	}))
	defer both.Close()
	c := New(Options{CacheDir: t.TempDir(), Endpoints: []string{both.URL}, Official: both.URL, ChunkSize: 1024, ReserveBytes: -1})
	c.progressEvery = 0
	e := h.entry()
	e.Aux = []catalog.Aux{{Repo: "acme/g2p"}}
	ctx := context.Background()
	p, err := c.Plan(ctx, e)
	if err != nil || p.TotalBytes != sumSizes(h.files)+sumSizes(g2p.files) || len(p.Files) != len(h.files) {
		t.Fatalf("plan %+v %v", p, err)
	}
	var last Progress
	if err := c.Download(ctx, e, func(pr Progress) {
		if pr.Bytes < last.Bytes || pr.Repo != testRepo {
			t.Errorf("progress went from %+v to %+v", last, pr)
		}
		last = pr
	}); err != nil {
		t.Fatal(err)
	}
	if last.Bytes != p.TotalBytes {
		t.Errorf("last progress %+v", last)
	}
	l, _ := c.Status(ctx, e)
	if l.State != Downloaded || l.Bytes != p.TotalBytes {
		t.Errorf("status %+v", l)
	}
	if bad, err := c.Verify(ctx, e); err != nil || len(bad) != 0 {
		t.Errorf("verify %v %v", bad, err)
	}
	// Without its aux repo the model is not usable.
	os.RemoveAll(RepoDir(c.opts.CacheDir, "acme/g2p"))
	l, _ = c.Status(ctx, e)
	if l.State != Partial || !slices.Contains(l.Missing, "acme/g2p:") {
		t.Errorf("status without aux %+v", l)
	}
	if bad, _ := c.Verify(ctx, e); !slices.Equal(bad, []string{"acme/g2p:"}) {
		t.Errorf("verify without aux %v", bad)
	}
	// Deleting the model keeps aux repos (shared, small).
	c.Download(ctx, e, nil)
	c.Delete(ctx, e)
	if _, err := os.Stat(RepoDir(c.opts.CacheDir, "acme/g2p")); err != nil {
		t.Error("aux repo deleted")
	}
}

func TestSystemProxy(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : internal.example.com
  }
  ExcludeSimpleHostnames : 1
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 7897
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7898
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7899
  SOCKSProxy : 127.0.0.1
}
`
	p := parseScutil(out)
	u := func(s string) *url.URL { x, _ := url.Parse(s); return x }
	for in, want := range map[string]string{
		"https://huggingface.co/api":   "http://127.0.0.1:7898",
		"http://huggingface.co/":       "http://127.0.0.1:7897",
		"ftp://example.com/":           "socks5://127.0.0.1:7899",
		"https://printer.local/":       "",
		"https://169.254.3.4/":         "",
		"https://intranet/":            "",
		"https://internal.example.com": "",
	} {
		got := ""
		if x := p.forURL(u(in)); x != nil {
			got = x.String()
		}
		if got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
	if p := parseScutil("<dictionary> {\n  HTTPEnable : 0\n}\n"); p.forURL(u("https://huggingface.co")) != nil {
		t.Error("disabled proxy used")
	}

	// Modes.
	saved := scutilProxy
	defer func() { scutilProxy = saved; sysProxy.read = time.Time{} }()
	scutilProxy = func() (string, error) { return out, nil }
	sysProxy.Lock()
	sysProxy.read = time.Time{}
	sysProxy.Unlock()
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	req, _ := http.NewRequest("GET", "https://huggingface.co/api", nil)
	check := func(mode, want string) {
		t.Helper()
		got, err := proxyFunc(mode)(req)
		s := ""
		if got != nil {
			s = got.String()
		}
		if err != nil || s != want {
			t.Errorf("mode %q: %q %v, want %q", mode, s, err, want)
		}
	}
	check("", "http://127.0.0.1:7898")
	check("direct", "")
	check("socks5://10.0.0.2:1080", "socks5://10.0.0.2:1080")
	check("10.0.0.2:3128", "http://10.0.0.2:3128")
	local, _ := http.NewRequest("GET", "http://127.0.0.1:8080/api", nil)
	if got, _ := proxyFunc("http://10.0.0.2:3128")(local); got != nil {
		t.Error("loopback through a proxy")
	}
	if DescribeProxy("") != "http://127.0.0.1:7898" || DescribeProxy("direct") != "" {
		t.Error(DescribeProxy(""))
	}
}

func TestCleanEngineCopies(t *testing.T) {
	dir := t.TempDir()
	if got, err := CleanEngineCopies(dir); err != nil || got != nil {
		t.Fatalf("no dir: %v %v", got, err)
	}
	copies := EngineCopiesDir(dir)
	os.MkdirAll(filepath.Join(copies, "mlx-community_Empty"), 0o755)
	os.MkdirAll(filepath.Join(copies, "mlx-community_Full"), 0o755)
	os.WriteFile(filepath.Join(copies, "mlx-community_Full", "config.json"), []byte("{}"), 0o644)
	got, err := CleanEngineCopies(dir)
	if err != nil || !slices.Equal(got, []string{"mlx-community_Empty"}) {
		t.Fatalf("removed %v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(copies, "mlx-community_Full", "config.json")); err != nil {
		t.Error("removed a full copy")
	}
}
