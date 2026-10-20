// Package gallery is the data side of the Voice Gallery: about 1,100
// ready-made designed voices ("archetypes", no real people) grouped by use
// case, with facets, search, paging, favorites, a preview planner, the
// Design page's starting-point presets, and (in package persona) the
// .asvoice voice-file format.
//
// The archetypes come from two embedded, hand-written data files:
// data/featured.json (hand-made voices with their own names and lines)
// and data/roles.json (use-case roles that expand combinatorially over
// gender × age × timbre "flavor" × accent/dialect/language "locale",
// filtered so no combination contradicts itself). IDs are derived from
// those parts, so they stay stable as long as the parts keep their ids.
package gallery

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// Archetype is one ready-made designed voice.
type Archetype struct {
	ID          string        `json:"id"`
	Featured    bool          `json:"featured,omitempty"`
	Category    string        `json:"category"`
	Role        string        `json:"role,omitempty"` // generated archetypes: the role id
	Name        vd.Text       `json:"name"`
	Description vd.Text       `json:"description"`
	Sample      vd.Text       `json:"sample"`          // preview line in English and Chinese
	Local       *LocalLine    `json:"local,omitempty"` // preview line for a language other than en/zh
	Attrs       vd.Attributes `json:"attrs"`
	Languages   []string      `json:"languages"` // recommended, BCP-47; the first is the preview language
	Tags        []string      `json:"tags,omitempty"`
	Seed        int64         `json:"seed"`
}

// LocalLine is a sample line in another language.
type LocalLine struct {
	Lang string `json:"lang"`
	Text string `json:"text"`
}

// Recipe is the archetype as a design recipe ("Use voice" saves it as a
// designed voice profile; nothing is rendered).
func (a Archetype) Recipe() vd.Recipe {
	return vd.Recipe{Attrs: a.Attrs, Seed: a.Seed}
}

// PreviewLine is the text and language a preview of the archetype speaks:
// its first recommended language, with the matching line.
func (a Archetype) PreviewLine() (text, lang string) {
	lang = "en"
	if len(a.Languages) > 0 {
		lang = a.Languages[0]
	}
	switch {
	case lang == "zh":
		return a.Sample.Zh, "zh"
	case lang == "en":
		return a.Sample.En, "en"
	case a.Local != nil && a.Local.Lang == lang:
		return a.Local.Text, lang
	}
	return a.Sample.En, "en"
}

// Gallery is an immutable, searchable set of archetypes. It is safe for
// concurrent use.
type Gallery struct {
	items []Archetype
	byID  map[string]int
	hay   []string // folded search text per item
}

//go:embed data/*.json
var dataFS embed.FS

var builtin = sync.OnceValues(func() (*Gallery, error) {
	items, err := Build()
	if err != nil {
		return nil, err
	}
	return New(items)
})

// Default returns the built-in gallery (built once, on first use; a few
// milliseconds).
func Default() *Gallery {
	g, err := builtin()
	if err != nil {
		panic("gallery: " + err.Error()) // caught by TestDefault
	}
	return g
}

// New indexes archetypes after validating them: unique ids, known
// categories, valid non-contradictory attributes, both name languages, a
// preview line for the preview language. Order is kept.
func New(items []Archetype) (*Gallery, error) {
	g := &Gallery{items: slices.Clone(items), byID: make(map[string]int, len(items)), hay: make([]string, len(items))}
	for i, a := range g.items {
		if err := validate(a); err != nil {
			return nil, err
		}
		if _, dup := g.byID[a.ID]; dup {
			return nil, fmt.Errorf("gallery: duplicate id %q", a.ID)
		}
		g.byID[a.ID] = i
		g.hay[i] = haystack(a)
	}
	return g, nil
}

func validate(a Archetype) error {
	switch {
	case a.ID == "":
		return fmt.Errorf("gallery: archetype without id")
	case categoryIndex(a.Category) < 0:
		return fmt.Errorf("gallery: %s: unknown category %q", a.ID, a.Category)
	case a.Name.En == "" || a.Name.Zh == "" || a.Description.En == "" || a.Description.Zh == "":
		return fmt.Errorf("gallery: %s: name and description need en and zh", a.ID)
	case a.Sample.En == "" || a.Sample.Zh == "":
		return fmt.Errorf("gallery: %s: sample needs en and zh", a.ID)
	case len(a.Languages) == 0:
		return fmt.Errorf("gallery: %s: no languages", a.ID)
	case a.Seed < 0 || a.Seed > vd.MaxSeed:
		return fmt.Errorf("gallery: %s: seed out of range", a.ID)
	}
	if err := a.Attrs.Validate(); err != nil {
		return fmt.Errorf("gallery: %s: %w", a.ID, err)
	}
	if !slices.Equal(a.Attrs.Canonical().Styles, a.Attrs.Styles) {
		return fmt.Errorf("gallery: %s: styles not canonical", a.ID)
	}
	if l := a.Languages[0]; l != "en" && l != "zh" && (a.Local == nil || a.Local.Lang != l || a.Local.Text == "") {
		return fmt.Errorf("gallery: %s: no sample line in %s", a.ID, l)
	}
	return nil
}

// Len is the number of archetypes.
func (g *Gallery) Len() int { return len(g.items) }

// All returns the archetypes in display order (a copy).
func (g *Gallery) All() []Archetype { return slices.Clone(g.items) }

// ByID finds an archetype.
func (g *Gallery) ByID(id string) (Archetype, bool) {
	i, ok := g.byID[id]
	if !ok {
		return Archetype{}, false
	}
	return g.items[i], true
}

// ---- building the built-in data ----

type roleSpec struct {
	ID      string        `json:"id"`
	Cat     string        `json:"cat"`
	Name    vd.Text       `json:"name"`
	Blurb   vd.Text       `json:"blurb"`
	Sample  vd.Text       `json:"sample"`
	Genders []string      `json:"genders"`
	Ages    []string      `json:"ages"`
	Flavors []string      `json:"flavors"`
	Locales []string      `json:"locales"`
	Base    vd.Attributes `json:"base"`
	Tags    []string      `json:"tags"`
}

type featuredSpec struct {
	ID          string        `json:"id"`
	Cat         string        `json:"cat"`
	Name        vd.Text       `json:"name"`
	Description vd.Text       `json:"description"`
	Sample      vd.Text       `json:"sample"`
	Attrs       vd.Attributes `json:"attrs"`
	Languages   []string      `json:"languages"`
	Local       *LocalLine    `json:"local"`
	Tags        []string      `json:"tags"`
	// Hidden says why the voice is not offered (it stays in the data, so
	// voices saved from it keep their origin).
	Hidden string `json:"hidden"`
}

func readJSON(name string, v any) error {
	b, err := dataFS.ReadFile(name)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("gallery: %s: %w", name, err)
	}
	return nil
}

// Build expands the embedded data into archetypes: featured first, then
// generated ones grouped by category in display order.
func Build() ([]Archetype, error) {
	var feats []featuredSpec
	var roles []roleSpec
	if err := readJSON("data/featured.json", &feats); err != nil {
		return nil, err
	}
	if err := readJSON("data/roles.json", &roles); err != nil {
		return nil, err
	}
	var out []Archetype
	for _, f := range feats {
		if f.Hidden != "" {
			continue
		}
		a := Archetype{
			ID: "f-" + f.ID, Featured: true, Category: f.Cat,
			Name: f.Name, Description: f.Description, Sample: f.Sample, Local: f.Local,
			Attrs: f.Attrs.Canonical(), Languages: f.Languages, Tags: f.Tags,
		}
		a.Seed = vd.SeedFrom(a.ID)
		out = append(out, a)
	}
	// generated, grouped by category (stable within a category: role order)
	slices.SortStableFunc(roles, func(x, y roleSpec) int { return categoryIndex(x.Cat) - categoryIndex(y.Cat) })
	for _, r := range roles {
		gen, err := expand(r)
		if err != nil {
			return nil, err
		}
		out = append(out, gen...)
	}
	return out, nil
}

var ageCode = map[string]string{"child": "c", "teen": "t", "young_adult": "y", "middle_aged": "m", "elderly": "e"}

func expand(r roleSpec) ([]Archetype, error) {
	if categoryIndex(r.Cat) < 0 {
		return nil, fmt.Errorf("gallery: role %s: unknown category %q", r.ID, r.Cat)
	}
	genders := r.Genders
	if len(genders) == 0 {
		genders = []string{"female", "male"}
	}
	var out []Archetype
	for _, locID := range r.Locales {
		loc, ok := locales[locID]
		if !ok {
			return nil, fmt.Errorf("gallery: role %s: unknown locale %q", r.ID, locID)
		}
		for _, flID := range r.Flavors {
			fl, ok := flavors[flID]
			if !ok {
				return nil, fmt.Errorf("gallery: role %s: unknown flavor %q", r.ID, flID)
			}
			for _, age := range r.Ages {
				for _, g := range genders {
					attrs, ok := combine(r.Base, fl, loc, g, age)
					if !ok {
						continue // contradictory or implausible: skip
					}
					id := fmt.Sprintf("g-%s-%s-%s-%s%s", r.ID, flID, locID, g[:1], ageCode[age])
					p := person(g, age)
					a := Archetype{
						ID: id, Category: r.Cat, Role: r.ID,
						Name: vd.Text{
							En: fl.name.En + " " + r.Name.En + " · " + loc.name.En + " " + p.En,
							Zh: fl.name.Zh + r.Name.Zh + " · " + loc.name.Zh + p.Zh,
						},
						Description: vd.Text{
							En: r.Blurb.En + ". " + vd.Describe(attrs, "en"),
							Zh: r.Blurb.Zh + "。" + vd.Describe(attrs, "zh"),
						},
						Sample:    r.Sample,
						Attrs:     attrs,
						Languages: loc.langs,
						Tags:      append(slices.Clone(r.Tags), flID),
						Seed:      vd.SeedFrom(id),
					}
					if loc.sample != "" {
						a.Local = &LocalLine{Lang: loc.langs[0], Text: loc.sample}
					}
					out = append(out, a)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("gallery: role %s expands to nothing", r.ID)
	}
	return out, nil
}

// combine builds a generated archetype's attributes; false when the parts
// contradict each other or the result is implausible for a ready-made
// voice (an adult man at a very high pitch, a gravelly child…).
func combine(base vd.Attributes, fl flavor, loc locale, gender, age string) (vd.Attributes, bool) {
	a := base.Canonical()
	a.Gender, a.Age = gender, age
	a.Accent, a.Dialect = loc.accent, loc.dialect
	if fl.pitch != "" {
		if a.Pitch != "" && a.Pitch != fl.pitch {
			return a, false
		}
		a.Pitch = fl.pitch
	}
	a.Styles = append(a.Styles, fl.styles...)
	a = a.Canonical()
	if len(a.Styles) > vd.MaxStyles || len(a.Conflicts()) > 0 {
		return a, false
	}
	// default pitch so the pitch facet is meaningful
	if a.Pitch == "" {
		switch {
		case age == "child":
			a.Pitch = "high"
		case age == "teen" && gender == "female":
			a.Pitch = "high"
		case age == "elderly" && gender == "male":
			a.Pitch = "low"
		default:
			a.Pitch = "moderate"
		}
	}
	if !plausible(a) {
		return a, false
	}
	return a, len(a.Conflicts()) == 0
}

func plausible(a vd.Attributes) bool {
	young := a.Age == "child" || a.Age == "teen"
	switch {
	case !young && a.Gender == "male" && a.Pitch == "very_high":
		return false
	case !young && a.Gender == "female" && a.Pitch == "very_low":
		return false
	case young && (a.Pitch == "low" || a.Pitch == "very_low"):
		return false
	case young && (a.HasStyle("raspy") || a.HasStyle("magnetic") || a.HasStyle("resonant") || a.HasStyle("professional")):
		return false
	case a.Age == "elderly" && a.HasStyle("sweet"):
		return false
	}
	return true
}
