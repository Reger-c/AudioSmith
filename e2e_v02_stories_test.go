package main

// TestV02's Stories and Audiobook steps; split from e2e_v02_test.go.

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/services"
)

// stories: a two-character script, auto-cast on Kokoro, rendered.
func (v *v02) stories(t *testing.T) string {
	a := v.a
	p := a.lfStories
	a.newLf(p)
	v.drain()
	p.doc.Model = v.model(t, "kokoro-82m-bf16").Repo
	p.doc.ImportText = "# The Storm\n\nThe rain fell all night on the little town.\n\nANNA: Did you hear that noise outside?\n\nBEN: It was only the wind. Go back to sleep.\n\nANNA: I am not so sure. Someone is at the door.\n\nBEN: Fine. I will go and look."
	a.autoCastStory(p)
	p.doc.Title = "V02 Story"
	if len(p.doc.Cast) != 2 {
		t.Fatalf("cast %+v", p.doc.Cast)
	}
	if msg := a.lfBlocker(p); msg != "" {
		t.Fatalf("blocked: %s", msg)
	}
	started := time.Now()
	a.startLf(p, "")
	v.wait(t, "story render start", time.Minute, func() bool { return p.render != "" })
	r := v.waitRender(t, p.render, 20*time.Minute)
	out := r.body.Output
	buf, _, err := wav.ReadFile(out.Book)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if buf.Seconds() < 10 {
		t.Errorf("story %.1f s", buf.Seconds())
	}
	return fmt.Sprintf("%d lines, 3 voices → %.1f s in %.0f s, %.1f LUFS", len(p.doc.Lines), buf.Seconds(), time.Since(started).Seconds(), loudness.Integrated(buf))
}

// waitRender waits for a longform render to end and returns it.
func (v *v02) waitRender(t *testing.T, id string, timeout time.Duration) *lfRender {
	t.Helper()
	a := v.a
	v.wait(t, "render "+id, timeout, func() bool {
		r := a.lfRenders[id]
		return r != nil && (r.rec.State == core.RenderDone || r.rec.State == core.RenderFailed || r.rec.State == core.RenderStopped) && !r.body.Assembling
	})
	// The live snapshot carries the summary; read the whole body.
	rec, body, err := a.lib.svc.Longform.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != core.RenderDone || body.Output == nil {
		t.Fatalf("render %s: %s %+v", id, rec.State, body.Error)
	}
	return &lfRender{rec: rec, body: body}
}

// audiobook: a TXT of two chapters, ACX loudness, M4B with a cover.
func (v *v02) audiobook(t *testing.T) string {
	a := v.a
	p := a.lfBook
	a.newLf(p)
	v.drain()
	book := filepath.Join(v.dir, "book.txt")
	os.WriteFile(book, []byte("Chapter One\n\n"+longText(enSentences[:5], 700)+"\n\nChapter Two\n\n"+longText(enSentences[5:], 700)+"\n"), 0o644)
	a.importBookFrom(p, book)
	v.wait(t, "import", time.Minute, func() bool { return !p.importing && strings.Contains(p.doc.Script, "Chapter Two") })
	cover := filepath.Join(v.dir, "cover.png")
	img := image.NewRGBA(image.Rect(0, 0, 600, 600))
	for y := range 600 {
		for x := range 600 {
			img.Set(x, y, color.RGBA{uint8(x / 3), 80, uint8(y / 3), 255})
		}
	}
	f, _ := os.Create(cover)
	png.Encode(f, img)
	f.Close()
	p.doc.Model = v.model(t, "kokoro-82m-bf16").Repo
	p.doc.Voice = services.SpeakerRef + "bm_george"
	p.doc.Title, p.doc.Book.Author, p.doc.Book.Cover = "V02 Book", "AudioSmith", cover
	p.doc.Book.Loudness = services.LoudnessACX
	p.doc.Format = services.FormatM4B
	if msg := a.lfBlocker(p); msg != "" {
		t.Fatalf("blocked: %s", msg)
	}
	started := time.Now()
	a.startLf(p, "")
	v.wait(t, "book render start", time.Minute, func() bool { return p.render != "" })
	r := v.waitRender(t, p.render, 30*time.Minute)
	out := r.body.Output
	if len(out.Chapters) != 2 {
		t.Errorf("chapters %+v", out.Chapters)
	}
	if out.Encoded == "" {
		t.Fatalf("no M4B: %s", out.EncodeError)
	}
	note := fmt.Sprintf("2 chapters → %.0f s in %.0f s, book %.1f LUFS, M4B %s", out.Seconds, time.Since(started).Seconds(), out.Loudness.Integrated, filepath.Base(out.Encoded))
	for _, c := range out.Chapters {
		if c.Report.Integrated < -24 || c.Report.Integrated > -18 || c.Report.TruePeak > -3 {
			t.Errorf("ACX chapter %q: %.1f LUFS, %.1f dBTP", c.Title, c.Report.Integrated, c.Report.TruePeak)
		}
	}
	cp := filepath.Join(v.dir, "V02 Book.m4b")
	copyWAV(out.Encoded, cp)
	if m := ffprobeJSON(t, "-show_chapters", "-show_streams", "-show_format", out.Encoded); m != nil {
		chs, _ := m["chapters"].([]any)
		streams, _ := m["streams"].([]any)
		var kinds []string
		pic := false
		for _, s := range streams {
			s := s.(map[string]any)
			kinds = append(kinds, fmt.Sprint(s["codec_type"], ":", s["codec_name"]))
			if d, ok := s["disposition"].(map[string]any); ok && d["attached_pic"] == float64(1) {
				pic = true
			}
		}
		tags := map[string]any{}
		if f, ok := m["format"].(map[string]any); ok {
			tags, _ = f["tags"].(map[string]any)
		}
		note += fmt.Sprintf(", ffprobe: %d chapters, streams %v, cover %v, title %v, artist %v", len(chs), kinds, pic, tags["title"], tags["artist"])
		if len(chs) != 2 || !pic || !slices.Contains(kinds, "audio:aac") {
			t.Errorf("M4B: %s", note)
		}
	}
	return note
}
