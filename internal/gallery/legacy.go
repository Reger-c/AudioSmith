// The earlier gallery (thread #94 stopped listing it): hand-made
// "featured" voices (data/featured.json) and use-case roles
// (data/roles.json) that expand combinatorially over gender × age × timbre
// "flavor" × accent/dialect/language "locale", filtered so no combination
// contradicts itself. IDs are derived from those parts, so they stay
// stable as long as the parts keep their ids. They still resolve
// (LegacyByID): dub casts and agents may name them.
package gallery

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

func validate(a Archetype) error {
	switch {
	case a.ID == "":
		return fmt.Errorf("gallery: archetype without id")
	case legacyCategoryIndex(a.Category) < 0:
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
	// Hidden said why the voice was not offered (before thread #94 none
	// of these is): kept as a note.
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

var legacy = sync.OnceValues(func() (*Gallery, error) {
	items, err := BuildLegacy()
	if err != nil {
		return nil, err
	}
	return New(items)
})

// LegacyByID finds a voice of the earlier gallery (built on first use, a
// few milliseconds), hidden ones too.
func LegacyByID(id string) (Archetype, bool) {
	g, err := legacy()
	if err != nil {
		panic("gallery: " + err.Error()) // caught by TestLegacy
	}
	return g.ByID(id)
}

// BuildLegacy expands the earlier gallery's data into archetypes: featured
// first (hidden ones too: they resolve like the rest), then generated ones
// grouped by category in display order.
func BuildLegacy() ([]Archetype, error) {
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
		a := Archetype{
			ID: "f-" + f.ID, Featured: true, Category: f.Cat, Legacy: true,
			Name: f.Name, Description: f.Description, Sample: f.Sample, Local: f.Local,
			Attrs: f.Attrs.Canonical(), Languages: f.Languages, Tags: f.Tags,
		}
		a.Seed = vd.SeedFrom(a.ID)
		a.Gender, a.Age = a.Attrs.Gender, a.Attrs.Age
		out = append(out, a)
	}
	// generated, grouped by category (stable within a category: role order)
	slices.SortStableFunc(roles, func(x, y roleSpec) int { return legacyCategoryIndex(x.Cat) - legacyCategoryIndex(y.Cat) })
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
	if legacyCategoryIndex(r.Cat) < 0 {
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
						ID: id, Category: r.Cat, Role: r.ID, Legacy: true,
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
						Gender:    attrs.Gender,
						Age:       attrs.Age,
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
