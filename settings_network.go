package main

// Settings → Network (M7a): where models download from and the proxy
// every download uses (moved here from Models and downloads), and a
// connection test through that proxy.

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// netCheck is the result of reaching one host.
type netCheck struct {
	host    string
	ok      bool
	latency time.Duration
	err     string
}

// networkState is the Network page's state.
type networkState struct {
	netChecks  []netCheck
	netTesting bool
}

// netHosts are the hosts AudioSmith downloads from.
var netHosts = []string{"https://huggingface.co", "https://hf-mirror.com", "https://github.com"}

// testNetwork reaches each host through the downloads' proxy.
func (a *app) testNetwork(then func([]netCheck)) {
	a.netTesting = true
	client := hub.NewHTTPClient(a.prefs.Models.Proxy, 10*time.Second)
	hosts := slices.Clone(netHosts)
	if eps := a.prefs.Models.Endpoints; len(eps) > 0 {
		for _, e := range eps {
			if !slices.Contains(hosts, e) {
				hosts = append(hosts, e)
			}
		}
	}
	a.background(func() {
		out := make([]netCheck, len(hosts))
		var wg sync.WaitGroup
		for i, h := range hosts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out[i] = reach(client, h)
			}()
		}
		wg.Wait()
		a.post(func() {
			a.netChecks, a.netTesting = out, false
			if then != nil {
				then(out)
			}
		})
	})
}

// reach asks host for its headers (a HEAD request, 10 s at most).
var reach = func(client *http.Client, host string) netCheck {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, host, nil)
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		if i := strings.LastIndex(msg, ": "); i > 0 {
			msg = msg[i+2:]
		}
		return netCheck{host: host, err: msg}
	}
	resp.Body.Close()
	return netCheck{host: host, ok: resp.StatusCode < 500, latency: time.Since(start), err: resp.Status}
}

// networkSettings is Settings → Network.
func (a *app) networkSettings(c *ui.Context) {
	t := c.Theme()
	sectionTitle(c, i18n.T("Downloads"))
	ui.Text(c, i18n.T("Models, ffmpeg and yt-dlp download through these settings. The engine itself never connects to the network.")).
		FontSize(px(t, 12)).TextColor(t.TextMuted)
	settingRow(c, "source", i18n.T("Download from"), i18n.T("Mirrors never receive your token; gated models always come from huggingface.co."), func() {
		opts := sourceOptions()
		if ui.Select(c.Key("source-select"), &a.source, opts).Label(i18n.T("Download from")).Width(px(t, 240)).Changed() {
			var eps []string
			switch slices.Index(opts, a.source) {
			case 1:
				eps = []string{"https://huggingface.co"}
			case 2:
				eps = []string{"https://hf-mirror.com"}
			case 3:
				eps = strings.Fields(a.endpoint)
				if len(eps) == 0 {
					return // until a URL is typed
				}
			}
			a.updateSettings(func(s *settings.Settings) { s.Models.Endpoints = eps })
		}
	})
	if slices.Index(sourceOptions(), a.source) == 3 {
		settingRow(c, "endpoint", i18n.T("Endpoint"), i18n.T("One or more URLs, separated by spaces; the fastest answers."), func() {
			in := ui.TextInput(c.Key("endpoint-input"), &a.endpoint).Placeholder("https://hf-mirror.com").Width(px(t, 280)).Label(i18n.T("Endpoint"))
			if in.Submitted() {
				eps := strings.Fields(a.endpoint)
				a.updateSettings(func(s *settings.Settings) { s.Models.Endpoints = eps })
			}
		})
	}
	settingRowNote(c, "proxy", i18n.T("Proxy"), a.proxyInfo, func() {
		opts := proxyOptions()
		if ui.Select(c.Key("proxy-select"), &a.proxyMode, opts).Label(i18n.T("Proxy")).Width(px(t, 240)).Changed() {
			mode := ""
			switch slices.Index(opts, a.proxyMode) {
			case 1:
				mode = hub.ProxyDirect
			case 2:
				mode = strings.TrimSpace(a.proxy)
				if mode == "" {
					return
				}
			}
			a.updateSettings(func(s *settings.Settings) { s.Models.Proxy = mode })
		}
	})
	if slices.Index(proxyOptions(), a.proxyMode) == 2 {
		settingRow(c, "proxy-url", i18n.T("Proxy URL"), i18n.T("http://, https:// or socks5://host:port"), func() {
			in := ui.TextInput(c.Key("proxy-input"), &a.proxy).Placeholder("http://127.0.0.1:7890").Width(px(t, 280)).Label(i18n.T("Proxy URL"))
			if in.Submitted() {
				mode := strings.TrimSpace(a.proxy)
				a.updateSettings(func(s *settings.Settings) { s.Models.Proxy = mode })
			}
		})
	}

	sectionTitle(c, i18n.T("Connection"))
	settingRow(c, "net-test", i18n.T("Test the connection"), i18n.T("Reaches Hugging Face, its mirror and GitHub through the proxy above."), func() {
		if a.netTesting {
			ui.Spinner(c)
		}
		if ui.Button(c.Key("net-test"), i18n.T("Test")).Disabled(a.netTesting).Clicked() {
			a.testNetwork(nil)
		}
	})
	for _, r := range a.netChecks {
		ui.Row(c.Key("net-" + r.host)).Gap(px(t, 8)).Children(func() {
			mark, col := "✓", t.Success
			detail := i18n.T("%d ms", r.latency.Milliseconds())
			if !r.ok {
				mark, col, detail = "✗", t.Danger, r.err
			}
			ui.Text(c, mark).TextColor(col)
			ui.Text(c, strings.TrimPrefix(r.host, "https://")).Width(px(t, 160))
			ui.Text(c, detail).TextColor(t.TextMuted).FontSize(px(t, 12)).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
		})
	}
}
