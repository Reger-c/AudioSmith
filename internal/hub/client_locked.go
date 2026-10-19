package hub

// Downloading one repo under the family locks (spec §7.4, actnowkit
// models): the repo's lock shared throughout, so that no app deletes it
// meanwhile; each missing blob's lock while fetching it, so that two apps
// (or huggingface_hub, which locks the same files) never write one blob
// at once, and a download waits for another app's instead of fetching it
// again; the repo's lock exclusive to link the snapshot and write the ref.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
)

// fetchRun is the download of one repo's plan.
type fetchRun struct {
	c       *Client
	p       *plan
	blobs   string // the repo's blobs directory
	done    atomic.Int64
	current atomic.Value // string: the file being fetched
	// waitFor is the app whose download of a blob this run waits for
	// (*string; nil when not waiting), extra the bytes of it written.
	waitFor atomic.Pointer[string]
	extra   atomic.Int64
	locks   map[string]*models.HeldLock // blob locks held, by blob
	report  func(final bool)
}

// waitPoll is how often a download waiting for another app's blob
// reports how far that one got.
var waitPoll = 500 * time.Millisecond

// fetchPlan downloads one repo's plan.
func (c *Client) fetchPlan(ctx context.Context, p *plan, progress func(Progress)) error {
	repoDir := RepoDir(c.opts.CacheDir, p.repo)
	r := &fetchRun{c: c, p: p, blobs: filepath.Join(repoDir, "blobs"), locks: map[string]*models.HeldLock{}}
	if err := os.MkdirAll(r.blobs, 0o755); err != nil {
		return err
	}
	r.current.Store("")
	r.report = throttle(c.progressEvery, func(bool) {
		if progress == nil {
			return
		}
		pr := Progress{Repo: p.repo, File: r.current.Load().(string), Bytes: r.done.Load(), TotalBytes: p.TotalBytes, Mirror: p.ep}
		if w := r.waitFor.Load(); w != nil {
			pr.Waiting, pr.WaitingFor = true, *w
			pr.Bytes += r.extra.Load()
		}
		progress(pr)
	})
	held, err := models.LockRepo(ctx, c.opts.CacheDir, p.repo, family.Shared, c.opts.Family.owner(models.OpDownload))
	if err != nil && !noLocks(err) {
		return err
	}
	defer held.Release()
	defer r.releaseAll()

	// What is missing: two files with the same content (identical
	// prompts) are one blob, fetched once.
	var missing []remoteFile
	scheduled := map[string]bool{}
	for _, f := range p.files {
		if scheduled[f.blob] {
			continue
		}
		scheduled[f.blob] = true
		if r.complete(f) {
			r.done.Add(f.Size)
		} else {
			missing = append(missing, f)
		}
	}
	r.report(true)

	// The blobs nobody else is fetching first, then those another app
	// is: wait for each, and fetch what it did not finish.
	var ready, busy []remoteFile
	for _, f := range missing {
		l, err := models.TryLockBlob(c.opts.CacheDir, p.repo, f.blob, c.opts.Family.owner(models.OpDownload))
		if _, isBusy := Holder(err); isBusy {
			busy = append(busy, f)
			continue
		}
		if err != nil && !noLocks(err) {
			return err
		}
		if r.take(f, l) {
			ready = append(ready, f)
		}
	}
	if err := r.fetchFiles(ctx, ready); err != nil {
		return err
	}
	for _, f := range busy {
		l, err := r.wait(ctx, f)
		if err != nil {
			return err
		}
		if r.take(f, l) {
			if err := r.fetchFiles(ctx, []remoteFile{f}); err != nil {
				return err
			}
		}
	}
	r.releaseAll()

	// Every blob is there: link the snapshot, then write the ref, holding
	// the repo exclusively. Upgrading lets others in between: check again.
	if held != nil {
		if err := held.Upgrade(ctx, models.OpFinish); err != nil {
			return err
		}
	}
	for _, f := range p.files {
		if !r.complete(f) {
			return fmt.Errorf("%s/%s went while downloading (another app deleted the model?); download again", p.repo, f.Path)
		}
	}
	if err := linkSnapshot(repoDir, p.commit, p.files); err != nil {
		return err
	}
	if err := writeRef(repoDir, p); err != nil {
		return err
	}
	// The verified sizes, so that a truncated file is not taken for a
	// model (spec §7.5), and Time Machine's mark (spec §7.12).
	sizes := map[string]int64{}
	for _, f := range p.files {
		sizes[f.blob] = f.Size
	}
	if err := models.RecordBlobSizes(c.opts.CacheDir, p.repo, sizes); err != nil {
		log.Printf("hub: sizes of %s: %v", p.repo, err)
	}
	_ = models.MarkRepoBackup(c.opts.CacheDir, p.repo, c.opts.Backup != nil && c.opts.Backup())
	// Still holding the repo exclusively: claim it before anyone may
	// delete it (spec §7.3).
	if claimed := c.opts.Claimed; claimed != nil {
		d := Finished{Entry: p.owner, Repo: p.repo, Commit: p.commit, Files: p.only, Aux: p.aux}
		if err := claimed(ctx, c.opts.CacheDir, d); err != nil {
			log.Printf("hub: claim %s@%s: %v", p.repo, p.commit, err)
		}
	}
	r.report(true)
	return nil
}

// complete reports whether f's blob is in the cache.
func (r *fetchRun) complete(f remoteFile) bool {
	st, err := os.Stat(filepath.Join(r.blobs, f.blob))
	return err == nil && st.Size() == f.Size
}

// take keeps the lock l of f's blob and reports whether f still needs
// fetching; when another app finished it meanwhile, l goes.
func (r *fetchRun) take(f remoteFile, l *models.HeldLock) bool {
	if r.complete(f) {
		l.Release()
		r.done.Add(f.Size)
		r.report(false)
		return false
	}
	r.locks[f.blob] = l
	return true
}

// wait takes the lock of f's blob, which another app holds (it downloads
// the blob), reporting meanwhile who and how far its partial file got.
func (r *fetchRun) wait(ctx context.Context, f remoteFile) (*models.HeldLock, error) {
	poll := func() {
		name := ""
		if o, ok := models.ReadOwner(models.BlobLockFile(r.c.opts.CacheDir, r.p.repo, f.blob)); ok {
			name = AppName(o.BundleID)
		}
		have, _ := partialDone(filepath.Join(r.blobs, f.blob), f.Size, r.c.chunks(f.Size))
		r.waitFor.Store(&name)
		r.extra.Store(have)
		r.report(true)
	}
	poll()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(waitPoll)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				poll()
			}
		}
	})
	l, err := models.LockBlob(ctx, r.c.opts.CacheDir, r.p.repo, f.blob, r.c.opts.Family.owner(models.OpDownload))
	close(stop)
	wg.Wait()
	r.waitFor.Store(nil)
	r.extra.Store(0)
	r.report(true)
	return l, err
}

// noLocks reports a lock the model folder's file system cannot take
// (no flock(2)): the download goes on without it, as before the family
// locks.
func noLocks(err error) bool {
	var pe *os.PathError
	return errors.Is(err, family.ErrLockUnsupported) || errors.As(err, &pe) && pe.Op == "flock"
}

// releaseAll releases the blob locks still held.
func (r *fetchRun) releaseAll() {
	for b, l := range r.locks {
		l.Release()
		delete(r.locks, b)
	}
}
