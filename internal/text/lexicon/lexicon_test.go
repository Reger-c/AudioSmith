package lexicon

import (
	"errors"
	"slices"
	"strconv"
	"testing"
)

func resp(term, rep, scope string) Entry {
	return Entry{Term: term, Replacement: rep, Type: Respelling, Scope: scope, Enabled: true}
}

func TestApply(t *testing.T) {
	entries := []Entry{
		resp("GIF", "jif", Global),
		resp("New York", "noo york", Global),
		resp("New", "nyoo", Global),
		resp("重庆", "崇庆", "zh"),
		resp("行长", "杭长", "zh"),
		resp("Straße", "Strasse", "de"),
		resp("ΟΔΟΣ", "odos", Global),
		resp("cat", "kat", Global),
		resp("ไทย", "ไท", "th"),
		resp("C++", "see plus plus", Global),
		resp("off", "OFF", Global),
		{Term: "tomato", Replacement: "təˈmɑːtoʊ", Type: IPA, Scope: Global, Enabled: true},
		{Term: "data", Replacement: "D EY1 T AH0", Type: CMU, Scope: "en", Enabled: true},
		{Term: "disabled", Replacement: "nope", Scope: Global, Enabled: false},
	}
	cases := []struct {
		name, lang, in, want string
		terms                []string
	}{
		{"case-insensitive", "en", "A gif, a Gif and a GIF.", "A jif, a jif and a jif.", []string{"gif", "Gif", "GIF"}},
		{"longest match", "en", "New York is new.", "noo york is nyoo.", []string{"New York", "new"}},
		{"word boundary", "en", "concatenate the cat, cats", "concatenate the kat, cats", []string{"cat"}},
		{"symbols in term", "en", "I like C++ a lot", "I like see plus plus a lot", []string{"C++"}},
		{"cjk inside sentence", "zh-Hans", "我在重庆工作，行长说好", "我在崇庆工作，杭长说好", []string{"重庆", "行长"}},
		{"cjk entries scoped", "ja", "重庆", "重庆", nil},
		{"mixed scripts", "zh", "用GIF图在重庆", "用jif图在崇庆", []string{"GIF", "重庆"}},
		{"german sharp s", "de-AT", "Die STRASSE und die straße", "Die STRASSE und die Strasse", []string{"straße"}},
		{"greek case fold", "el", "οδος", "odos", []string{"οδος"}},
		{"thai no spaces", "th", "ภาษาไทยดี", "ภาษาไทดี", []string{"ไทย"}},
		{"thai not before a mark", "th", "ไทยั", "ไทยั", nil},
		{"tags kept", "en", "[cat] cat [voice:cat]", "[cat] kat [voice:cat]", []string{"cat"}},
		{"inline override wins", "en", "Say [[GIF|ghif]] not GIF", "Say ghif not jif", []string{"GIF", "GIF"}},
		{"bad override kept", "en", "x [[GIF]] y", "x [[GIF]] y", nil},
		{"disabled", "en", "disabled", "disabled", nil},
		{"annotations keep text", "en-US", "tomato data", "tomato data", []string{"tomato", "data"}},
		{"cmu scoped to en", "fr", "data", "data", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Compile(entries, c.lang).Apply(c.in)
			if r.Text != c.want {
				t.Errorf("got %q, want %q", r.Text, c.want)
			}
			var terms []string
			for _, m := range r.Matches {
				terms = append(terms, m.Term)
				if m.Src.Of(c.in) != m.Term && !m.Inline {
					t.Errorf("src span %q != term %q", m.Src.Of(c.in), m.Term)
				}
				if m.Out.Of(r.Text) != map[bool]string{true: m.Entry.Replacement, false: m.Term}[m.Entry.typ() == Respelling] {
					t.Errorf("out span %q wrong for %+v", m.Out.Of(r.Text), m)
				}
			}
			if !slices.Equal(terms, c.terms) {
				t.Errorf("matched %q, want %q", terms, c.terms)
			}
		})
	}
}

func TestAnnotations(t *testing.T) {
	d := Compile([]Entry{
		{Term: "tomato", Replacement: "/təˈmɑːtoʊ/", Type: IPA, Scope: Global, Enabled: true},
		resp("GIF", "jif", Global),
	}, "en")
	r := d.Apply("GIF tomato")
	as := r.Annotations()
	if len(as) != 1 || as[0].Entry.Type != IPA || as[0].Out.Of(r.Text) != "tomato" || r.Text != "jif tomato" {
		t.Errorf("annotations %+v in %q", as, r.Text)
	}
	if got := (*Dictionary)(nil).Apply("a [[b|c]]").Text; got != "a c" {
		t.Errorf("nil dictionary: %q", got)
	}
}

func TestScopePrecedence(t *testing.T) {
	entries := []Entry{resp("Paris", "pa-REE", "fr"), resp("Paris", "PAIR-iss", Global), resp("Paris", "PA-ris", "en-GB")}
	for lang, want := range map[string]string{"fr-CA": "pa-REE", "en-US": "PAIR-iss", "en-GB": "PA-ris", "de": "PAIR-iss"} {
		if got := Compile(entries, lang).Apply("Paris").Text; got != want {
			t.Errorf("%s: %q, want %q", lang, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	entries := []Entry{
		resp("GIF", "jif", Global),
		resp("gif", "ghif", Global), // duplicate (case-insensitive)
		resp("gif", "jif", "en"),    // other scope: fine
		resp("", "x", Global),       // empty term
		resp("x", " ", Global),      // empty replacement
		{Term: "a", Replacement: "AH1 B", Type: CMU, Enabled: true},
		{Term: "b", Replacement: "AH9", Type: CMU, Enabled: true}, // bad stress
		{Term: "c", Replacement: "B1", Type: CMU, Enabled: true},  // consonant with stress
		{Term: "d", Replacement: "/dʒɪf/", Type: IPA, Enabled: true},
		{Term: "e", Replacement: "JIF", Type: IPA, Enabled: true}, // upper case is not IPA
		{Term: "f", Replacement: "x", Type: "klingon", Enabled: true},
		{Term: "GIF", Replacement: "x", Enabled: false},            // disabled duplicate: fine
		{Term: "g", Replacement: "ɕi˥˩", Type: IPA, Enabled: true}, // tone letters
	}
	var got []string
	for _, is := range Validate(entries) {
		got = append(got, strconv.Itoa(is.Index)+":"+is.Code)
	}
	want := []string{"1:" + CodeDuplicate, "3:" + CodeEmptyTerm, "4:" + CodeEmptyReplacement, "6:" + CodeBadCMU,
		"7:" + CodeBadCMU, "9:" + CodeBadIPA, "10:" + CodeBadType}
	if !slices.Equal(got, want) {
		t.Errorf("issues\n got  %q\n want %q", got, want)
	}
}

func TestImportExport(t *testing.T) {
	existing := []Entry{resp("GIF", "jif", Global), resp("SQL", "sequel", Global)}
	data, err := Export(existing)
	if err != nil {
		t.Fatal(err)
	}
	back, rep, err := Import(data, nil, Replace)
	if err != nil || !slices.Equal(back, existing) || rep.Added != 2 {
		t.Fatalf("round trip: %v %+v %+v", err, back, rep)
	}

	// Merge: update in place, add new, skip identical and term-less.
	in := `{"entries":[{"term":"gif","replacement":"ghif"},{"term":"SQL","replacement":"sequel","type":"respelling","scope":"global","enabled":true},{"term":"重庆","replacement":"崇庆","scope":"zh","enabled":false},{"replacement":"x"}]}`
	got, rep, err := Import([]byte(in), existing, Merge)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{resp("gif", "ghif", Global), resp("SQL", "sequel", Global), {Term: "重庆", Replacement: "崇庆", Type: Respelling, Scope: "zh"}}
	if !slices.Equal(got, want) || rep != (ImportReport{Added: 1, Updated: 1, Skipped: 2}) {
		t.Errorf("merge:\n got  %+v %+v\n want %+v", got, rep, want)
	}

	// Bare array with aliases, and a plain map.
	got, _, err = Import([]byte(`[{"word":"Nguyen","say":"win","language":"en"}]`), nil, Merge)
	if err != nil || len(got) != 1 || got[0] != resp("Nguyen", "win", "en") {
		t.Errorf("array: %v %+v", err, got)
	}
	got, _, err = Import([]byte("\xef\xbb\xbf"+`{"b":"bee","a":"ay"}`), existing, Replace)
	if err != nil || !slices.Equal(got, []Entry{resp("a", "ay", Global), resp("b", "bee", Global)}) {
		t.Errorf("map: %v %+v", err, got)
	}
	for _, bad := range []string{"", "42", `{"entries": 5}`, `{"a": 1}`, "[1]"} {
		if _, _, err := Import([]byte(bad), existing, Merge); !errors.Is(err, ErrFormat) {
			t.Errorf("Import(%q) err = %v", bad, err)
		}
	}
}
