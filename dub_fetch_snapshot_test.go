package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsDubFetch renders a dub's URL download (thread #99): at 37 %
// with its size and time left on the page, in its footer and on the
// activity card; before its size is known and while merging (no bar,
// and no empty track on the card), light and dark, in English and
// Chinese (dub-fetch-*.png):
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsDubFetch .
func TestSnapshotsDubFetch(t *testing.T) { runSnapshots(t, snapshotsDubFetch) }

func snapshotsDubFetch(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range []struct {
		lang string
		dark bool
	}{{"en", false}, {"en", true}, {"zh-Hans", false}, {"zh-Hans", true}} {
		i18n.SetLocale("en")
		d := newDubTest(t, 3)
		a, tt := d.a, d.tt
		if run.lang != "en" {
			a.updateSettings(func(s *settings.Settings) { s.General.Language = run.lang })
		}
		tt.SetDark(run.dark)
		tt.SetScale(2)
		suffix := "light"
		if run.dark {
			suffix = "dark"
		}
		if run.lang != "en" {
			suffix += "-" + run.lang
		}
		save := func(name string) {
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, "dub-fetch-"+name+"-"+suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		fetch := startFetch(t, d)
		a.toasts = nil

		// Before yt-dlp says the size: a spinner, no bar on the card.
		update(a, fetch, jobs.Progress{Stage: services.FetchDownloading, Fraction: -1})
		save("starting")

		// 37 %: the bar, the size and the time left, everywhere.
		update(a, fetch, jobs.Progress{Stage: services.FetchDownloading, Fraction: 0.37, Bytes: 48e6, TotalBytes: 130e6, ETA: 75})
		save("progress")

		// Joining the streams: no fraction again, no bar.
		update(a, fetch, jobs.Progress{Stage: services.FetchMerging, Fraction: -1})
		save("merging")
	}
}

// startFetch opens a dub of a URL named after the video, with its fetch
// running and the stages after it waiting, as Run leaves them (the fake
// scheduler runs nothing of a dub here: its jobs are published by hand).
func startFetch(t *testing.T, d *dubTest) jobs.Job {
	t.Helper()
	a := d.a
	u := "https://www.youtube.com/watch?v=jNQXAC9IVRw"
	a.newDub(dub.Source{Kind: dub.SourceURL, URL: u, Name: u})
	a.dubSet(func(p *dub.Project) { p.Title, p.Source.Name = "Me at the zoo", "Me at the zoo" }) // as fetch names it
	id := a.dub.ID
	at := time.Date(2026, 10, 23, 23, 20, 0, 0, time.UTC)
	var fetch jobs.Job
	for i, st := range []dub.Stage{dub.StageFetch, dub.StageIngest, dub.StageSeparate, dub.StageTranscribe, dub.StageReferences} {
		j := jobs.Job{ID: "fetch-" + string(st), State: jobs.StateWaiting, Created: at.Add(time.Duration(i) * time.Second),
			Spec: jobs.Spec{Kind: "dub." + string(st), Owner: services.DubOwner + id, Title: "Dub: " + u}}
		if i == 0 {
			j.State, j.Started = jobs.StateRunning, at
			fetch = j
		}
		a.bus.Publish(jobs.Updated{Job: j})
	}
	return fetch
}

// update publishes a running job's progress.
func update(a *app, j jobs.Job, pr jobs.Progress) {
	j.Progress = pr
	a.bus.Publish(jobs.Updated{Job: j})
}
