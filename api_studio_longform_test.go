package main

// Tests of the Stories and Audiobook modules for agents
// (api_studio_longform*.go): an audiobook from an EPUB, and a story's
// render progress and cancel. The harness is api_studio_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store"
)

// TestStudioAudiobook: an EPUB becomes an audiobook project; voices and
// format are set; the render is a job followed to its M4B; exports
// write into the agents' folder or a folder given; deleting needs
// confirm. REST and MCP.
func TestStudioAudiobook(t *testing.T) {
	e := newStudioEnv(t)
	book := filepath.Join(t.TempDir(), "lighthouse.epub")
	writeEPUB(t, book)

	e.fails(t, "create_audiobook", map[string]any{"path": "lighthouse.epub"}, api.CodeInvalid)
	e.fails(t, "create_audiobook", map[string]any{"path": filepath.Join(t.TempDir(), "none.epub")}, api.CodeNotFound)
	e.fails(t, "create_audiobook", map[string]any{"path": book, "model": "no-such-model"}, api.CodeModelNotFound)
	e.fails(t, "create_audiobook", map[string]any{}, api.CodeInvalid)

	var p api.LongformJSON
	e.must(t, "create_audiobook", map[string]any{"path": book}, &p)
	if p.Kind != "audiobook" || p.Title != "The Lighthouse" || len(p.Chapters) != 2 || p.Chapters[0].Title != "Arrival" ||
		p.Book.Author != "Ann Writer" || p.Voice == "" || p.Model != catalog.Builtin().Entries[0].ID {
		t.Fatalf("project %+v", p)
	}
	// The page's library lists it.
	var items struct{ Items []api.ProjectJSON }
	e.must(t, "list_projects", map[string]any{"category": "audiobook"}, &items)
	if len(items.Items) != 1 || items.Items[0].ID != p.ID || items.Items[0].Tool != "get_longform" {
		t.Fatalf("projects %+v", items)
	}
	e.fails(t, "list_projects", map[string]any{"category": "nope"}, api.CodeInvalid)

	// Voices and settings.
	e.fails(t, "update_longform", map[string]any{"project_id": p.ID, "voice": "Nobody"}, api.CodeVoiceNotFound)
	e.fails(t, "update_longform", map[string]any{"project_id": p.ID, "cast": map[string]string{"Ghost": "am_adam"}}, api.CodeInvalid)
	e.fails(t, "update_longform", map[string]any{"project_id": "missing"}, api.CodeNotFound)
	e.must(t, "update_longform", map[string]any{"project_id": p.ID, "voice": "am_adam", "format": "m4b", "loudness": "podcast", "narrator": "Bob"}, &p)
	if p.Voice != "speaker:am_adam" || p.VoiceName != "am_adam" || p.Format != "m4b" || p.Book.Loudness != "podcast" || p.Book.Narrator != "Bob" {
		t.Fatalf("updated %+v", p)
	}

	// Render: a job, followed to the M4B.
	var j jobAnswer
	e.must(t, "render_longform", map[string]any{"project_id": p.ID}, &j)
	if !strings.HasPrefix(j.JobID, "longform:") || j.Kind != "longform.render" {
		t.Fatalf("render %+v", j)
	}
	j = e.wait(t, j.JobID)
	if j.State != "succeeded" || len(j.Files) < 3 || !strings.HasSuffix(j.Files[0], ".m4b") {
		t.Fatalf("rendered %+v", j)
	}
	e.must(t, "get_longform", map[string]any{"project_id": p.ID}, &p)
	if p.Render == nil || p.Render.State != "done" || p.Render.M4B == "" || len(p.Render.Chapter) != 2 {
		t.Fatalf("last render %+v", p.Render)
	}

	// Exports.
	var x api.ExportJSON
	e.must(t, "export_longform", map[string]any{"project_id": p.ID, "format": "m4b"}, &x)
	if len(x.Files) != 1 || filepath.Dir(x.Files[0]) != e.out || filepath.Base(x.Files[0]) != "The Lighthouse.m4b" {
		t.Fatalf("m4b %+v", x)
	}
	if data, _ := os.ReadFile(x.Files[0]); !bytes.HasPrefix(data, []byte("FAKE-M4A")) {
		t.Fatalf("m4b %q", data[:min(len(data), 20)])
	}
	dir := filepath.Join(t.TempDir(), "made", "here")
	e.must(t, "export_longform", map[string]any{"project_id": p.ID, "format": "chapters", "directory": dir, "filename": "../../escape"}, &x)
	if len(x.Files) != 2 || filepath.Dir(filepath.Dir(x.Files[0])) != dir || !strings.HasSuffix(x.Files[0], "01 Arrival.wav") {
		t.Fatalf("chapters %+v", x)
	}
	e.must(t, "export_longform", map[string]any{"project_id": p.ID, "format": "wav", "bits": 16, "filename": "book"}, &x)
	if _, info, err := wav.ReadFile(x.Files[0]); err != nil || filepath.Base(x.Files[0]) != "book.wav" || info.Encoding.Bits() != 16 {
		t.Fatalf("wav %+v %v %+v", x, err, info)
	}
	e.fails(t, "export_longform", map[string]any{"project_id": p.ID, "format": "mp3"}, api.CodeUnsupportedFormat)
	e.fails(t, "export_longform", map[string]any{"project_id": p.ID, "directory": "relative/dir"}, api.CodeInvalid)
	exports, _ := e.lib.svc.Library.Search(context.Background(), store.LibraryQuery{Category: store.LibExport})
	if len(exports) < 4 {
		t.Errorf("exports recorded: %d", len(exports))
	}

	// REST: a manuscript, the project, a render's job by id, deleting.
	status, body := e.rest(t, "POST", "/api/audiobooks", map[string]any{"text": "# One\nHello. [voice:Mara] Hi there.\n# Two\nBye.", "cast": map[string]string{"Mara": "af_heart"}, "title": "Short"})
	var p2 api.LongformJSON
	json.Unmarshal(body, &p2)
	if status != 201 || len(p2.Chapters) != 2 || len(p2.Cast) != 1 || p2.Cast[0].Voice != "speaker:af_heart" {
		t.Fatalf("POST /api/audiobooks %d %s", status, body)
	}
	if status, body = e.rest(t, "GET", "/api/longform/"+p2.ID, nil); status != 200 || !strings.Contains(string(body), `"Short"`) {
		t.Fatalf("GET %d %s", status, body)
	}
	if status, body = e.rest(t, "PATCH", "/api/longform/"+p2.ID, map[string]any{"cast": map[string]string{"Mara": "default"}}); status != 200 || !strings.Contains(string(body), `"cast":[{"name":"Mara"}]`) {
		t.Fatalf("PATCH %d %s", status, body)
	}
	if status, body = e.rest(t, "POST", "/api/longform/"+p2.ID+"/export", map[string]any{}); status != 409 || !strings.Contains(string(body), "not_ready") {
		t.Fatalf("export before render %d %s", status, body)
	}
	if status, body = e.rest(t, "GET", "/api/jobs/"+j.JobID+"?wait=1", nil); status != 200 || !strings.Contains(string(body), `"done":true`) || !strings.Contains(string(body), ".m4b") {
		t.Fatalf("GET job %d %s", status, body)
	}
	if status, body = e.rest(t, "DELETE", "/api/longform/"+p2.ID, nil); status != 400 || !strings.Contains(string(body), api.CodeConfirmRequired) {
		t.Fatalf("DELETE without confirm %d %s", status, body)
	}
	if status, _ = e.rest(t, "DELETE", "/api/longform/"+p2.ID+"?confirm=true", nil); status != 200 {
		t.Fatalf("DELETE %d", status)
	}
	e.fails(t, "delete_item", map[string]any{"kind": "project", "id": p.ID}, api.CodeConfirmRequired)
	if _, err := e.lib.svc.Projects.Get(context.Background(), p.ID); err != nil {
		t.Fatal("deleted without confirm")
	}
	e.must(t, "delete_item", map[string]any{"kind": "project", "id": p.ID, "confirm": true}, nil)
	if _, err := e.lib.svc.Projects.Get(context.Background(), p.ID); err == nil {
		t.Fatal("not deleted")
	}
}

// TestStudioStoryProgressAndCancel: a two-speaker story from a script;
// its render shows progress while held, and cancel_job stops it.
func TestStudioStoryProgressAndCancel(t *testing.T) {
	e := newStudioEnv(t)
	var p api.LongformJSON
	e.must(t, "create_story", map[string]any{"text": "# Morning\nAlice: Good morning, Bob.\nBob: Morning! Coffee?\nAlice: Yes, please.",
		"cast": map[string]string{"Alice": "af_heart"}, "voice": "am_adam"}, &p)
	if p.Kind != "story" || len(p.Cast) != 2 || len(p.Lines) != 4 || !p.Lines[0].Chapter || p.Voice != "speaker:am_adam" {
		t.Fatalf("story %+v", p)
	}
	var alice, bob api.CastJSON
	for _, c := range p.Cast {
		switch c.Name {
		case "Alice":
			alice = c
		case "Bob":
			bob = c
		}
	}
	if alice.Voice != "speaker:af_heart" || alice.Lines != 2 || bob.Voice == "" || bob.Voice == alice.Voice {
		t.Fatalf("cast %+v", p.Cast)
	}
	// Lines as data, and the line voices.
	var p2 api.LongformJSON
	e.must(t, "create_story", map[string]any{"lines": []map[string]any{{"text": "Intro", "chapter": true}, {"text": "Hi.", "speaker": "Ann"}, {"text": "Hello.", "speaker": "Ben", "pause_after_ms": 300}}}, &p2)
	if len(p2.Lines) != 3 || len(p2.Cast) != 2 || p2.Cast[0].Voice == "" || p2.Lines[2].PauseAfter != 300 {
		t.Fatalf("lines %+v", p2)
	}
	e.must(t, "update_longform", map[string]any{"project_id": p2.ID, "line_voices": map[string]string{p2.Lines[1].ID: "bf_emma"}}, &p2)
	if p2.Lines[1].Voice != "speaker:bf_emma" {
		t.Fatalf("line voice %+v", p2.Lines[1])
	}
	e.fails(t, "create_story", map[string]any{"text": "x", "lines": []map[string]any{{"text": "y"}}}, api.CodeInvalid)
	e.fails(t, "render_longform", map[string]any{"project_id": p.ID, "chapters": []int{3}}, api.CodeInvalid)
	// A removed model (thread #43): its replacement, with a warning.
	var pr api.LongformJSON
	e.must(t, "create_story", map[string]any{"text": "Ann: Hi.", "model": "kitten-tts-mini-0.8"}, &pr)
	if pr.Model != "kokoro-82m-bf16" || !strings.Contains(pr.Warning, "KittenTTS mini was removed") {
		t.Fatalf("removed model: %q %q", pr.Model, pr.Warning)
	}
	e.must(t, "update_longform", map[string]any{"project_id": pr.ID, "model": "Pocket TTS"}, &pr)
	if pr.Model != "kokoro-82m-bf16" || !strings.Contains(pr.Warning, "Pocket TTS was removed") {
		t.Fatalf("update to a removed model: %q %q", pr.Model, pr.Warning)
	}

	// Held speech: the job runs, with a stage; cancel stops it.
	e.gate.hold = true
	var j jobAnswer
	e.must(t, "render_longform", map[string]any{"project_id": p.ID}, &j)
	deadline := time.Now().Add(10 * time.Second)
	for j.State != "running" || j.Stage == "" {
		if time.Now().After(deadline) {
			t.Fatalf("not running: %+v", j)
		}
		time.Sleep(20 * time.Millisecond)
		e.must(t, "get_job", map[string]any{"job_id": j.JobID}, &j)
	}
	if j.Done || j.Progress == nil || !strings.HasPrefix(j.Stage, "chapter 1/1") || len(j.Jobs) != 2 {
		t.Fatalf("running %+v", j)
	}
	// A second render waits its turn: busy.
	e.fails(t, "render_longform", map[string]any{"project_id": p2.ID}, "busy")
	e.must(t, "cancel_job", map[string]any{"job_id": j.JobID}, &j)
	j = e.wait(t, j.JobID)
	if j.State != "cancelled" || j.Error == nil || j.Error.Code != jobs.CodeCancelled {
		t.Fatalf("cancelled %+v", j)
	}
	// Released, a render of one chapter succeeds, with stems.
	e.gate.hold = false
	e.fails(t, "export_longform", map[string]any{"project_id": p.ID, "format": "stems"}, "not_ready")
	e.must(t, "update_longform", map[string]any{"project_id": p.ID, "stems": true}, &p)
	e.must(t, "render_longform", map[string]any{"project_id": p.ID, "chapters": []int{1}}, &j)
	if j = e.wait(t, j.JobID); j.State != "succeeded" {
		t.Fatalf("again %+v", j)
	}
	var x api.ExportJSON
	e.must(t, "export_longform", map[string]any{"project_id": p.ID, "format": "stems"}, &x)
	if len(x.Files) != 2 {
		t.Fatalf("stems %+v", x)
	}

	// REST: a story; render answers 202 with the job.
	status, body := e.rest(t, "POST", "/api/stories", map[string]any{"text": "Ann: Hello.\nBen: Hi."})
	var p3 api.LongformJSON
	json.Unmarshal(body, &p3)
	if status != 201 || len(p3.Cast) != 2 {
		t.Fatalf("POST /api/stories %d %s", status, body)
	}
	status, body = e.rest(t, "POST", "/api/longform/"+p3.ID+"/render", nil)
	if status != 202 || !strings.Contains(string(body), `"job_id":"longform:`) {
		t.Fatalf("render %d %s", status, body)
	}
	var acc struct {
		JobID string `json:"job_id"`
	}
	json.Unmarshal(body, &acc)
	e.wait(t, acc.JobID)
	if status, body = e.rest(t, "POST", "/api/jobs/"+acc.JobID+"/cancel", nil); status != 200 || !strings.Contains(string(body), `"done":true`) {
		t.Fatalf("cancel a finished render %d %s", status, body)
	}
}
