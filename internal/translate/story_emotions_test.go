package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var testVocab = []EmotionWord{{"steady", "平稳"}, {"pleading", "央求"}, {"worried", "担心"}, {"excited", "兴奋"}, {"tender", "温柔"}}

func TestDirectEmotions(t *testing.T) {
	s := kittenScript()
	s.Lines[2].Emotion, s.Lines[2].Intensity, s.Lines[2].Fixed = "excited", 3, true
	m := &model{handle: func(sys, user string) (string, error) {
		for _, want := range []string{"emotional curve", "- pleading (央求)", "Most lines are steady", "Chinese"} {
			if !strings.Contains(sys, want) {
				t.Errorf("system prompt lacks %q", want)
			}
		}
		for _, want := range []string{"1. 孩子: 妈妈，我想领养", "3. 孩子 [fixed: excited 3]: 我能！", "4. Narrator: 妈妈笑了。"} {
			if !strings.Contains(user, want) {
				t.Errorf("user prompt lacks %q:\n%s", want, user)
			}
		}
		return `{"scene":"傍晚的家里，孩子想领养小猫","cast":[{"name":"孩子","baseline":"excited"},{"name":"妈妈","baseline":"（tender）"},{"name":"路人","baseline":"sad"}],
		"lines":[{"n":1,"emotion":"pleading","intensity":2,"turn":false,"note":"撒娇央求，期待妈妈答应"},{"n":2,"emotion":"worried","intensity":5,"turn":false,"note":"担心照顾不过来"},
		{"n":3,"emotion":"steady","intensity":0,"turn":true,"note":"急切保证"},{"n":4,"emotion":"steady","intensity":0,"turn":false,"note":""},{"n":7,"emotion":"sad","intensity":1,"turn":false,"note":""}]}`, nil
	}}
	got, err := (&StoryDirector{Router: router(m)}).DirectEmotions(context.Background(), s, testVocab)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scene != "傍晚的家里，孩子想领养小猫" || fmt.Sprint(got.Baselines) != "[{孩子 excited} {妈妈 tender}]" {
		t.Errorf("scene %q cast %v", got.Scene, got.Baselines)
	}
	// Intensities clamped, the user's line kept, notes cut, lines beyond the script dropped.
	want := "[{1  pleading 2 false 撒娇央求，期待妈妈答应} {2  worried 3 false 担心照顾不过来} {3  excited 3 true 急切保证} {4  steady 0 false }]"
	if fmt.Sprint(got.Lines) != want {
		t.Errorf("lines %v\nwant  %s", got.Lines, want)
	}
	raw, _ := json.Marshal(EmotionSchema())
	if !strings.Contains(string(raw), `"intensity":{"type":"integer"}`) || !strings.Contains(string(raw), `"turn":{"type":"boolean"}`) {
		t.Errorf("schema %s", raw)
	}
	if _, err := (&StoryDirector{}).DirectEmotions(context.Background(), s, testVocab); Code(err) != CodeNoLLM {
		t.Errorf("no router: %v", err)
	}
	if _, err := (&StoryDirector{Router: router(m)}).DirectEmotions(context.Background(), StoryScript{}, testVocab); Code(err) != CodeNoSegments {
		t.Errorf("no lines: %v", err)
	}
}

func TestDirectEmotionsEnglishAndBatches(t *testing.T) {
	s := StoryScript{}
	for i := range 45 {
		s.Lines = append(s.Lines, StoryScriptLine{Speaker: []string{"Ann", "Bob"}[i%2], Text: fmt.Sprintf("Line number %d.", i+1)})
	}
	var ranges []string
	m := &model{handle: func(sys, user string) (string, error) {
		if !strings.Contains(sys, "at most 8 words") || strings.Contains(sys, "Chinese characters") {
			t.Error("not the English prompt")
		}
		var first, last int
		if i := strings.Index(user, "Direct lines "); i >= 0 {
			fmt.Sscanf(user[i:], "Direct lines %d to %d", &first, &last)
		}
		ranges = append(ranges, fmt.Sprintf("%d-%d", first, last))
		var lines []string
		for n := first; n <= last; n++ {
			lines = append(lines, fmt.Sprintf(`{"n":%d,"emotion":"steady","intensity":1,"turn":false,"note":"goes on calmly, as before, nothing much changes here at all, really"}`, n))
		}
		return `{"scene":"A kitchen","cast":[{"name":"Ann","baseline":"tender"}],"lines":[` + strings.Join(lines, ",") + `]}`, nil
	}}
	got, err := (&StoryDirector{Router: router(m)}).DirectEmotions(context.Background(), s, testVocab)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ranges, " ") != "1-40 41-45" || len(got.Lines) != 45 {
		t.Errorf("batches %v, %d lines", ranges, len(got.Lines))
	}
	if n := got.Lines[0].Note; n != "goes on calmly, as before, nothing much changes here at all" {
		t.Errorf("note %q", n)
	}
}

// An answer that slipped a line (numbers one off) is asked again; lines
// still misnumbered stay undirected.
func TestDirectEmotionsMisnumbered(t *testing.T) {
	s := StoryScript{Lines: []StoryScriptLine{{Speaker: "Kid", Text: "Can I?"}, {Speaker: "Mom", Text: "Maybe."}, {Speaker: "Kid", Text: "Please!"}, {Speaker: "Mom", Text: "Fine."}}}
	slipped := `{"scene":"","cast":[],"lines":[{"n":1,"speaker":"Kid","emotion":"pleading","intensity":1,"turn":false,"note":""},
	{"n":2,"speaker":"Kid","emotion":"pleading","intensity":2,"turn":false,"note":""},{"n":3,"speaker":"Mom","emotion":"tender","intensity":1,"turn":true,"note":""}]}`
	good := `{"scene":"","cast":[],"lines":[{"n":1,"speaker":"Kid","emotion":"pleading","intensity":1,"turn":false,"note":""},
	{"n":2,"speaker":"Mom","emotion":"hesitant","intensity":1,"turn":false,"note":""},{"n":3,"speaker":"Kid","emotion":"pleading","intensity":2,"turn":false,"note":""},
	{"n":4,"speaker":"Mom","emotion":"tender","intensity":1,"turn":true,"note":""}]}`
	calls := 0
	m := &model{handle: func(sys, user string) (string, error) {
		calls++
		if calls == 1 {
			return slipped, nil
		}
		if !strings.Contains(user, "numbered the lines wrongly") || !strings.Contains(user, "line 2 is spoken by Mom") {
			t.Errorf("no repair note: %s", user)
		}
		return good, nil
	}}
	got, err := (&StoryDirector{Router: router(m)}).DirectEmotions(context.Background(), s, testVocab)
	if err != nil || calls != 2 || len(got.Lines) != 4 || got.Lines[1].Emotion != "hesitant" {
		t.Fatalf("%d calls, %+v, %v", calls, got.Lines, err)
	}
	// Still slipped the second time: only the lines whose speaker fits.
	calls = 0
	m.handle = func(string, string) (string, error) { calls++; return slipped, nil }
	got, err = (&StoryDirector{Router: router(m)}).DirectEmotions(context.Background(), s, testVocab)
	if err != nil || calls != 2 || fmt.Sprint(got.Lines) != "[{1 Kid pleading 1 false }]" {
		t.Errorf("%d calls, %v, %v", calls, got.Lines, err)
	}
}
