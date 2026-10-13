package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/gallery"
	"github.com/hehehai/AudioSmith/internal/i18n"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// galleryFacets are the facets offered as filters (categories are tabs).
var galleryFacets = []gallery.Facet{gallery.FacetGender, gallery.FacetAge, gallery.FacetPitch, gallery.FacetAccent, gallery.FacetLanguage, gallery.FacetStyle}

// facetTitle names a facet.
func facetTitle(f gallery.Facet) string {
	switch f {
	case gallery.FacetGender:
		return i18n.T("Gender")
	case gallery.FacetAge:
		return i18n.T("Age")
	case gallery.FacetPitch:
		return i18n.T("Pitch")
	case gallery.FacetAccent:
		return i18n.T("Accent or dialect")
	case gallery.FacetLanguage:
		return i18n.T("Language")
	case gallery.FacetStyle:
		return i18n.T("Style")
	}
	return string(f)
}

// galleryPage is /gallery.
func (a *app) galleryPage(c *ui.Context) {
	t := c.Theme()
	g := &a.gallery
	page := a.galleryResults()
	header(c, i18n.T("Gallery"), func() {
		ui.Text(c, i18n.N(page.Total, "%d voice", "%d voices", page.Total)).FontSize(px(t, 12)).TextColor(t.TextMuted)
		if ms := designModels(); len(ms) > 1 {
			labels := make([]string, len(ms))
			for i, m := range ms {
				labels[i] = m.Label
			}
			if g.model == "" {
				g.model = labels[0]
			}
			ui.Select(c.Key("gallery-model"), &g.model, labels).Label(i18n.T("Preview with"))
		}
	})
	ui.Column(c).Padding(px(t, 10), px(t, 20), px(t, 4)).Gap(px(t, 8)).Children(func() {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			if ui.TextInput(c.Key("gallery-search"), &g.text).Placeholder(i18n.T("Search: narrator, 东北, whisper…")).
				Label(i18n.T("Search the gallery")).Grow(1).MaxWidth(px(t, 360)).Changed() {
				g.limit = galleryPageSize
			}
			if ui.Toggle(c.Key("gallery-favs"), &g.favOnly, i18n.T("★ Favorites")).Changed() {
				g.limit = galleryPageSize
			}
			if len(g.filters) > 0 || g.text != "" || g.tab != 0 || g.favOnly {
				if ui.Button(c.Key("gallery-reset"), i18n.T("Reset")).Clicked() {
					g.text, g.tab, g.favOnly, g.limit = "", 0, false, galleryPageSize
					clear(g.filters)
				}
			}
		})
		a.galleryTabs(c, page)
		ui.Row(c.Key("gallery-facets")).Gap(px(t, 6)).Wrap().Children(func() {
			for _, f := range galleryFacets {
				a.facetMenu(c, f, page.Facets[f])
			}
		})
	})
	if len(page.Items) == 0 {
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, i18n.T("No voices match these filters.")).TextColor(t.TextMuted)
		})
		return
	}
	items := page.Items
	ui.GridView(c.Key("gallery-grid"), &g.grid, len(items)+boolInt(page.Next >= 0), px(t, 260), px(t, 150), func(i int) {
		if i == len(items) {
			ui.Column(c.Key("more")).Grow(1).Center().Children(func() {
				if ui.Button(c.Key("gallery-more"), i18n.T("Load more")).Clicked() {
					g.limit += galleryPageSize
				}
			})
			return
		}
		a.archetypeCard(c, items[i])
	}).Grow(1).Padding(px(t, 8), px(t, 16)).Gap(px(t, 10))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// galleryTabs are the categories, with counts.
func (a *app) galleryTabs(c *ui.Context, page gallery.Page) {
	t := c.Theme()
	g := &a.gallery
	loc := i18n.Locale()
	counts := map[string]int{}
	total := 0
	for _, fc := range page.Facets[gallery.FacetCategory] {
		counts[fc.Value] = fc.Count
		total += fc.Count
	}
	ui.Row(c.Key("gallery-tabs")).Gap(px(t, 4)).Wrap().Children(func() {
		tab := func(i int, label string, n int) {
			on := g.tab == i
			if ui.Toggle(c.Key("tab-"+itoa(i)), &on, label+" "+itoa(n)).Changed() {
				g.tab, g.limit = i, galleryPageSize
			}
		}
		tab(0, i18n.T("All"), total)
		for i, cat := range gallery.Categories() {
			tab(i+1, cat.Name.In(loc), counts[cat.ID])
		}
	})
}

// facetMenu is one facet as a menu button of its values with counts.
func (a *app) facetMenu(c *ui.Context, f gallery.Facet, values []gallery.FacetCount) {
	g := &a.gallery
	loc := i18n.Locale()
	cur := g.filters[f]
	label := facetTitle(f)
	if cur != "" {
		for _, v := range gallery.FacetValues(f) {
			if v.Value == cur {
				label += ": " + v.Label.In(loc)
			}
		}
	}
	ui.MenuButton(c.Key("facet-"+string(f)), label, func(m *ui.Menu) {
		if m.Item(i18n.T("Any")).Checked(cur == "").Chosen() {
			delete(g.filters, f)
			g.limit = galleryPageSize
		}
		for _, v := range values {
			if m.Item(v.Label.In(loc) + "  " + itoa(v.Count)).Checked(cur == v.Value).Chosen() {
				g.filters[f] = v.Value
				g.limit = galleryPageSize
			}
		}
	}).Label(facetTitle(f))
}

// archetypeCard is one gallery voice.
func (a *app) archetypeCard(c *ui.Context, arch gallery.Archetype) {
	t := c.Theme()
	g := &a.gallery
	loc := i18n.Locale()
	ui.Column(c.Key("arch-"+arch.ID)).Grow(1).Gap(px(t, 6)).Padding(px(t, 12)).Radius(10).Border(1, t.Border).Background(t.Surface).Children(func() {
		name := arch.Name.In(loc)
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Avatar(c, name, nil)
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, name).Bold().SingleLine().Ellipsis("…")
				ui.Text(c, arch.Attrs.Summary(loc)).FontSize(px(t, 11)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			})
			fav := g.favs.Has(arch.ID)
			star := "☆"
			tip := i18n.T("Add to favorites")
			if fav {
				star, tip = "★", i18n.T("Remove from favorites")
			}
			if ui.Button(c.Key("fav"), star).Label(tip).Tooltip(tip).Clicked() {
				a.toggleFavorite(arch.ID)
			}
		})
		ui.Text(c, arch.Description.In(loc)).FontSize(px(t, 12)).MaxLines(2).Ellipsis("…").Grow(1)
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			p := g.previews[arch.ID]
			switch {
			case p != nil && p.rendering:
				ui.Spinner(c).Label(i18n.T("Rendering the preview…"))
			default:
				label := i18n.T("Preview")
				if a.pl.Playing(galleryOwner + arch.ID) {
					label = i18n.T("Stop")
				}
				b := ui.Button(c.Key("preview"), label)
				if p != nil && p.err != "" {
					b.Tooltip(p.err)
				}
				if b.Clicked() {
					a.previewArchetype(arch)
				}
			}
			if p != nil && p.err != "" {
				ui.Text(c, i18n.T("Preview failed")).FontSize(px(t, 11)).TextColor(t.Danger).Tooltip(p.err)
			}
			ui.Box(c).Grow(1)
			if ui.PrimaryButton(c.Key("use"), i18n.T("Use voice")).Disabled(g.using).
				Tooltip(i18n.T("Save it to your voices and open it in Design")).Clicked() {
				a.useArchetype(arch, false)
			}
			ui.Button(c.Key("more"), "⋯").Label(i18n.T("More actions")).Menu(func(m *ui.Menu) {
				if m.Item(i18n.T("Use in Studio")).Chosen() {
					a.useArchetype(arch, true)
				}
				if m.Item(i18n.T("Open in Design without saving")).Chosen() {
					a.designFromArchetype(arch)
				}
			})
		})
	})
}

// designFromArchetype opens Design on an archetype's recipe, unsaved.
func (a *app) designFromArchetype(arch gallery.Archetype) {
	d := &a.design
	script := d.script
	d.reset()
	d.attrs, d.seed = arch.Attrs.Canonical(), float64(arch.Seed)
	d.script, d.language = arch.PreviewLine()
	if d.script == "" {
		d.script = script
	}
	d.saveName = arch.Name.In(i18n.Locale())
	d.parsed, d.parsedFor = vd.Result{}, ""
	a.navigate("/design")
}
