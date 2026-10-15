package main

// Panels the Stories and Audiobook pages share: the setup card, the
// production overrides, the book's details and loudness, the project
// card, the render bar with its chapter tracker, and the outputs.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/lang"
	"github.com/hehehai/AudioSmith/internal/services"
)

// lfLayout places a longform page's inspector: beside the main column
// when both fit, else a drawer; it also notes whether the main column is
// narrow (rows fold their actions into ⋯, the header shows icons).
func (a *app) lfLayout(c *ui.Context, p *lfPage) (inline, shown bool) {
	t := c.Theme()
	inline, shown = p.panel.layout(a, c, px(t, inspectorWidth), px(t, 480))
	w := a.pageWidth(c)
	if inline && shown {
		w -= px(t, inspectorWidth)
	}
	p.narrow = w < px(t, 600)
	return inline, shown
}

// lfInspector is a longform page's inspector: its sections, beside the
// page or as a drawer.
func (a *app) lfInspector(c *ui.Context, p *lfPage, inline, shown bool, sections func()) {
	t := c.Theme()
	p.panel.view(c, "lf-inspector", i18n.T("Inspector"), inline, shown, false, px(t, inspectorWidth), func() {
		ui.Column(c).Padding(px(t, sp2), px(t, sp4), px(t, sp5)).Gap(px(t, sp1)).Children(sections)
	})
}

// lfSection is a collapsible section of a page's inspector; summary
// shows on its header (a model, a count).
func (a *app) lfSection(c *ui.Context, p *lfPage, key, title, summary string, fn func()) {
	t := c.Theme()
	open := p.open[key]
	var trailing func()
	if summary != "" {
		trailing = func() {
			caption(c, summary).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1).MaxWidth(px(t, 150))
		}
	}
	collapsible(c.Key("sec-"+key), title, &open, trailing, func() {
		ui.Column(c).Gap(px(t, sp3)).Padding(0, 0, px(t, sp2)).Children(fn)
	})
	p.open[key] = open
}

// lfCount is a section's count summary, "" for none.
func lfCount(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// lfSetup is the setup card: title, model, default voice, speed, language
// and format.
func (a *app) lfSetup(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	d := &p.doc
	placeholder := i18n.T("Untitled story")
	if p.kind == core.ProjectAudiobook {
		placeholder = i18n.T("Untitled audiobook")
	}
	ui.Field(c, i18n.T("Title"), func() {
		ui.TextInput(c.Key("lf-title"), &d.Title).Placeholder(placeholder).Label(i18n.T("Title")).Disabled(locked)
	})
	e := entryFor(d.Model)
	ui.Field(c, i18n.T("Model"), func() {
		a.modelPicker(c, "lf-model", lfWorkflow(p), e, func(ne catalog.Entry) {
			d.Model, p.auto = ne.Repo, false
		}, pickerOpts{Locked: locked})
	})
	if m := a.model(e); e.ID != "custom" && m.known && m.local.State != hub.Downloaded {
		ui.Row(c.Key("lf-download")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			iconMark(c, icons.DangerTriangle, t.Warning, "")
			caption(c, i18n.T("%s is not downloaded yet.", e.Label)).TextColor(t.Warning).Grow(1).MinWidth(0)
			if !m.downloading && secondaryIconButton(c.Key("lf-dl"), icons.DownloadMinimalistic, i18n.T("Download")).Label(i18n.T("Download")).Tooltip(e.Repo).Clicked() {
				a.download(e)
			}
		})
	}
	label := i18n.T("Narrator voice")
	if p.kind == core.ProjectStory {
		label = i18n.T("Default voice")
	}
	ui.Field(c, label, func() {
		before := d.Voice
		a.lfVoiceSelect(c, "lf-voice", &d.Voice, e, i18n.T("None"), locked)
		if d.Voice != before {
			p.auto = false
		}
	})
	if e.NeedsReference {
		caption(c.Key("lf-needs-ref"), i18n.T("%s only clones voices: each line needs a saved cloned voice.", e.Label))
	} else if len(a.lfVoices(e)) == 0 {
		caption(c, i18n.T("No voices for this model yet: clone or design one first."))
	}
	if e.Has(catalog.CapSpeed) {
		if d.Speed <= 0 {
			d.Speed = 1
		}
		ui.Field(c, i18n.T("Speed %.2f×", d.Speed), func() {
			snapped(ui.Slider(c.Key("lf-speed"), &d.Speed, 0.5, 2).Label(i18n.T("Speed")).Disabled(locked), &d.Speed, 0.05)
		})
		if p.kind == core.ProjectStory {
			n := 0
			for _, l := range d.Lines {
				if l.Speed > 0 {
					n++
				}
			}
			if n > 0 {
				ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
					caption(c, i18n.N(n, "%d line has its own speed", "%d lines have their own speed", n)).Grow(1).MinWidth(0)
					if ghostButton(c.Key("lf-speed-all"), i18n.T("Use for all lines")).Disabled(locked).Clicked() {
						for i := range d.Lines {
							d.Lines[i].Speed = 0
						}
					}
				})
			}
		}
	}
	if e.Has(catalog.CapLanguage) || e.ID == "custom" {
		ui.Field(c, i18n.T("Language"), func() { a.lfLanguage(c, d, e, locked) })
	}
	formats := []string{i18n.T("WAV")}
	if a.lfEncode != nil && a.lfEncode() {
		formats = append(formats, i18n.T("M4B (chapters)"))
	}
	f := 0
	if d.Format == services.FormatM4B && len(formats) > 1 {
		f = 1
	}
	ui.Field(c, i18n.T("Format"), func() {
		if segmentedFit(c.Key("lf-format"), &f, formats...).Label(i18n.T("Format")).Disabled(locked).Changed() {
			d.Format = services.FormatWAV
			if f == 1 {
				d.Format = services.FormatM4B
			}
		}
	})
	if len(formats) == 1 {
		caption(c, i18n.T("M4B with chapters and cover comes once the engine can encode it; books are WAV until then.")).TextColor(tok(t).Text3)
	}
}

// lfVoiceSelect picks a voice reference for model e; none is the label of
// "" (the default voice).
func (a *app) lfVoiceSelect(c *ui.Context, key string, ref *string, e catalog.Entry, none string, locked bool) {
	vs := a.lfVoices(e)
	labels, refs := []string{none}, []string{""}
	seen := map[string]int{}
	for _, v := range vs {
		l := v.label
		if n := seen[l]; n > 0 {
			l += " (" + strconv.Itoa(n+1) + ")"
		}
		seen[v.label]++
		labels, refs = append(labels, l), append(refs, v.ref)
	}
	cur := labels[0]
	if i := slices.Index(refs, *ref); i >= 0 {
		cur = labels[i]
	} else if *ref != "" {
		cur = a.lfVoiceLabel(*ref) + " ⚠"
		labels, refs = append(labels, cur), append(refs, *ref)
	}
	if ui.Select(c.Key(key), &cur, labels).Label(none).Disabled(locked).Changed() {
		if i := slices.Index(labels, cur); i >= 0 {
			*ref = refs[i]
		}
	}
}

// lfLanguage is a select of the model's languages (codes), Auto first.
func (a *app) lfLanguage(c *ui.Context, d *services.LongformDoc, e catalog.Entry, locked bool) {
	codes, _, all := languageSupport(e)
	if all {
		codes = []string{"en", "zh", "ja", "ko", "es", "fr", "de", "it", "pt", "ru"}
	}
	labels, values := []string{i18n.T("Auto-detect")}, []string{""}
	for _, code := range codes {
		name := code
		if l, ok := lang.Lookup(code); ok {
			name = strings.TrimSpace(l.Flag() + " " + l.NameIn(i18n.Locale()))
		}
		labels, values = append(labels, name), append(values, code)
	}
	cur := labels[0]
	if i := slices.Index(values, d.Language); i >= 0 {
		cur = labels[i]
	}
	if ui.Select(c.Key("lf-lang"), &cur, labels).Label(i18n.T("Language")).Disabled(locked).Changed() {
		if i := slices.Index(labels, cur); i >= 0 {
			d.Language = values[i]
		}
	}
}

// lfProduction is the production overrides card.
func (a *app) lfProduction(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	pr := &p.doc.Production
	e := entryFor(p.doc.Model)
	if modelInfo(e.Repo).Steps {
		steps := float64(pr.Steps)
		if steps == 0 {
			steps = services.StepsBalanced
		}
		ui.Field(c, i18n.T("Steps %d", int(steps)), func() {
			if ui.StepSlider(c.Key("lf-steps"), &steps, 8, 64, 4).Label(i18n.T("Steps")).Disabled(locked).Changed() {
				pr.Steps = int(steps)
			}
		})
	}
	if e.Has(catalog.CapGuidance) {
		g := pr.Guidance
		if g == 0 {
			g = 2
		}
		ui.Field(c, i18n.T("Guidance %.1f", g), func() {
			if snapped(ui.Slider(c.Key("lf-cfg"), &g, 0, 4).Label(i18n.T("Guidance")).Disabled(locked), &g, 0.1).Changed() {
				pr.Guidance = g
			}
		})
	}
	formRow(c, "lf-vary-row", i18n.T("Vary repeated lines"), i18n.T("Give identical lines takes of their own instead of reusing one recording."), func() {
		ui.Switch(c.Key("lf-vary"), &pr.VaryRepeats).Label(i18n.T("Vary repeated lines")).Disabled(locked)
	})
	if _, paced := services.GapsOf(pr.Pacing); paced && p.kind == core.ProjectStory {
		caption(c.Key("lf-paced"), i18n.T("Gaps between lines follow the pacing: %s (Performance).", pacingLabel(pr.Pacing)))
	} else {
		lg := float64(pr.LineGapMs)
		ui.Field(c, i18n.T("Gap between lines %d ms", pr.LineGapMs), func() {
			if snapped(ui.Slider(c.Key("lf-line-gap"), &lg, 0, 2000).Label(i18n.T("Gap between lines")).Disabled(locked), &lg, 50).Changed() {
				pr.LineGapMs = int(lg)
			}
		})
	}
	pg := float64(pr.ParagraphGapMs)
	ui.Field(c, i18n.T("Gap between paragraphs %d ms", pr.ParagraphGapMs), func() {
		if snapped(ui.Slider(c.Key("lf-para-gap"), &pg, 0, 3000).Label(i18n.T("Gap between paragraphs")).Disabled(locked), &pg, 50).Changed() {
			pr.ParagraphGapMs = int(pg)
		}
	})
	trim := !pr.KeepEdges
	formRow(c, "lf-trim-row", i18n.T("Trim engine silence"), i18n.T("Cut the silence engines pad onto each chunk before joining, so the gaps above are what you hear."), func() {
		if ui.Switch(c.Key("lf-trim"), &trim).Label(i18n.T("Trim engine silence")).Disabled(locked).Changed() {
			pr.KeepEdges = !trim
		}
	})
	seed := float64(pr.Seed)
	formRow(c, "lf-seed-row", i18n.T("Seed"), i18n.T("0: the project's own seed (%d), so a render can be made again from the cache.", p.doc.Seed), func() {
		if numberInput(c.Key("lf-seed"), &seed, 0, 1<<31-1, 1, numOpts{Label: i18n.T("Seed"), Width: px(t, 160), Disabled: locked}) {
			pr.Seed = int64(seed)
		}
	})
	ui.Row(c).Children(func() {
		def := services.DefaultProduction()
		if p.kind == core.ProjectStory {
			def = services.DefaultStoryProduction()
		}
		if secondaryIconButton(c.Key("lf-prod-reset"), icons.Restart, i18n.T("Reset")).Label(i18n.T("Reset")).Disabled(locked || *pr == def).Clicked() {
			*pr = def
		}
	})
}

// Loudness choices of the book card.
var lfLoudness = []string{services.LoudnessOff, services.LoudnessACX, services.LoudnessPodcast}

// lfBookCard is the book's details, cover, loudness and stems.
func (a *app) lfBookCard(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	b := &p.doc.Book
	for _, f := range []struct {
		key, label string
		v          *string
	}{
		{"author", i18n.T("Author"), &b.Author}, {"narrator", i18n.T("Narrator"), &b.Narrator},
		{"year", i18n.T("Year"), &b.Year}, {"genre", i18n.T("Genre"), &b.Genre},
	} {
		ui.Field(c, f.label, func() {
			ui.TextInput(c.Key("book-"+f.key), f.v).Label(f.label).Disabled(locked)
		})
	}
	ui.Field(c, i18n.T("Description"), func() {
		ui.TextArea(c.Key("book-desc"), &b.Description).Label(i18n.T("Description")).Lines(2, 6).MinHeight(0).Disabled(locked)
	})
	ui.Row(c.Key("book-cover")).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
		if bmp := a.coverBitmap(b.Cover); bmp != nil {
			ui.Image(c, bmp).Width(px(t, 36)).Height(px(t, 48)).Radius(px(t, 4))
		}
		name := i18n.T("No cover")
		if b.Cover != "" {
			name = filepath.Base(b.Cover)
		}
		truncText(c, name).FontSize(px(t, fsCaption)).TextColor(tok(t).Text2).Grow(1)
		if ghostButton(c.Key("cover-add"), i18n.T("Choose cover…")).Tooltip(i18n.T("PNG or JPEG, at most 8 MB; embedded in M4B books")).Disabled(locked).Clicked() {
			a.chooseCover(p)
		}
		if b.Cover != "" && iconButton(c.Key("cover-remove"), icons.TrashBinMinimalistic, i18n.T("Remove")).Disabled(locked).Clicked() {
			b.Cover = ""
		}
	})
	li := max(slices.Index(lfLoudness, b.Loudness), 0)
	note := i18n.T("Chapters as rendered, not normalised.")
	switch b.Loudness {
	case services.LoudnessACX:
		note = i18n.T("Audible's ACX: −19 LUFS, peaks under −3 dBTP; each chapter is checked.")
	case services.LoudnessPodcast:
		note = i18n.T("Podcast: −16 LUFS, peaks under −1.5 dBTP.")
	}
	fieldInfo(c, i18n.T("Loudness"), note, func() {
		if segmentedFit(c.Key("book-loud"), &li, i18n.T("Off"), i18n.T("ACX"), i18n.T("Podcast")).Label(i18n.T("Loudness")).Disabled(locked).Changed() {
			b.Loudness = lfLoudness[li]
		}
	})
	formRow(c, "book-stems-row", i18n.T("A stem per character"), i18n.T("Each character's lines on the book's timeline, silent elsewhere, for mixing. As large as the book per character."), func() {
		ui.Switch(c.Key("book-stems"), &p.doc.Stems).Label(i18n.T("A stem per character")).Disabled(locked)
	})
}

// coverCache holds decoded covers by path (main thread only).
var coverCache = map[string]*ui.Bitmap{}

// coverBitmap is a cover's thumbnail, decoded in the background.
func (a *app) coverBitmap(path string) *ui.Bitmap {
	if path == "" {
		return nil
	}
	if bmp, ok := coverCache[path]; ok {
		return bmp
	}
	coverCache[path] = nil
	a.background(func() {
		data, err := os.ReadFile(path)
		var bmp *ui.Bitmap
		if err == nil {
			bmp, _ = ui.DecodeBitmap(data)
		}
		a.post(func() { coverCache[path] = bmp })
	})
	return nil
}

// chooseCover asks for a cover image and copies it into the library.
func (a *app) chooseCover(p *lfPage) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: i18n.T("Choose cover…"),
			Filters: []mygo.FileFilter{{Name: i18n.T("Images"), Extensions: []string{"png", "jpg", "jpeg"}}}})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.setCover(p, paths[0]) })
	}()
}

// setCover copies an image into <data>/covers and makes it the cover.
func (a *app) setCover(p *lfPage, src string) {
	dir := filepath.Join(a.lib.store.Dir(), "covers")
	a.background(func() {
		fi, err := os.Stat(src)
		switch {
		case err != nil:
		case fi.Size() > services.MaxCoverBytes:
			err = fmt.Errorf("%s", i18n.T("%s is larger than 8 MB.", filepath.Base(src)))
		default:
			ext := strings.ToLower(filepath.Ext(src))
			if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
				err = fmt.Errorf("%s", i18n.T("A cover is a PNG or JPEG image."))
				break
			}
			dst := filepath.Join(dir, core.NewID(time.Now())+ext)
			if err = os.MkdirAll(dir, 0o755); err == nil {
				err = services.CopyFile(context.Background(), src, dst)
			}
			if err == nil {
				a.post(func() { p.doc.Book.Cover = dst })
				return
			}
		}
		a.post(func() { a.toast(i18n.T("Can't use this cover: %v", err)) })
	})
}

// lfProjectCard is the project card: name, Save, Save a copy, New, and the
// saved projects of the page's kind.
func (a *app) lfProjectCard(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	if p.saveName == "" && p.doc.Title != "" && p.project == "" {
		p.saveName = p.doc.Title
	}
	ui.Field(c, i18n.T("Project name"), func() {
		ui.TextInput(c.Key("proj-name"), &p.saveName).Placeholder(i18n.T("Project name")).Label(i18n.T("Project name"))
	})
	ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
		if ui.Button(c.Key("proj-save"), i18n.T("Save")).Disabled(strings.TrimSpace(p.saveName) == "" && strings.TrimSpace(p.doc.Title) == "").Clicked() {
			a.saveLf(p, false)
		}
		if p.project != "" && p.unsaved() {
			statusDot(c, t.Warning)
			caption(c, i18n.T("Unsaved changes")).SingleLine().Ellipsis("…").MinWidth(0)
		}
		ui.Spacer(c)
		moreMenu(c.Key("proj-more"), i18n.T("Project actions"), func(m *ui.Menu) {
			if p.project != "" && m.Item(i18n.T("Save a copy")).Chosen() {
				a.saveLf(p, true)
			}
			if m.Item(i18n.T("New")).Disabled(locked).Chosen() {
				if p.unsaved() {
					p.confirm, p.confirmOpen = "new", true
				} else {
					a.newLf(p)
				}
			}
		})
	})
	if len(p.projects) == 0 {
		caption(c, i18n.T("No saved projects yet."))
		return
	}
	ui.Column(c).Gap(px(t, 2)).Children(func() {
		for _, pr := range p.projects {
			row := ui.Row(c.Key("proj-"+pr.ID)).Gap(px(t, 6)).AlignItems(ui.Center).Padding(px(t, 4), px(t, 4), px(t, 4), px(t, sp2)).Radius(px(t, radiusControl))
			if pr.ID == p.project {
				row.Background(tok(t).Pressed)
			}
			row.Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					truncText(c, pr.Title)
					ui.Text(c, i18n.DateTime(pr.UpdatedAt)).FontSize(px(t, fsMicro)).TextColor(tok(t).Text3)
				})
				if pr.ID != p.project && ghostButton(c.Key("open"), i18n.T("Open")).Disabled(locked).Clicked() {
					if p.unsaved() {
						p.confirm, p.confirmOpen = pr.ID, true
					} else {
						a.openLfProject(pr.ID)
					}
				}
				if iconButton(c.Key("delete"), icons.TrashBinMinimalistic, i18n.T("Delete")).Disabled(locked && pr.ID == p.project).Clicked() {
					p.confirm, p.confirmOpen = "delete:"+pr.ID, true
				}
			})
		}
	})
}

// lfConfirm asks before replacing or deleting.
func (a *app) lfConfirm(c *ui.Context, p *lfPage) {
	if !p.confirmOpen {
		return
	}
	switch {
	case p.confirm == "new":
		if ui.AlertDialog(c, &p.confirmOpen, i18n.T("Start a new project?"), i18n.T("The current draft has unsaved changes. Save your current work first."),
			i18n.T("Cancel"), i18n.T("Discard and start new")) == 1 {
			a.newLf(p)
		}
	case p.confirm == "clear":
		msg := i18n.T("Remove the whole manuscript? This cannot be undone.")
		if p.kind == core.ProjectStory {
			msg = i18n.N(len(p.doc.Lines), "Clear %d line and the text being reviewed? The cast stays. This cannot be undone.",
				"Clear all %d lines and the text being reviewed? The cast stays. This cannot be undone.", len(p.doc.Lines))
		}
		if ui.AlertDialog(c, &p.confirmOpen, i18n.T("Clear the script?"), msg, i18n.T("Cancel"), i18n.T("Clear")) == 1 {
			a.clearLf(p)
		}
	case strings.HasPrefix(p.confirm, "delete:"):
		id := strings.TrimPrefix(p.confirm, "delete:")
		if ui.AlertDialog(c, &p.confirmOpen, i18n.T("Delete this project permanently?"), i18n.T("Its finished renders and exported files are kept."),
			i18n.T("Cancel"), i18n.T("Delete")) == 1 {
			a.deleteLfProject(id)
		}
	default:
		title := p.confirm
		for _, pr := range p.projects {
			if pr.ID == p.confirm {
				title = pr.Title
			}
		}
		if ui.AlertDialog(c, &p.confirmOpen, i18n.T("Open “%s”?", title), i18n.T("It replaces the current draft. Save your current work first."),
			i18n.T("Cancel"), i18n.T("Open")) == 1 {
			a.openLfProject(p.confirm)
		}
	}
}

// clearLf empties the script (the cast stays).
func (a *app) clearLf(p *lfPage) {
	p.doc.Lines, p.doc.ImportText, p.sel = nil, "", -1
	if p.kind == core.ProjectAudiobook {
		a.setManuscript(p, "")
	}
}

// clearLfUndo empties a story's script at once, with Undo.
func (a *app) clearLfUndo(p *lfPage) {
	lines, review := slices.Clone(p.doc.Lines), p.doc.ImportText
	a.clearLf(p)
	a.toastUndo(i18n.N(len(lines), "Cleared %d line", "Cleared %d lines", len(lines)), func() {
		p.doc.Lines, p.doc.ImportText = lines, review
	})
}

// --- the render bar --------------------------------------------------------

// lfRenderPanel is the render bar and the outputs, scrolling when they
// would take more than half the page.
func (a *app) lfRenderPanel(c *ui.Context, p *lfPage) {
	t := c.Theme()
	ui.Scroll(c.Key("lf-panel")).MaxHeightPercent(55).Shrink(0).Children(func() {
		ui.Column(c).Gap(px(t, sp3)).Children(func() {
			a.lfRenderBar(c, p)
			a.lfOutputs(c, p)
		})
	})
}

// lfRenderBar is Generate / Stop, the reason it can't, and the progress.
func (a *app) lfRenderBar(c *ui.Context, p *lfPage) {
	t := c.Theme()
	k := tok(t)
	r := a.lfRenders[p.render]
	running := r != nil && r.rec.State == core.RenderRunning
	blocker := a.lfBlocker(p)
	ui.Column(c.Key("lf-render")).Gap(px(t, sp2)).Children(func() {
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).MinHeight(px(t, 32)).Children(func() {
			label := i18n.T("Generate")
			if p.kind == core.ProjectAudiobook {
				label = i18n.T("Create audiobook")
			}
			switch {
			case running:
				ui.Spinner(c)
				ui.Text(c, a.renderStatus(r)).FontSize(px(t, fsCaption)).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
				if secondaryIconButton(c.Key("lf-stop"), icons.Stop, i18n.T("Stop")).Label(i18n.T("Stop")).Disabled(r.body.Stopping).Clicked() {
					a.stopLf(p)
				}
			default:
				msg := blocker
				if msg == "" {
					msg = a.lfReadyLine(p)
				}
				if blocker != "" {
					iconMark(c, icons.DangerTriangle, t.Warning, "")
				}
				txt := caption(c.Key("lf-blocker"), msg).Grow(1).MinWidth(0)
				if blocker != "" {
					txt.TextColor(t.Warning)
				}
				if r != nil && (r.rec.State == core.RenderFailed || r.rec.State == core.RenderStopped) {
					if ui.Button(c.Key("lf-resume"), i18n.X("render", "Resume")).Tooltip(i18n.T("Render what is missing: finished chapters are kept")).
						Disabled(a.activeRender() != nil).Clicked() {
						a.resumeLf(p, r.rec.ID)
					}
				}
				// Clear: start over with other text (Undo brings it back).
				if p.kind == core.ProjectStory && len(p.doc.Lines) > 0 {
					if ghostIconButton(c.Key("lf-clear"), icons.TrashBinMinimalistic, i18n.X("story", "Clear")).
						Tooltip(i18n.T("Remove every line to start again; the cast stays")).Clicked() {
						a.clearLfUndo(p)
					}
				}
				if primaryButton(c.Key("lf-generate"), label).Disabled(blocker != "").Clicked() {
					a.startLf(p, "")
				}
			}
		})
		if r == nil {
			return
		}
		switch {
		case running:
			a.chapterTracker(c, r)
		case r.rec.State == core.RenderFailed:
			n := r.body.Failed()
			msg := renderErrorText(&r.body)
			if n > 0 {
				msg = i18n.N(n, "%d chapter failed: Resume retries just that one.", "%d chapters failed: Resume retries just those.", n) + " " + msg
			}
			ui.Row(c).Gap(px(t, 6)).Children(func() {
				iconMark(c, icons.DangerCircle, t.Danger, "")
				caption(c.Key("lf-failed"), msg).TextColor(t.Danger).Selectable().Grow(1).MinWidth(0)
			})
			a.chapterTracker(c, r)
		case r.rec.State == core.RenderStopped:
			caption(c, i18n.T("Stopped: finished chapters are kept; Resume goes on from them.")).TextColor(k.Text2)
		}
	})
	a.interruptedRenders(c, p)
}

// lfReadyLine says what Generate will make.
func (a *app) lfReadyLine(p *lfPage) string {
	st := p.stats
	if st.Chapters == 0 || p.kind == core.ProjectAudiobook { // the Audiobook's stats show above its manuscript
		return ""
	}
	return i18n.N(st.Chapters, "%d chapter", "%d chapters", st.Chapters) + " · " + i18n.T("≈ %s", formatDuration(st.Seconds))
}

// renderStatus is a running render's one line.
func (a *app) renderStatus(r *lfRender) string {
	b := &r.body
	if b.Stopping {
		return i18n.T("Stopping…")
	}
	if b.Assembling {
		if b.Assembled > 0 {
			return i18n.T("Assembling the audiobook… %d%%", int(b.Assembled*100))
		}
		return i18n.T("Assembling the audiobook…")
	}
	cur := 0
	for i, c := range b.Chapters {
		if c.State == services.ChapterRendering {
			cur = i + 1
		}
	}
	if cur == 0 {
		return i18n.T("Waiting for the engine…")
	}
	return i18n.T("Synthesizing chapter %d of %d", cur, len(b.Chapters))
}

// chapterTracker: elapsed, ETA, a bar, and every chapter's state.
func (a *app) chapterTracker(c *ui.Context, r *lfRender) {
	t := c.Theme()
	k := tok(t)
	b := &r.body
	total, done := 0.0, 0.0
	for _, ch := range b.Chapters {
		total += float64(ch.Chars)
		switch ch.State {
		case services.ChapterDone, services.ChapterCached:
			done += float64(ch.Chars)
		case services.ChapterRendering:
			done += float64(ch.Chars) * ch.Fraction
		}
	}
	frac := 0.0
	if total > 0 {
		frac = done / total
	}
	if b.Assembling || r.rec.State == core.RenderDone {
		frac = 1
	}
	now := time.Now()
	if r.rec.State == core.RenderRunning {
		c.After(time.Second) // the clock
	}
	fillCard(c.Key("lf-track")).Padding(px(t, sp3)).Gap(px(t, sp2)).Children(func() {
		ui.Progress(c, frac).Label(i18n.T("Render progress"))
		info := i18n.T("%d of %d chapters", b.Done(), len(b.Chapters))
		if !b.Started.IsZero() && r.rec.State == core.RenderRunning {
			info += " · " + i18n.T("elapsed %s", lfClock(now.Sub(b.Started)))
			if eta, ok := services.ETA(b, now); ok && !b.Assembling {
				info += " · " + i18n.T("~%s left", lfClock(eta))
			}
		}
		caption(c, info).SingleLine().Ellipsis("…").MinWidth(0)
		ui.Scroll(c.Key("lf-chapters")).MaxHeight(px(t, 200)).Children(func() {
			ui.Column(c).Gap(px(t, 2)).Children(func() {
				for i, ch := range b.Chapters {
					title := ch.Title
					if title == "" {
						title = i18n.T("Chapter %d", i+1)
					}
					extra := ""
					switch ch.State {
					case services.ChapterRendering:
						extra = " · " + strconv.Itoa(int(ch.Fraction*100)) + "%"
					case services.ChapterCached:
						extra = " · " + i18n.T("cached")
					case services.ChapterFailed:
						extra = " · " + i18n.T("failed: %s", jobErrorText(ch.Error))
					case services.ChapterStopped:
						extra = " · " + i18n.T("stopped")
					}
					ui.Row(c.Key("ch-" + strconv.Itoa(i))).Gap(px(t, sp2)).AlignItems(ui.Center).MinHeight(px(t, 22)).Children(func() {
						ui.Box(c).Size(px(t, 14), px(t, 14)).Center().Shrink(0).Children(func() {
							switch ch.State {
							case services.ChapterRendering:
								statusDot(c, k.Accent)
							case services.ChapterDone:
								iconMark(c, icons.CheckCircle, t.Success, "")
							case services.ChapterCached:
								iconMark(c, icons.CheckCircle, k.Text2, "")
							case services.ChapterFailed:
								iconMark(c, icons.CloseCircle, t.Danger, "")
							case services.ChapterStopped:
								iconMark(c, icons.Stop, k.Text3, "")
							default:
								statusDot(c, k.G[5])
							}
						})
						truncText(c, title+extra).FontSize(px(t, fsCaption)).Grow(1)
					})
				}
			})
		})
	})
}

// lfClock is "M:SS" or "H:MM:SS".
func lfClock(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// interruptedRenders lists this page's renders that stopped or failed
// (other than the one shown), with Resume.
func (a *app) interruptedRenders(c *ui.Context, p *lfPage) {
	t := c.Theme()
	var list []*lfRender
	for _, id := range a.lfRecent {
		r := a.lfRenders[id]
		if r == nil || r.rec.Kind != p.kind || id == p.render {
			continue
		}
		if r.rec.State == core.RenderFailed || r.rec.State == core.RenderStopped {
			list = append(list, r)
		}
		if len(list) == 3 {
			break
		}
	}
	for _, r := range list {
		ui.Row(c.Key("int-"+r.rec.ID)).Gap(px(t, sp2)).AlignItems(ui.Center).Padding(px(t, sp2), px(t, sp2), px(t, sp2), px(t, sp3)).
			Radius(px(t, radiusControl)).Border(1, tok(t).Line).Children(func() {
			iconMark(c, icons.Stop, tok(t).Text3, "")
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				truncText(c, i18n.T("Interrupted: %s", r.rec.Title))
				ui.Text(c, i18n.T("%d of %d chapters are made", r.body.Done(), len(r.body.Chapters))).FontSize(px(t, fsMicro)).TextColor(tok(t).Text2)
			})
			if ghostButton(c.Key("show"), i18n.T("Show")).Clicked() {
				p.render = r.rec.ID
			}
			if ui.Button(c.Key("resume"), i18n.X("render", "Resume")).Disabled(a.activeRender() != nil).Clicked() {
				a.resumeLf(p, r.rec.ID)
			}
		})
	}
}

// --- outputs ---------------------------------------------------------------

// lfOutputs shows a finished render as a flat card like the Studio's
// take: play, what it is, Save ▾ (the book, chapters, cue sheet, stems)
// and ⋯ (Show in Finder); then the loudness checks and (Audiobook) the
// read-along player.
func (a *app) lfOutputs(c *ui.Context, p *lfPage) {
	t := c.Theme()
	k := tok(t)
	r := a.lfRenders[p.render]
	if r == nil || r.rec.State != core.RenderDone || r.body.Output == nil {
		return
	}
	out := r.body.Output
	id := "book:" + r.rec.ID
	card(c.Key("lf-out")).Padding(px(t, sp3), px(t, sp4)).Children(func() {
		ui.Row(c).Gap(px(t, sp3)).AlignItems(ui.Center).Children(func() {
			if playButton(c.Key("out-play"), a.pl.Playing(id), false, 36).Clicked() {
				if err := a.pl.Toggle(id, out.Book); err != nil {
					a.toast(i18n.T("Can't play: %v", err))
				}
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
				title := i18n.T("Story ready")
				if r.rec.Kind == core.ProjectAudiobook {
					title = i18n.T("Audiobook ready")
				}
				ui.Text(c, title).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0)
				meta := lfClock(time.Duration(out.Seconds*float64(time.Second))) + " · " + humanBytes(out.Bytes)
				if n := doneCached(&r.body); n > 0 {
					meta += " · " + i18n.N(n, "Reused %d already-rendered chapter.", "Reused %d already-rendered chapters.", n)
				}
				truncText(c, meta).FontSize(px(t, fsCaption)).TextColor(k.Text2)
			})
			dropButton(c.Key("out-save"), icons.Export, i18n.T("Save")).Shrink(0).Menu(func(m *ui.Menu) {
				m.Submenu(i18n.T("Whole book"), func(m *ui.Menu) {
					if out.Encoded != "" && m.Item(i18n.T("M4B audiobook")).Chosen() {
						a.saveOutput(r, "book", out.Encoded, "m4b", 0)
					}
					for _, bits := range bitDepths {
						if m.Item(i18n.T("WAV, %s", bitsLabel(bits))).Chosen() {
							a.saveOutput(r, "book", out.Book, "wav", bits)
						}
					}
					a.ffmpegBookItems(m, r) // M7a: MP3/Opus with chapters (ffmpeg_formats.go)
				})
				if m.Item(i18n.T("One WAV per chapter…")).Chosen() {
					a.saveChapters(r)
				}
				if m.Item(i18n.T("Chapter times (.txt)…")).Chosen() {
					a.saveOutput(r, "cues", out.Cues, "txt", 0)
				}
				if len(out.Stems) > 0 {
					m.Submenu(i18n.T("Stems"), func(m *ui.Menu) {
						for _, s := range out.Stems {
							name := s.Character
							if name == "" {
								name = i18n.T("Narrator")
							}
							if m.Item(name).Chosen() {
								a.saveOutput(r, "stem", s.File, "wav", 0)
							}
							a.ffmpegStemItems(m, r, s, name) // M7a: ffmpeg_formats.go
						}
					})
				}
			})
			moreMenu(c.Key("out-more"), "", func(m *ui.Menu) {
				if m.Item(i18n.T("Show in Finder")).Chosen() {
					mygo.Shell.ShowItemInFolder(out.Book)
				}
			})
		})
		if a.pl.Current() == id {
			prog := float64(a.pl.Progress(id))
			if ui.Slider(c.Key("out-seek"), &prog, 0, 1).Label(i18n.T("Position")).Changed() {
				a.pl.Seek(id, float32(prog))
			}
		}
		if out.EncodeError != "" {
			ui.Row(c).Gap(px(t, 6)).Children(func() {
				iconMark(c, icons.DangerTriangle, t.Warning, "")
				caption(c, i18n.T("No M4B: %s", out.EncodeError)).TextColor(t.Warning).Grow(1).MinWidth(0)
			})
		}
		a.loudnessReport(c, p, r)
		if r.rec.Kind == core.ProjectAudiobook {
			a.readAlongView(c, p, r)
		}
	})
}

// doneCached counts the chapters a render reused.
func doneCached(b *services.RenderBody) int {
	n := 0
	for _, c := range b.Chapters {
		if c.State == services.ChapterCached {
			n++
		}
	}
	return n
}

// loudnessReport shows the book's loudness against its target and each
// chapter's checks.
func (a *app) loudnessReport(c *ui.Context, p *lfPage, r *lfRender) {
	t := c.Theme()
	out := r.body.Output
	rep := out.Loudness
	line := i18n.T("%.1f LUFS · peak %.1f dBTP · LRA %.1f LU", rep.Integrated, rep.TruePeak, rep.Range)
	if rep.Target == "" {
		caption(c, line)
		return
	}
	failed := 0
	for _, ch := range out.Chapters {
		if !ch.Report.Compliant {
			failed++
		}
	}
	open := p.open["loudness"]
	head := i18n.T("%s: every chapter passes", strings.ToUpper(rep.Target))
	if failed > 0 {
		head = i18n.N(failed, "%[2]s: %[1]d chapter fails a check", "%[2]s: %[1]d chapters fail a check", failed, strings.ToUpper(rep.Target))
	}
	var summary func()
	if !p.narrow {
		summary = func() { caption(c, line).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1).Tooltip(line) }
	}
	collapsible(c.Key("loud"), head, &open, summary, func() {
		ui.Column(c).Gap(px(t, 4)).Children(func() {
			if p.narrow {
				caption(c, line)
			}
			for i, ch := range out.Chapters {
				title := ch.Title
				if title == "" {
					title = i18n.T("Chapter %d", i+1)
				}
				var bad []string
				for _, k := range ch.Report.Checks {
					if !k.OK {
						bad = append(bad, checkText(k.Code))
					}
				}
				floor := fmt.Sprintf("%.0f dB", ch.Report.NoiseFloor)
				if ch.Report.NoiseFloor <= -119 {
					floor = i18n.T("digital silence")
				}
				text := fmt.Sprintf("%s · %.1f LUFS · %.1f dBTP · RMS %.1f dB · %s %s", title, ch.Report.Integrated, ch.Report.TruePeak, ch.Report.RMS, i18n.T("floor"), floor)
				if len(bad) > 0 {
					text += " · " + strings.Join(bad, ", ")
				}
				ui.Row(c.Key("loud-" + strconv.Itoa(i))).Gap(px(t, sp2)).Children(func() {
					if ch.Report.Compliant {
						iconMark(c, icons.CheckCircle, t.Success, "")
					} else {
						iconMark(c, icons.CloseCircle, t.Danger, "")
					}
					ui.Text(c, text).FontSize(px(t, fsCaption)).TextColor(tok(t).Text2).Grow(1).MinWidth(0)
				})
			}
		})
	})
	p.open["loudness"] = open
}

// checkText names a loudness check.
func checkText(code string) string {
	switch code {
	case "integrated":
		return i18n.T("loudness out of range")
	case "true_peak":
		return i18n.T("peaks too high")
	case "rms":
		return i18n.T("RMS out of range")
	case "noise_floor":
		return i18n.T("noise floor too high")
	case "silent":
		return i18n.T("silent")
	}
	return code
}

// saveOutput asks where to save one of a render's files, writes it (a WAV
// at bits, 0 = as it is) and records the export.
func (a *app) saveOutput(r *lfRender, kind, src, ext string, bits int) {
	name := services.SafeFileName(r.rec.Title, "audiobook")
	switch kind {
	case "cues":
		name += " cues"
	case "stem":
		name += " " + strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	}
	rec, lib := r.rec, a.lib.svc.Library
	go func() {
		dst, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.win, Title: i18n.T("Save as"), DefaultPath: name + "." + ext,
			Filters: []mygo.FileFilter{{Name: strings.ToUpper(ext), Extensions: []string{ext}}}})
		if err != nil || dst == "" {
			return
		}
		if filepath.Ext(dst) == "" {
			dst += "." + ext
		}
		a.writeOutput(lib, rec, kind, src, dst, ext, bits)
	}()
}

// writeOutput copies (or converts) src to dst and records the export.
func (a *app) writeOutput(lib *services.Library, rec core.Render, kind, src, dst, ext string, bits int) {
	err := writeRenderFile(context.Background(), lib, rec, kind, src, dst, ext, bits)
	a.post(func() {
		if err != nil {
			a.toast(i18n.T("Export failed: %v", err))
		} else {
			a.toast(i18n.T("Saved %s", filepath.Base(dst)))
		}
	})
}

// saveChapters copies every chapter's WAV into a folder.
func (a *app) saveChapters(r *lfRender) {
	rec, chs, lib := r.rec, slices.Clone(r.body.Output.Chapters), a.lib.svc.Library
	go func() {
		dirs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: i18n.T("Save chapters…"), Directory: true,
			ButtonLabel: i18n.T("Save here")})
		if err != nil || len(dirs) == 0 {
			return
		}
		a.writeChapters(lib, rec, chs, dirs[0])
	}()
}

func (a *app) writeChapters(lib *services.Library, rec core.Render, chs []services.BookChapter, dir string) {
	_, err := writeRenderChapters(context.Background(), lib, rec, chs, dir)
	a.post(func() {
		if err != nil {
			a.toast(i18n.T("Export failed: %v", err))
		} else {
			a.toast(i18n.N(len(chs), "Saved %d chapter", "Saved %d chapters", len(chs)))
		}
	})
}

// writeRenderFile copies (or converts to a WAV of bits; 0 = as it is) a
// render's file src to dst and records the export (Save and the API's
// export_longform).
func writeRenderFile(ctx context.Context, lib *services.Library, rec core.Render, kind, src, dst, ext string, bits int) error {
	var err error
	if bits > 0 {
		err = services.ConvertWAV(ctx, src, dst, bits)
	} else {
		err = services.CopyFile(ctx, src, dst)
	}
	if err == nil {
		_, err = lib.RecordExport(ctx, core.Export{Kind: kind, SourceID: rec.ID, ProjectID: rec.ProjectID, Title: rec.Title, Path: dst, Format: ext})
	}
	return err
}

// writeRenderChapters copies every chapter's WAV into dir as "NN Title.wav"
// and records each export; it returns the files written.
func writeRenderChapters(ctx context.Context, lib *services.Library, rec core.Render, chs []services.BookChapter, dir string) ([]string, error) {
	var files []string
	for i, ch := range chs {
		title := ch.Title
		if title == "" {
			title = services.Untitled(i + 1)
		}
		dst := filepath.Join(dir, fmt.Sprintf("%02d %s.wav", i+1, services.SafeFileName(title, "chapter")))
		if err := services.CopyFile(ctx, ch.File, dst); err != nil {
			return files, err
		}
		files = append(files, dst)
		lib.RecordExport(ctx, core.Export{Kind: "chapter", SourceID: rec.ID, ProjectID: rec.ProjectID, Title: rec.Title + " · " + title, Path: dst, Format: "wav"})
	}
	return files, nil
}
