package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// The probe's errors read as what went wrong, with yt-dlp's reason cut
// short (the reasons recorded on the Mac: internal/dub/testdata/ytdlp-info).
func TestDubProbeErrorText(t *testing.T) {
	for name, want := range map[string]string{
		"vimeo": "The site wants a sign-in (or its cookies) for this video.", "douyin": "The site wants a sign-in (or its cookies) for this video.",
		"twitter": "This video is unavailable.", "private": "This video is unavailable.", "unsupported": "This site is not supported for importing.",
	} {
		b, err := os.ReadFile(filepath.Join("../internal/dub/testdata/ytdlp-info", name+".err"))
		if err != nil {
			t.Fatal(err)
		}
		detail := strings.TrimSpace(string(b))
		detail = detail[strings.Index(detail, ": ")+2:]
		head, why := dubProbeErrorText(dub.CodeYtDlp, detail)
		if head != want {
			t.Errorf("%s: %q, want %q", name, head, want)
		}
		if strings.Contains(why, "--cookies") || strings.Contains(why, "https://github.com") {
			t.Errorf("%s: reason %q keeps yt-dlp's advice", name, why)
		}
	}
	if head, _ := dubProbeErrorText(dub.CodeYtDlp, "Private video. Sign in if you've been granted access"); head != "This video is private." {
		t.Errorf("private: %q", head)
	}
	if head, why := dubProbeErrorText(dub.CodeURLPlaylist, ""); head != "That is a playlist: paste the link of one video." || why != "" {
		t.Errorf("playlist: %q %q", head, why)
	}
}

// The facts line says what is known, in the language shown.
func TestDubSourceFacts(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	p := &dub.Project{Source: dub.Source{Kind: dub.SourceURL}}
	if got := dubSourceFacts(p); got != "" {
		t.Errorf("nothing known: %q", got)
	}
	m := snapMetaBilibili()
	p.Source.Meta = &m
	if got := dubSourceFacts(p); got != "3:32 · 1080p · ≈ 75.2 MB · Dec 31, 2019 · 107.6M views" {
		t.Errorf("en: %q", got)
	}
	i18n.SetLocale("zh-Hans")
	if got := dubSourceFacts(p); got != "3:32 · 1080p · ≈ 75.2 MB · 2019年12月31日 · 1.1亿 次播放" {
		t.Errorf("zh: %q", got)
	}
	i18n.SetLocale("en")
	m.Width, m.Height, m.Views, m.Uploaded, m.Size = 576, 1024, 1, "", 0
	if got := dubSourceFacts(p); got != "3:32 · 576×1024 · 1 view" {
		t.Errorf("upright: %q", got)
	}
	// Once read, the file's own facts.
	p.Source.Duration, p.Source.Size = 19, 742369
	p.Source.Video = &dub.VideoInfo{Codec: "h264", Width: 320, Height: 240}
	if got := dubSourceFacts(p); !strings.HasPrefix(got, "0:19 · 240p H264 · 742 KB") {
		t.Errorf("read: %q", got)
	}
	for n, want := range map[int64]string{999: "999", 1234: "1,234", 45678: "45.7K", 2_000_000: "2M", 3_210_000_000: "3.2B"} {
		if got := compactCount(n); got != want {
			t.Errorf("compactCount(%d) = %q, want %q", n, got, want)
		}
	}
}

// The video shows once it is on disk in a form the preview plays.
func TestDubVideoReady(t *testing.T) {
	yt := snapMetaYouTube()
	audio := dub.SourceMeta{VCodec: "none", ACodec: "opus", Views: -1}
	for _, tc := range []struct {
		name string
		p    dub.Project
		want bool
	}{
		{"not downloaded", dub.Project{Source: dub.Source{Kind: dub.SourceURL, Meta: &yt}}, false},
		{"downloaded", dub.Project{Source: dub.Source{Kind: dub.SourceURL, Path: "/d/source.mp4", Meta: &yt}}, true},
		{"audio only", dub.Project{Source: dub.Source{Kind: dub.SourceURL, Path: "/d/source.m4a", Meta: &audio}}, false},
		{"webm, no preview yet", dub.Project{Source: dub.Source{Path: "/v/a.webm", HasVideo: true, Video: &dub.VideoInfo{Codec: "vp9"}}}, false},
		{"webm, preview made", dub.Project{Source: dub.Source{Path: "/v/a.webm", HasVideo: true}, Media: dub.Media{Proxy: "preview.mp4"}}, true},
		{"a file read", dub.Project{Source: dub.Source{Path: "/v/a.mov", HasVideo: true, Video: &dub.VideoInfo{Codec: "hevc"}}}, true},
		{"a WAV", dub.Project{Source: dub.Source{Path: "/v/a.wav"}, Media: dub.Media{Audio: "audio.wav"}}, false},
	} {
		if got := dubVideoReady(&tc.p); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// While the link is read, the footer says so with Cancel, the stepper's
// Source step spins, and Cancel stops it (Dubs.Cancel, as a run's).
func TestDubProbingFooter(t *testing.T) {
	d := newDubTest(t, 3)
	a, tt := d.a, d.tt
	u := "https://www.youtube.com/watch?v=jNQXAC9IVRw"
	a.newDub(dub.Source{Kind: dub.SourceURL, URL: u, Name: u})
	a.dubProbes[a.dub.ID] = true
	tt.Frame()
	if !slices.Contains(tt.Texts(), "Reading the video's details…") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if _, running, _ := a.dubStepState(dubStepSource); !running {
		t.Error("the Source step does not spin")
	}
	d.click(t, "Cancel")
	if a.dubSvc.Probing(a.dub.ID) {
		t.Error("still probing")
	}
	delete(a.dubProbes, a.dub.ID)
	tt.Frame()
	if !slices.Contains(tt.Texts(), "Read the video's details") {
		t.Errorf("no way to read them again: %q", tt.Texts())
	}
}
