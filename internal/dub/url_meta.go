package dub

// What a URL's video is before it downloads (thread #106): the parts of
// yt-dlp's metadata the Dub page shows (SourceMeta) — the site, title,
// channel, length, description, cover, captions and the format the
// download will take — read by Info from yt-dlp's --dump-single-json.

import (
	"encoding/json"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// SourceMeta is what a URL source says about itself, kept in the project
// (Source.Meta) so that it shows before and while the video downloads,
// and without the network later. Fields not known are zero.
type SourceMeta struct {
	// Site is yt-dlp's extractor ("Youtube", "BiliBili"); SiteName is how
	// the site is called ("YouTube", "Bilibili"), or its domain.
	Site     string  `json:"site,omitempty"`
	SiteName string  `json:"siteName,omitempty"`
	ID       string  `json:"id,omitempty"`
	Page     string  `json:"page,omitempty"` // the video's page
	Title    string  `json:"title,omitempty"`
	Channel  string  `json:"channel,omitempty"`  // the channel or uploader
	Uploaded string  `json:"uploaded,omitempty"` // "2005-04-23"
	Duration float64 `json:"duration,omitempty"`
	// Views is the view count (-1: not known).
	Views       int64  `json:"views"`
	Description string `json:"description,omitempty"` // at most MaxDescription runes
	// Thumb is the cover's address (yt-dlp's choice of its covers, of a
	// size and kind the app shows); Thumbnail the copy in the project's
	// folder ("" = none yet).
	Thumb     string `json:"thumb,omitempty"`
	Thumbnail string `json:"thumbnail,omitempty"`
	Language  string `json:"language,omitempty"`
	// Captions are the languages with captions written by people.
	Captions []string `json:"captions,omitempty"`
	// The format the download takes: ids ("137+140"), container, codecs,
	// picture, and its size in bytes (Approx: yt-dlp's estimate).
	Format string  `json:"format,omitempty"`
	Ext    string  `json:"ext,omitempty"`
	VCodec string  `json:"vcodec,omitempty"`
	ACodec string  `json:"acodec,omitempty"`
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	FPS    float64 `json:"fps,omitempty"`
	Size   int64   `json:"size,omitempty"`
	Approx bool    `json:"approx,omitempty"`
	// NeedsFFmpeg: the site has the picture and the sound only apart,
	// which only ffmpeg joins, and none is set up.
	NeedsFFmpeg bool `json:"needsFFmpeg,omitempty"`
	// At is when it was read.
	At time.Time `json:"at"`
	// Error is why it could not be read (a URLError's code and detail);
	// the other fields are then empty.
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// MaxDescription caps the description kept (runes).
const MaxDescription = 4000

// HasVideo reports whether the format chosen has a picture (true when not
// known).
func (m *SourceMeta) HasVideo() bool {
	return m == nil || m.VCodec != "none" || m.Width > 0
}

// rawInfo is the part of yt-dlp's info JSON read.
type rawInfo struct {
	URLInfo
	Subtitles        map[string]json.RawMessage `json:"subtitles"`
	ExtractorName    string                     `json:"extractor"`
	Domain           string                     `json:"webpage_url_domain"`
	Page             string                     `json:"webpage_url"`
	Uploader         string                     `json:"uploader"`
	Channel          string                     `json:"channel"`
	Creator          string                     `json:"creator"`
	UploadDate       string                     `json:"upload_date"`
	Timestamp        float64                    `json:"timestamp"`
	ViewCount        *float64                   `json:"view_count"`
	Description      string                     `json:"description"`
	Thumbnail        string                     `json:"thumbnail"`
	Thumbnails       []rawThumb                 `json:"thumbnails"`
	RequestedFormats []rawFormat                `json:"requested_formats"`
	rawFormat
}

type rawFormat struct {
	FormatID       string  `json:"format_id"`
	Ext            string  `json:"ext"`
	VCodec         string  `json:"vcodec"`
	ACodec         string  `json:"acodec"`
	Width          float64 `json:"width"`
	Height         float64 `json:"height"`
	FPS            float64 `json:"fps"`
	Filesize       float64 `json:"filesize"`
	FilesizeApprox float64 `json:"filesize_approx"`
}

type rawThumb struct {
	URL    string  `json:"url"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// siteNames are the sites' names by extractor, where yt-dlp's key is not
// how people call it.
var siteNames = map[string]string{
	"youtube": "YouTube", "bilibili": "Bilibili", "biliintl": "Bilibili", "vimeo": "Vimeo", "twitter": "X", "tiktok": "TikTok",
	"douyin": "抖音", "xiaohongshu": "小红书", "weibo": "微博", "ixigua": "西瓜视频", "youku": "优酷", "dailymotion": "Dailymotion",
	"twitch": "Twitch", "instagram": "Instagram", "facebook": "Facebook", "reddit": "Reddit", "ted": "TED", "niconico": "niconico",
}

// metaOf reads the shown metadata out of yt-dlp's info.
func metaOf(r rawInfo) SourceMeta {
	m := SourceMeta{Site: r.ExtractorName, ID: r.ID, Page: r.Page, Title: strings.TrimSpace(r.Title), Duration: r.Duration,
		Language: r.Language, Views: -1}
	if r.URLInfo.Extractor != "" {
		m.Site = r.URLInfo.Extractor
	}
	m.SiteName = siteName(m.Site, r.Domain, r.Page)
	m.Channel = strings.TrimSpace(firstNonEmpty(r.Channel, r.Uploader, r.Creator))
	if d := r.UploadDate; len(d) == 8 {
		m.Uploaded = d[:4] + "-" + d[4:6] + "-" + d[6:]
	} else if r.Timestamp > 0 {
		m.Uploaded = time.Unix(int64(r.Timestamp), 0).UTC().Format(time.DateOnly)
	}
	if r.ViewCount != nil && *r.ViewCount >= 0 {
		m.Views = int64(*r.ViewCount)
	}
	if d := strings.TrimSpace(r.Description); d != "-" && d != r.Title { // Bilibili's "-": none
		m.Description = clip(d, MaxDescription)
	}
	m.Thumb = pickThumb(r.Thumbnails, r.Thumbnail)
	for l := range r.Subtitles {
		if l != "live_chat" && l != "danmaku" && l != "comments" {
			m.Captions = append(m.Captions, l)
		}
	}
	slices.Sort(m.Captions)
	// The format: one file, or the streams merged.
	f := r.rawFormat
	m.Format, m.Ext = f.FormatID, f.Ext
	m.VCodec, m.ACodec = f.VCodec, f.ACodec
	m.Width, m.Height, m.FPS = int(f.Width), int(f.Height), f.FPS
	size, approx := f.Filesize, false
	if size <= 0 {
		size, approx = f.FilesizeApprox, f.FilesizeApprox > 0
	}
	if len(r.RequestedFormats) > 0 {
		size, approx = 0, false
		for _, rf := range r.RequestedFormats {
			switch {
			case rf.Filesize > 0:
				size += rf.Filesize
			case rf.FilesizeApprox > 0:
				size, approx = size+rf.FilesizeApprox, true
			}
			if rf.VCodec != "" && rf.VCodec != "none" {
				m.VCodec, m.Width, m.Height = rf.VCodec, max(m.Width, int(rf.Width)), max(m.Height, int(rf.Height))
				m.FPS = max(m.FPS, rf.FPS)
			}
			if rf.ACodec != "" && rf.ACodec != "none" {
				m.ACodec = rf.ACodec
			}
		}
	}
	m.Size, m.Approx = int64(size), approx
	return m
}

// siteName is a site's display name: known, else the page's domain, else
// the extractor's key ("" for the generic extractor without a page).
func siteName(key, domain, page string) string {
	k := strings.ToLower(key)
	if n, ok := siteNames[k]; ok {
		return n
	}
	for pre, n := range siteNames {
		if strings.HasPrefix(k, pre) {
			return n
		}
	}
	if domain == "" && page != "" {
		if u, err := url.Parse(page); err == nil {
			domain = u.Hostname()
		}
	}
	if domain != "" {
		return strings.TrimPrefix(domain, "www.")
	}
	if k == "generic" {
		return ""
	}
	return key
}

// badThumbExts are covers the app does not decode (JPEG, PNG and WebP
// it does; an address of another kind may still serve one, which
// FetchThumbnail checks).
var badThumbExts = map[string]bool{".avif": true, ".heic": true, ".heif": true, ".gif": true, ".svg": true, ".bmp": true, ".tif": true, ".tiff": true}

// pickThumb chooses a cover: not of a kind the app cannot decode, the widest up to 1280
// pixels, else the narrowest above, else yt-dlp's best (the last).
func pickThumb(ts []rawThumb, best string) string {
	ok := func(u string) bool {
		pu, err := url.Parse(u)
		if err != nil || pu.Scheme != "http" && pu.Scheme != "https" {
			return false
		}
		return !badThumbExts[strings.ToLower(path.Ext(pu.Path))]
	}
	pick, pickW, jpg := "", 0.0, false
	for _, t := range ts {
		if !ok(t.URL) || t.Width <= 0 {
			continue
		}
		isJPG := strings.Contains(strings.ToLower(t.URL), ".jpg")
		better := false
		switch {
		case pick == "":
			better = true
		case t.Width <= 1280 && (pickW > 1280 || t.Width > pickW || t.Width == pickW && isJPG && !jpg):
			better = true
		case t.Width > 1280 && pickW > 1280 && t.Width < pickW:
			better = true
		}
		if better {
			pick, pickW, jpg = t.URL, t.Width, isJPG
		}
	}
	if pick != "" {
		return pick
	}
	for i := len(ts) - 1; i >= 0; i-- {
		if ok(ts[i].URL) {
			return ts[i].URL
		}
	}
	if ok(best) {
		return best
	}
	return ""
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// clip cuts s to n runes (with "…").
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}
