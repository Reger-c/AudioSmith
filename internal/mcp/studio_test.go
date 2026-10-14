package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// TestStudioToolsBasics: over a backend without the app's modules, the
// module tools answer unsupported; get_job follows a queue job to its
// file (wait_seconds); delete_item needs confirm; export_transcript
// checks its id. The app's own tests (api_studio_test.go) run the
// modules for real.
func TestStudioToolsBasics(t *testing.T) {
	f := newFake(t)
	_, _, ts := serve(t, f)
	cs := connect(t, ts.URL, "claude-code", nil)

	for _, name := range []string{"list_projects", "list_workflows", "list_batch", "search_voices"} {
		res := call(t, cs, name, nil)
		if !res.IsError || !strings.HasPrefix(textOf(res), api.CodeUnsupported+":") {
			t.Errorf("%s: %s", name, textOf(res))
		}
	}
	res := call(t, cs, "delete_item", map[string]any{"kind": "take", "id": "t1"})
	if !res.IsError || !strings.HasPrefix(textOf(res), api.CodeConfirmRequired+":") {
		t.Fatalf("delete without confirm: %s", textOf(res))
	}
	res = call(t, cs, "delete_item", map[string]any{"kind": "take", "id": "t1", "confirm": true})
	if res.IsError || !strings.Contains(textOf(res), `"deleted": true`) {
		t.Fatalf("delete: %s", textOf(res))
	}
	res = call(t, cs, "delete_item", map[string]any{"kind": "nothing", "id": "x", "confirm": true})
	if !res.IsError || !strings.HasPrefix(textOf(res), api.CodeInvalid+":") {
		t.Fatalf("delete kind: %s", textOf(res))
	}

	// A take's job, followed by get_job to its file.
	res = call(t, cs, "generate_speech", map[string]any{"text": "A job to follow.", "output": "file"})
	if res.IsError {
		t.Fatal(textOf(res))
	}
	f.mu.Lock()
	var take string
	for id := range f.takes {
		take = id
	}
	f.mu.Unlock()
	js, _ := f.sched.List(t.Context(), jobs.Filter{Owner: "take:" + take, States: []jobs.State{jobs.StateSucceeded}})
	if len(js) != 1 {
		t.Fatalf("jobs %v", js)
	}
	res = call(t, cs, "get_job", map[string]any{"job_id": js[0].ID, "wait_seconds": 5})
	var j jobOut
	json.Unmarshal([]byte(textOf(res)), &j)
	if res.IsError || !j.Done || j.State != "succeeded" || len(j.Files) != 1 || !strings.HasSuffix(j.Files[0], take+".wav") {
		t.Fatalf("get_job: %s", textOf(res))
	}
	res = call(t, cs, "get_job", map[string]any{"job_id": "nope"})
	if !res.IsError || !strings.HasPrefix(textOf(res), api.CodeNotFound+":") {
		t.Fatalf("missing job: %s", textOf(res))
	}
	res = call(t, cs, "cancel_job", map[string]any{"job_id": "longform:x"})
	if !res.IsError || !strings.HasPrefix(textOf(res), api.CodeUnsupported+":") {
		t.Fatalf("group job without studio: %s", textOf(res))
	}
	res = call(t, cs, "export_transcript", map[string]any{"transcript_id": "missing"})
	if !res.IsError || !strings.HasPrefix(textOf(res), api.CodeNotFound+":") {
		t.Fatalf("export_transcript: %s", textOf(res))
	}
}
