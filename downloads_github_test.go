package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/ffmpeg"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/actnowkit/mirror"
)

// TestGitHubSources: the download source picks the sources of yt-dlp and
// ffmpeg (family spec §9.6).
func TestGitHubSources(t *testing.T) {
	t.Setenv(mirror.EnvSources, "")
	both := []string{mirror.GitHub, mirror.ActNow}
	for _, tc := range []struct {
		eps  []string
		want []string
	}{
		{nil, both}, // automatic
		{[]string{"https://huggingface.co"}, []string{mirror.GitHub}},
		{[]string{"https://hf-mirror.com/"}, []string{mirror.ActNow}},
		{[]string{"https://hf.example.com"}, both}, // custom: tools as automatic
		{[]string{"https://huggingface.co", "https://hf-mirror.com"}, both},
	} {
		var s settings.Settings
		s.Models.Endpoints = tc.eps
		if got := githubSources(s); !slices.Equal(got, tc.want) {
			t.Errorf("%v: %v, want %v", tc.eps, got, tc.want)
		}
	}
	t.Setenv(mirror.EnvSources, "http://127.0.0.1:1 http://127.0.0.1:2")
	if got := githubSources(settings.Settings{}); len(got) != 2 || got[0] != "http://127.0.0.1:1" {
		t.Errorf("$%s: %v", mirror.EnvSources, got)
	}
}

// TestSourceWordings: the options say what actnowkit's reference
// wordings say, in English and Chinese.
func TestSourceWordings(t *testing.T) {
	defer i18n.SetLocale("en")
	want := []struct{ en, zh string }{
		{mirror.TextSourceAuto.EN, mirror.TextSourceAuto.ZH},
		{mirror.TextSourceOfficial.EN, mirror.TextSourceOfficial.ZH},
		{mirror.TextSourceMirror.EN, mirror.TextSourceMirror.ZH},
	}
	for _, loc := range []string{"en", "zh-Hans"} {
		i18n.SetLocale(loc)
		opts := sourceOptions()
		for i, w := range want {
			exp := w.en
			if loc != "en" {
				exp = w.zh
			}
			if opts[i] != exp {
				t.Errorf("%s option %d: %q, want %q", loc, i, opts[i], exp)
			}
		}
		note := i18n.T("Models come from Hugging Face, ffmpeg and yt-dlp from GitHub. Every download is checked (signature or checksum), so a mirror cannot change what you get.")
		if exp := map[string]string{"en": mirror.TextSourceNote.EN, "zh-Hans": mirror.TextSourceNote.ZH}[loc]; note != exp {
			t.Errorf("%s note: %q, want %q", loc, note, exp)
		}
	}
	if !slices.Contains(netHosts, "https://mirror.actnow.dev") || !slices.Contains(netHosts, "https://github.com") {
		t.Errorf("connection test: %v", netHosts)
	}
}

func TestPinnedPaths(t *testing.T) {
	for _, a := range []ffmpeg.Asset{ffmpeg.Pinned.FFmpeg, ffmpeg.Pinned.FFprobe} {
		if !strings.HasPrefix(a.Path, "/shaka-project/static-ffmpeg-binaries/releases/download/") ||
			a.URL() != "https://github.com"+a.Path || len(a.SHA256) != 64 || a.Size <= 0 {
			t.Errorf("%+v", a)
		}
	}
}
