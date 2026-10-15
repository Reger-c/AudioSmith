package main

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/gallery"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
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
	narrow := a.pageWidth(c) < px(t, 720)
	pageHeader(c, i18n.T("Discover voices"), func() {
		caption(c, i18n.N(page.Total, "%d voice", "%d voices", page.Total)).SingleLine()
		if ms := designModels(); len(ms) > 1 {
			// The model previews use; models not downloaded say so, and
			// are not chosen unless asked for (previewModel).
			cur, _ := a.previewModel()
			dropButton(c.Key("gallery-model"), icons.Soundwave, cur.DisplayName()).Tooltip(i18n.T("Preview with")).MaxWidth(px(t, 220)).Menu(func(m *ui.Menu) {
				for _, e := range ms {
					label := e.DisplayName()
					if ok, known := a.modelOnDisk(e); known && !ok {
						label = i18n.T("%s (not downloaded)", e.DisplayName())
					}
					if m.Item(label).Checked(e.Label == cur.Label).Chosen() {
						g.model = e.Label
					}
				}
			})
		}
	})
	ui.Column(c).Padding(px(t, sp1), px(t, sp6), px(t, sp3)).Gap(px(t, sp2)).Children(func() {
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Wrap().Children(func() {
			if searchField(c, "gallery-search", &g.text, i18n.T("Search: narrator, 东北, whisper…"), px(t, 360)).
				Label(i18n.T("Search Discover voices")).Changed() {
				g.limit = galleryPageSize
			}
			a.galleryCategorySelect(c, page, narrow)
			if iconToggle(c.Key("gallery-favs"), icons.Star, g.favOnly, i18n.T("Favorites")).Tooltip(i18n.T("Show only your favorites")).Clicked() {
				g.favOnly = !g.favOnly
				g.limit = galleryPageSize
			}
		})
		ui.Row(c.Key("gallery-facets")).Gap(px(t, sp1)).Wrap().AlignItems(ui.Center).Children(func() {
			for _, f := range galleryFacets {
				a.facetMenu(c, f, page.Facets[f])
			}
			if len(g.filters) > 0 || g.text != "" || g.tab != 0 || g.favOnly {
				if quietButton(c.Key("gallery-reset"), icons.Restart, i18n.T("Reset")).Clicked() {
					g.text, g.tab, g.favOnly, g.limit = "", 0, false, galleryPageSize
					clear(g.filters)
				}
			}
		})
	})
	if len(page.Items) == 0 {
		emptyState(c, icons.Widget, i18n.T("No voices match these filters."), "", nil)
		return
	}
	items := page.Items
	g.moreAsked = false
	ui.GridView(c.Key("gallery-grid"), &g.grid, len(items)+boolInt(page.Next >= 0), px(t, 260), g.cardHeight(t), func(i int) {
		if i == len(items) {
			// The grid builds only the rows in view: this cell is built
			// when the end scrolls into view, so the next page loads then
			// (an in-memory search: at once), with no button to press.
			if !g.moreAsked {
				g.moreAsked = true
				g.limit += galleryPageSize
				c.After(time.Millisecond)
			}
			ui.Column(c.Key("more")).Grow(1).Center().Children(func() {
				ui.Spinner(c).Label(i18n.T("Loading…"))
			})
			return
		}
		a.archetypeCard(c, items[i])
	}).Grow(1).Padding(0, px(t, sp6), px(t, sp4))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// galleryCounts are the voices of each category, and of all of them.
func galleryCounts(page gallery.Page) (counts map[string]int, total int) {
	counts = map[string]int{}
	for _, fc := range page.Facets[gallery.FacetCategory] {
		counts[fc.Value] = fc.Count
		total += fc.Count
	}
	return counts, total
}

// galleryCategorySelect is the categories, with their counts, as a
// select beside the search field.
func (a *app) galleryCategorySelect(c *ui.Context, page gallery.Page, narrow bool) {
	t := c.Theme()
	g := &a.gallery
	loc := i18n.Locale()
	counts, total := galleryCounts(page)
	cats := gallery.Categories()
	options := make([]string, 0, len(cats)+1)
	options = append(options, i18n.T("All categories")+"  "+itoa(total))
	for _, cat := range cats {
		options = append(options, cat.Name.In(loc)+"  "+itoa(counts[cat.ID]))
	}
	if g.tab < 0 || g.tab >= len(options) {
		g.tab = 0
	}
	cur := options[g.tab]
	w := px(t, 220)
	if narrow {
		w = px(t, 170)
	}
	if ui.Select(c.Key("gallery-category"), &cur, options).Label(i18n.T("Category")).Tooltip(i18n.T("Category")).Width(w).Shrink(1).MinWidth(px(t, 120)).Changed() {
		for i, o := range options {
			if o == cur {
				g.tab, g.limit = i, galleryPageSize
			}
		}
	}
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
	dropText(c.Key("facet-"+string(f)), label, cur != "").Label(facetTitle(f)).Tooltip(label).Menu(func(m *ui.Menu) {
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
	})
}

// archetypeCard is one gallery voice: a flat card with its avatar, name,
// traits, star, description, Preview, Use voice and ⋯.
func (a *app) archetypeCard(c *ui.Context, arch gallery.Archetype) {
	t := c.Theme()
	k := tok(t)
	g := &a.gallery
	loc := i18n.Locale()
	cd := card(c.Key("arch-" + arch.ID)).Grow(1).Gap(px(t, sp2)).Padding(px(t, sp3))
	cd.Children(func() {
		name := arch.Name.In(loc)
		desc := arch.Description.In(loc)
		head := ui.Row(c.Key("head")).Gap(px(t, sp2)).AlignItems(ui.Start)
		head.Children(func() {
			ui.Avatar(c, name, nil)
			ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
				ui.Text(c, name).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0).Tooltip(name)
				ui.Text(c, desc).FontSize(px(t, fsCaption)).TextColor(k.Text2).SingleLine().Ellipsis("…").MinWidth(0).Tooltip(desc)
			})
			fav := g.favs.Has(arch.ID)
			tip := i18n.T("Add to favorites")
			if fav {
				tip = i18n.T("Remove from favorites")
			}
			if iconToggle(c.Key("fav"), icons.Star, fav, tip).Clicked() {
				a.toggleFavorite(arch.ID)
			}
		})
		// The voice's details as tags, all of them: its language first,
		// then gender, age, pitch and the rest; they wrap.
		// At most two rows (the card has a fixed height): the rest are
		// cut, and the tooltip lists them all.
		var tags []string
		if len(arch.Languages) > 0 {
			tags = append(tags, dubLangName(arch.Languages[0]))
		}
		tags = append(tags, archetypeAttrLabels(arch.Attrs, loc)...)
		// Two rows' height comes from a tag as last laid out (its font and
		// size vary: Chinese runs taller than English).
		meta := ui.Row(c.Key("meta")).Gap(px(t, 4)).Wrap().AlignContent(ui.Start).Shrink(0).Tooltip(strings.Join(tags, " · "))
		if g.tagH > 0 {
			meta.Height(2*g.tagH + px(t, 4)).ClipY() // footers line up across cards
		}
		meta.Children(func() {
			for i, l := range tags {
				chip := tagChip(c, l)
				if i == 0 {
					g.measure(c, &g.tagH, chip.Bounds().H)
				}
			}
		})
		foot := ui.Row(c.Key("foot")).Gap(px(t, sp1)).AlignItems(ui.Center)
		// The card's content runs from its top to the footer's bottom, as
		// laid out (fonts differ between Mac and Linux).
		defer func() {
			top, fb := cd.Bounds(), foot.Bounds()
			if top.H > 0 && fb.H > 0 {
				g.measure(c, &g.contentH, fb.Y+fb.H-top.Y)
			}
		}()
		foot.Children(func() {
			p := g.previews[arch.ID]
			switch {
			case p != nil && p.rendering:
				ui.Spinner(c).Label(i18n.T("Rendering the preview…"))
			default:
				label, ic := i18n.T("Preview"), icons.Play
				if a.pl.Playing(galleryOwner + arch.ID) {
					label, ic = i18n.T("Stop"), icons.Stop
				}
				b := ghostIconButtonSized(c.Key("preview"), ic, label, t.FontSize*0.9).Label(label)
				if p != nil && p.err != "" {
					b.Tooltip(p.err)
				}
				if b.Clicked() {
					a.previewArchetype(arch)
				}
			}
			if p != nil && p.err != "" {
				iconMark(c, icons.DangerCircle, t.Danger, i18n.T("Preview failed")+": "+p.err).Label(i18n.T("Preview failed"))
			}
			ui.Spacer(c)
			if ui.Button(c.Key("use"), i18n.T("Use voice")).Disabled(g.using).
				Tooltip(i18n.T("Save it to your voices and open it in Design")).Clicked() {
				a.useArchetype(arch, false)
			}
			moreMenu(c.Key("more"), "", func(m *ui.Menu) {
				if m.Item(i18n.T("Use in Studio")).Chosen() {
					a.useArchetype(arch, true)
				}
				a.longformVoiceItems(m, func(then func(*core.Voice)) { a.useArchetypeThen(arch, then) }) // M3 (thread #18)
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

// archetypeAttrLabels are an archetype's attributes as labels, in the
// design categories' order (what Summary joins with " · ").
func archetypeAttrLabels(attrs vd.Attributes, locale string) []string {
	attrs = attrs.Canonical()
	var out []string
	for _, cat := range vd.Categories {
		for _, v := range attrs.Get(cat) {
			if op, ok := vd.Lookup(cat, v); ok {
				out = append(out, op.Label.In(locale))
			}
		}
	}
	return out
}

// measure keeps a card size as last laid out (a tag's height, the
// content's), and lays the grid out again when it changed: the cards'
// fixed height is made of them, so there is no slack whatever the fonts.
func (g *galleryState) measure(c *ui.Context, dst *float32, v float32) {
	if v > 0 && v != *dst {
		*dst = v
		c.Invalidate()
	}
}

// cardHeight is a Discover voices card's height: its content (top padding
// to the footer's bottom) and the bottom padding and border; a guess until
// measured.
func (g *galleryState) cardHeight(t *ui.Theme) float32 {
	if g.contentH <= 0 {
		return px(t, 158)
	}
	return g.contentH + px(t, sp3) + 1
}
