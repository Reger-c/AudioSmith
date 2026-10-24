package main

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsDubSource renders the Dub page's centre before the
// transcript (thread #106): reading a link's details; its details (a
// YouTube-like video with a long description, a Bilibili-like one with
// an estimated size and no description, one with little known); the
// download at 37 % under the cover; the video downloaded (its poster: no
// web view in tests) with the details under it while the next steps run;
// a link that could not be read; a local file. Chinese and English at
// 1100×720 and 820×520 @125 % (dub-src-*.png):
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsDubSource .
func TestSnapshotsDubSource(t *testing.T) { runSnapshots(t, snapshotsDubSource) }

func snapshotsDubSource(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, cfg := range []b2Config{
		{"1100-zh", 1100, 720, 1, false, "zh-Hans"},
		{"min125-zh", 820, 520, 1.25, false, "zh-Hans"},
		{"1100-en", 1100, 720, 1, false, "en"},
		{"min125-en", 820, 520, 1.25, false, "en"},
	} {
		i18n.SetLocale("en")
		d := newDubTest(t, 3)
		a, tt := d.a, d.tt
		a.updateSettings(func(s *settings.Settings) {
			s.General.Language = cfg.lang
			s.Appearance.Scale = cfg.scale
		})
		tt.SetSize(cfg.w, cfg.h)
		tt.SetScale(2)
		shoot := func(name string) {
			a.toasts = nil
			tt.Frame()
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, "dub-src-"+name+"-"+cfg.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		u := "https://www.youtube.com/watch?v=jNQXAC9IVRw"
		a.newDub(dub.Source{Kind: dub.SourceURL, URL: u, Name: u})
		id := a.dub.ID

		// Reading the link: a spinner where the cover goes, the link.
		a.dubProbes[id] = true
		shoot("probing")
		delete(a.dubProbes, id)

		// Its details: cover, site, channel, title, facts, description.
		writeCover(t, a.dubSvc.Path(id, "thumb.jpg"), 1280, 720, color.RGBA{38, 110, 120, 255})
		yt := snapMetaYouTube()
		a.dubSet(func(p *dub.Project) { p.Source.Meta, p.Title, p.Source.Name = &yt, yt.Title, yt.Title })
		shoot("info-youtube")
		a.dubDescOpen = true
		shoot("info-youtube-expanded")
		a.dubDescOpen = false

		bili := snapMetaBilibili()
		writeCover(t, a.dubSvc.Path(id, "thumb2.jpg"), 1146, 717, color.RGBA{200, 80, 120, 255})
		bili.Thumbnail = "thumb2.jpg"
		a.dubSet(func(p *dub.Project) { p.Source.Meta, p.Title, p.Source.Name = &bili, bili.Title, bili.Title })
		shoot("info-bilibili")

		little := dub.SourceMeta{Site: "Generic", Title: "clip_0423_final.mp4", Views: -1, At: bili.At}
		a.dubSet(func(p *dub.Project) { p.Source.Meta, p.Title, p.Source.Name = &little, little.Title, little.Title })
		shoot("info-missing")

		// The download at 37 %: the bar and size under the cover.
		a.dubSet(func(p *dub.Project) { p.Source.Meta, p.Title, p.Source.Name = &yt, yt.Title, yt.Title })
		fetch := startFetchOf(t, d, id)
		update(a, fetch, jobs.Progress{Stage: services.FetchDownloading, Fraction: 0.37, Bytes: 48e6, TotalBytes: 130e6, ETA: 75})
		shoot("downloading")

		// Downloaded and read, transcribing: the video (its poster here),
		// play, the details; the footer says what runs.
		fetch.State = jobs.StateSucceeded
		a.bus.Publish(jobs.Updated{Job: fetch})
		writeCover(t, a.dubSvc.Path(id, "poster.jpg"), 640, 360, color.RGBA{30, 60, 90, 255})
		a.dubSet(func(p *dub.Project) {
			p.Source.Path, p.Source.Size, p.Source.Duration, p.Source.HasVideo = a.dubSvc.Path(id, "download/source.mp4"), 742369, 19, true
			p.Source.Video = &dub.VideoInfo{Codec: "h264", Width: 320, Height: 240}
			p.Media.Audio, p.Media.Poster = "audio.wav", "poster.jpg"
		})
		for _, k := range []string{"fetch-ingest", "fetch-separate"} {
			j := jobs.Job{ID: k, State: jobs.StateSucceeded, Spec: jobs.Spec{Kind: "dub." + strings.TrimPrefix(k, "fetch-"), Owner: services.DubOwner + id}}
			a.bus.Publish(jobs.Updated{Job: j})
		}
		tr := jobs.Job{ID: "fetch-transcribe", State: jobs.StateRunning, Spec: jobs.Spec{Kind: services.KindDubTranscribe, Owner: services.DubOwner + id}}
		tr.Progress = jobs.Progress{Fraction: 0.4}
		a.bus.Publish(jobs.Updated{Job: tr})
		a.dubStep = dubStepTranscribe
		shoot("video")
		tr.State = jobs.StateCancelled
		a.bus.Publish(jobs.Updated{Job: tr})
		a.bus.Publish(jobs.Updated{Job: jobs.Job{ID: "fetch-references", State: jobs.StateCancelled, Spec: jobs.Spec{Owner: services.DubOwner + id}}})
		a.dubStep = dubStepSource

		// A link that could not be read.
		a.newDub(dub.Source{Kind: dub.SourceURL, URL: "https://www.youtube.com/watch?v=aaaaaaaaaaa", Name: "https://www.youtube.com/watch?v=aaaaaaaaaaa"})
		bad := dub.SourceMeta{Error: dub.CodeYtDlp, Detail: "Private video. Sign in if you've been granted access to this video. Use --cookies, --cookies-from-browser for the authentication.", Views: -1, At: bili.At}
		a.dubSet(func(p *dub.Project) { p.Source.Meta = &bad })
		shoot("error")

		// A local file: its poster frame, name, length, picture and size.
		a.newDub(dub.Source{Kind: dub.SourceFile, Path: d.src, Name: "Interview.mov", Size: 52_400_000})
		writeCover(t, a.dubSvc.Path(a.dub.ID, "poster.jpg"), 640, 360, color.RGBA{90, 70, 40, 255})
		a.dubSet(func(p *dub.Project) {
			p.Source.HasVideo, p.Source.Duration, p.Media.Poster = true, 754, "poster.jpg"
			p.Source.Video = &dub.VideoInfo{Codec: "hevc", Width: 1920, Height: 1080}
		})
		shoot("file")
	}
}

func snapMetaYouTube() dub.SourceMeta {
	return dub.SourceMeta{Site: "Youtube", SiteName: "YouTube", ID: "jNQXAC9IVRw", Title: "Me at the zoo", Channel: "jawed",
		Uploaded: "2005-04-24", Duration: 19, Views: 446162447, Captions: []string{"de", "en", "ja"}, Format: "133+140", Ext: "mp4",
		VCodec: "avc1.4D400C", Width: 320, Height: 240, Size: 742369, Thumbnail: "thumb.jpg",
		Description: "The first video on YouTube. While you wait for Part 2, listen to this great song: the elephants are cool, " +
			"they have really, really, really long trunks, and that's cool.\nAnd that's pretty much all there is to say.\n\n" +
			"Filmed at the San Diego Zoo, April 23, 2005, by Yakov Lapitsky. Uploaded the next day.\n#zoo #elephants #history",
		At: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
}

func snapMetaBilibili() dub.SourceMeta {
	return dub.SourceMeta{Site: "BiliBili", SiteName: "Bilibili", ID: "BV1GJ411x7h7", Title: "【官方 MV】Never Gonna Give You Up - Rick Astley",
		Channel: "索尼音乐中国", Uploaded: "2019-12-31", Duration: 212.393, Views: 107601128, Format: "30080+30280", Ext: "mp4",
		VCodec: "avc1.640032", Width: 1920, Height: 1080, Size: 75219776, Approx: true, At: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
}

// startFetchOf publishes project id's fetch as running and the stages
// after it as waiting (as startFetch does for a new project).
func startFetchOf(t *testing.T, d *dubTest, id string) jobs.Job {
	t.Helper()
	at := time.Date(2026, 10, 24, 9, 1, 0, 0, time.UTC)
	var fetch jobs.Job
	for i, st := range []dub.Stage{dub.StageFetch, dub.StageIngest, dub.StageSeparate, dub.StageTranscribe, dub.StageReferences} {
		j := jobs.Job{ID: "fetch-" + string(st), State: jobs.StateWaiting, Created: at.Add(time.Duration(i) * time.Second),
			Spec: jobs.Spec{Kind: "dub." + string(st), Owner: services.DubOwner + id, Title: "Dub: Me at the zoo"}}
		if i == 0 {
			j.State, j.Started = jobs.StateRunning, at
			fetch = j
		}
		d.a.bus.Publish(jobs.Updated{Job: j})
	}
	return fetch
}

// writeCover writes a JPEG picture standing for a cover or a poster: a
// gradient with a lighter disc, w×h.
func writeCover(t *testing.T, path string, w, h int, base color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f := float64(x+y) / float64(w+h)
			c := color.RGBA{uint8(float64(base.R) * (0.6 + 0.4*f)), uint8(float64(base.G) * (0.6 + 0.4*f)), uint8(float64(base.B) * (0.6 + 0.4*f)), 255}
			dx, dy := x-w*2/3, y-h/2
			if dx*dx+dy*dy < (h/4)*(h/4) {
				c = color.RGBA{min(c.R+60, 255), min(c.G+60, 255), min(c.B+60, 255), 255}
			}
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
}
