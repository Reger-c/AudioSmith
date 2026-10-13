package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

func waitJob(t *testing.T, s jobs.Scheduler, id string, ok func(jobs.Job) bool) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, err := s.Get(context.Background(), id)
		if err == nil && ok(j) {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s: %+v %v", id, j, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A download runs as a job, stops with the app (interrupted) and resumes
// where it stopped at the next start.
func TestDownloadJobSurvivesRestart(t *testing.T) {
	h := newFakeHub(t)
	c := h.client(t, func(o *Options) { o.Parallel = 1 })
	store := jobs.NewMemStore()
	ctx := context.Background()

	var quit atomic.Bool
	var sched1 *jobs.Sched
	sched1 = jobs.New(jobs.Options{Store: store, Logf: t.Logf})
	sched1.Register(KindDownload, DownloadExecutor(func() Downloader { return c }, func(p Progress) {
		if p.Bytes > p.TotalBytes/2 && quit.CompareAndSwap(false, true) {
			go sched1.Close() // the app quits half way through
		}
	}))
	sched1.Start(ctx)
	spec := DownloadSpec(catalog.Custom(testRepo), "Download")
	if spec.Lane != jobs.LaneNet || !spec.Idempotent || spec.Owner != "model:"+testRepo {
		t.Fatalf("spec %+v", spec)
	}
	id, err := sched1.Submit(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		js, _ := store.LoadJobs(ctx, jobs.Filter{States: []jobs.State{jobs.StateInterrupted, jobs.StateQueued}})
		if len(js) == 1 && quit.Load() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not interrupted: %+v", js)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if l, _ := c.Status(ctx, h.entry()); l.State != Partial {
		t.Fatalf("after quit: %+v", l)
	}
	p, _ := c.Plan(ctx, h.entry())
	served := h.count("served")

	sched2 := jobs.New(jobs.Options{Store: store, Logf: t.Logf})
	defer sched2.Close()
	sched2.Register(KindDownload, DownloadExecutor(func() Downloader { return c }, nil))
	sched2.Start(ctx)
	j := waitJob(t, sched2, id, func(j jobs.Job) bool { return j.State.Terminal() })
	if j.State != jobs.StateSucceeded {
		t.Fatalf("after restart: %+v %+v", j, j.Err)
	}
	var res DownloadResult
	json.Unmarshal(j.Result, &res)
	if res.Snapshot == "" || res.Bytes != sumSizes(h.files) {
		t.Errorf("result %+v", res)
	}
	checkSnapshot(t, c, h)
	if again := h.count("served") - served; again > p.Bytes+1024 {
		t.Errorf("resumed job fetched %d bytes, %d were left", again, p.Bytes)
	}
}

// Failures keep their kind; cancelling by id stops a download.
func TestDownloadJobErrorsAndCancel(t *testing.T) {
	h := newFakeHub(t)
	h.gated = true
	c := h.client(t, nil)
	sched := jobs.New(jobs.Options{Store: jobs.NewMemStore(), Logf: t.Logf})
	defer sched.Close()
	started := make(chan struct{})
	var once atomic.Bool
	sched.Register(KindDownload, DownloadExecutor(func() Downloader { return c }, func(Progress) {
		if once.CompareAndSwap(false, true) {
			close(started)
		}
	}))
	ctx := context.Background()
	sched.Start(ctx)
	id, _ := sched.Submit(ctx, DownloadSpec(catalog.Custom(testRepo), "Download"))
	j := waitJob(t, sched, id, func(j jobs.Job) bool { return j.State.Terminal() })
	if j.State != jobs.StateFailed || j.Err.Code != CodeGated || !errors.Is(ErrorOf(j.Err), ErrGated) {
		t.Fatalf("gated: %+v %+v", j, j.Err)
	}

	h.gated = false
	h.apiDelay.Store(int64(300 * time.Millisecond))
	id, _ = sched.Submit(ctx, DownloadSpec(catalog.Custom(testRepo), "Download"))
	waitJob(t, sched, id, func(j jobs.Job) bool { return j.State == jobs.StateRunning })
	if err := sched.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, sched, id, func(j jobs.Job) bool { return j.State.Terminal() }); j.State != jobs.StateCancelled {
		t.Fatalf("cancel: %+v", j)
	}
}
