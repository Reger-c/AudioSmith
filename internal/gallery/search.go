package gallery

import (
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// Facet is a filter dimension of the gallery.
type Facet string

const (
	FacetCategory Facet = "category"
	FacetGender   Facet = "gender"
	FacetAge      Facet = "age"
	FacetPitch    Facet = "pitch"
	FacetAccent   Facet = "accent" // English accent or Chinese dialect value id
	FacetLanguage Facet = "language"
	FacetStyle    Facet = "style"
)

// Facets lists the facets in display order.
var Facets = []Facet{FacetCategory, FacetGender, FacetAge, FacetPitch, FacetAccent, FacetLanguage, FacetStyle}

// Page sizes.
const (
	DefaultLimit = 60
	MaxLimit     = 500
)

// Query filters and pages the gallery. Empty fields don't filter.
type Query struct {
	Text     string // every word must occur in the name, description, tags or attributes (en or zh)
	Category string
	Gender   string
	Age      string
	Pitch    string
	Accent   string // an Accent or a Dialect value ("british", "sichuan")
	Language string // BCP-47 primary subtag among the recommended languages
	Style    string
	Featured bool // featured archetypes only
	// Favorites restricts to favorites (FavoritesOnly) and is otherwise
	// unused; nil means no favorites.
	Favorites     FavoriteSet
	FavoritesOnly bool
	Offset        int
	Limit         int // 0 = DefaultLimit; capped at MaxLimit
}

// FavoriteSet answers whether an id is a favorite (*Favorites implements it).
type FavoriteSet interface{ Has(id string) bool }

// FacetCount is one value of a facet with the number of results it would
// give (counted with every other filter applied).
type FacetCount struct {
	Value string  `json:"value"`
	Label vd.Text `json:"label"`
	Count int     `json:"count"`
}

// Page is one page of results.
type Page struct {
	Items  []Archetype            `json:"items"`
	Total  int                    `json:"total"`  // matches across all pages
	Offset int                    `json:"offset"` // of Items[0]
	Next   int                    `json:"next"`   // offset of the next page, -1 = none
	Facets map[Facet][]FacetCount `json:"facets"` // values with Count > 0, in display order
}

// fold prepares text for matching: NFKC, lower case, accents kept.
func fold(s string) string {
	return strings.Map(unicode.ToLower, norm.NFKC.String(s))
}

func haystack(a Archetype) string {
	var b strings.Builder
	add := func(s string) { b.WriteString(fold(s)); b.WriteByte('\n') }
	add(a.Name.En)
	add(a.Name.Zh)
	add(a.Description.En)
	add(a.Description.Zh)
	add(a.Role)
	for _, t := range a.Tags {
		add(t)
	}
	if i := categoryIndex(a.Category); i >= 0 {
		add(categories[i].Name.En)
		add(categories[i].Name.Zh)
	}
	add(a.Attrs.Summary("en"))
	add(a.Attrs.Summary("zh"))
	for _, l := range a.Languages {
		add(l)
		add(languageName(l).En)
		add(languageName(l).Zh)
	}
	return b.String()
}

func terms(q string) []string {
	return strings.FieldsFunc(fold(q), func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '，' || r == '、' || r == ';'
	})
}

type matcher struct {
	q     Query
	terms []string
}

// match reports whether item i passes every filter except skip.
func (m *matcher) match(g *Gallery, i int, skip Facet) bool {
	a := &g.items[i]
	q := &m.q
	if q.Featured && !a.Featured {
		return false
	}
	if q.FavoritesOnly && (q.Favorites == nil || !q.Favorites.Has(a.ID)) {
		return false
	}
	if skip != FacetCategory && q.Category != "" && a.Category != q.Category {
		return false
	}
	if skip != FacetGender && q.Gender != "" && a.Attrs.Gender != q.Gender {
		return false
	}
	if skip != FacetAge && q.Age != "" && a.Attrs.Age != q.Age {
		return false
	}
	if skip != FacetPitch && q.Pitch != "" && a.Attrs.Pitch != q.Pitch {
		return false
	}
	if skip != FacetAccent && q.Accent != "" && a.Attrs.Accent != q.Accent && a.Attrs.Dialect != q.Accent {
		return false
	}
	if skip != FacetLanguage && q.Language != "" && !slices.Contains(a.Languages, q.Language) {
		return false
	}
	if skip != FacetStyle && q.Style != "" && !a.Attrs.HasStyle(q.Style) {
		return false
	}
	for _, t := range m.terms {
		if !strings.Contains(g.hay[i], t) {
			return false
		}
	}
	return true
}

// Search filters, counts facets and returns one page. Results keep the
// gallery's order: featured first, then by category.
func (g *Gallery) Search(q Query) Page {
	m := &matcher{q: q, terms: terms(q.Text)}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	offset := max(q.Offset, 0)

	var hits []int
	for i := range g.items {
		if m.match(g, i, "") {
			hits = append(hits, i)
		}
	}
	p := Page{Total: len(hits), Offset: offset, Next: -1, Items: []Archetype{}}
	if offset < len(hits) {
		end := min(offset+limit, len(hits))
		for _, i := range hits[offset:end] {
			p.Items = append(p.Items, g.items[i])
		}
		if end < len(hits) {
			p.Next = end
		}
	}
	p.Facets = g.facetCounts(m)
	return p
}

// facetCounts counts, for each facet, the results each of its values would
// give with all other filters kept (so picking a value never empties the
// list unexpectedly).
func (g *Gallery) facetCounts(m *matcher) map[Facet][]FacetCount {
	out := map[Facet][]FacetCount{}
	for _, f := range Facets {
		counts := map[string]int{}
		for i := range g.items {
			if !m.match(g, i, f) {
				continue
			}
			a := &g.items[i]
			switch f {
			case FacetCategory:
				counts[a.Category]++
			case FacetGender:
				counts[a.Attrs.Gender]++
			case FacetAge:
				counts[a.Attrs.Age]++
			case FacetPitch:
				counts[a.Attrs.Pitch]++
			case FacetAccent:
				counts[a.Attrs.Accent]++
				counts[a.Attrs.Dialect]++
			case FacetLanguage:
				for _, l := range a.Languages {
					counts[l]++
				}
			case FacetStyle:
				for _, s := range a.Attrs.Styles {
					counts[s]++
				}
			}
		}
		delete(counts, "")
		var list []FacetCount
		for _, v := range facetValues(f) {
			if n := counts[v.Value]; n > 0 {
				v.Count = n
				list = append(list, v)
			}
		}
		out[f] = list
	}
	return out
}

// facetValues lists every value of a facet in display order, with labels.
func facetValues(f Facet) []FacetCount {
	var out []FacetCount
	fromCat := func(c vd.Category) {
		for _, op := range vd.ValuesOf(c) {
			out = append(out, FacetCount{Value: op.ID, Label: op.Label})
		}
	}
	switch f {
	case FacetCategory:
		for _, c := range categories {
			out = append(out, FacetCount{Value: c.ID, Label: c.Name})
		}
	case FacetGender:
		fromCat(vd.Gender)
	case FacetAge:
		fromCat(vd.Age)
	case FacetPitch:
		fromCat(vd.Pitch)
	case FacetAccent:
		fromCat(vd.Accent)
		fromCat(vd.Dialect)
	case FacetLanguage:
		for _, l := range Languages {
			out = append(out, FacetCount{Value: l, Label: languageName(l)})
		}
	case FacetStyle:
		fromCat(vd.Style)
	}
	return out
}

// FacetValues lists every value of a facet in display order (Count = 0),
// for building the filter UI before any search.
func FacetValues(f Facet) []FacetCount { return facetValues(f) }

var languageNames = map[string]vd.Text{
	"en": {En: "English", Zh: "英语"}, "zh": {En: "Chinese", Zh: "中文"}, "es": {En: "Spanish", Zh: "西班牙语"},
	"fr": {En: "French", Zh: "法语"}, "de": {En: "German", Zh: "德语"}, "it": {En: "Italian", Zh: "意大利语"},
	"pt": {En: "Portuguese", Zh: "葡萄牙语"}, "ru": {En: "Russian", Zh: "俄语"}, "hi": {En: "Hindi", Zh: "印地语"},
	"ja": {En: "Japanese", Zh: "日语"}, "ko": {En: "Korean", Zh: "韩语"},
}

func languageName(code string) vd.Text {
	if t, ok := languageNames[code]; ok {
		return t
	}
	return vd.Text{En: code, Zh: code}
}
