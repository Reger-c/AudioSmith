package main

// The Voice Gallery (/gallery): ready-made designed voices
// (internal/gallery) to search, filter by facets, favorite, preview and
// use. Previews are tts.synthesize jobs at background priority, planned by
// gallery.PlanPreviews and cached by their key in the cache directory.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/gallery"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/licence"
	"github.com/hehehai/AudioSmith/internal/services"
)

// galleryPageSize is how many voices a page adds.
const galleryPageSize = gallery.DefaultLimit

// galleryOwner prefixes the owners of preview jobs.
const galleryOwner = "archetype:"

// galleryState is the Gallery page's view state.
type galleryState struct {
	text      string
	tab       int // 0 = all categories, i = gallery.Categories()[i-1]
	filters   map[gallery.Facet]string
	favOnly   bool
	limit     int
	moreAsked bool    // the next page was asked for in this frame (infinite scroll)
	tagH      float32 // a card's tag, as last laid out: its tags show two rows

	page    gallery.Page
	pageFor string // the query page answers
	favVer  int    // bumps when favorites change
	grid    ui.GridState

	favs     *gallery.Favorites
	previews map[string]*galleryPreview // by archetype id
	model    string                     // label of the preview model; "" = the first that can design
	using    bool
}

// galleryPreview is an archetype's preview: cached, rendering or failed.
type galleryPreview struct {
	key, path string
	jobID     string
	rendering bool
	err       string
}

// openGallery loads the favorites and follows the preview jobs.
func (a *app) openGallery(lib *library) {
	g := &a.gallery
	g.filters = map[gallery.Facet]string{}
	g.previews = map[string]*galleryPreview{}
	g.limit = galleryPageSize
	g.favs, _ = gallery.LoadFavorites(context.Background(), nil)
	g.grid.Key = func(i int) any {
		if i < len(g.page.Items) {
			return g.page.Items[i].ID
		}
		return i
	}
	store := services.GalleryFavorites{Store: lib.store.Favorites()}
	a.background(func() {
		favs, err := gallery.LoadFavorites(context.Background(), store)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not read the favorites: %v", err))
			}
			g.favs = favs
			g.favVer++
		})
	})
	core.On(lib.bus, func(u jobs.Updated) {
		if u.Chunk != nil || !strings.HasPrefix(u.Job.Owner, galleryOwner) || !u.Job.State.Terminal() {
			return
		}
		a.post(func() { a.galleryJobDone(u.Job) })
	})
}

// galleryQuery is the page's query.
func (a *app) galleryQuery() gallery.Query {
	g := &a.gallery
	cat := ""
	if cats := gallery.Categories(); g.tab > 0 && g.tab <= len(cats) {
		cat = cats[g.tab-1].ID
	}
	q := galleryFilterQuery(g.text, cat, g.filters)
	q.Limit, q.FavoritesOnly, q.Favorites = g.limit, g.favOnly, g.favs
	return q
}

// galleryFilterQuery is the gallery's query for a search text, a
// category and facet filters: the page's and the API's search_voices.
func galleryFilterQuery(text, category string, f map[gallery.Facet]string) gallery.Query {
	return gallery.Query{Text: text, Category: category,
		Gender: f[gallery.FacetGender], Age: f[gallery.FacetAge], Pitch: f[gallery.FacetPitch],
		Accent: f[gallery.FacetAccent], Language: f[gallery.FacetLanguage], Style: f[gallery.FacetStyle]}
}

// galleryResults searches when the query changed (in memory: about a
// third of a millisecond).
func (a *app) galleryResults() gallery.Page {
	g := &a.gallery
	q := a.galleryQuery()
	key := fmt.Sprintf("%q|%s|%d|%v|%v|%d", q.Text, q.Category, q.Limit, g.filters, g.favOnly, g.favVer)
	if key != g.pageFor {
		g.page, g.pageFor = gallery.Default().Search(q), key
	}
	return g.page
}

// toggleFavorite stars or unstars an archetype.
func (a *app) toggleFavorite(id string) {
	g := &a.gallery
	favs := g.favs
	a.background(func() {
		_, err := favs.Toggle(context.Background(), id)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not save the favorite: %v", err))
			}
			g.favVer++
		})
	})
}

// previewModel is the model gallery previews render with: the one chosen
// at the top of the page, else the first that can design and is
// downloaded (OmniVoice comes first in the catalogue, but few have it),
// else the first that can design.
func (a *app) previewModel() (catalog.Entry, bool) {
	ms := designModels()
	for _, e := range ms {
		if e.Label == a.gallery.model {
			return e, true
		}
	}
	for _, e := range ms {
		if ok, _ := a.modelOnDisk(e); ok {
			return e, true
		}
	}
	if len(ms) == 0 {
		return catalog.Entry{}, false
	}
	return ms[0], true
}

// galleryModel describes a model to the preview planner.
func galleryModel(e catalog.Entry) gallery.Model {
	m := gallery.Model{ID: e.ID, Family: e.Family, Repo: e.Repo}
	for _, l := range e.Languages {
		code, _, _ := strings.Cut(l.Code, "-")
		m.Languages = append(m.Languages, code)
	}
	for _, v := range e.Voices {
		m.Speakers = append(m.Speakers, v.ID)
	}
	return m
}

// previewArchetype plays an archetype's preview, rendering it first
// (once: it is cached by its key).
func (a *app) previewArchetype(arch gallery.Archetype) {
	g := &a.gallery
	pid := galleryOwner + arch.ID
	if a.pl.Playing(pid) {
		a.pl.Stop()
		return
	}
	e, ok := a.previewModel()
	if !ok {
		a.toast(i18n.T("No model that can design voices is available."))
		return
	}
	if ok, known := a.modelOnDisk(e); known && !ok {
		// Not a failed render: say what to do instead.
		a.toast(i18n.T("Previews use %s, which is not downloaded: choose a downloaded model at the top right, or download it on the Models page.", e.Label))
		return
	}
	if licence.Required(e) != nil && !licence.Covers(a.accepted, e) {
		a.askLicence(e, func() { a.previewArchetype(arch) })
		return
	}
	plan, err := gallery.PlanPreviews([]gallery.Archetype{arch}, galleryModel(e), nil, 1)
	if err != nil {
		a.toast(err.Error())
		return
	}
	if len(plan.Jobs) == 0 {
		_, lang := arch.PreviewLine()
		a.toast(i18n.T("%s doesn't speak %s, so this voice has no preview.", e.Label, languageName(lang)))
		return
	}
	pj := plan.Jobs[0]
	p := &galleryPreview{key: pj.Key, path: filepath.Join(a.voiceCacheDir(), pj.Key+".wav"), rendering: true}
	g.previews[arch.ID] = p
	spec, err := pj.Spec(pj.Params(e.Repo, modelLanguage(e, pj.Lang), p.path))
	if err != nil {
		p.rendering, p.err = false, err.Error()
		return
	}
	spec.Title = i18n.T("Voice preview")
	sched := a.lib.sched
	a.background(func() {
		if _, err := os.Stat(p.path); err == nil {
			a.post(func() {
				p.rendering = false
				a.pl.Toggle(pid, p.path)
			})
			return
		}
		os.MkdirAll(filepath.Dir(p.path), 0o755)
		id, err := sched.Submit(context.Background(), spec)
		a.post(func() {
			if err != nil {
				p.rendering, p.err = false, err.Error()
				return
			}
			if p.jobID == "" {
				p.jobID = id
			}
		})
	})
}

// galleryJobDone plays a preview that finished rendering.
func (a *app) galleryJobDone(j jobs.Job) {
	id := strings.TrimPrefix(j.Owner, galleryOwner)
	p := a.gallery.previews[id]
	if p == nil || !p.rendering {
		return
	}
	p.rendering = false
	switch j.State {
	case jobs.StateSucceeded:
		if err := a.pl.Toggle(galleryOwner+id, p.path); err != nil {
			p.err = err.Error()
		}
	case jobs.StateCancelled:
	default:
		p.err = jobErrorText(j.Err)
	}
}

// useArchetype saves an archetype as a designed voice (once: a second use
// finds the voice it saved) and opens it in Design, or the Studio.
func (a *app) useArchetype(arch gallery.Archetype, inStudio bool) {
	done := func(v *core.Voice) {
		if inStudio {
			a.useVoiceInStudio(v)
		} else {
			a.openDesignFor(v)
		}
	}
	a.useArchetypeThen(arch, done)
}

// useArchetypeThen saves an archetype as a designed voice (once) and hands
// it to done (M3: Use in Stories, Narrate an audiobook).
func (a *app) useArchetypeThen(arch gallery.Archetype, done func(*core.Voice)) {
	g := &a.gallery
	origin := galleryOrigin(arch.ID)
	for i := range a.voices {
		if a.voices[i].Origin == origin {
			done(&a.voices[i])
			return
		}
	}
	if g.using {
		return
	}
	g.using = true
	model := ""
	if e, ok := a.previewModel(); ok {
		model = e.Repo
	}
	sample := ""
	if p := g.previews[arch.ID]; p != nil && !p.rendering && p.err == "" {
		sample = p.path
	}
	v, files := archetypeVoice(arch, i18n.Locale(), model, sample)
	svc := a.lib.svc.Voices
	a.background(func() {
		saved, err := svc.SaveFiles(context.Background(), v, files)
		a.post(func() {
			g.using = false
			if err != nil {
				a.toast(i18n.T("Could not create that voice: %v", err))
				return
			}
			a.voiceChanged(core.VoiceChanged{Voice: saved})
			a.toast(i18n.T("Saved “%s” to your voices", saved.Name))
			if v := a.findVoice(saved.ID); v != nil {
				done(v)
			}
		})
	})
}

// archetypeVoice is the designed voice "Use voice" saves for an
// archetype: its name in the UI's language, attributes, seed, first
// language and tags, the model it was previewed with, and the preview
// (sample, "" = none) as its sample. The API's use_gallery_voice saves
// the same.
func archetypeVoice(arch gallery.Archetype, loc, model, sample string) (core.Voice, core.VoiceFiles) {
	attrs, _ := json.Marshal(arch.Attrs)
	v := core.Voice{Name: arch.Name.In(loc), Kind: core.VoiceDesigned, DesignAttrs: attrs, Seed: arch.Seed,
		Language: arch.Languages[0], Origin: galleryOrigin(arch.ID), Tags: arch.Tags, CreatedWith: model}
	var files core.VoiceFiles
	if sample != "" {
		v.SampleText, _ = arch.PreviewLine()
		files.Sample = sample
	}
	return v, files
}

// galleryOrigin is the origin of a voice saved from an archetype.
func galleryOrigin(id string) string { return "gallery:" + id }
