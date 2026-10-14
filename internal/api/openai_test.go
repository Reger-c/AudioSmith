package api

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/translate"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

// golden compares got (JSON) with testdata/name, or rewrites it with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("%s: not JSON: %v\n%s", name, err, got)
	}
	norm, _ := json.MarshalIndent(v, "", "  ")
	path := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile(path, append(norm, '\n'), 0o644)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run with -update)", name, err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(string(norm)) {
		t.Fatalf("%s differs:\n got %s\nwant %s", name, norm, want)
	}
}

// openAIHeaders are the headers the official SDKs send (openai-python
// 1.x): a Bearer key the server ignores on loopback, and Stainless's.
func openAIHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer sk-not-needed")
	req.Header.Set("User-Agent", "OpenAI/Python 1.99.0")
	req.Header.Set("X-Stainless-Lang", "python")
	req.Header.Set("X-Stainless-Package-Version", "1.99.0")
	req.Header.Set("Accept", "application/octet-stream")
}

// sdkSpeech posts testdata/<file> as openai-python's audio.speech.create does.
func sdkSpeech(t *testing.T, ts *httptest.Server, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	openAIHeaders(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// parseWAV reads a WAV (streamed or not): its rate and its 16-bit data.
func parseWAV(t *testing.T, b []byte) (rate int, data []byte) {
	t.Helper()
	if len(b) < 44 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatalf("not a WAV: % x", b[:min(len(b), 16)])
	}
	p := 12
	for p+8 <= len(b) {
		id, n := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:]))
		switch id {
		case "fmt ":
			rate = int(binary.LittleEndian.Uint32(b[p+12:]))
		case "data":
			if n == -1 || uint32(n) == 0xFFFFFFFF || p+8+n > len(b) {
				return rate, b[p+8:]
			}
			return rate, b[p+8 : p+8+n]
		}
		p += 8 + n + n%2
	}
	t.Fatal("no data chunk")
	return
}

func TestSpeechOpenAISDK(t *testing.T) {
	f := newFake(t)
	_, ts := f.server()
	// client.audio.speech.create(model="tts-1", voice="alloy", input=…): no
	// response_format, so WAV (OpenAI's mp3 needs an encoder).
	resp := sdkSpeech(t, ts, readGolden(t, "speech_request.json"))
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("%d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	rate, data := parseWAV(t, body)
	def := defaultTTS(f.Settings())
	if rate != 24000 || len(data) != 2*24000*len("Today is a wonderful day to build something people love!")/20 {
		t.Fatalf("rate %d, %d bytes", rate, len(data))
	}
	if resp.Header.Get("X-AudioSmith-Model") != def.ID || resp.Header.Get("X-AudioSmith-Take-Id") == "" || resp.Header.Get("X-AudioSmith-Voice") != "alloy" {
		t.Fatalf("headers %v", resp.Header)
	}
	f.mu.Lock()
	req := f.generated[0]
	f.mu.Unlock()
	if req.Model.ID != def.ID || req.Gender != "" || req.Normalize || req.Background {
		t.Fatalf("request %+v", req)
	}
}

func TestSpeechFormats(t *testing.T) {
	f := newFake(t)
	f.renderRate = 48000
	_, ts := f.server()
	text := "Twenty characters!!"
	// pcm: 24 kHz mono s16le, streamed (resampled from 48 kHz).
	resp := sdkSpeech(t, ts, `{"model":"gpt-4o-mini-tts","input":"`+text+`","voice":"coral","response_format":"pcm","instructions":"Speak cheerfully."}`)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/pcm" {
		t.Fatalf("pcm %d %s", resp.StatusCode, body)
	}
	want := 2 * 24000 * len(text) / 20
	if d := len(body) - want; d < -64 || d > 64 {
		t.Fatalf("pcm: %d bytes, want ≈%d", len(body), want)
	}
	f.mu.Lock()
	req := f.generated[0]
	f.mu.Unlock()
	if req.Gender != "female" || req.Instructions != "Speak cheerfully." {
		t.Fatalf("request %+v", req)
	}
	// normalize: the whole take, with its length.
	resp = sdkSpeech(t, ts, `{"model":"tts-1","input":"`+text+`","voice":"alloy","response_format":"wav","normalize":true}`)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Length") == "" || resp.Header.Get("X-AudioSmith-Seed") != "42" {
		t.Fatalf("normalized %d %v", resp.StatusCode, resp.Header)
	}
	if rate, data := parseWAV(t, body); rate != 48000 || len(data) != 2*48000*len(text)/20 {
		t.Fatalf("normalized: %d Hz, %d bytes", rate, len(data))
	}
	// aac: through media.encode.
	resp = sdkSpeech(t, ts, `{"model":"tts-1","input":"`+text+`","voice":"alloy","response_format":"aac"}`)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/mp4" || !bytes.HasPrefix(body, []byte("M4A:RIFF")) {
		t.Fatalf("aac %d %s %q", resp.StatusCode, resp.Header.Get("Content-Type"), body[:min(len(body), 8)])
	}
	entries, _ := os.ReadDir(filepath.Join(f.TempDir(), "encoded"))
	if len(entries) != 0 {
		t.Fatalf("encoded files left: %v", entries)
	}
	f.update(func(f *fakeBackend) { f.canEncode = false })
	resp = sdkSpeech(t, ts, `{"model":"tts-1","input":"x","voice":"alloy","response_format":"aac"}`)
	var e apiErr
	json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != 400 || e.Error.Code != CodeUnsupportedFormat {
		t.Fatalf("aac without encoder: %d %+v", resp.StatusCode, e)
	}
}

func TestSpeechSSE(t *testing.T) {
	f := newFake(t)
	_, ts := f.server()
	resp := sdkSpeech(t, ts, `{"model":"gpt-4o-mini-tts","input":"Hello there, friend.","voice":"nova","response_format":"pcm","stream_format":"sse"}`)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	var audio []byte
	var types []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<22)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type  string         `json:"type"`
			Audio string         `json:"audio"`
			Usage map[string]int `json:"usage"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		types = append(types, ev.Type)
		if ev.Type == "speech.audio.delta" {
			b, _ := base64.StdEncoding.DecodeString(ev.Audio)
			audio = append(audio, b...)
		}
		if ev.Type == "speech.audio.done" && ev.Usage["input_tokens"] != len("Hello there, friend.") {
			t.Fatalf("usage %v", ev.Usage)
		}
	}
	if len(types) != 3 || types[0] != "speech.audio.delta" || types[2] != "speech.audio.done" {
		t.Fatalf("events %v", types)
	}
	if want := 2 * 24000 * len("Hello there, friend.") / 20; len(audio) != want {
		t.Fatalf("audio %d bytes, want %d", len(audio), want)
	}
}

func TestSpeechErrors(t *testing.T) {
	f := newFake(t)
	f.voices = []core.Voice{{ID: "v1", Name: "Ana", Kind: core.VoiceCloned}, {ID: "v2", Name: "Twin", Kind: core.VoiceCloned}, {ID: "v3", Name: "twin", Kind: core.VoiceCloned}}
	_, ts := f.server()
	omni, _ := catalog.Builtin().ByID("omnivoice-bf16")
	kitten, _ := catalog.Builtin().ByID("kitten-tts-mini-0.8")
	f.licence[omni.Repo] = true
	f.notReady[kitten.Repo] = true
	for _, c := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"empty input", `{"model":"tts-1","input":"  ","voice":"alloy"}`, 400, CodeInvalid},
		{"long input", `{"model":"tts-1","input":"` + strings.Repeat("a", 4097) + `","voice":"alloy"}`, 400, CodeInvalid},
		{"mp3", `{"model":"tts-1","input":"hi","voice":"alloy","response_format":"mp3"}`, 400, CodeUnsupportedFormat},
		{"opus", `{"model":"tts-1","input":"hi","voice":"alloy","response_format":"opus"}`, 400, CodeUnsupportedFormat},
		{"speed", `{"model":"tts-1","input":"hi","voice":"alloy","speed":5}`, 400, CodeInvalid},
		{"stream format", `{"model":"tts-1","input":"hi","voice":"alloy","stream_format":"chunks"}`, 400, CodeInvalid},
		{"unknown model", `{"model":"tts-9000","input":"hi","voice":"alloy"}`, 404, CodeModelNotFound},
		{"unknown voice", `{"model":"tts-1","input":"hi","voice":"Bob"}`, 400, CodeVoiceNotFound},
		{"ambiguous voice", `{"model":"tts-1","input":"hi","voice":"TWIN"}`, 409, CodeAmbiguousVoice},
		{"clone on a model that can't", `{"model":"kokoro-82m-bf16","input":"hi","voice":"Ana"}`, 400, CodeUnsupportedModel},
		{"licence", `{"model":"omnivoice-bf16","input":"hi"}`, 403, CodeLicenceRequired},
		{"not downloaded", `{"model":"kitten-tts-mini-0.8","input":"hi"}`, 409, CodeNotDownloaded},
		{"bad json", `{"model":`, 400, CodeInvalid},
	} {
		resp := sdkSpeech(t, ts, c.body)
		var e apiErr
		json.NewDecoder(resp.Body).Decode(&e)
		if resp.StatusCode != c.status || e.Error.Code != c.code || e.Error.Message == "" || e.Error.Type == "" {
			t.Errorf("%s: %d %+v", c.name, resp.StatusCode, e)
		}
	}
	// The engine is down: 503 with Retry-After.
	f.update(func(f *fakeBackend) { f.status = Status{State: StateFailed, Detail: "it crashed", RetryAfter: 7} })
	resp := sdkSpeech(t, ts, `{"model":"tts-1","input":"hi","voice":"alloy"}`)
	var e apiErr
	json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") != "7" || e.Error.Code != CodeEngineUnavailable || e.Error.Type != "server_error" {
		t.Fatalf("down: %d %v %+v", resp.StatusCode, resp.Header, e)
	}
	// A failing render: the job's error.
	f.update(func(f *fakeBackend) {
		f.status = Status{State: StateReady}
		f.failRender = &jobs.Error{Code: "oom", Message: "out of memory", Retryable: true}
	})
	resp = sdkSpeech(t, ts, `{"model":"tts-1","input":"hi","voice":"alloy","normalize":true}`)
	json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != 503 || e.Error.Code != "oom" {
		t.Fatalf("oom: %d %+v", resp.StatusCode, e)
	}
}

func TestSpeechVoices(t *testing.T) {
	f := newFake(t)
	f.voices = []core.Voice{{ID: "c1", Name: "Narrator", Kind: core.VoiceCloned, Model: "mlx-community/chatterbox-8bit"}}
	_, ts := f.server()
	// A saved clone by name: the default (Kokoro) can't clone, so its own model.
	resp := sdkSpeech(t, ts, `{"model":"tts-1","input":"hi","voice":"narrator","response_format":"wav","normalize":true}`)
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("X-AudioSmith-Model") != "chatterbox-8bit" || resp.Header.Get("X-AudioSmith-Voice") != "Narrator" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	// A built-in speaker; OpenAI's {"id": …} form; ignored parameters.
	resp = sdkSpeech(t, ts, `{"model":"kokoro-82m-bf16","input":"hi","voice":{"id":"af_heart"},"seed":3,"instructions":"whisper"}`)
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("X-AudioSmith-Ignored") != "instructions, seed" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	f.mu.Lock()
	reqs := f.generated
	f.mu.Unlock()
	if reqs[0].Voice == nil || reqs[0].Voice.ID != "c1" || reqs[1].Speaker != "af_heart" || reqs[1].Seed != 3 {
		t.Fatalf("requests %+v", reqs)
	}
	// /v1/audio/voices lists OpenAI's names, saved voices and speakers.
	var list struct {
		Data []map[string]any `json:"data"`
	}
	do(t, ts, "GET", "/v1/audio/voices", nil, &list)
	types := map[string]int{}
	for _, v := range list.Data {
		types[v["type"].(string)]++
	}
	if types["openai"] != 13 || types["saved"] != 1 || types["builtin"] < 20 {
		t.Fatalf("voices %v", types)
	}
}

// multipartFile builds openai-python's multipart body for a file upload.
func multipartFile(t *testing.T, name string, content []byte, fields ...string) (string, *bytes.Buffer) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for i := 0; i+1 < len(fields); i += 2 {
		w.WriteField(fields[i], fields[i+1])
	}
	fw, _ := w.CreateFormFile("file", name)
	fw.Write(content)
	w.Close()
	return w.FormDataContentType(), &b
}

func sdkTranscribe(t *testing.T, ts *httptest.Server, path string, fields ...string) *http.Response {
	t.Helper()
	ct, body := multipartFile(t, "interview.m4a", []byte("fake audio"), fields...)
	req, _ := http.NewRequest("POST", ts.URL+path, body)
	req.Header.Set("Content-Type", ct)
	openAIHeaders(req)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestTranscriptionsOpenAISDK(t *testing.T) {
	f := newFake(t)
	_, ts := f.server()
	// client.audio.transcriptions.create(model="whisper-1", file=…)
	resp := sdkTranscribe(t, ts, "/v1/audio/transcriptions", "model", "whisper-1")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	golden(t, "transcription_json.json", body)
	// verbose_json with word and segment timestamps.
	resp = sdkTranscribe(t, ts, "/v1/audio/transcriptions", "model", "whisper-1", "response_format", "verbose_json",
		"timestamp_granularities[]", "word", "timestamp_granularities[]", "segment", "language", "fr")
	body, _ = io.ReadAll(resp.Body)
	golden(t, "transcription_verbose.json", body)
	for _, format := range []string{"text", "srt", "vtt"} {
		resp = sdkTranscribe(t, ts, "/v1/audio/transcriptions", "model", "gpt-4o-transcribe", "response_format", format)
		body, _ = io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || !strings.Contains(string(body), "Bonjour") {
			t.Fatalf("%s: %d %s", format, resp.StatusCode, body)
		}
		if format == "vtt" && !strings.HasPrefix(string(body), "WEBVTT") || format == "srt" && !strings.HasPrefix(string(body), "1\n") {
			t.Fatalf("%s: %s", format, body)
		}
	}
	f.mu.Lock()
	reqs := f.transcribed
	f.mu.Unlock()
	accurate, fast := defaultSTT(f.st, false), defaultSTT(f.st, true)
	if reqs[0].Model != accurate.Repo || reqs[0].Words || reqs[1].Language != "fr" || !reqs[1].Words {
		t.Fatalf("requests %+v", reqs[:2])
	}
	resp = sdkTranscribe(t, ts, "/v1/audio/transcriptions", "model", "gpt-4o-mini-transcribe")
	io.Copy(io.Discard, resp.Body)
	f.mu.Lock()
	last := f.transcribed[len(f.transcribed)-1]
	f.mu.Unlock()
	if last.Model != fast.Repo || last.Mode != "fast" {
		t.Fatalf("mini: %+v", last)
	}
	// Uploads are removed.
	entries, _ := os.ReadDir(filepath.Join(f.TempDir(), "uploads"))
	if len(entries) != 0 {
		t.Fatalf("uploads left: %v", entries)
	}
	// Errors.
	resp = sdkTranscribe(t, ts, "/v1/audio/transcriptions", "model", "kokoro-82m-bf16")
	var e apiErr
	json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != 404 || e.Error.Code != CodeModelNotFound {
		t.Fatalf("tts model: %d %+v", resp.StatusCode, e)
	}
	resp = sdkTranscribe(t, ts, "/v1/audio/transcriptions", "model", "whisper-1", "response_format", "xml")
	json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != 400 || e.Error.Code != CodeUnsupportedFormat {
		t.Fatalf("xml: %d %+v", resp.StatusCode, e)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/v1/audio/transcriptions", strings.NewReader(`{"model":"whisper-1"}`))
	req.Header.Set("Content-Type", "application/json")
	r2, _ := http.DefaultClient.Do(req)
	if r2.StatusCode != 400 {
		t.Fatalf("no file: %d", r2.StatusCode)
	}
	r2.Body.Close()
}

func TestTranslations(t *testing.T) {
	f := newFake(t)
	_, ts := f.server()
	resp := sdkTranscribe(t, ts, "/v1/audio/translations", "model", "whisper-1")
	var e apiErr
	json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != 501 || e.Error.Code != CodeTranslationNotReady {
		t.Fatalf("no LLM: %d %+v", resp.StatusCode, e)
	}
	f.update(func(f *fakeBackend) {
		f.translator = func(req translate.Request) (translate.Result, error) {
			if req.Target != "en" || req.Source != "fr" {
				t.Errorf("translate %s → %s", req.Source, req.Target)
			}
			en := map[string]string{"Bonjour tout le monde.": "Hello everyone.", "Ça va bien.": "All is well."}
			var res translate.Result
			for _, s := range req.Segments {
				res.Segments = append(res.Segments, translate.Translated{ID: s.ID, Text: en[s.Text]})
			}
			return res, nil
		}
	})
	resp = sdkTranscribe(t, ts, "/v1/audio/translations", "model", "whisper-1", "response_format", "verbose_json")
	body, _ := io.ReadAll(resp.Body)
	golden(t, "translation_verbose.json", body)
}

func TestModelsOpenAI(t *testing.T) {
	f := newFake(t)
	_, ts := f.server()
	var list struct {
		Object string        `json:"object"`
		Data   []openAIModel `json:"data"`
	}
	do(t, ts, "GET", "/v1/models", nil, &list)
	ids := map[string]openAIModel{}
	for _, m := range list.Data {
		ids[m.ID] = m
	}
	def := defaultTTS(f.st)
	if list.Object != "list" || ids["tts-1"].AudioSmith["alias_for"] != def.ID || ids["whisper-1"].AudioSmith["kind"] != "stt" ||
		ids["kokoro-82m-bf16"].Object != "model" || ids["kokoro-82m-bf16"].OwnedBy != "mlx-community" {
		t.Fatalf("models %+v", list)
	}
	if _, ok := ids["silero-vad"]; ok {
		t.Fatal("non-speech model listed")
	}
	var one openAIModel
	do(t, ts, "GET", "/v1/models/gpt-4o-mini-tts", nil, &one)
	if one.ID != "gpt-4o-mini-tts" {
		t.Fatalf("one %+v", one)
	}
	resp := do(t, ts, "GET", "/v1/models/nope", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatal(resp.StatusCode)
	}
}
