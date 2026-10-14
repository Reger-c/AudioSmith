package main

// The M5 pages in the shell: a sidebar section (Tools, Integrations), the
// routes (and the API reference viewer's page), the titles and the
// palette's entries (one call each from shell*.go).

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

var (
	toolsIcon        = icon(`<path d="M10.5 2.5a3 3 0 0 0-2.9 3.8L2.8 11.1a1.4 1.4 0 0 0 2 2l4.8-4.8a3 3 0 0 0 3.8-2.9l-1.7 1.1-1.6-.4-.4-1.6z"/>`)
	integrationsIcon = icon(`<rect x="2" y="2" width="5" height="5" rx="1"/><rect x="9" y="9" width="5" height="5" rx="1"/><path d="M7 4.5h2.5a2 2 0 0 1 2 2V9M9 11.5H6.5a2 2 0 0 1-2-2V7"/>`)
)

// m5Pages are the sidebar's M5 pages.
var m5Pages = []struct {
	path, title string
	icon        *ui.SVG
}{
	{"/tools", "Tools", toolsIcon},
	{"/integrations", "Integrations", integrationsIcon},
}

// m5PageTitle translates the M5 pages' titles.
func m5PageTitle(path string) (string, bool) {
	switch path {
	case "/tools":
		return i18n.T("Tools"), true
	case "/integrations":
		return i18n.T("Integrations"), true
	case "/api-reference":
		return i18n.T("API reference"), true
	}
	return "", false
}

// m5Sidebar is the sidebar's section of the M5 pages.
func (a *app) m5Sidebar(c *ui.Context) {
	ui.SidebarSection(c, i18n.T("Utilities"), nil, func() {
		for _, p := range m5Pages {
			title, _ := m5PageTitle(p.path)
			ui.SidebarItem(c, p.path, p.icon, title)
		}
	})
}

// m5Route builds the M5 pages.
func (a *app) m5Route(c *ui.Context, r *ui.Route) bool {
	switch {
	case r.Match("/tools"):
		r.Title(i18n.T("Tools"))
		a.toolsPage(c)
	case r.Match("/integrations"):
		r.Title(i18n.T("Integrations"))
		a.integrationsPage(c)
	case r.Match("/integrations/{slug}"):
		r.Title(i18n.T("Integrations"))
		a.integrationPage(c, r.Param("slug"))
	case r.Match("/api-reference"):
		r.Title(i18n.T("API reference"))
		a.apiReferencePage(c)
	default:
		return false
	}
	return true
}

// m5Commands adds the palette's entries.
func (a *app) m5Commands(nav string, add func(id, group, title, en, keys string, enabled bool, run func())) {
	for _, p := range m5Pages {
		title, _ := m5PageTitle(p.path)
		add("go"+p.path, nav, title, p.title, "", true, func() { a.navigate(p.path) })
	}
	add("go/api-reference", nav, i18n.T("API reference"), "API reference OpenAPI docs", "", a.apiPort != 0, func() { a.navigate("/api-reference") })
}

// apiReferencePage shows the bundled reference viewer (RapiDoc, served
// by the API itself) in a web view.
func (a *app) apiReferencePage(c *ui.Context) {
	t := c.Theme()
	header(c, i18n.T("API reference"), func() {
		if ui.Button(c.Key("ref-browser"), i18n.T("Open in the browser")).Disabled(a.apiPort == 0).Clicked() {
			go mygo.Shell.OpenExternal(a.apiBaseURL() + "/docs")
		}
		if ui.Button(c.Key("ref-settings"), i18n.T("Settings…")).Clicked() {
			a.navigate("/settings/api")
		}
	})
	if a.apiPort == 0 {
		ui.Column(c).Grow(1).Center().Gap(px(t, 8)).Children(func() {
			ui.Text(c, i18n.T("The Local API is not running.")).TextColor(t.TextMuted)
		})
		return
	}
	if a.apiDocs == nil && a.win != nil && a.apiDocsErr == "" {
		theme := ""
		if t.Dark {
			theme = "?theme=dark"
		}
		v, err := a.win.NewWebView(mygo.WebViewOptions{URL: a.apiBaseURL() + "/docs" + theme})
		if err != nil {
			a.apiDocsErr = err.Error()
		}
		a.apiDocs = v
	}
	if a.apiDocs == nil {
		ui.Column(c).Grow(1).Center().Gap(px(t, 8)).Padding(px(t, 20)).Children(func() {
			msg := i18n.T("The reference opens in a web view of the app window.")
			if a.apiDocsErr != "" {
				msg = i18n.T("The reference could not open here: %s", a.apiDocsErr)
			}
			ui.Text(c, msg).TextColor(t.TextMuted).Wrap()
			ui.Text(c, a.apiBaseURL()+"/docs").Font(monoFont).Selectable()
		})
		return
	}
	ui.WebView(c, a.apiDocs).Grow(1)
}
