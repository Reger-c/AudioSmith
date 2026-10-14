package main

// Tests of the app's modules for agents (thread #35): every REST route
// and MCP tool of api_studio*.go over a real library, job queue and
// services, with dubtest's fake engine behind speech, dubbing and
// workflows.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/dub/dubtest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/gallery"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/llm"
	mcpsrv "github.com/hehehai/AudioSmith/internal/mcp"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/translate"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// studioEnv is the app over a real library and job queue, its API and
// MCP server, and an MCP client.
type studioEnv struct {
	a    *app
	lib  *library
	fe   *dubtest.Engine
	b    *apiBackend
	srv  *api.Server
	ts   *httptest.Server
	cs   *sdk.ClientSession
	out  string // the agents' folder
	gate *gatedSynth
}

// gatedSynth holds speech while closed (progress and cancel tests).
type gatedSynth struct {
	ex   jobs.Executor
	open chan struct{}
	hold bool
}

func (g *gatedSynth) Run(ctx context.Context, job jobs.Job, report func(jobs.Progress), chunk func(json.RawMessage)) (json.RawMessage, error) {
	if g.hold {
		select {
		case <-g.open:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.ex.Run(ctx, job, report, chunk)
}

func newStudioEnv(t *testing.T) *studioEnv {
	t.Helper()
	saved := llmSecrets
	llmSecrets = llm.StaticSecrets{}
	t.Cleanup(func() { llmSecrets = saved })
	savedTr := dubMakeTranslator
	dubMakeTranslator = func(*llm.Router, dub.Settings) (translate.Translator, error) { return &dubFakeTranslator{}, nil }
	t.Cleanup(func() { dubMakeTranslator = savedTr })

	dir := t.TempDir()
	lib := openTestLibraryWith(t, dir, libraryOptions{})
	useAllModelFiles(t, useLocalModels(t, lib))
	out := t.TempDir()
	if err := lib.settings.Update(context.Background(), func(s *settings.Settings) { s.API.MCPDir = out }); err != nil {
		t.Fatal(err)
	}
	eng := newFakeEngine()
	a := newApp(eng.host, lib)
	eng.a = a
	// The main thread: one goroutine runs what the app posts.
	q := make(chan func(), 1<<14)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case fn := <-q:
				fn()
			case <-stop:
				return
			}
		}
	}()
	a.postQueue = func(fn func()) { q <- fn }
	a.enginePost = a.postQueue
	a.connectEngine(eng.host)
	fe := dubtest.New()
	fe.Script = []string{"Hello there.", "How are you today?", "I am fine, thank you.", "See you tomorrow."}
	a.dubEngine, a.wfEngine = fe.Executor, fe.Executor
	g := &gatedSynth{ex: fe.Executor(proto.MethodTTSSynthesize), open: make(chan struct{})}
	lib.useEngine(func() jobs.Executor { return g })
	if err := lib.sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := a.newAPIBackend(dir)
	b.sup = &apiTestSup{}
	srv := api.New(api.Options{Backend: b, Sched: lib.sched, Bus: lib.bus, Version: "test", Logf: t.Logf})
	srv.MountMCP(mcpsrv.New(mcpsrv.Options{API: srv, Backend: b}).Handler())
	ts := httptest.NewServer(srv.Handler())
	c := sdk.NewClient(&sdk.Implementation{Name: "studio-test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: http.DefaultClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		ts.Close()
		srv.Close(context.Background())
		if b.unsub != nil {
			b.unsub()
		}
		lib.sched.Close()
		close(stop)
	})
	return &studioEnv{a: a, lib: lib, fe: fe, b: b, srv: srv, ts: ts, cs: cs, out: out, gate: g}
}

// call calls a tool and decodes its JSON answer into v (nil: ignored);
// it returns the error text, "" on success.
func (e *studioEnv) call(t *testing.T, name string, args map[string]any, v any) string {
	t.Helper()
	res, err := e.cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.IsError {
		return b.String()
	}
	if v != nil {
		if err := json.Unmarshal([]byte(b.String()), v); err != nil {
			t.Fatalf("%s: %v in %s", name, err, b.String())
		}
	}
	return ""
}

// must calls a tool that must succeed.
func (e *studioEnv) must(t *testing.T, name string, args map[string]any, v any) {
	t.Helper()
	if msg := e.call(t, name, args, v); msg != "" {
		t.Fatalf("%s %v: %s", name, args, msg)
	}
}

// fails calls a tool that must fail with code.
func (e *studioEnv) fails(t *testing.T, name string, args map[string]any, code string) {
	t.Helper()
	if msg := e.call(t, name, args, nil); !strings.HasPrefix(msg, code+":") {
		t.Fatalf("%s %v: want %s, got %q", name, args, code, msg)
	}
}

// rest calls the REST API: status and body.
func (e *studioEnv) rest(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.HeaderCSRF, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// jobAnswer is get_job's answer.
type jobAnswer struct {
	JobID    string          `json:"job_id"`
	Kind     string          `json:"kind"`
	State    string          `json:"state"`
	Done     bool            `json:"done"`
	Stage    string          `json:"stage"`
	Progress *float64        `json:"progress"`
	Error    *Code           `json:"error"`
	Files    []string        `json:"files"`
	Result   json.RawMessage `json:"result"`
	Jobs     []string        `json:"jobs"`
}

// Code is a job error's code.
type Code struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// wait follows a job to its end with get_job's long poll.
func (e *studioEnv) wait(t *testing.T, id string) jobAnswer {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var j jobAnswer
		e.must(t, "get_job", map[string]any{"job_id": id, "wait_seconds": 5}, &j)
		if j.Done {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not end: %+v", id, j)
		}
	}
}

// writeEPUB writes a small EPUB 2 with two chapters.
func writeEPUB(t *testing.T, path string) {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	page := func(title, body string) string {
		return `<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>` + title + `</title></head><body>` + body + `</body></html>`
	}
	for _, f := range [][2]string{
		{"mimetype", "application/epub+zip"},
		{"META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`},
		{"content.opf", `<?xml version="1.0" encoding="UTF-8"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>The Lighthouse</dc:title><dc:creator>Ann Writer</dc:creator><dc:language>en</dc:language></metadata>
<manifest><item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/><item id="a" href="a.html" media-type="application/xhtml+xml"/><item id="b" href="b.html" media-type="application/xhtml+xml"/></manifest>
<spine toc="ncx"><itemref idref="a"/><itemref idref="b"/></spine></package>`},
		{"toc.ncx", `<?xml version="1.0"?><ncx xmlns="http://www.daisy.org/z3986/2005/ncx/"><navMap><navPoint id="p1"><navLabel><text>Arrival</text></navLabel><content src="a.html"/></navPoint><navPoint id="p2"><navLabel><text>Night</text></navLabel><content src="b.html"/></navPoint></navMap></ncx>`},
		{"a.html", page("a", `<h1>Arrival</h1><p>The keeper climbed the stairs.</p>`)},
		{"b.html", page("b", `<h1>Night</h1><p>The light turned all night.</p>`)},
	} {
		fw, _ := w.Create(f[0])
		io.WriteString(fw, f[1])
	}
	w.Close()
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

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

// TestStudioDub: a dub from a file, run as a job to its track; its lines
// and translations; an edited translation made again; exports; the
// batch queue; deleting needs confirm.
func TestStudioDub(t *testing.T) {
	e := newStudioEnv(t)
	src := filepath.Join(t.TempDir(), "Interview.wav")
	if _, err := dubtest.Speech(src, 4); err != nil {
		t.Fatal(err)
	}
	e.fails(t, "create_dub", map[string]any{"path": src, "target_languages": []string{"xx-nope-1"}}, api.CodeInvalid)
	notMedia := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(notMedia, []byte("x"), 0o644)
	e.fails(t, "create_dub", map[string]any{"path": notMedia}, api.CodeUnsupportedFormat)

	var d api.DubJSON
	e.must(t, "create_dub", map[string]any{"path": src, "target_languages": []string{"es"}}, &d)
	if d.JobID != "dub:"+d.ID || d.Title != "Interview" || !slices.Equal(d.Targets, []string{"es"}) {
		t.Fatalf("dub %+v", d)
	}
	j := e.wait(t, d.JobID)
	if j.State != "succeeded" || len(j.Files) != 1 || !strings.HasSuffix(j.Files[0], ".wav") {
		t.Fatalf("dubbed %+v", j)
	}
	e.must(t, "get_dub", map[string]any{"dub_id": d.ID}, &d)
	if len(d.Segments) != 4 || d.Segments[0].Translation != "[es] Hello there." || d.Tracks["es"].File == "" || d.Phase != string(dub.PhaseDone) {
		t.Fatalf("get_dub %+v", d)
	}
	// Edit a translation; the run makes that line again.
	seg := d.Segments[1].ID
	e.must(t, "edit_dub", map[string]any{"dub_id": d.ID, "segments": []map[string]any{{"id": seg, "translation": "¿Qué tal?"}}, "names": map[string]string{"S1": "Ana"}}, &d)
	if d.Segments[1].Translation != "¿Qué tal?" || d.Speakers[0].Name != "Ana" {
		t.Fatalf("edited %+v", d)
	}
	e.fails(t, "edit_dub", map[string]any{"dub_id": d.ID, "segments": []map[string]any{{"id": "nope", "text": "x"}}}, api.CodeInvalid)
	e.must(t, "run_dub", map[string]any{"dub_id": d.ID}, &j)
	if j = e.wait(t, j.JobID); j.State != "succeeded" {
		t.Fatalf("re-run %+v", j)
	}
	// Exports: subtitles and the track.
	e.must(t, "export_dub", map[string]any{"dub_id": d.ID, "format": "srt"}, &j)
	j = e.wait(t, j.JobID)
	if j.State != "succeeded" || len(j.Files) != 1 || filepath.Dir(j.Files[0]) != e.out {
		t.Fatalf("srt %+v", j)
	}
	if data, _ := os.ReadFile(j.Files[0]); !strings.Contains(string(data), "¿Qué tal?") {
		t.Fatalf("srt %s", data)
	}
	e.must(t, "export_dub", map[string]any{"dub_id": d.ID, "format": "wav", "filename": "track"}, &j)
	if j = e.wait(t, j.JobID); j.State != "succeeded" || filepath.Base(j.Files[0]) != "track.wav" {
		t.Fatalf("wav %+v", j)
	}
	e.fails(t, "export_dub", map[string]any{"dub_id": d.ID, "format": "wav", "language": "fr"}, "not_ready")
	e.fails(t, "export_dub", map[string]any{"dub_id": d.ID, "format": "mp3"}, api.CodeUnsupportedFormat)

	// REST.
	status, body := e.rest(t, "GET", "/api/dubs/"+d.ID+"?segments=false", nil)
	if status != 200 || strings.Contains(string(body), `"segments"`) || !strings.Contains(string(body), `"phase":"done"`) {
		t.Fatalf("GET dub %d %s", status, body)
	}
	if status, body = e.rest(t, "PATCH", "/api/dubs/"+d.ID, map[string]any{"cast": map[string]string{"S9": "x"}}); status != 400 {
		t.Fatalf("PATCH %d %s", status, body)
	}
	if status, body = e.rest(t, "POST", "/api/dubs/"+d.ID+"/run", map[string]any{"through": "translation", "retranslate": true}); status != 202 {
		t.Fatalf("run %d %s", status, body)
	}
	e.wait(t, "dub:"+d.ID)
	if status, body = e.rest(t, "POST", "/api/dubs/"+d.ID+"/export", map[string]any{"format": "vtt", "directory": t.TempDir()}); status != 202 {
		t.Fatalf("export %d %s", status, body)
	}
	if status, body = e.rest(t, "POST", "/api/dubs", map[string]any{"path": src, "through": "none"}); status != 201 || !strings.Contains(string(body), `"running":false`) {
		t.Fatalf("POST /api/dubs %d %s", status, body)
	}
	var d2 api.DubJSON
	json.Unmarshal(body, &d2)
	if status, body = e.rest(t, "DELETE", "/api/dubs/"+d2.ID, nil); status != 400 || !strings.Contains(string(body), api.CodeConfirmRequired) {
		t.Fatalf("DELETE %d %s", status, body)
	}
	if status, _ = e.rest(t, "DELETE", "/api/dubs/"+d2.ID+"?confirm=1", nil); status != 200 {
		t.Fatalf("DELETE %d", status)
	}
	e.fails(t, "delete_item", map[string]any{"kind": "dub", "id": d.ID}, api.CodeConfirmRequired)
	e.must(t, "delete_item", map[string]any{"kind": "dub", "id": d.ID, "confirm": true}, nil)
	e.fails(t, "get_dub", map[string]any{"dub_id": d.ID}, api.CodeNotFound)
}

// TestStudioBatch: a folder dubbed by the batch queue (the page's
// settings untouched), the queue and its watch folder listed.
func TestStudioBatch(t *testing.T) {
	e := newStudioEnv(t)
	dir := t.TempDir()
	for _, n := range []string{"a.wav", "b.wav"} {
		if _, err := dubtest.Speech(filepath.Join(dir, n), 2); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o644)
	before := e.a.dubSvc.BatchSettings(context.Background())
	var b api.BatchJSON
	e.must(t, "batch_dub", map[string]any{"folder": dir, "target_languages": []string{"fr"}, "paths": []string{filepath.Join(dir, "missing.wav")}}, &b)
	if len(b.Added) != 2 || len(b.Errors) != 1 || b.Errors[0].Code != api.CodeNotFound {
		t.Fatalf("batch %+v", b)
	}
	for _, it := range b.Added {
		if j := e.wait(t, it.JobID); j.State != "succeeded" || !slices.Equal(it.Languages, []string{"fr"}) {
			t.Fatalf("%s: %+v", it.Title, j)
		}
	}
	if after := e.a.dubSvc.BatchSettings(context.Background()); !slices.Equal(after.Targets, before.Targets) {
		t.Errorf("the page's settings changed: %v → %v", before.Targets, after.Targets)
	}
	e.must(t, "list_batch", nil, &b)
	if len(b.Queue) != 2 || b.Watch == nil || b.Watch.Active || b.Prefs == nil {
		t.Fatalf("queue %+v", b)
	}
	e.fails(t, "batch_dub", map[string]any{}, api.CodeInvalid)
	if status, body := e.rest(t, "GET", "/api/batch", nil); status != 200 || !strings.Contains(string(body), `"watch_folder"`) {
		t.Fatalf("GET /api/batch %d %s", status, body)
	}
	if status, body := e.rest(t, "POST", "/api/batch", map[string]any{"folder": "/no/such/folder"}); status != 404 {
		t.Fatalf("POST /api/batch %d %s", status, body)
	}
}

// TestStudioGallery: search_voices gives what the page's filters give;
// one voice by id; "Use voice" saves it once.
func TestStudioGallery(t *testing.T) {
	e := newStudioEnv(t)
	for _, f := range []map[string]string{
		{"query": "warm", "gender": "female"},
		{"category": "narration", "language": "en", "age": "elderly"},
		{"accent": "british", "style": ""},
		{"query": "旁白"},
	} {
		var page api.GalleryPage
		args := map[string]any{"limit": 100}
		for k, v := range f {
			args[k] = v
		}
		e.must(t, "search_voices", args, &page)
		// The page's query for the same filters.
		g := galleryFilterQuery(f["query"], f["category"], map[gallery.Facet]string{gallery.FacetGender: f["gender"], gallery.FacetAge: f["age"],
			gallery.FacetAccent: f["accent"], gallery.FacetLanguage: f["language"], gallery.FacetStyle: f["style"]})
		g.Limit = 100
		ui := gallery.Default().Search(g)
		if page.Total != ui.Total || len(page.Items) != len(ui.Items) || page.Total == 0 {
			t.Fatalf("%v: %d/%d, the page %d/%d", f, len(page.Items), page.Total, len(ui.Items), ui.Total)
		}
		for i := range ui.Items {
			if page.Items[i].ID != ui.Items[i].ID {
				t.Fatalf("%v: item %d is %s, the page's %s", f, i, page.Items[i].ID, ui.Items[i].ID)
			}
		}
		if len(page.Facets["gender"]) == 0 {
			t.Errorf("no facets")
		}
	}
	// The page's own state gives the same.
	var page api.GalleryPage
	e.must(t, "search_voices", map[string]any{"category": "news", "gender": "male", "limit": 60}, &page)
	done := make(chan gallery.Page)
	e.a.postQueue(func() {
		g := &e.a.gallery
		g.tab = slices.IndexFunc(gallery.Categories(), func(c gallery.Category) bool { return c.ID == "news" }) + 1
		g.filters[gallery.FacetGender] = "male"
		g.limit = 60
		done <- e.a.galleryResults()
	})
	if ui := <-done; ui.Total != page.Total || len(ui.Items) != len(page.Items) || ui.Items[0].ID != page.Items[0].ID {
		t.Fatalf("page state %d, search_voices %d", ui.Total, page.Total)
	}

	id := page.Items[0].ID
	e.must(t, "search_voices", map[string]any{"id": id}, &page)
	if len(page.Items) != 1 || page.Items[0].ID != id || page.Items[0].Sample == "" {
		t.Fatalf("by id %+v", page)
	}
	e.fails(t, "search_voices", map[string]any{"id": "nope"}, api.CodeNotFound)
	e.fails(t, "search_voices", map[string]any{"category": "nope"}, api.CodeInvalid)
	var use api.GalleryUse
	e.must(t, "use_gallery_voice", map[string]any{"id": id}, &use)
	if !use.Created || use.Voice.Kind != "designed" || use.Voice.Origin != "gallery:"+id || use.Model == "" || len(use.Models) == 0 || !use.Downloaded {
		t.Fatalf("use %+v", use)
	}
	e.must(t, "use_gallery_voice", map[string]any{"id": id}, &use)
	if use.Created {
		t.Fatal("saved twice")
	}
	e.must(t, "search_voices", map[string]any{"id": id}, &page)
	if page.Items[0].Saved != use.Voice.ID {
		t.Errorf("saved id %+v", page.Items[0])
	}
	e.fails(t, "use_gallery_voice", map[string]any{"id": "nope"}, api.CodeNotFound)
	if status, body := e.rest(t, "GET", "/api/gallery?gender=female&limit=3", nil); status != 200 || !strings.Contains(string(body), `"facets"`) {
		t.Fatalf("GET /api/gallery %d %s", status, body)
	}
	if status, _ := e.rest(t, "GET", "/api/gallery/"+id, nil); status != 200 {
		t.Fatalf("GET /api/gallery/{id} %d", status)
	}
	if status, _ := e.rest(t, "GET", "/api/gallery/nope", nil); status != 404 {
		t.Fatalf("GET missing %d", status)
	}
	if status, body := e.rest(t, "POST", "/api/gallery/"+id+"/use", nil); status != 200 || !strings.Contains(string(body), use.Voice.ID) {
		t.Fatalf("POST use %d %s", status, body)
	}
}

// TestStudioExportsAndHistory: a take exported with the dialog's
// options (format, rate, channels, loudness, trim, extras), an M4A
// through the engine's encoder, MP3 refused without ffmpeg; a transcript
// as SRT; the History's takes with their files.
func TestStudioExportsAndHistory(t *testing.T) {
	e := newStudioEnv(t)
	take := core.Take{ID: core.NewID(time.Now()), Text: "Welcome to the show. This is AudioSmith.", Model: kokoro, Origin: core.TakeAPI, CreatedAt: time.Now(),
		Render: &core.TakeRender{Chunks: []core.TakeChunk{{Text: "Welcome to the show.", Start: 0, End: 0.9}, {Text: "This is AudioSmith.", Start: 0.9, End: 1.8}}}}
	take.File = take.ID + ".wav"
	writeTestWAV(t, e.lib.svc.Takes.AudioPath(take), 24000, 2)
	if _, err := e.lib.svc.Takes.Add(context.Background(), take); err != nil {
		t.Fatal(err)
	}
	var x api.ExportJSON
	e.must(t, "export_audio", map[string]any{"take_id": take.ID, "bits": 16, "sample_rate": 48000, "channels": 2, "loudness": "podcast",
		"trim": true, "script": true, "subtitles": true, "filename": "show"}, &x)
	if len(x.Files) != 3 || filepath.Base(x.Files[0]) != "show.wav" || x.Loudness > -15 || x.Loudness < -17 {
		t.Fatalf("export %+v", x)
	}
	if _, info, err := wav.ReadFile(x.Files[0]); err != nil || info.Encoding.Bits() != 16 || info.Rate != 48000 || info.Channels != 2 {
		t.Fatalf("wav %v %+v", err, info)
	}
	if srt, _ := os.ReadFile(filepath.Join(e.out, "show.srt")); !strings.Contains(string(srt), "Welcome to the show.") {
		t.Fatalf("srt %s", srt)
	}
	e.must(t, "export_audio", map[string]any{"take_id": take.ID, "format": "m4a", "bitrate_kbps": 192, "title": "Show"}, &x)
	data, _ := os.ReadFile(x.Files[0])
	if !bytes.HasPrefix(data, []byte("FAKE-M4A")) || !strings.Contains(string(data), `"bitrate":192000`) || !strings.Contains(string(data), `"Show"`) {
		t.Fatalf("m4a %s", data)
	}
	e.fails(t, "export_audio", map[string]any{"take_id": take.ID, "format": "mp3"}, api.CodeUnsupportedFormat)
	e.fails(t, "export_audio", map[string]any{"take_id": take.ID, "bits": 12}, api.CodeInvalid)
	e.fails(t, "export_audio", map[string]any{"take_id": "missing"}, api.CodeNotFound)
	if status, body := e.rest(t, "POST", "/api/takes/"+take.ID+"/export", map[string]any{"format": "wav", "bits": 24}); status != 200 || !strings.Contains(string(body), ".wav") {
		t.Fatalf("POST export %d %s", status, body)
	}

	// The History.
	var raw struct {
		Takes []map[string]any `json:"takes"`
	}
	e.must(t, "list_takes", map[string]any{"search": "show"}, &raw)
	if len(raw.Takes) != 1 || raw.Takes[0]["file"] != e.lib.svc.Takes.AudioPath(take) || raw.Takes[0]["model"] != catalog.Builtin().Entries[0].ID {
		t.Fatalf("takes %+v", raw)
	}
	e.must(t, "list_takes", map[string]any{"take_id": take.ID}, &raw)
	if len(raw.Takes) != 1 {
		t.Fatal("by id")
	}

	// A transcript as SRT.
	tr := core.Transcript{ID: core.NewID(time.Now()), Title: "meeting.m4a (API)", Language: "en", Duration: 2,
		Segments: []core.TranscriptSegment{{Start: 0, End: 1.5, Text: "Hello from the meeting."}}}
	tr.CreatedAt, tr.UpdatedAt = time.Now(), time.Now()
	if err := e.lib.store.Transcripts().Put(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	e.must(t, "export_transcript", map[string]any{"transcript_id": tr.ID, "format": "srt"}, &x)
	if len(x.Files) != 1 || filepath.Base(x.Files[0]) != "meeting.srt" {
		t.Fatalf("transcript %+v", x)
	}
	if data, _ := os.ReadFile(x.Files[0]); !strings.Contains(string(data), "00:00:00,000 --> ") {
		t.Fatalf("srt %s", data)
	}
	e.fails(t, "export_transcript", map[string]any{"transcript_id": tr.ID, "format": "doc"}, api.CodeInvalid)
	if status, body := e.rest(t, "POST", "/api/transcripts/"+tr.ID+"/export", map[string]any{"format": "vtt", "filename": "m"}); status != 200 || !strings.Contains(string(body), "m.vtt") {
		t.Fatalf("POST transcript export %d %s", status, body)
	}
	var items struct{ Items []api.ProjectJSON }
	e.must(t, "list_projects", map[string]any{"category": "export"}, &items)
	if len(items.Items) < 3 {
		t.Errorf("exports listed: %d", len(items.Items))
	}
}

// TestStudioWorkflows: a saved workflow listed with its inputs and run
// with new scripts as a job, to its files; errors.
func TestStudioWorkflows(t *testing.T) {
	e := newStudioEnv(t)
	tpl, _ := workflow.TemplateByID("narration")
	w := tpl.Make("", "Lessons", time.Now(), workflow.TemplateOptions{Model: kokoro})
	saved, err := e.a.wfSvc.Save(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	var list struct{ Workflows []api.WorkflowJSON }
	e.must(t, "list_workflows", nil, &list)
	if len(list.Workflows) != 1 || list.Workflows[0].ID != saved.ID || list.Workflows[0].Input != "scripts" || len(list.Workflows[0].Scripts) != 1 {
		t.Fatalf("workflows %+v", list)
	}
	var j jobAnswer
	e.must(t, "run_workflow", map[string]any{"workflow_id": saved.ID, "scripts": []string{"Lesson one.", "Lesson two."}}, &j)
	if j.JobID != "workflow:"+saved.ID {
		t.Fatalf("run %+v", j)
	}
	j = e.wait(t, j.JobID)
	if j.State != "succeeded" || len(j.Files) != 2 {
		t.Fatalf("ran %+v", j)
	}
	var res struct {
		Items []struct {
			Name, State string
		} `json:"items"`
	}
	json.Unmarshal(j.Result, &res)
	if len(res.Items) != 2 || res.Items[0].State != "done" {
		t.Fatalf("result %s", j.Result)
	}
	e.fails(t, "run_workflow", map[string]any{"workflow_id": saved.ID, "files": []string{"/no/such.wav"}}, api.CodeNotFound)
	e.fails(t, "run_workflow", map[string]any{"workflow_id": "missing"}, api.CodeNotFound)
	if status, body := e.rest(t, "POST", "/api/workflows/"+saved.ID+"/run", map[string]any{"again": true}); status != 202 {
		t.Fatalf("POST run %d %s", status, body)
	}
	e.wait(t, "workflow:"+saved.ID)
	if status, body := e.rest(t, "GET", "/api/workflows", nil); status != 200 || !strings.Contains(string(body), "Lessons") || !strings.Contains(string(body), "Lesson two.") {
		t.Fatalf("GET %d %s", status, body)
	}
	// Group job events end with "done".
	resp, err := http.Get(e.ts.URL + "/api/jobs/workflow:" + saved.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(data), "event: done") {
		t.Fatalf("events %s", data)
	}
}
