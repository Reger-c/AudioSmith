package dub

// The cover of a URL's video (thread #106), fetched by the app itself and
// with care: http(s) only, every hop checked as CheckURL checks the link
// (no address on this machine or the local network, also when a name
// resolves to one at connect time), through the proxy yt-dlp uses, at
// most 5 MB in 20 s, and kept only when it is a JPEG, PNG or WebP picture
// of a sane size. The UI shows the copy in the project, never the
// address.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // FetchThumbnail
	_ "image/png"  // FetchThumbnail
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "golang.org/x/image/webp" // FetchThumbnail
)

// Limits of FetchThumbnail.
const (
	ThumbMaxBytes   = 5 << 20
	thumbMaxSide    = 8192
	thumbTimeout    = 20 * time.Second
	thumbMaxHops    = 4
	thumbFilePrefix = "thumb"
)

// ThumbOptions configure FetchThumbnail.
type ThumbOptions struct {
	// Proxy is yt-dlp's --proxy ("" = direct).
	Proxy string
	// Lookup resolves names for the checks (nil: LookupHost).
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// allowPrivate lets tests fetch from a local server.
	allowPrivate bool
}

// FetchThumbnail downloads the cover at raw into dir as thumb.jpg,
// thumb.png or thumb.webp (by what it is) and returns the file's name.
func FetchThumbnail(ctx context.Context, raw, dir string, o ThumbOptions) (string, error) {
	if o.Lookup == nil {
		o.Lookup = LookupHost
	}
	ctx, cancel := context.WithTimeout(ctx, thumbTimeout)
	defer cancel()
	check := func(ctx context.Context, u *url.URL) error {
		if o.allowPrivate {
			return nil
		}
		_, err := CheckURL(ctx, u.String(), o.Lookup)
		return err
	}
	first, err := url.Parse(raw)
	if err != nil {
		return "", &URLError{Code: CodeURLInvalid}
	}
	if err := check(ctx, first); err != nil {
		return "", err
	}
	tr := &http.Transport{ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		MaxResponseHeaderBytes: 64 << 10}
	var proxyAddr string
	if o.Proxy != "" {
		pu, err := url.Parse(o.Proxy)
		if err != nil || pu.Host == "" {
			return "", fmt.Errorf("thumbnail: bad proxy %q", o.Proxy)
		}
		tr.Proxy = http.ProxyURL(pu)
		proxyAddr = pu.Host
	}
	// Connections go to the proxy, or to public addresses only: a name
	// may resolve to another address at connect time than it did for the
	// check (DNS rebinding).
	d := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		if o.allowPrivate || proxyAddr != "" && sameHostPort(address, proxyAddr) {
			return nil
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil || private(ap.Addr()) {
			return &URLError{Code: CodeURLPrivate}
		}
		return nil
	}}
	tr.DialContext = d.DialContext
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= thumbMaxHops {
			return errors.New("thumbnail: too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return &URLError{Code: CodeURLScheme}
		}
		return check(req.Context(), req.URL)
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, first.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "image/jpeg,image/png,image/webp;q=0.9")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15")
	resp, err := client.Do(req)
	if err != nil {
		var ue *URLError
		if errors.As(err, &ue) {
			return "", ue
		}
		return "", fmt.Errorf("thumbnail: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("thumbnail: %s", resp.Status)
	}
	if resp.ContentLength > ThumbMaxBytes {
		return "", fmt.Errorf("thumbnail: %d bytes (over %d)", resp.ContentLength, ThumbMaxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, ThumbMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("thumbnail: %w", err)
	}
	if len(data) > ThumbMaxBytes {
		return "", fmt.Errorf("thumbnail: over %d bytes", ThumbMaxBytes)
	}
	ext, err := thumbKind(data)
	if err != nil {
		return "", err
	}
	name := thumbFilePrefix + ext
	tmp, err := os.CreateTemp(dir, ".thumb-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // after the rename: nothing
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// One cover a project: a WebP replacing a JPEG leaves no other.
	for _, e := range []string{".jpg", ".png", ".webp"} {
		if e != ext {
			os.Remove(filepath.Join(dir, thumbFilePrefix+e))
		}
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return "", err
	}
	return name, nil
}

// thumbKind checks that data is a JPEG, PNG or WebP picture of a sane
// size and returns its file extension.
func thumbKind(data []byte) (string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("thumbnail: not a picture the app shows: %w", err)
	}
	ext := map[string]string{"jpeg": ".jpg", "png": ".png", "webp": ".webp"}[format]
	switch {
	case ext == "":
		return "", fmt.Errorf("thumbnail: %s pictures are not shown", format)
	case cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > thumbMaxSide || cfg.Height > thumbMaxSide:
		return "", fmt.Errorf("thumbnail: %d×%d", cfg.Width, cfg.Height)
	}
	return ext, nil
}

// sameHostPort reports whether a dialled address is the proxy's (its
// host may be a name: "localhost:7897").
func sameHostPort(address, proxy string) bool {
	if address == proxy {
		return true
	}
	ah, ap, err1 := net.SplitHostPort(address)
	ph, pp, err2 := net.SplitHostPort(proxy)
	if err1 != nil || err2 != nil || ap != pp {
		return false
	}
	if ph == "localhost" {
		a, err := netip.ParseAddr(ah)
		return err == nil && a.Unmap().IsLoopback()
	}
	return ah == ph
}
