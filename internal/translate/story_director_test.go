package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/llm"
)

func kittenScript() StoryScript {
	return StoryScript{Title: "我想领养一只小猫", Cast: []StoryRole{{Name: "孩子", Voice: "明亮的年轻女声"}, {Name: "妈妈", Persona: "温柔的妈妈"}},
		Lines: []StoryScriptLine{
			{Speaker: "孩子", Text: "妈妈，我想领养一只小猫，可以吗？"},
			{Speaker: "妈妈", Text: "领养是好事，可是养猫要花很多时间照顾它。你能一直照顾它吗？"},
			{Speaker: "孩子", Text: "我能！我每天喂它，给它换水，铲猫砂。", Direction: "急切保证"},
			{Text: "妈妈笑了。"},
		}}
}

func TestStoryDirector(t *testing.T) {
	m := &model{handle: func(sys, user string) (string, error) {
		if !strings.Contains(sys, "radio drama") || !strings.Contains(sys, "Chinese") {
			t.Errorf("system prompt: %s", sys)
		}
		for _, want := range []string{"1. 孩子: 妈妈，我想领养", "3. 孩子 (急切保证): 我能！", "4. Narrator: 妈妈笑了。", "- 妈妈: 温柔的妈妈", "- 孩子 (voice: 明亮的年轻女声)\n"} {
			if !strings.Contains(user, want) {
				t.Errorf("user prompt lacks %q:\n%s", want, user)
			}
		}
		return `{"scene":"傍晚的家里，孩子想领养小猫，妈妈犹豫","cast":[{"name":"孩子","persona":"七岁女孩，活泼爱撒娇"},{"name":"妈妈","persona":""},{"name":"路人","persona":"多余"}],
		"lines":[{"n":1,"direction":"（撒娇央求）","intensity":2},{"n":2,"direction":"犹豫但温和","intensity":1},{"n":3,"direction":"急切保证","intensity":3},{"n":4,"direction":"none","intensity":2},{"n":9,"direction":"越界","intensity":2},{"n":1,"direction":"重复","intensity":2}]}`, nil
	}}
	d := &StoryDirector{Router: router(m)}
	got, err := d.Direct(context.Background(), kittenScript())
	if err != nil {
		t.Fatal(err)
	}
	if got.Scene != "傍晚的家里，孩子想领养小猫，妈妈犹豫" {
		t.Errorf("scene %q", got.Scene)
	}
	// Known characters only, a persona the model left empty kept.
	if fmt.Sprint(got.Cast) != "[{孩子 七岁女孩，活泼爱撒娇 明亮的年轻女声} {妈妈 温柔的妈妈 }]" {
		t.Errorf("cast %v", got.Cast)
	}
	want := "[{1 撒娇央求 2} {2 犹豫但温和 1} {3 急切保证 3} {4  0}]"
	if fmt.Sprint(got.Lines) != want {
		t.Errorf("lines %v, want %s", got.Lines, want)
	}
}

func TestStoryDirectorSchemaAndErrors(t *testing.T) {
	// The schema: an object with scene, cast and lines (n, direction, intensity 1–3).
	raw, err := json.Marshal(StorySchema())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"required":["scene","cast","lines"]`, `"intensity":{"maximum":3,"minimum":1,"type":"integer"}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("schema lacks %s: %s", want, raw)
		}
	}
	if errs := StorySchema().Validate(json.RawMessage(`{"scene":"x","cast":[],"lines":[{"n":1,"direction":"y","intensity":5}]}`)); len(errs) == 0 {
		t.Error("intensity 5 passes the schema")
	}

	// No model set up.
	if _, err := (&StoryDirector{}).Direct(context.Background(), kittenScript()); Code(err) != CodeNoLLM {
		t.Errorf("no router: %v", err)
	}
	// Nothing to direct.
	m := &model{handle: func(string, string) (string, error) { return `{}`, nil }}
	if _, err := (&StoryDirector{Router: router(m)}).Direct(context.Background(), StoryScript{}); Code(err) != CodeNoSegments {
		t.Errorf("no lines: %v", err)
	}
	// A model that never answers in the schema.
	m = &model{handle: func(string, string) (string, error) { return `I think the child is excited.`, nil }}
	if _, err := (&StoryDirector{Router: router(m)}).Direct(context.Background(), kittenScript()); Code(err) != llm.CodeInvalidJSON {
		t.Errorf("prose: %v", err)
	}
	// The model's own failure comes through.
	m = &model{handle: func(string, string) (string, error) { return "", &llm.Error{Code: llm.CodeTimeout} }}
	if _, err := (&StoryDirector{Router: router(m)}).Direct(context.Background(), kittenScript()); Code(err) != llm.CodeTimeout {
		t.Errorf("timeout: %v", err)
	}
}

func TestStoryDirectorBatches(t *testing.T) {
	s := StoryScript{}
	for i := range 90 {
		s.Lines = append(s.Lines, StoryScriptLine{Speaker: []string{"Ann", "Bob"}[i%2], Text: fmt.Sprintf("Line number %d.", i+1)})
	}
	var ranges []string
	m := &model{handle: func(sys, user string) (string, error) {
		var first, last int
		if i := strings.Index(user, "Direct lines "); i >= 0 {
			fmt.Sscanf(user[i:], "Direct lines %d to %d", &first, &last)
		}
		ranges = append(ranges, fmt.Sprintf("%d-%d", first, last))
		if first > 1 && !strings.Contains(user, "The lines just before") {
			t.Errorf("batch %d has no context", first)
		}
		var lines []string
		for n := first; n <= last; n++ {
			lines = append(lines, fmt.Sprintf(`{"n":%d,"direction":"calm","intensity":2}`, n))
		}
		return `{"scene":"A kitchen","cast":[{"name":"Ann","persona":"tired"}],"lines":[` + strings.Join(lines, ",") + `]}`, nil
	}}
	got, err := (&StoryDirector{Router: router(m)}).Direct(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ranges, " ") != "1-40 41-80 81-90" || len(got.Lines) != 90 || got.Scene != "A kitchen" {
		t.Errorf("batches %v, %d lines, scene %q", ranges, len(got.Lines), got.Scene)
	}
	if fmt.Sprint(got.Cast) != "[{Ann tired } {Bob  }]" {
		t.Errorf("cast %v", got.Cast)
	}
}
