package main

// Tools from GitHub releases (yt-dlp, ffmpeg): from github.com or the
// ActNow mirror (https://mirror.actnow.dev/github), as the download
// source in Settings → Network says (actnowkit's mirror package, family
// spec §9.6). Neither source is trusted: each file must match its pinned
// SHA-256, and one that does not is fetched from the next source.

import (
	"log"
	"net/http"
	"time"

	"github.com/hehehai/AudioSmith/internal/family"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/actnowkit/mirror"
)

// githubSources are the sources of GitHub release files under s: the
// download source in the family's form (network.hfEndpoints; while
// AudioSmith follows the family, Shared has written the family's value
// into s.Models.Endpoints), mapped by mirror.Sources: "official" →
// github.com, "mirror" → the ActNow mirror, automatic or custom
// Hugging Face URLs → both, raced.
func githubSources(s settings.Settings) []string {
	return mirror.Sources(family.EndpointsValue(s.Models.Endpoints))
}

// githubClient downloads GitHub release files under s, through the
// downloads' proxy, logging which source served each file.
func githubClient(s settings.Settings) *mirror.Client {
	hc := hub.NewHTTPClient(s.Models.Proxy, time.Minute)
	return &mirror.Client{Sources: githubSources(s), HTTP: func() *http.Client { return hc }, Logf: log.Printf}
}
