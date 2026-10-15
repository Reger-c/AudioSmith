package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/gallery"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

func TestGalleryPage(t *testing.T) {
	dir := t.TempDir()
	a, eng, _ := newTestAppIn(t, dir)
	tt := ui.NewTester(a.view, 1200, 1800)
	g := &a.gallery
	a.navigate("/gallery")
	tt.Frame()
	all := gallery.Default().Len()
	first := gallery.Default().All()[0]
	if !tt.HasText(itoa(all)+" voices") || !tt.HasText(first.Name.En) {
		t.Fatalf("gallery: %q", tt.Texts())
	}

	// Search, a category tab, a facet.
	g.text = "whisper"
	tt.Frame()
	if g.page.Total == 0 || g.page.Total >= all {
		t.Errorf("search: %d", g.page.Total)
	}
	g.text = ""
	tt.Frame()
	games := gallery.Default().Search(gallery.Query{Category: "games"}).Total
	// The categories are a select beside the search field.
	if err := tt.Click("Category"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Games  " + itoa(games)); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if g.page.Total != games || g.tab == 0 {
		t.Errorf("tab: %d %d", g.page.Total, g.tab)
	}
	if err := tt.Click("Gender"); err != nil {
		t.Fatal(err)
	}
	var female string
	for _, it := range tt.Menu() {
		if strings.HasPrefix(it, "Female") {
			female = it
		}
	}
	if female == "" {
		t.Fatalf("menu %q", tt.Menu())
	}
	if err := tt.ChooseMenuItem(female); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if g.filters[gallery.FacetGender] != "female" || !tt.HasText("Gender: Female") {
		t.Errorf("facet: %v %q", g.filters, tt.Texts())
	}
	for _, it := range g.page.Items {
		if it.Category != "games" || it.Attrs.Gender != "female" {
			t.Fatalf("filtered item %+v", it)
		}
	}
	tt.Click("Reset")
	tt.Frame()
	if g.page.Total != all {
		t.Errorf("reset: %d", g.page.Total)
	}
	// Paging.
	if len(g.page.Items) != galleryPageSize {
		t.Errorf("page %d", len(g.page.Items))
	}
	g.limit += galleryPageSize
	tt.Frame()
	if len(g.page.Items) != 2*galleryPageSize {
		t.Errorf("more %d", len(g.page.Items))
	}
	g.limit = galleryPageSize
	tt.Frame()

	// Favorites persist.
	if err := tt.Click("Add to favorites"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !g.favs.Has(first.ID) {
		t.Fatal("not a favorite")
	}
	tt.Click("Favorites")
	tt.Frame()
	if g.page.Total != 1 {
		t.Errorf("favorites only: %d", g.page.Total)
	}
	g.favOnly = false
	b, _, _ := newTestAppIn(t, dir)
	if !b.gallery.favs.Has(first.ID) {
		t.Error("favorite not saved")
	}
	tt.Frame()

	// Preview: licence first, then a background job, cached.
	if err := tt.Click("Preview"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.licenceOpen {
		t.Fatal("no licence dialog")
	}
	a.licenceOpen = false
	acceptLicences(t, a)
	tt.Frame()
	sent := len(eng.sent)
	tt.Click("Preview")
	if len(eng.sent) != sent+1 {
		t.Fatal("no preview job")
	}
	req := eng.last(t)
	if req.Spec.Priority != jobs.PriorityBackground || req.Spec.Owner != galleryOwner+first.ID || req.Model != omni ||
		!strings.HasPrefix(req.Output.Path, a.voiceCacheDir()) || req.Text == "" || req.Seed == nil || *req.Seed != first.Seed {
		t.Fatalf("preview request %+v %+v", req.Spec, req.SynthesizeParams)
	}
	tt.Frame()
	if !tt.HasText("Rendering the preview…") && !strings.Contains(strings.Join(tt.Texts(), "|"), "Rendering") {
		t.Errorf("rendering: %q", tt.Texts())
	}
	writeTestWAV(t, req.Output.Path, 24000, 1)
	eng.succeed(req.ID, proto.SynthesizeResult{Output: req.Output.Path, SampleRate: 24000, AudioSeconds: 1, Seconds: 1})
	p := g.previews[first.ID]
	if p.rendering || p.err != "" {
		t.Fatalf("preview %+v", p)
	}
	a.pl.Stop()
	tt.Frame()
	tt.Click("Preview")
	if len(eng.sent) != sent+1 {
		t.Error("a cached preview rendered again")
	}

	// Use voice: a designed voice with the preview as its sample, in Design.
	if err := tt.Click("Use voice"); err != nil {
		t.Fatal(err)
	}
	if len(a.voices) != 1 {
		t.Fatalf("voices %+v", a.voices)
	}
	v := a.voices[0]
	if v.Origin != "gallery:"+first.ID || v.Kind != core.VoiceDesigned || v.Seed != first.Seed || v.Sample != "sample.wav" || v.Name != first.Name.En {
		t.Fatalf("voice %+v", v)
	}
	if a.router.Path() != "/design" || a.design.linked != v.ID {
		t.Errorf("design: %s %s", a.router.Path(), a.design.linked)
	}
	// Using it again finds it; Use in Studio picks it there.
	a.navigate("/gallery")
	tt.Frame()
	tt.Click("More actions")
	if err := tt.ChooseMenuItem("Use in Studio"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// The recommended model that speaks it (a designed voice: cloned from
	// its sample on Qwen3-TTS 1.7B Base, thread #41).
	if len(a.voices) != 1 || a.studioVoice != v.ID || a.router.Path() != "/studio" || a.preset().ID != "qwen3-tts-1.7b-base-8bit" {
		t.Errorf("use in studio: %d %s %s", len(a.voices), a.router.Path(), a.preset().Repo)
	}
	if _, err := os.Stat(filepath.Join(a.voiceCacheDir(), filepath.Base(req.Output.Path))); err != nil {
		t.Error(err)
	}
}

func TestGalleryPreviewFailure(t *testing.T) {
	a, eng := newVoiceTestApp(t)
	acceptLicences(t, a)
	tt := ui.NewTester(a.view, 1200, 1800)
	a.navigate("/gallery")
	tt.Frame()
	tt.Click("Preview")
	req := eng.last(t)
	eng.fail(req.ID, proto.CodeOOM, "out of memory")
	tt.Frame()
	if !tt.HasText("Preview failed") {
		t.Errorf("failure: %q", tt.Texts())
	}
	// Open in Design without saving.
	first := gallery.Default().All()[0]
	tt.Click("More actions")
	tt.ChooseMenuItem("Open in Design without saving")
	if a.router.Path() != "/design" || a.design.linked != "" || int64(a.design.seed) != first.Seed || len(a.voices) != 0 {
		t.Errorf("open in design: %+v", a.design)
	}
}

// TestGalleryFiltersKeepTheirPlace: Reset appearing with a filter (or
// Favorites) does not make the filter row taller and push the cards down.
func TestGalleryFiltersKeepTheirPlace(t *testing.T) {
	a, _, _ := newTestAppIn(t, t.TempDir())
	tt := ui.NewTester(a.view, 1200, 900)
	g := &a.gallery
	a.navigate("/gallery")
	tt.Frame()
	tt.Frame()
	first := gallery.Default().All()[0].Name.En
	before, ok := tt.Find(first)
	if !ok {
		t.Fatalf("no %q", first)
	}
	g.filters[gallery.FacetGender] = gallery.Default().All()[0].Attrs.Gender
	tt.Frame()
	tt.Frame()
	if !tt.HasText("Reset") {
		t.Fatal("no Reset with a filter on")
	}
	after, ok := tt.Find(first)
	if !ok {
		t.Fatalf("no %q with the filter", first)
	}
	if after.Y != before.Y {
		t.Errorf("cards moved from y=%.1f to %.1f when Reset appeared", before.Y, after.Y)
	}
}

// TestGalleryLoadsMoreAtTheEnd: no Load more button: scrolling to the end
// of the grid loads the next page.
func TestGalleryLoadsMoreAtTheEnd(t *testing.T) {
	a, _, _ := newTestAppIn(t, t.TempDir())
	tt := ui.NewTester(a.view, 1100, 720)
	g := &a.gallery
	a.navigate("/gallery")
	tt.Frame()
	tt.Frame()
	start := len(g.page.Items)
	if start == 0 || start >= gallery.Default().Len() {
		t.Fatalf("first page: %d items", start)
	}
	if tt.HasText("Load more") {
		t.Error("a Load more button")
	}
	for range 40 {
		tt.Scroll(700, 500, 0, 2000)
		tt.Frame()
	}
	if got := len(g.page.Items); got <= start {
		t.Errorf("scrolled to the end: still %d items", got)
	}
}

// TestGalleryPreviewModelDownloaded: previews use a model that is
// downloaded (OmniVoice comes first in the catalogue but often isn't);
// the menu says which are not downloaded; asking for one of those says
// what to do instead of failing.
func TestGalleryPreviewModelDownloaded(t *testing.T) {
	a, eng, _ := newTestAppIn(t, t.TempDir())
	tt := ui.NewTester(a.view, 1200, 900)
	ms := designModels()
	if len(ms) < 2 {
		t.Skip("one design model")
	}
	first, second := ms[0], ms[1]
	a.model(first).known, a.model(first).local.State = true, hub.Missing
	a.model(second).known, a.model(second).local.State = true, hub.Downloaded
	a.navigate("/gallery")
	tt.Frame()
	if e, _ := a.previewModel(); e.ID != second.ID {
		t.Fatalf("previews use %s, not the downloaded %s", e.Label, second.Label)
	}
	if !tt.HasText(second.DisplayName()) {
		t.Errorf("header does not name %s: %q", second.DisplayName(), tt.Texts())
	}
	// Choosing the one not downloaded: Preview explains, sends no job.
	a.gallery.model = first.Label
	tt.Frame()
	before := len(eng.sent)
	a.toasts = nil
	a.previewArchetype(gallery.Default().All()[0])
	if len(eng.sent) != before || len(a.toasts) != 1 || !strings.Contains(a.toasts[0], "not downloaded") {
		t.Errorf("preview with a missing model: sent %d, toasts %q", len(eng.sent)-before, a.toasts)
	}
}
