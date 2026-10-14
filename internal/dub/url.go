package dub

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Import from a URL (YouTube and the other sites yt-dlp knows), opt-in:
// the app downloads yt-dlp's standalone macOS build (pinned, checked by
// SHA-256) the first time the user turns URL import on. The rules VS
// applies are kept: http(s) only; no addresses on this machine or the
// local network (a page could otherwise make the app fetch from the
// LAN); no live streams, premieres, playlists or stream manifests.

// URL import error codes (*URLError).
const (
	CodeURLScheme   = "url.scheme"   // not http or https
	CodeURLPrivate  = "url.private"  // this machine or the local network
	CodeURLPlaylist = "url.playlist" // a playlist or channel
	CodeURLLive     = "url.live"     // a live stream or premiere
	CodeURLManifest = "url.manifest" // an HLS/DASH manifest
	CodeURLInvalid  = "url.invalid"
	CodeNoYtDlp     = "url.no_ytdlp" // URL import is off (yt-dlp not installed)
	CodeYtDlp       = "url.ytdlp"    // yt-dlp failed (Detail: its last line)
	CodeChecksum    = "url.checksum" // the download of yt-dlp did not match its checksum
	CodeCookies     = "url.cookies"  // the cookies file is over 1 MB
)

// URLError is a refused or failed URL import.
type URLError struct {
	Code   string
	Detail string
}

func (e *URLError) Error() string {
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

// CheckURL validates u before anything is fetched. lookup resolves host
// names (nil skips the resolution: only literal addresses are checked).
func CheckURL(ctx context.Context, raw string, lookup func(ctx context.Context, host string) ([]netip.Addr, error)) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, &URLError{Code: CodeURLInvalid}
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return nil, &URLError{Code: CodeURLScheme}
	}
	if u.Host == "" || u.Scheme == "" {
		return nil, &URLError{Code: CodeURLInvalid}
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".home.arpa") || !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return nil, &URLError{Code: CodeURLPrivate}
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if private(a) {
			return nil, &URLError{Code: CodeURLPrivate}
		}
	} else if lookup != nil {
		addrs, err := lookup(ctx, host)
		if err != nil {
			return nil, &URLError{Code: CodeURLInvalid, Detail: err.Error()}
		}
		for _, a := range addrs {
			if private(a) {
				return nil, &URLError{Code: CodeURLPrivate}
			}
		}
	}
	path := strings.ToLower(u.Path)
	switch {
	case strings.HasSuffix(path, ".m3u8") || strings.HasSuffix(path, ".mpd") || strings.HasSuffix(path, ".m3u"):
		return nil, &URLError{Code: CodeURLManifest}
	case strings.Contains(path, "/playlist") || (u.Query().Get("list") != "" && u.Query().Get("v") == "" && !strings.Contains(host, "youtu.be")):
		return nil, &URLError{Code: CodeURLPlaylist}
	case strings.HasPrefix(path, "/live/") || strings.HasSuffix(path, "/live") || strings.Contains(path, "/streams"):
		return nil, &URLError{Code: CodeURLLive}
	}
	return u, nil
}

// private reports addresses on this machine or a local network.
func private(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(a) // carrier-grade NAT
}

// LookupHost is CheckURL's resolver over the system's.
func LookupHost(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	return ips, err
}

// --- yt-dlp -----------------------------------------------------------------------

// The yt-dlp build URL import uses: the standalone macOS binary (no
// Python), pinned. Update both together.
const (
	YtDlpVersion = "2026.08.19"
	YtDlpSHA256  = "0f192b7ec147ab6288885d6351d9ab67367640029b4377576ef46dd79cf7b202"
	YtDlpURL     = "https://github.com/yt-dlp/yt-dlp/releases/download/" + YtDlpVersion + "/yt-dlp_macos"
	YtDlpSize    = 37146048
)

// YtDlp runs yt-dlp.
type YtDlp struct {
	// Path is the executable ("" = not installed).
	Path string
	// Proxy is passed as --proxy ("" = none).
	Proxy string
}

// InstallYtDlp downloads yt-dlp into dir (as "yt-dlp"), checks it and
// makes it executable. progress gets bytes done of the total. It returns
// the executable's path.
func InstallYtDlp(ctx context.Context, client *http.Client, dir string, progress func(done, total int64)) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, YtDlpURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("yt-dlp: download: %s", resp.Status)
	}
	dst := filepath.Join(dir, "yt-dlp")
	tmp, err := os.CreateTemp(dir, ".yt-dlp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	total := resp.ContentLength
	if total <= 0 {
		total = YtDlpSize
	}
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				tmp.Close()
				return "", err
			}
			h.Write(buf[:n])
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			return "", rerr
		}
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != YtDlpSHA256 {
		return "", &URLError{Code: CodeChecksum, Detail: got}
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// VerifyYtDlp checks an installed yt-dlp against the pinned checksum.
func VerifyYtDlp(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != YtDlpSHA256 {
		return &URLError{Code: CodeChecksum, Detail: got}
	}
	return nil
}

// VideoInfo is what yt-dlp says about a URL.
type URLInfo struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Duration   float64  `json:"duration"`
	Type       string   `json:"_type"`
	IsLive     bool     `json:"is_live"`
	LiveStatus string   `json:"live_status"`
	Extractor  string   `json:"extractor_key"`
	Language   string   `json:"language"`
	Captions   []string `json:"-"` // languages with captions written by people
}

// Info asks yt-dlp about a URL (no download) and refuses playlists and
// live streams.
func (y YtDlp) Info(ctx context.Context, u string) (URLInfo, error) {
	var info URLInfo
	if y.Path == "" {
		return info, &URLError{Code: CodeNoYtDlp}
	}
	args := append(y.base(), "--dump-single-json", "--no-playlist", "--", u)
	out, err := y.run(ctx, args, nil)
	if err != nil {
		return info, err
	}
	var raw struct {
		URLInfo
		Subtitles map[string]json.RawMessage `json:"subtitles"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return info, &URLError{Code: CodeYtDlp, Detail: err.Error()}
	}
	info = raw.URLInfo
	for l := range raw.Subtitles {
		if l != "live_chat" {
			info.Captions = append(info.Captions, l)
		}
	}
	switch {
	case info.Type == "playlist" || info.Type == "multi_video":
		return info, &URLError{Code: CodeURLPlaylist}
	case info.IsLive || info.LiveStatus == "is_live" || info.LiveStatus == "is_upcoming" || info.LiveStatus == "post_live":
		return info, &URLError{Code: CodeURLLive}
	}
	return info, nil
}

// DownloadOptions choose what Download fetches.
type DownloadOptions struct {
	// Captions downloads the caption track closest to this language
	// (written by people, not translated automatically); "" = none.
	Captions string
	// Cookies is a cookies.txt export for sites that need a sign-in
	// (≤ 1 MB; used for this download only).
	Cookies string
	// Merge allows separate video and audio streams merged by ffmpeg
	// (FFmpeg names it); without, the best single file (often ≤ 720p).
	FFmpeg string
}

// Download fetches the video into dir and returns the file (and the
// caption file, if any). progress gets the fraction done (0–1).
func (y YtDlp) Download(ctx context.Context, u, dir string, o DownloadOptions, progress func(float64)) (video, captions string, err error) {
	if y.Path == "" {
		return "", "", &URLError{Code: CodeNoYtDlp}
	}
	if o.Cookies != "" {
		if fi, err := os.Stat(o.Cookies); err != nil {
			return "", "", err
		} else if fi.Size() > 1<<20 {
			return "", "", &URLError{Code: CodeCookies}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	format := "b[ext=mp4][vcodec^=avc1]/b[ext=mp4]/b"
	args := y.base()
	if o.FFmpeg != "" {
		format = "bv*[ext=mp4][vcodec^=avc1]+ba[ext=m4a]/bv*[ext=mp4]+ba[ext=m4a]/b[ext=mp4]/bv*+ba/b"
		args = append(args, "--ffmpeg-location", o.FFmpeg, "--merge-output-format", "mp4")
	}
	args = append(args, "--no-playlist", "--newline", "--no-part", "--restrict-filenames",
		"-f", format, "-o", filepath.Join(dir, "source.%(ext)s"),
		"--progress-template", "download:AS %(progress.downloaded_bytes)s %(progress.total_bytes)s %(progress.total_bytes_estimate)s",
		"--print", "after_move:filepath")
	if o.Captions != "" {
		args = append(args, "--write-subs", "--sub-langs", o.Captions+".*,"+o.Captions, "--sub-format", "vtt/srt/best", "--convert-subs", "srt")
		if o.FFmpeg == "" {
			args = args[:len(args)-2] // converting needs ffmpeg
		}
	}
	if o.Cookies != "" {
		args = append(args, "--cookies", o.Cookies)
	}
	args = append(args, "--", u)
	var last string
	out, err := y.run(ctx, args, func(line string) {
		if f, ok := strings.CutPrefix(line, "AS "); ok && progress != nil {
			fields := strings.Fields(f)
			if len(fields) == 3 {
				done, _ := strconv.ParseFloat(fields[0], 64)
				total, _ := strconv.ParseFloat(fields[1], 64)
				if total <= 0 {
					total, _ = strconv.ParseFloat(fields[2], 64)
				}
				if total > 0 {
					progress(min(done/total, 1))
				}
			}
			return
		}
		last = line
	})
	if err != nil {
		return "", "", err
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "AS ") {
			video = l
		}
	}
	if video == "" {
		video = last
	}
	if _, err := os.Stat(video); err != nil {
		return "", "", &URLError{Code: CodeYtDlp, Detail: "no file was written"}
	}
	if o.Captions != "" {
		matches, _ := filepath.Glob(filepath.Join(dir, "source*.srt"))
		vtts, _ := filepath.Glob(filepath.Join(dir, "source*.vtt"))
		matches = append(matches, vtts...)
		if len(matches) > 0 {
			captions = matches[0]
		}
	}
	return video, captions, nil
}

func (y YtDlp) base() []string {
	args := []string{"--ignore-config", "--no-warnings", "--no-color"}
	if y.Proxy != "" {
		args = append(args, "--proxy", y.Proxy)
	}
	return args
}

var errorLine = regexp.MustCompile(`^ERROR:\s*(?:\[[^\]]*\]\s*)?`)

// run runs yt-dlp, feeding stderr's and stdout's lines to line (when set)
// and returning stdout.
func (y YtDlp) run(ctx context.Context, args []string, line func(string)) ([]byte, error) {
	cmd := exec.CommandContext(ctx, y.Path, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{b: &stderr, n: 64 << 10}
	if err := cmd.Start(); err != nil {
		return nil, &URLError{Code: CodeYtDlp, Detail: err.Error()}
	}
	var out strings.Builder
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		l := sc.Text()
		out.WriteString(l + "\n")
		if line != nil {
			line(l)
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\nERROR:"); i >= 0 {
			msg = msg[i+1:]
		}
		msg = errorLine.ReplaceAllString(msg, "")
		lower := strings.ToLower(msg)
		switch {
		case strings.Contains(lower, "live event") || strings.Contains(lower, "premiere") || strings.Contains(lower, "is live"):
			return nil, &URLError{Code: CodeURLLive, Detail: msg}
		}
		return nil, &URLError{Code: CodeYtDlp, Detail: msg}
	}
	return []byte(out.String()), nil
}

type limitWriter struct {
	b *strings.Builder
	n int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.b.Len() < w.n {
		w.b.Write(p[:min(len(p), w.n-w.b.Len())])
	}
	return len(p), nil
}

// ErrURLRefused reports any refused URL (errors.Is).
var ErrURLRefused = errors.New("dub: URL refused")

// Is lets errors.Is(err, ErrURLRefused) match the refusals.
func (e *URLError) Is(target error) bool {
	if target != ErrURLRefused {
		return false
	}
	switch e.Code {
	case CodeURLScheme, CodeURLPrivate, CodeURLPlaylist, CodeURLLive, CodeURLManifest, CodeURLInvalid:
		return true
	}
	return false
}
