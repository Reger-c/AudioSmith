package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/api"
)

// An audiobook's dialogue blocks for agents: the manuscript round trip,
// dialogue_blocks with their mode, the cast with the blocks' speakers, the
// dialogue model on by default, a block's preview, and voices given to
// uncast speakers.
func TestStudioAudiobookDialogue(t *testing.T) {
	stubMachine(t, 16, true)
	e := newStudioEnv(t)
	script := "# 渡口\n\n雾还没有散。\n\n[对话]\n伊内丝：这是去哈罗角的船吗？\n托马斯：唯一的一班。\n他把缆绳抛上岸。\n[/对话]\n\n[dialogue]\n伊内丝：一个人。\n[/dialogue]"
	var p api.LongformJSON
	e.must(t, "create_audiobook", map[string]any{"text": script, "voice": "af_heart", "cast": map[string]string{"伊内丝": "af_bella"}}, &p)
	if p.Script != strings.TrimSpace(script)+"\n" || !p.Dialogue || p.DialogueModel != "soulx" {
		t.Fatalf("project script %q dialogue %v %q", p.Script, p.Dialogue, p.DialogueModel)
	}
	if len(p.DialogueBlocks) != 2 {
		t.Fatalf("blocks %+v", p.DialogueBlocks)
	}
	b0, b1 := p.DialogueBlocks[0], p.DialogueBlocks[1]
	if b0.Chapter != 1 || b0.Index != 1 || !slices.Equal(b0.Speakers, []string{"伊内丝", "托马斯"}) || b0.Lines != 2 ||
		!slices.Equal(b0.Uncast, []string{"托马斯"}) || b0.Mode != "dialogue" || p.Script[b0.Start:b0.Start+len("[对话]")] != "[对话]" {
		t.Errorf("block 1 %+v", b0)
	}
	if b1.Index != 2 || b1.Mode != "per_line" || b1.Reason != "one_speaker" {
		t.Errorf("block 2 %+v", b1)
	}
	var cast []string
	for _, c := range p.Cast {
		cast = append(cast, c.Name+":"+c.Voice+":"+map[bool]string{true: "dialogue"}[c.Dialogue])
	}
	if !slices.Equal(cast, []string{"伊内丝:speaker:af_bella:dialogue", "托马斯::dialogue"}) {
		t.Errorf("cast %q", cast)
	}
	if !slices.ContainsFunc(p.Problems, func(pr api.ProblemJSON) bool { return pr.Code == "longform.dialogue_uncast" && !pr.Blocking }) {
		t.Errorf("problems %+v", p.Problems)
	}

	// The dialogue model off: every block line by line.
	id := p.ID
	p = api.LongformJSON{}
	e.must(t, "update_longform", map[string]any{"project_id": id, "dialogue": false}, &p)
	if p.DialogueBlocks[0].Mode != "per_line" || p.DialogueBlocks[0].Reason != "dialogue_off" {
		t.Errorf("off %+v", p.DialogueBlocks[0])
	}
	// A new manuscript; the cast by a block's name.
	script2 := strings.Replace(script, "他把缆绳抛上岸。\n", "他把缆绳抛上岸。\n船长：开船了。\n", 1)
	p = api.LongformJSON{}
	e.must(t, "update_longform", map[string]any{"project_id": id, "script": script2, "dialogue": true, "cast": map[string]string{"托马斯": "am_adam"}}, &p)
	if !strings.Contains(p.Script, "船长：开船了。") || !slices.Equal(p.DialogueBlocks[0].Uncast, []string{"船长"}) || p.DialogueBlocks[0].Mode != "dialogue" {
		t.Errorf("updated %+v", p.DialogueBlocks)
	}
	e.fails(t, "update_longform", map[string]any{"project_id": p.ID, "script": "  "}, api.CodeInvalid)
	e.fails(t, "update_longform", map[string]any{"project_id": p.ID, "cast": map[string]string{"Nobody": "am_adam"}}, api.CodeInvalid)

	// A block's preview; the uncast speaker gets a voice of their own.
	e.fails(t, "render_longform", map[string]any{"project_id": p.ID, "dialogue_block": map[string]int{"chapter": 1, "index": 9}}, api.CodeInvalid)
	var j jobAnswer
	e.must(t, "render_longform", map[string]any{"project_id": p.ID, "dialogue_block": map[string]int{"chapter": 1, "index": 1}}, &j)
	var res struct {
		Given map[string]string `json:"voices_given"`
	}
	json.Unmarshal(j.Result, &res)
	if res.Given["船长"] == "" {
		t.Errorf("voices given %s", j.Result)
	}
	j = e.wait(t, j.JobID)
	if j.State != "succeeded" {
		t.Errorf("preview %+v", j)
	}
	p = api.LongformJSON{}
	e.must(t, "get_longform", map[string]any{"project_id": id}, &p)
	if len(p.DialogueBlocks[0].Uncast) != 0 || p.Render != nil {
		t.Errorf("after the preview: %+v, last render %+v", p.DialogueBlocks[0], p.Render)
	}
	// A story has no script to set.
	var s api.LongformJSON
	e.must(t, "create_story", map[string]any{"text": "A: hi\nB: ho"}, &s)
	e.fails(t, "update_longform", map[string]any{"project_id": s.ID, "script": "x"}, api.CodeInvalid)
	e.fails(t, "render_longform", map[string]any{"project_id": s.ID, "dialogue_block": map[string]int{"chapter": 1, "index": 1}}, api.CodeInvalid)
}
