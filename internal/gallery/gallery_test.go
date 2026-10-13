package gallery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/jobs"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

func TestDefault(t *testing.T) {
	g := Default()
	if n := g.Len(); n < 1000 || n > 1300 {
		t.Errorf("Len = %d, want about 1,100", n)
	}
	featured, perCat := 0, map[string]int{}
	namesEn, namesZh := map[string]string{}, map[string]string{}
	usedFlavor, usedLocale := map[string]bool{}, map[string]bool{}
	for _, a := range g.All() {
		if a.Featured {
			featured++
		}
		perCat[a.Category]++
		if prev, dup := namesEn[a.Name.En]; dup {
			t.Errorf("duplicate English name %q: %s and %s", a.Name.En, prev, a.ID)
		}
		if prev, dup := namesZh[a.Name.Zh]; dup {
			t.Errorf("duplicate Chinese name %q: %s and %s", a.Name.Zh, prev, a.ID)
		}
		namesEn[a.Name.En], namesZh[a.Name.Zh] = a.ID, a.ID
		if a.Seed != vd.SeedFrom(a.ID) {
			t.Errorf("%s: seed not derived from id", a.ID)
		}
		if !a.Featured && !plausible(a.Attrs) {
			t.Errorf("%s: implausible %+v", a.ID, a.Attrs)
		}
		if a.Attrs.Gender == "" || a.Attrs.Age == "" || a.Attrs.Pitch == "" {
			t.Errorf("%s: gender/age/pitch missing (facets need them)", a.ID)
		}
		for _, l := range a.Languages {
			if !slices.Contains(Languages, l) {
				t.Errorf("%s: language %q not in the facet", a.ID, l)
			}
		}
		if line, _ := a.PreviewLine(); line == "" {
			t.Errorf("%s: no preview line", a.ID)
		}
		for _, w := range []string{"  ", " .", "..", "。。"} {
			if strings.Contains(a.Description.En+a.Description.Zh, w) {
				t.Errorf("%s: description has %q", a.ID, w)
			}
		}
		if a.Role != "" {
			parts := strings.TrimPrefix(a.ID, "g-"+a.Role+"-")
			for id := range flavors {
				if strings.HasPrefix(parts, id+"-") {
					usedFlavor[id] = true
				}
			}
		}
		for id, l := range locales {
			if l.accent == a.Attrs.Accent && l.dialect == a.Attrs.Dialect && slices.Equal(l.langs, a.Languages) {
				usedLocale[id] = true
			}
		}
	}
	if featured < 45 || featured > 70 {
		t.Errorf("featured = %d, want about 50", featured)
	}
	for _, c := range Categories() {
		if perCat[c.ID] < 80 {
			t.Errorf("category %s has %d archetypes", c.ID, perCat[c.ID])
		}
	}
	for id := range flavors {
		if !usedFlavor[id] {
			t.Errorf("flavor %q unused", id)
		}
	}
	for id := range locales {
		if !usedLocale[id] {
			t.Errorf("locale %q unused", id)
		}
	}
	// every English accent and Chinese dialect is reachable from the facet
	counts := g.Search(Query{}).Facets[FacetAccent]
	if want := len(vd.ValuesOf(vd.Accent)) + len(vd.ValuesOf(vd.Dialect)); len(counts) != want {
		t.Errorf("accent facet has %d values, want %d", len(counts), want)
	}
}

// TestIDsStable guards favorites and preview caches: archetype ids must
// not change by accident. Update the hash only when data changes on
// purpose (and say so in the commit).
func TestIDsStable(t *testing.T) {
	var ids []string
	for _, a := range Default().All() {
		ids = append(ids, a.ID)
	}
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	if got := hex.EncodeToString(sum[:8]); got != goldenIDs {
		t.Errorf("archetype ids changed: hash %s, want %s (%d ids)", got, goldenIDs, len(ids))
	}
}

const goldenIDs = "651283ca2f7bd2c7"

func TestNewValidates(t *testing.T) {
	good := Default().All()[0]
	bad := func(f func(a *Archetype)) Archetype { a := good; f(&a); return a }
	for name, a := range map[string]Archetype{
		"no id":        bad(func(a *Archetype) { a.ID = "" }),
		"category":     bad(func(a *Archetype) { a.Category = "cooking" }),
		"name zh":      bad(func(a *Archetype) { a.Name.Zh = "" }),
		"conflict":     bad(func(a *Archetype) { a.Attrs.Dialect = "sichuan" }),
		"no languages": bad(func(a *Archetype) { a.Languages = nil }),
		"no local":     bad(func(a *Archetype) { a.Languages = []string{"fr"} }),
		"seed":         bad(func(a *Archetype) { a.Seed = -1 }),
	} {
		if _, err := New([]Archetype{a}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New([]Archetype{good, good}); err == nil {
		t.Error("duplicate id accepted")
	}
}

func TestSearch(t *testing.T) {
	g := Default()
	all := g.Search(Query{Limit: MaxLimit + 100})
	if all.Total != g.Len() || len(all.Items) != MaxLimit || all.Next != MaxLimit {
		t.Errorf("all: total %d items %d next %d", all.Total, len(all.Items), all.Next)
	}
	if !all.Items[0].Featured {
		t.Error("featured not first")
	}
	// facet counts over the whole set add up
	sum := 0
	for _, c := range all.Facets[FacetCategory] {
		sum += c.Count
	}
	if sum != g.Len() {
		t.Errorf("category facet sums to %d", sum)
	}

	p := g.Search(Query{Category: "narration", Gender: "female", Accent: "british"})
	if p.Total == 0 {
		t.Fatal("no British female narrators")
	}
	for _, a := range p.Items {
		if a.Category != "narration" || a.Attrs.Gender != "female" || a.Attrs.Accent != "british" {
			t.Fatalf("filter leak: %+v", a)
		}
	}
	// disjunctive facets: the gender facet still counts men
	var male int
	for _, c := range p.Facets[FacetGender] {
		if c.Value == "male" {
			male = c.Count
		}
	}
	if male == 0 {
		t.Error("gender facet lost the other value")
	}
	if n := g.Search(Query{Category: "narration", Gender: "male", Accent: "british"}).Total; n != male {
		t.Errorf("facet count %d but search gives %d", male, n)
	}

	// paging
	p1 := g.Search(Query{Category: "games", Limit: 10})
	p2 := g.Search(Query{Category: "games", Limit: 10, Offset: p1.Next})
	if p1.Next != 10 || len(p2.Items) != 10 || p1.Items[9].ID == p2.Items[0].ID {
		t.Errorf("paging: next %d", p1.Next)
	}
	if last := g.Search(Query{Category: "games", Offset: p1.Total - 1}); len(last.Items) != 1 || last.Next != -1 {
		t.Errorf("last page: %d items next %d", len(last.Items), last.Next)
	}
	if past := g.Search(Query{Offset: 1 << 20}); len(past.Items) != 0 || past.Items == nil {
		t.Error("offset past the end")
	}

	// text search, English and Chinese, across names, tags and attributes
	for q, wantSome := range map[string]string{
		"british narrator": "accent:british",
		"四川":               "dialect:sichuan",
		"whisper":          "style:whisper",
		"晚星":               "id:f-abendstern",
		"spanish":          "lang:es",
		"zzzz-nothing":     "",
	} {
		r := g.Search(Query{Text: q, Limit: MaxLimit})
		if wantSome == "" {
			if r.Total != 0 {
				t.Errorf("%q: %d results", q, r.Total)
			}
			continue
		}
		if r.Total == 0 {
			t.Errorf("%q: no results", q)
			continue
		}
		kind, val, _ := strings.Cut(wantSome, ":")
		found := false
		for _, a := range r.Items {
			switch kind {
			case "accent":
				found = found || a.Attrs.Accent == val
			case "dialect":
				found = found || a.Attrs.Dialect == val
			case "style":
				found = found || a.Attrs.HasStyle(val)
			case "id":
				found = found || a.ID == val
			case "lang":
				found = found || slices.Contains(a.Languages, val)
			}
		}
		if !found {
			t.Errorf("%q: nothing with %s", q, wantSome)
		}
	}

	if r := g.Search(Query{Featured: true}); r.Total < 45 || r.Total > 70 {
		t.Errorf("featured: %d", r.Total)
	}
	if r := g.Search(Query{Language: "ja"}); r.Total == 0 {
		t.Error("no Japanese voices")
	}
	if r := g.Search(Query{FavoritesOnly: true}); r.Total != 0 {
		t.Error("favorites without a set")
	}
	if len(FacetValues(FacetLanguage)) != len(Languages) {
		t.Error("language facet values")
	}
}

func TestFavorites(t *testing.T) {
	ctx := context.Background()
	store := &MemoryFavorites{}
	f, err := LoadFavorites(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	g := Default()
	id := g.All()[100].ID
	if on, err := f.Toggle(ctx, id); err != nil || !on || !f.Has(id) {
		t.Fatalf("toggle on: %v %v", on, err)
	}
	r := g.Search(Query{FavoritesOnly: true, Favorites: f})
	if r.Total != 1 || r.Items[0].ID != id {
		t.Errorf("favorites search: %+v", r.Total)
	}
	// persisted: a fresh load sees it
	f2, _ := LoadFavorites(ctx, store)
	if !slices.Equal(f2.List(), []string{id}) {
		t.Errorf("reload: %v", f2.List())
	}
	store.Err = errors.New("disk full")
	if err := f.Set(ctx, "x", true); err == nil || f.Has("x") {
		t.Error("store error not reported or set changed")
	}
	if _, err := LoadFavorites(ctx, store); err == nil {
		t.Error("load error not reported")
	}
	store.Err = nil
	if on, _ := f.Toggle(ctx, id); on || f.Has(id) {
		t.Error("toggle off")
	}
	var nilFav *Favorites
	if nilFav.Has(id) {
		t.Error("nil favorites")
	}
	mem, _ := LoadFavorites(ctx, nil)
	if err := mem.Set(ctx, "a", true); err != nil || !mem.Has("a") {
		t.Error("memory-only favorites")
	}
}

func TestPlanPreviews(t *testing.T) {
	g := Default()
	cat := catalog.Builtin()
	omni, _ := cat.ByID("omnivoice-bf16")
	m := Model{ID: omni.ID, Family: omni.Family, Repo: omni.Repo}
	items := g.Search(Query{Limit: 120}).Items

	plan, err := PlanPreviews(items, m, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != len(items) || plan.Cached != 0 {
		t.Fatalf("jobs %d cached %d", len(plan.Jobs), plan.Cached)
	}
	j := plan.Jobs[0]
	a, _ := g.ByID(j.ArchetypeID)
	line, lang := a.PreviewLine()
	if j.Target != vd.TargetOmniVoice || j.Text != line || j.Lang != lang || j.Seed != a.Seed ||
		j.Key != PreviewKey(a.Recipe(), m.ID, line, lang) || j.Voice == "" {
		t.Errorf("job %+v", j)
	}
	// cached keys are skipped; limit caps; order kept
	cached := map[string]bool{plan.Jobs[0].Key: true, plan.Jobs[1].Key: true}
	p2, _ := PlanPreviews(items, m, func(k string) bool { return cached[k] }, 5)
	if p2.Cached != 2 || len(p2.Jobs) != 5 || p2.Jobs[0].ArchetypeID != plan.Jobs[2].ArchetypeID {
		t.Errorf("cached %d jobs %d", p2.Cached, len(p2.Jobs))
	}
	// duplicates planned once
	p3, _ := PlanPreviews([]Archetype{items[0], items[0]}, m, nil, 0)
	if len(p3.Jobs) != 1 {
		t.Errorf("dup: %d", len(p3.Jobs))
	}
	// languages the model doesn't speak
	p4, _ := PlanPreviews(g.Search(Query{Language: "ja", Limit: MaxLimit}).Items, Model{ID: "x", Family: "omnivoice", Languages: []string{"en", "zh"}}, nil, 0)
	if len(p4.Unsupported) == 0 {
		t.Error("no unsupported")
	}
	// a model that can't design
	kokoro, _ := cat.ByID("kokoro-82m-bf16")
	if _, err := PlanPreviews(items, Model{ID: kokoro.ID, Family: kokoro.Family, Repo: kokoro.Repo}, nil, 0); err == nil {
		t.Error("kokoro planned")
	}
	// the key depends on the model and the recipe
	if PreviewKey(a.Recipe(), "a", line, lang) == PreviewKey(a.Recipe(), "b", line, lang) {
		t.Error("model not in key")
	}
	if got := PreviewKey(vd.Recipe{Attrs: vd.Attributes{Gender: "male"}, Seed: 1}, "omnivoice-bf16", "Hello.", "en"); got != goldenPreviewKey {
		t.Errorf("PreviewKey = %s, want %s", got, goldenPreviewKey)
	}
	// job spec
	spec, err := j.Spec(j.Params("/models/omni", "English", "/cache/x.wav"))
	if err != nil || spec.Lane != jobs.LaneGPU || spec.Priority != jobs.PriorityBackground || !spec.Idempotent || spec.Kind != "tts.synthesize" {
		t.Fatalf("spec %+v %v", spec, err)
	}
	var params map[string]any
	_ = json.Unmarshal(spec.Params, &params)
	if params["voice"] != j.Voice || params["seed"].(float64) != float64(j.Seed) || params["text"] != j.Text {
		t.Errorf("params %v", params)
	}

	// CustomVoice and VoiceDesign lowerings work too
	for _, mm := range []Model{
		{ID: "qwen3-cv", Family: "qwen3_tts", Repo: "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit", Speakers: []string{"Vivian", "Ryan"}},
		{ID: "qwen3-vd", Family: "qwen3_tts", Repo: "mlx-community/Qwen3-TTS-12Hz-1.7B-VoiceDesign-4bit"},
	} {
		p, err := PlanPreviews(items[:10], mm, nil, 0)
		if err != nil || len(p.Jobs) != 10 {
			t.Fatalf("%s: %v", mm.ID, err)
		}
		for _, j := range p.Jobs {
			if j.Voice == "" {
				t.Errorf("%s: empty voice", mm.ID)
			}
			if mm.Speakers != nil && !strings.HasPrefix(j.Voice, "Vivian") && !strings.HasPrefix(j.Voice, "Ryan") {
				t.Errorf("speaker outside the build: %q", j.Voice)
			}
		}
	}
}

const goldenPreviewKey = "e3be1f13e298b6c21602b445b2bfcb3ab2a827fc56ea2041dc321efafda3d198"

func TestPresets(t *testing.T) {
	ps := Presets()
	if len(ps) < 12 {
		t.Errorf("%d presets", len(ps))
	}
	seen := map[string]bool{}
	demos := 0
	for _, p := range ps {
		if seen[p.ID] {
			t.Errorf("duplicate %s", p.ID)
		}
		seen[p.ID] = true
		if p.Demo {
			demos++
		}
		if p.Name.En == "" || p.Name.Zh == "" || p.Description.Zh == "" || p.StyleHint.En == "" || p.StyleHint.Zh == "" ||
			p.Script.En == "" || p.Script.Zh == "" || p.Icon == "" || (p.Language != "en" && p.Language != "zh") {
			t.Errorf("%s: incomplete", p.ID)
		}
		if err := p.Attrs.Validate(); err != nil || p.Attrs.IsAuto() {
			t.Errorf("%s: %v", p.ID, err)
		}
		if r := p.Recipe(42); r.Seed != 42 || r.Validate() != nil {
			t.Errorf("%s: recipe", p.ID)
		}
	}
	if demos < 5 {
		t.Errorf("%d demos", demos)
	}
	if _, ok := PresetByID("narrator"); !ok {
		t.Error("PresetByID")
	}
}

func BenchmarkSearch(b *testing.B) {
	g := Default()
	for b.Loop() {
		g.Search(Query{Text: "warm", Gender: "female", Limit: 60})
	}
}
