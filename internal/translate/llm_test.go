package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/llm"
)

// translator answers translate prompts with f(line) per line, and
// reflect/adapt/fit prompts with the given functions (nil: echo).
func translator(f func(l lineJSON) string) func(sys, user string) (string, error) {
	return func(sys, user string) (string, error) {
		var lines []lineJSON
		switch {
		case strings.HasPrefix(sys, "You are the dialogue translator"):
			jsonAfterNoT(user, "Lines to translate:\n", &lines)
		default:
			return "", fmt.Errorf("unexpected prompt %q", sys[:40])
		}
		return linesReply(lines, f), nil
	}
}

func jsonAfterNoT(s, header string, v any) {
	i := strings.Index(s, header)
	if i < 0 {
		return
	}
	raw := llm.ExtractJSON(s[i+len(header):])
	_ = json.Unmarshal(raw, v)
}

func segs(texts ...string) []Segment {
	out := make([]Segment, len(texts))
	for i, t := range texts {
		out[i] = Segment{ID: fmt.Sprint(i + 1), Text: t}
	}
	return out
}

func TestLLMFastKeepsTagsAndOrder(t *testing.T) {
	m := &model{handle: translator(func(l lineJSON) string {
		// A model that moves the marker to the front, as languages do.
		if strings.Contains(l.Text, "⟦1⟧") {
			return "⟦1⟧ ES " + strings.TrimSpace(strings.ReplaceAll(l.Text, "⟦1⟧", ""))
		}
		return "ES " + l.Text
	})}
	tr := &LLM{Router: router(m), BatchLines: 2}
	var prog []Progress
	res, err := tr.Translate(context.Background(), Request{
		Source: "en", Target: "es-MX",
		Segments: segs("Hello there [laughter]", "The Acme rocket launches.", "See you [pause 300ms] tomorrow"),
		Glossary: Glossary{{Source: "Acme", Target: "ACME Corp", Note: "company"}, {Source: "unused", Target: "x"}},
		Style:    "light comedy",
		Progress: func(p Progress) { prog = append(prog, p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"[laughter] ES Hello there", "ES The Acme rocket launches.", "[pause 300ms] ES See you tomorrow"}
	for i, s := range res.Segments {
		if s.ID != fmt.Sprint(i+1) || s.Text != want[i] || s.Err != nil {
			t.Errorf("segment %d = %+v, want %q", i, s, want[i])
		}
	}
	if res.Usage.TotalTokens != 30 || res.Translator != "llm" || res.Failed != 0 {
		t.Fatalf("result %+v", res)
	}
	calls := m.history()
	if len(calls) != 2 {
		t.Fatalf("%d calls", len(calls))
	}
	sys0, user1 := calls[0].System, calls[1].User
	for _, w := range []string{"Spanish (Mexico)", "Mexican Spanish", "ACME Corp (company)", "light comedy", "⟦1⟧"} {
		if !strings.Contains(sys0, w) {
			t.Errorf("system prompt lacks %q", w)
		}
	}
	if strings.Contains(sys0, "unused") {
		t.Error("irrelevant glossary term injected")
	}
	// Second batch sees the earlier lines (with their translations) as context.
	var before []ctxLine
	jsonAfter(t, user1, "Context before (do not translate):\n", &before)
	if len(before) != 2 || before[1].Translation != "ES The Acme rocket launches." {
		t.Fatalf("context before: %+v", before)
	}
	if len(prog) != 2 || prog[1].Done != 3 || prog[1].Total != 3 {
		t.Fatalf("progress %+v", prog)
	}
}

func TestLLMRetriesMissingAndBrokenLines(t *testing.T) {
	n := 0
	m := &model{handle: func(sys, user string) (string, error) {
		n++
		var lines []lineJSON
		jsonAfterNoT(user, "Lines to translate:\n", &lines)
		if len(lines) > 1 {
			// Batch: drop line 2, garble line 3's marker, duplicate nothing.
			var keep []lineJSON
			for _, l := range lines {
				if l.ID != "2" {
					keep = append(keep, l)
				}
			}
			return linesReply(keep, func(l lineJSON) string {
				if l.ID == "3" {
					return "FR sans marqueur"
				}
				return "FR " + l.Text
			}), nil
		}
		// Single-line retries: 2 succeeds, 3 still loses its marker.
		return linesReply(lines, func(l lineJSON) string {
			if l.ID == "3" {
				return "FR toujours sans"
			}
			return "FR " + l.Text
		}), nil
	}}
	tr := &LLM{Router: router(m)}
	res, err := tr.Translate(context.Background(), Request{Target: "fr", Segments: segs("one", "two", "three [sigh]")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Segments[1].Text != "FR two" || res.Segments[1].Err != nil {
		t.Fatalf("line 2 %+v", res.Segments[1])
	}
	s3 := res.Segments[2]
	if s3.Text != "FR toujours sans [sigh]" || len(s3.Warnings) != 1 || s3.Warnings[0] != WarnTagsRepaired {
		t.Fatalf("line 3 %+v", s3)
	}
	if n != 3 {
		t.Fatalf("%d calls", n)
	}
}

func TestLLMLineFailures(t *testing.T) {
	m := &model{handle: func(sys, user string) (string, error) {
		return "", &llm.Error{Code: llm.CodeFiltered}
	}}
	tr := &LLM{Router: router(m)}
	res, err := tr.Translate(context.Background(), Request{Target: "de", Segments: segs("a", "b")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 2 || res.Segments[0].Err == nil || res.Segments[0].Err.Code != CodeLineFailed || res.Segments[0].Err.Params["reason"] != llm.CodeFiltered {
		t.Fatalf("%+v", res.Segments[0])
	}
	if !errors.Is(res.Segments[0].Err, &llm.Error{Code: llm.CodeFiltered}) {
		t.Fatal("cause not wrapped")
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.handle = func(string, string) (string, error) { cancel(); return "", context.Canceled }
	_, err = tr.Translate(ctx, Request{Target: "de", Segments: segs("a")})
	if Code(err) != CodeCancelled || !errors.Is(err, core.ErrCancelled) {
		t.Fatalf("err %v", err)
	}
}

func TestValidation(t *testing.T) {
	tr := &LLM{Router: router(&model{handle: translator(func(l lineJSON) string { return l.Text })})}
	cases := []struct {
		req  Request
		code string
	}{
		{Request{Target: "es"}, CodeNoSegments},
		{Request{Target: "es", Segments: []Segment{{ID: "a"}, {ID: "a"}}}, CodeDuplicateID},
		{Request{Target: "klingon-xx", Segments: segs("x")}, CodeLanguage},
		{Request{Source: "en-US", Target: "en", Segments: segs("x")}, CodeSameLanguage},
	}
	for _, c := range cases {
		if _, err := tr.Translate(context.Background(), c.req); Code(err) != c.code {
			t.Errorf("%+v: got %v want %s", c.req, err, c.code)
		}
	}
	// en-US → en-GB is a real request.
	if _, err := tr.Translate(context.Background(), Request{Source: "en-US", Target: "en-GB", Segments: segs("color")}); err != nil {
		t.Fatal(err)
	}
	// No route.
	none := &LLM{Router: &llm.Router{Registry: llm.NewRegistry(nil, nil)}}
	if _, err := none.Translate(context.Background(), Request{Target: "es", Segments: segs("x")}); Code(err) != CodeNoLLM || !errors.Is(err, core.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestLLMCinematic(t *testing.T) {
	m := &model{}
	m.handle = func(sys, user string) (string, error) {
		var lines []lineJSON
		switch {
		case strings.HasPrefix(sys, "You are the dialogue translator"):
			jsonAfterNoT(user, "Lines to translate:\n", &lines)
			return linesReply(lines, func(l lineJSON) string { return "草稿" + l.Text }), nil
		case strings.HasPrefix(sys, "You review dubbing"):
			jsonAfterNoT(user, "Source lines with drafts:\n", &lines)
			if len(lines) != 2 || lines[1].Draft != "草稿Wow ⟦1⟧" {
				return "", fmt.Errorf("reflect got %+v", lines)
			}
			return mustJSON(map[string]any{"lines": []map[string]string{{"id": "1", "issues": ""}, {"id": "2", "issues": "too literal; keep the ⟦1⟧ marker"}}}), nil
		case strings.HasPrefix(sys, "You are the dialogue adapter"):
			jsonAfterNoT(user, "Lines (source, draft, notes):\n", &lines)
			if len(lines) != 1 || lines[0].ID != "2" || lines[0].Notes == "" || !strings.Contains(lines[0].Draft, "⟦1⟧") {
				return "", fmt.Errorf("adapt got %+v", lines)
			}
			return linesReply(lines, func(l lineJSON) string { return "润色⟦1⟧" }), nil
		}
		return "", fmt.Errorf("unexpected %q", sys[:30])
	}
	tr := &LLM{Router: router(m)}
	res, err := tr.Translate(context.Background(), Request{Source: "en", Target: "zh-Hans", Quality: Cinematic, Segments: segs("Hi", "Wow [gasp]")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Segments[0].Text != "草稿Hi" || res.Segments[0].Draft != "" {
		t.Fatalf("line 1 %+v", res.Segments[0])
	}
	if res.Segments[1].Text != "润色[gasp]" || res.Segments[1].Draft != "草稿Wow [gasp]" {
		t.Fatalf("line 2 %+v", res.Segments[1])
	}
	sys := m.history()[0].System
	if !strings.Contains(sys, "Simplified Chinese") || !strings.Contains(sys, "full-width") {
		t.Fatal("zh-Hans notes missing")
	}
}

func TestLLMAutofit(t *testing.T) {
	m := &model{}
	var fitLines []lineJSON
	m.handle = func(sys, user string) (string, error) {
		var lines []lineJSON
		switch {
		case strings.HasPrefix(sys, "You are the dialogue translator"):
			jsonAfterNoT(user, "Lines to translate:\n", &lines)
			if !strings.Contains(sys, "maxSeconds") {
				return "", fmt.Errorf("no timing rule in autofit prompt")
			}
			return linesReply(lines, func(l lineJSON) string {
				if l.ID == "1" {
					return "Esta es una frase extraordinariamente larga que nunca jamás cabría en tan poco tiempo disponible"
				}
				return "Sí"
			}), nil
		case strings.HasPrefix(sys, "You tighten dubbed dialogue"):
			jsonAfterNoT(user, "Lines to shorten:\n", &lines)
			fitLines = lines
			return linesReply(lines, func(l lineJSON) string { return "Frase corta" }), nil
		}
		return "", fmt.Errorf("unexpected %q", sys[:30])
	}
	tr := &LLM{Router: router(m)}
	req := Request{Source: "en", Target: "es", Quality: Autofit, Segments: []Segment{
		{ID: "1", Text: "This line is short in English.", Slot: 1.5},
		{ID: "2", Text: "Yes", Slot: 1},
		{ID: "3", Text: "No slot"},
	}}
	res, err := tr.Translate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	s1 := res.Segments[0]
	if s1.Text != "Frase corta" || !strings.HasPrefix(s1.Draft, "Esta es") || s1.Fit == nil || s1.Fit.Verdict == FitOverflow {
		t.Fatalf("line 1 %+v fit %+v", s1, s1.Fit)
	}
	if len(fitLines) != 1 || fitLines[0].MaxSeconds != 1.5 || fitLines[0].MaxChars <= 0 || fitLines[0].Source == "" {
		t.Fatalf("fit request %+v", fitLines)
	}
	if res.Segments[1].Fit == nil || res.Segments[1].Fit.Verdict != FitOK || res.Segments[2].Fit != nil {
		t.Fatalf("fits %+v %+v", res.Segments[1].Fit, res.Segments[2].Fit)
	}
	// The first pass carried budgets.
	var first []lineJSON
	jsonAfter(t, m.history()[0].User, "Lines to translate:\n", &first)
	if first[0].MaxSeconds != 1.5 || first[0].MaxChars == 0 || first[2].MaxSeconds != 0 {
		t.Fatalf("budgets %+v", first)
	}

}

func TestLLMSuggestShorter(t *testing.T) {
	m := &model{handle: func(sys, user string) (string, error) {
		var lines []lineJSON
		if strings.HasPrefix(sys, "You tighten") {
			jsonAfterNoT(user, "Lines to shorten:\n", &lines)
			return linesReply(lines, func(lineJSON) string { return "Corto" }), nil
		}
		jsonAfterNoT(user, "Lines to translate:\n", &lines)
		return linesReply(lines, func(lineJSON) string { return "Una traducción bastante más larga que el hueco que tiene" }), nil
	}}
	tr := &LLM{Router: router(m)}
	res, err := tr.Translate(context.Background(), Request{Target: "es", SuggestShorter: true, Segments: []Segment{{ID: "a", Text: "Short", Slot: 0.8}}})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Segments[0]
	if s.Shorter != "Corto" || !strings.HasPrefix(s.Text, "Una") || s.Fit.Verdict != FitOverflow || s.Fit.Over <= 0 {
		t.Fatalf("%+v %+v", s, s.Fit)
	}
}

func TestFitterCheck(t *testing.T) {
	m := &model{handle: func(sys, user string) (string, error) {
		var lines []lineJSON
		jsonAfterNoT(user, "Lines to shorten:\n", &lines)
		return linesReply(lines, func(l lineJSON) string { return "Vamos ⟦1⟧" }), nil
	}}
	f := &Fitter{Router: router(m)}
	res, err := f.Check(context.Background(), FitRequest{Text: "Tenemos que irnos ahora mismo antes de que sea demasiado tarde [sigh]", Lang: "es", Slot: 1, Rewrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Fit.Verdict != FitOverflow || res.Rewrite != "Vamos [sigh]" || res.RewriteFit == nil || res.RewriteFit.Predicted >= res.Fit.Predicted {
		t.Fatalf("%+v", res)
	}
	// Fits: no call.
	n := len(m.history())
	res, _ = f.Check(context.Background(), FitRequest{Text: "Sí", Lang: "es", Slot: 2, Rewrite: true})
	if res.Fit.Verdict != FitOK || len(m.history()) != n {
		t.Fatal(res)
	}
	// Predict-only Fitter.
	if _, err := (&Fitter{}).Check(context.Background(), FitRequest{Text: "palabras y más palabras sin fin", Lang: "es", Slot: 0.3, Rewrite: true}); Code(err) != CodeNoLLM {
		t.Fatal(err)
	}
}

func TestInfoNetworkFlag(t *testing.T) {
	m := &model{local: true, handle: translator(func(l lineJSON) string { return l.Text })}
	tr := &LLM{Router: router(m)}
	if in := tr.Info(); in.Network || in.ThirdParty || !in.Glossary || len(in.Qualities) != 3 {
		t.Fatalf("%+v", in)
	}
	m.local = false
	if in := tr.Info(); !in.Network {
		t.Fatalf("%+v", in)
	}
}
