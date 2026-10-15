package hub

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
)

// Errors of the Downloader; services map them to core sentinels
// (ErrGated → ErrLicenceRequired-like access errors, ErrNotFound →
// core.ErrNotFound…).
var (
	ErrGated     = errors.New("the repo is gated: accept its terms on huggingface.co and add a token")
	ErrNotFound  = errors.New("repo or revision not found")
	ErrDiskSpace = errors.New("not enough free disk space")
	ErrChecksum  = errors.New("checksum mismatch")
	ErrOffline   = errors.New("no endpoint answered")
)

// DefaultEndpoints are raced when Options.Endpoints is empty and
// $HF_ENDPOINT unset: the official hub and its best-known mirror.
var DefaultEndpoints = []string{"https://huggingface.co", "https://hf-mirror.com"}

// winnerTTL is how long a race's winner is reused before racing again.
const winnerTTL = 24 * time.Hour

// Client implements Downloader over the Hugging Face HTTP API.
type Client struct {
	opts Options
	http *http.Client

	// progressEvery is the least time between progress reports.
	progressEvery time.Duration

	mu       sync.Mutex
	winner   string // endpoint that won the last race
	wonAt    time.Time
	repoLock map[string]*sync.Mutex
}

var _ Downloader = (*Client)(nil)

// New returns a Client. Options left zero take their defaults.
func New(o Options) *Client {
	if o.CacheDir == "" {
		o.CacheDir = CacheDir()
	}
	env := strings.TrimRight(os.Getenv("HF_ENDPOINT"), "/")
	if o.Official == "" {
		o.Official = env
	}
	if o.Official == "" {
		o.Official = DefaultEndpoints[0]
	}
	o.Official = strings.TrimRight(o.Official, "/")
	if len(o.Endpoints) == 0 && env != "" {
		o.Endpoints = []string{env}
	}
	if len(o.Endpoints) == 0 {
		o.Endpoints = DefaultEndpoints
	}
	var eps []string
	for _, e := range o.Endpoints {
		if e = strings.TrimRight(strings.TrimSpace(e), "/"); e != "" && !slices.Contains(eps, e) {
			eps = append(eps, e)
		}
	}
	o.Endpoints = eps
	if o.Parallel <= 0 {
		o.Parallel = 4
	}
	if o.ChunkSize <= 0 {
		o.ChunkSize = 16 << 20
	}
	if o.ReserveBytes == 0 {
		o.ReserveBytes = 2e9
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = NewHTTPClient(o.Proxy, 30*time.Second)
	}
	return &Client{opts: o, http: hc, repoLock: map[string]*sync.Mutex{}, progressEvery: 250 * time.Millisecond}
}

// Options returns the options in use, defaults filled in.
func (c *Client) Options() Options { return c.opts }

func (c *Client) lockRepo(repo string) func() {
	c.mu.Lock()
	l := c.repoLock[repo]
	if l == nil {
		l = &sync.Mutex{}
		c.repoLock[repo] = l
	}
	c.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// ---- the HF API -------------------------------------------------------------

// repoInfo is what /api/models/{repo}/revision/{rev}?blobs=true says.
type repoInfo struct {
	ID       string          `json:"id"`
	SHA      string          `json:"sha"`
	Gated    json.RawMessage `json:"gated"` // false, "auto" or "manual"
	Siblings []struct {
		Path   string `json:"rfilename"`
		BlobID string `json:"blobId"`
		Size   int64  `json:"size"`
		LFS    *struct {
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"lfs"`
	} `json:"siblings"`
}

func (r *repoInfo) gated() bool {
	g := strings.TrimSpace(string(r.Gated))
	return g != "" && g != "false" && g != "null"
}

// apiError is a failed request.
type apiError struct {
	status int
	err    error
}

func (e *apiError) Error() string { return e.err.Error() }
func (e *apiError) Unwrap() error { return e.err }

func revisionOf(e catalog.Entry) string {
	if e.Revision == "" {
		return "main"
	}
	return e.Revision
}

var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

func isCommit(rev string) bool { return commitRE.MatchString(rev) }

func (c *Client) official(ep string) bool { return ep == c.opts.Official }

func (c *Client) newRequest(ctx context.Context, ep, rawURL, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "AudioSmith (hub)")
	// Mirrors never see the token. Go drops it on redirects to other
	// hosts (the CDN).
	if token != "" && c.official(ep) {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func (c *Client) fetchInfo(ctx context.Context, ep, repo, rev, token string) (*repoInfo, error) {
	u := ep + "/api/models/" + repo + "/revision/" + url.PathEscape(rev) + "?blobs=true"
	req, err := c.newRequest(ctx, ep, u, token)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &apiError{resp.StatusCode, fmt.Errorf("%s: %w (HTTP %d)", repo, ErrGated, resp.StatusCode)}
	case resp.StatusCode == http.StatusNotFound:
		return nil, &apiError{resp.StatusCode, fmt.Errorf("%s@%s: %w", repo, rev, ErrNotFound)}
	case resp.StatusCode != http.StatusOK:
		return nil, &apiError{resp.StatusCode, fmt.Errorf("%s: HTTP %d from %s", repo, resp.StatusCode, ep)}
	}
	var info repoInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&info); err != nil {
		return nil, fmt.Errorf("%s: %s: %w", repo, ep, err)
	}
	if info.SHA == "" {
		return nil, fmt.Errorf("%s: %s sent no commit", repo, ep)
	}
	return &info, nil
}

// info asks the endpoints for repo@rev, racing them unless a recent winner
// is known, and returns the answer and the endpoint that gave it. Gated
// repos are then asked of the official endpoint, with the token.
func (c *Client) info(ctx context.Context, repo, rev string) (*repoInfo, string, error) {
	token := ""
	if c.opts.Token != nil {
		token = strings.TrimSpace(c.opts.Token())
	}
	c.mu.Lock()
	winner := c.winner
	if time.Since(c.wonAt) > winnerTTL {
		winner = ""
	}
	c.mu.Unlock()

	var info *repoInfo
	var ep string
	var err error
	if winner != "" {
		info, err = c.fetchInfo(ctx, winner, repo, rev, token)
		ep = winner
		if err != nil && !isDefinite(err) {
			info, winner = nil, "" // the winner failed: race again
		}
	}
	if winner == "" {
		info, ep, err = c.race(ctx, repo, rev, token)
	}
	if err != nil {
		return nil, "", err
	}
	if info.gated() {
		if token == "" {
			return nil, "", fmt.Errorf("%s: %w", repo, ErrGated)
		}
		if !c.official(ep) {
			ep = c.opts.Official
			if info, err = c.fetchInfo(ctx, ep, repo, rev, token); err != nil {
				return nil, "", err
			}
		}
	}
	return info, ep, nil
}

// isDefinite reports an answer that another endpoint would repeat (the
// repo is missing or gated), as opposed to an endpoint being down.
func isDefinite(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && (ae.status == 401 || ae.status == 403 || ae.status == 404)
}

// race asks every endpoint at once and takes the first good answer.
func (c *Client) race(ctx context.Context, repo, rev, token string) (*repoInfo, string, error) {
	eps := c.opts.Endpoints
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		info *repoInfo
		ep   string
		err  error
	}
	ch := make(chan answer, len(eps))
	for _, ep := range eps {
		go func() {
			info, err := c.fetchInfo(ctx, ep, repo, rev, token)
			ch <- answer{info, ep, err}
		}()
	}
	var errs []error
	var definite error
	for range eps {
		a := <-ch
		if a.err == nil {
			c.mu.Lock()
			c.winner, c.wonAt = a.ep, time.Now()
			c.mu.Unlock()
			return a.info, a.ep, nil
		}
		if isDefinite(a.err) && definite == nil {
			definite = a.err
		}
		errs = append(errs, a.err)
	}
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if definite != nil {
		return nil, "", definite
	}
	return nil, "", fmt.Errorf("%s: %w: %w", repo, ErrOffline, errors.Join(errs...))
}

// ---- planning ---------------------------------------------------------------

// remoteFile is a file to download and the blob it becomes.
type remoteFile struct {
	catalog.File
	blob string // blob name: the LFS sha256, else the git blob id
	lfs  bool
}

type plan struct {
	Plan
	repo   string
	rev    string // what was asked: "main", a branch or a commit
	commit string
	ep     string
	files  []remoteFile
}

// Plan lists the files of e at its revision and checks the disk. Files
// are e's own; the byte counts include its auxiliary repos.
func (c *Client) Plan(ctx context.Context, e catalog.Entry) (Plan, error) {
	_, p, err := c.planAll(ctx, e)
	return p, err
}

func (c *Client) plan(ctx context.Context, e catalog.Entry) (*plan, error) {
	rev := revisionOf(e)
	info, ep, err := c.info(ctx, e.Repo, rev)
	if err != nil {
		return nil, err
	}
	p := &plan{repo: e.Repo, rev: rev, commit: info.SHA, ep: ep}
	p.Revision, p.Endpoint, p.Gated = info.SHA, ep, info.gated()
	restrict := len(e.Files) > 0
	blobs := map[string]bool{} // files with the same content share one blob, counted once
	want := map[string]catalog.File{}
	for _, f := range e.Files {
		want[f.Path] = f
	}
	for _, s := range info.Siblings {
		f := remoteFile{File: catalog.File{Path: s.Path, Size: s.Size}, blob: s.BlobID}
		if s.LFS != nil {
			f.lfs, f.SHA256, f.blob = true, s.LFS.SHA256, s.LFS.SHA256
			f.Size = s.LFS.Size
		}
		if restrict {
			w, ok := want[s.Path]
			if !ok {
				continue
			}
			if w.SHA256 != "" && w.SHA256 != f.SHA256 {
				return nil, fmt.Errorf("%s/%s: the catalogue pins sha256 %s, %s lists %s: %w", e.Repo, s.Path, w.SHA256, ep, f.SHA256, ErrChecksum)
			}
			delete(want, s.Path)
		}
		if f.blob == "" || !safeRel(f.Path) {
			return nil, fmt.Errorf("%s: bad file entry %q", e.Repo, f.Path)
		}
		p.files = append(p.files, f)
		p.Files = append(p.Files, f.File)
		if !blobs[f.blob] {
			blobs[f.blob] = true
			p.TotalBytes += f.Size
			p.Bytes += f.Size - c.have(e.Repo, f)
		}
	}
	for missing := range want {
		return nil, fmt.Errorf("%s@%s has no %s: %w", e.Repo, rev, missing, ErrNotFound)
	}
	return p, nil
}

// auxEntry is an auxiliary repo as an entry of its own.
func auxEntry(e catalog.Entry, a catalog.Aux) catalog.Entry {
	return catalog.Entry{ID: e.ID + "+" + a.Repo, Repo: a.Repo, Revision: a.Revision, Files: a.Files}
}

// planAll plans e's auxiliary repos and e (last), adds them up and checks
// the disk for all of them.
func (c *Client) planAll(ctx context.Context, e catalog.Entry) ([]*plan, Plan, error) {
	var plans []*plan
	var sum Plan
	for _, a := range e.Aux {
		p, err := c.plan(ctx, auxEntry(e, a))
		if err != nil {
			return nil, Plan{}, err
		}
		plans = append(plans, p)
		sum.TotalBytes += p.TotalBytes
		sum.Bytes += p.Bytes
	}
	p, err := c.plan(ctx, e)
	if err != nil {
		return nil, Plan{}, err
	}
	plans = append(plans, p)
	sum.Revision, sum.Files, sum.Endpoint, sum.Gated = p.Revision, p.Files, p.Endpoint, p.Gated
	sum.TotalBytes += p.TotalBytes
	sum.Bytes += p.Bytes
	sum.FreeBytes = freeBytes(c.opts.CacheDir)
	if c.opts.ReserveBytes > 0 && sum.FreeBytes >= 0 && sum.Bytes+c.opts.ReserveBytes > sum.FreeBytes {
		return plans, sum, fmt.Errorf("%s needs %s more and keeps %s free; %s free: %w",
			e.Repo, catalog.HumanSize(sum.Bytes), catalog.HumanSize(c.opts.ReserveBytes), catalog.HumanSize(sum.FreeBytes), ErrDiskSpace)
	}
	return plans, sum, nil
}

// safeRel reports whether a repo path stays inside the snapshot.
func safeRel(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && !strings.Contains(p, `\`) && path.Clean(p) == p && !strings.HasPrefix(p, "../") && p != ".."
}

// have is how many bytes of f are on disk already.
func (c *Client) have(repo string, f remoteFile) int64 {
	blobs := filepath.Join(RepoDir(c.opts.CacheDir, repo), "blobs")
	if st, err := os.Stat(filepath.Join(blobs, f.blob)); err == nil && st.Size() == f.Size {
		return f.Size
	}
	done, _ := partialDone(filepath.Join(blobs, f.blob), f.Size, c.chunks(f.Size))
	return done
}

// ---- downloading ------------------------------------------------------------

// chunks is how many ranges a file of size downloads in: 1 under two chunks.
func (c *Client) chunks(size int64) int {
	if size < 2*c.opts.ChunkSize {
		return 1
	}
	return int((size + c.opts.ChunkSize - 1) / c.opts.ChunkSize)
}

// partsFile records which chunks of a .incomplete blob are written.
type partsFile struct {
	Size  int64  `json:"size"`
	Chunk int64  `json:"chunk"`
	Done  []bool `json:"done"`
}

// partialDone reads what of blob's .incomplete is done: from its parts
// file when chunked, else the length of the file (written in order, by us
// or by huggingface_hub, which uses the same name).
func partialDone(blob string, size int64, chunks int) (int64, *partsFile) {
	var pf partsFile
	if data, err := os.ReadFile(blob + ".incomplete.parts"); err == nil && json.Unmarshal(data, &pf) == nil && pf.Size == size && len(pf.Done) == chunks && chunks > 1 {
		var done int64
		for i, d := range pf.Done {
			if d {
				done += min(pf.Chunk, size-int64(i)*pf.Chunk)
			}
		}
		return done, &pf
	}
	if st, err := os.Stat(blob + ".incomplete"); err == nil && st.Size() <= size {
		return st.Size(), nil
	}
	return 0, nil
}

// task is one request: a whole file, or one range of a big one.
type task struct {
	file  *fileState
	index int // chunk index
	from  int64
	to    int64 // exclusive
}

type fileState struct {
	remoteFile
	path   string // blobs/<blob>.incomplete
	f      *os.File
	mu     sync.Mutex
	parts  *partsFile // nil: sequential
	left   int        // tasks not done
	failed error
}

// Download fetches what Plan lists into the cache: the auxiliary repos
// first, then e's.
func (c *Client) Download(ctx context.Context, e catalog.Entry, progress func(Progress)) error {
	defer c.lockRepo(e.Repo)()
	plans, sum, err := c.planAll(ctx, e)
	if err != nil {
		return err
	}
	var base int64
	for _, p := range plans {
		unlock := func() {}
		if p.repo != e.Repo {
			unlock = c.lockRepo(p.repo)
		}
		err := c.fetchPlan(ctx, p, func(pr Progress) {
			if progress != nil {
				pr.Repo, pr.Bytes, pr.TotalBytes = e.Repo, base+pr.Bytes, sum.TotalBytes
				progress(pr)
			}
		})
		unlock()
		if err != nil {
			return err
		}
		base += p.TotalBytes
	}
	return nil
}

// fetchPlan downloads one repo's plan.
func (c *Client) fetchPlan(ctx context.Context, p *plan, progress func(Progress)) error {
	repoDir := RepoDir(c.opts.CacheDir, p.repo)
	blobs := filepath.Join(repoDir, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		return err
	}

	var done atomic.Int64
	done.Store(p.TotalBytes - p.Bytes)
	var current atomic.Value
	current.Store("")
	report := throttle(c.progressEvery, func(final bool) {
		if progress != nil {
			progress(Progress{Repo: p.repo, File: current.Load().(string), Bytes: done.Load(), TotalBytes: p.TotalBytes, Mirror: p.ep})
		}
	})
	report(true)

	// Tasks for what is missing.
	var tasks []task
	var states []*fileState
	scheduled := map[string]bool{}
	for _, f := range p.files {
		// Two files with the same content (identical prompts) are one
		// blob: fetch it once, or the two writers race on its .incomplete.
		if scheduled[f.blob] {
			continue
		}
		scheduled[f.blob] = true
		final := filepath.Join(blobs, f.blob)
		if st, err := os.Stat(final); err == nil && st.Size() == f.Size {
			continue
		}
		fs := &fileState{remoteFile: f, path: final + ".incomplete"}
		n := c.chunks(f.Size)
		have, parts := partialDone(final, f.Size, n)
		if n > 1 {
			if parts == nil {
				// Start a parts file: chunks inside what a sequential
				// writer left are done.
				parts = &partsFile{Size: f.Size, Chunk: c.opts.ChunkSize, Done: make([]bool, n)}
				for i := range n {
					parts.Done[i] = int64(i+1)*c.opts.ChunkSize <= have
				}
				if err := writeParts(fs.path+".parts", parts); err != nil {
					return err
				}
			}
			fs.parts = parts
			for i := range n {
				if !parts.Done[i] {
					from := int64(i) * c.opts.ChunkSize
					tasks = append(tasks, task{file: fs, index: i, from: from, to: min(from+c.opts.ChunkSize, f.Size)})
				}
			}
		} else {
			tasks = append(tasks, task{file: fs, from: have, to: f.Size})
		}
		fh, err := os.OpenFile(fs.path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return err
		}
		fs.f = fh
		defer fh.Close()
		states = append(states, fs)
	}
	for i := range tasks {
		tasks[i].file.left++
	}
	// Files with nothing left to fetch (all chunks were done) finish now.
	for _, fs := range states {
		if fs.left == 0 {
			if err := c.finishFile(ctx, fs); err != nil {
				return err
			}
		}
	}

	// Workers.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan task)
	var firstErr error
	var errOnce sync.Once
	fail := func(err error) {
		errOnce.Do(func() { firstErr = err; cancel() })
	}
	var wg sync.WaitGroup
	for range c.opts.Parallel {
		wg.Go(func() {
			for t := range queue {
				current.Store(t.file.Path)
				err := c.fetch(ctx, p, t, func(n int64) { done.Add(n); report(false) })
				if err != nil {
					fail(fmt.Errorf("%s/%s: %w", p.repo, t.file.Path, err))
					continue
				}
				if err := c.taskDone(ctx, t); err != nil {
					fail(err)
				}
			}
		})
	}
	for _, t := range tasks {
		if ctx.Err() != nil {
			break
		}
		select {
		case queue <- t:
		case <-ctx.Done():
		}
	}
	close(queue)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Every blob is there: link the snapshot, then write the ref.
	if err := linkSnapshot(repoDir, p.commit, p.files); err != nil {
		return err
	}
	ref := p.rev
	if isCommit(ref) {
		// A pinned commit is found by its path; tools that resolve the
		// repo by id (the engine for auxiliary repos and for families it
		// cannot load from a directory, the Python tools) read refs/main.
		// Point it here unless it names another snapshot that is there.
		ref = "main"
		if old, err := os.ReadFile(filepath.Join(repoDir, "refs", "main")); err == nil {
			if commit := strings.TrimSpace(string(old)); commit != p.commit && isDir(filepath.Join(repoDir, "snapshots", commit)) {
				ref = ""
			}
		}
	}
	if ref != "" {
		if err := writeAtomic(filepath.Join(repoDir, "refs", filepath.FromSlash(ref)), []byte(p.commit)); err != nil {
			return err
		}
	}
	report(true)
	return nil
}

// throttle calls fn at most once every d, and always when final.
func throttle(d time.Duration, fn func(final bool)) func(final bool) {
	var mu sync.Mutex
	var last time.Time
	return func(final bool) {
		mu.Lock()
		defer mu.Unlock()
		if !final && time.Since(last) < d {
			return
		}
		last = time.Now()
		fn(final)
	}
}

// fetch downloads one task, retrying transient failures from where it got.
func (c *Client) fetch(ctx context.Context, p *plan, t task, add func(int64)) error {
	u := p.ep + "/" + p.repo + "/resolve/" + p.commit + "/" + escapePath(t.file.Path)
	token := ""
	if c.opts.Token != nil {
		token = strings.TrimSpace(c.opts.Token())
	}
	from := t.from
	var err error
	for attempt := range 4 {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt*attempt) * 500 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if from >= t.to && t.to > 0 {
			return nil
		}
		var n int64
		n, err = c.fetchRange(ctx, p.ep, u, token, t, from, add)
		from += n
		if err == nil || ctx.Err() != nil || !retryable(err) {
			break
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

func retryable(err error) bool {
	var pe permanent
	return !errors.As(err, &pe)
}

// fetchRange writes bytes [from, t.to) of the file at their offsets and
// returns how many it wrote.
func (c *Client) fetchRange(ctx context.Context, ep, u, token string, t task, from int64, add func(int64)) (int64, error) {
	req, err := c.newRequest(ctx, ep, u, token)
	if err != nil {
		return 0, permanent{err}
	}
	ranged := from > 0 || t.to < t.file.Size
	if ranged {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, t.to-1))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent:
	case resp.StatusCode == http.StatusOK:
		if from > 0 || t.to < t.file.Size {
			if t.file.parts != nil {
				return 0, permanent{fmt.Errorf("%s does not serve ranges", ep)}
			}
			// The server ignored the range: start the file again.
			add(-from)
			from = 0
			if err := t.file.f.Truncate(0); err != nil {
				return 0, permanent{err}
			}
		}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return 0, permanent{fmt.Errorf("%w (HTTP %d)", ErrGated, resp.StatusCode)}
	case resp.StatusCode == http.StatusNotFound:
		return 0, permanent{ErrNotFound}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return 0, permanent{fmt.Errorf("HTTP 416 for bytes %d-%d", from, t.to-1)}
	default:
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 256<<10)
	off := from
	for off < t.to {
		n, rerr := resp.Body.Read(buf[:min(int64(len(buf)), t.to-off)])
		if n > 0 {
			if _, err := t.file.f.WriteAt(buf[:n], off); err != nil {
				return off - from, permanent{err}
			}
			off += int64(n)
			add(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return off - from, rerr
		}
	}
	if off < t.to {
		return off - from, io.ErrUnexpectedEOF
	}
	return off - from, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// taskDone records a finished task and finishes its file after the last.
func (c *Client) taskDone(ctx context.Context, t task) error {
	fs := t.file
	fs.mu.Lock()
	fs.left--
	last := fs.left == 0
	var err error
	if fs.parts != nil {
		fs.parts.Done[t.index] = true
		err = writeParts(fs.path+".parts", fs.parts)
	}
	fs.mu.Unlock()
	if err != nil {
		return err
	}
	if last {
		return c.finishFile(ctx, fs)
	}
	return nil
}

// finishFile checks a complete .incomplete blob and moves it in place.
func (c *Client) finishFile(ctx context.Context, fs *fileState) error {
	if err := fs.f.Sync(); err != nil {
		return err
	}
	ok, err := checkBlob(ctx, fs.path, fs.remoteFile)
	if err != nil {
		return err
	}
	if !ok {
		os.Remove(fs.path)
		os.Remove(fs.path + ".parts")
		return fmt.Errorf("%s: %w", fs.Path, ErrChecksum)
	}
	if err := os.Rename(fs.path, strings.TrimSuffix(fs.path, ".incomplete")); err != nil {
		return err
	}
	os.Remove(fs.path + ".parts")
	return nil
}

// checkBlob hashes a file: sha256 for LFS files, the git blob id for others.
func checkBlob(ctx context.Context, path string, f remoteFile) (bool, error) {
	fh, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return false, err
	}
	if st.Size() != f.Size {
		return false, nil
	}
	var h hash.Hash
	want := f.blob
	if f.lfs || f.SHA256 != "" {
		h = sha256.New()
		want = f.SHA256
	} else {
		h = sha1.New()
		fmt.Fprintf(h, "blob %d\x00", f.Size)
	}
	if want == "" {
		return true, nil
	}
	if _, err := io.Copy(h, ctxReader{ctx, fh}); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == strings.ToLower(want), nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func writeParts(path string, p *partsFile) error {
	data, _ := json.Marshal(p)
	return writeAtomic(path, data)
}

// writeAtomic writes data to path through a temporary file and a rename.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// linkSnapshot makes snapshots/<commit>/<path> → ../../blobs/<blob> for
// every file. A new snapshot is built beside its place and renamed in, so
// it is never half there; one that exists (made by another tool) gets the
// links it lacks.
func linkSnapshot(repoDir, commit string, files []remoteFile) error {
	snaps := filepath.Join(repoDir, "snapshots")
	final := filepath.Join(snaps, commit)
	_, err := os.Stat(final)
	exists := err == nil
	dir := final
	if !exists {
		dir = final + ".incomplete"
		os.RemoveAll(dir)
	}
	for _, f := range files {
		link := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return err
		}
		depth := strings.Count(f.Path, "/") + 2 // up from the file's directory to repoDir
		target := strings.Repeat("../", depth) + "blobs/" + f.blob
		if cur, err := os.Readlink(link); err == nil && cur == target {
			continue
		}
		os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}
	if !exists {
		return os.Rename(dir, final)
	}
	return nil
}
