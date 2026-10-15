package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
)

// TestInstructions: initialize returns the instructions, which name every
// tool and only arguments the tools' schemas have; list_models marks the
// models that speak cloned voices only.
func TestInstructions(t *testing.T) {
	f := newFake(t)
	_, _, ts := serve(t, f)
	cs := connect(t, ts.URL, "claude-code", nil)
	init := cs.InitializeResult()
	if init == nil || init.Instructions != Instructions {
		t.Fatalf("initialize: %+v", init)
	}
	if n := len(Instructions); n < 800 || n > 3000 {
		t.Errorf("instructions are %d bytes: compact, but enough to work without the skill", n)
	}
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	props := map[string]map[string]bool{}
	for _, tl := range tools.Tools {
		if !strings.Contains(Instructions, tl.Name) {
			t.Errorf("the instructions do not name %s", tl.Name)
		}
		var s struct {
			Properties map[string]any `json:"properties"`
		}
		data, _ := json.Marshal(tl.InputSchema)
		json.Unmarshal(data, &s)
		props[tl.Name] = map[string]bool{}
		for p := range s.Properties {
			props[tl.Name][p] = true
		}
	}
	// The arguments the instructions name, by tool.
	for _, ta := range [][2]string{
		{"generate_speech", "text"}, {"generate_speech", "voice"}, {"generate_speech", "output"},
		{"generate_speech", "filename"}, {"generate_speech", "instructions"},
		{"transcribe", "path"}, {"transcribe", "timestamps"},
		{"clone_voice", "name"}, {"clone_voice", "path"}, {"clone_voice", "consent"},
		{"design_voice", "description"}, {"describe_voice", "description"},
		{"get_job", "wait_seconds"}, {"delete_item", "confirm"}, {"create_audiobook", "path"}, {"create_story", "text"},
		{"create_dub", "path"}, {"create_dub", "target_languages"}, {"update_longform", "voice"}, {"update_longform", "cast"},
		{"update_longform", "format"}, {"update_longform", "scene"}, {"update_longform", "directions"}, {"update_longform", "direct"},
		{"export_audio", "format"}, {"export_audio", "loudness"},
	} {
		if !props[ta[0]][ta[1]] {
			t.Errorf("%s has no argument %s", ta[0], ta[1])
		}
		if !strings.Contains(Instructions, ta[1]) {
			t.Errorf("the instructions do not name %s", ta[1])
		}
	}
	for _, s := range []string{"127.0.0.1", "licence_required", "model_not_downloaded", "needs_reference", "voice://{id}", "history://recent", "consent: true", "timestamps: true",
		"job_id", "confirm: true", "confirm_required", "busy"} {
		if !strings.Contains(Instructions, s) {
			t.Errorf("the instructions lack %q", s)
		}
	}

	var nr string
	for _, e := range catalog.Builtin().Entries {
		if e.NeedsReference {
			nr = e.ID
		}
	}
	if nr == "" {
		t.Skip("no model needs a reference")
	}
	res := call(t, cs, "list_models", map[string]any{"task": "tts"})
	var out struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal([]byte(textOf(res)), &out)
	marked := map[string]bool{}
	for _, m := range out.Models {
		if m["needs_reference"] == true {
			marked[m["id"].(string)] = true
		}
	}
	if !marked[nr] || len(marked) != 1 {
		t.Errorf("needs_reference marks %v, want %s", marked, nr)
	}
}
