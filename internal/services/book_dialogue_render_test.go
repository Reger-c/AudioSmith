package services

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

const ferryBook = "旁白开头，雾还没有散。\n\n[对话]\n伊内丝：这是去哈罗角的船吗？\nTomas：唯一的一班，上来吧。\n他把缆绳抛上岸。\n" +
	"伊内丝：要多久才能到呢？\nTomas：两个钟头，随便坐吧。\n[/对话]\n\n她选了灯下的长椅，看着灯一盏一盏熄灭。"

// bookRun renders spec and reads its chapter.
func bookRun(t *testing.T, e *lfEnv, spec LongformSpec) ChapterResult {
	t.Helper()
	rr, err := e.svc.Longform.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	_, body := e.wait(t, rr.ID)
	if st := body.Chapters[0].State; st != "done" && st != "cached" {
		t.Fatalf("chapter %+v", body.Chapters[0])
	}
	res, err := ReadChapter(body.Chapters[0].File)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// lineLUFS measures the chapter's lines (LUFS) where they landed.
func lineLUFS(t *testing.T, res ChapterResult) []float64 {
	t.Helper()
	b, _, err := wav.ReadFile(res.Output)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, len(res.Lines))
	for i, l := range res.Lines {
		a, z := int(l.Start*float64(b.Rate)), min(int(l.End*float64(b.Rate)), b.Frames())
		out[i] = loudness.Integrated(b.Slice(a, z))
	}
	return out
}

// A book's dialogue block goes to the dialogue model, the rest of the
// chapter (narration inside the block too) to the book's model; the
// block's lines are levelled to the narration; a preview of the block
// alone finds the same segment in the cache; without the model the block
// is read line by line.
func TestBookDialogueRender(t *testing.T) {
	e := newLongformEnv(t, t.TempDir(), false)
	defer e.close()
	fd := &fakeDialogue{said: map[string]string{}}
	r := e.svc.Longform.render
	r.engine, r.recognizer = fd.executor, "m/asr"
	d := bookOf(ferryBook)
	d.Language = "zh"
	d.Production.Dialogue, d.Production.DialogueModel = true, "m/soulx"
	spec, probs := Compile(&d, testVoices())
	if len(probs) != 0 {
		t.Fatalf("problems %v", codesOf(probs))
	}
	spec.Audition = true
	res := bookRun(t, e, spec)
	if len(fd.calls) != 1 || res.Dialogues != 1 || res.DialogueLines != 4 || res.DialogueFailed != 0 {
		t.Fatalf("calls %d, result %+v", len(fd.calls), res)
	}
	call := fd.calls[0]
	if len(call.Turns) != 4 || len(call.Speakers) != 2 || *call.Seed != 42+1000 || call.Turns[1].Text != "唯一的一班，上来吧。" {
		t.Errorf("call %+v", call)
	}
	said := e.synth.texts()
	for _, s := range []string{"旁白开头，雾还没有散。", "他把缆绳抛上岸。", "她选了灯下的长椅，看着灯一盏一盏熄灭。", PortableLine("zh")} {
		if !slices.Contains(said, s) {
			t.Errorf("book model did not say %q: %q", s, said)
		}
	}
	if len(said) != 5 { // three narration lines, two references
		t.Errorf("synth %q", said)
	}
	// Levelled: the dialogue model's lines (−14 dBFS tones) sit at the
	// narration's level (−12 dBFS).
	if res.Levelled == 0 {
		t.Error("not levelled")
	}
	lufs := lineLUFS(t, res)
	var dlg, other []float64
	for i, l := range spec.Chapters[0].Lines {
		if l.Character != "" {
			dlg = append(dlg, lufs[i])
		} else {
			other = append(other, lufs[i])
		}
	}
	if diff := math.Abs(median(dlg) - median(other)); diff > 1.5 {
		t.Errorf("dialogue %v vs narration %v: %.2f LU apart", dlg, other, diff)
	}
	// Gaps inside the block are the dialogue pacing's.
	if g := res.Lines[2].Start - res.Lines[1].End; g > 0.5 {
		t.Errorf("gap in the block %.2f s", g)
	}

	// The block's preview (its lines alone) finds the segment in the cache.
	var lines []LineSpec
	for _, l := range spec.Chapters[0].Lines {
		if l.Block == 1 {
			lines = append(lines, l)
		}
	}
	lines[0].Paragraph = false
	preview := spec
	preview.Chapters = []ChapterSpec{{Lines: lines}}
	e.synth.reset()
	if res = bookRun(t, e, preview); len(fd.calls) != 1 || res.DialogueLines != 4 || len(e.synth.texts()) != 0 {
		t.Errorf("preview not from the cache: %d calls, synth %q", len(fd.calls), e.synth.texts())
	}

	// The dialogue model is not downloaded: line by line, with a warning.
	os.RemoveAll(filepath.Join(e.dir, "cache", "longform"))
	e.synth.reset()
	fd.calls = nil
	r.modelPath = func(_ context.Context, m string) (string, error) {
		if m == "m/soulx" {
			return "", core.ErrNotDownloaded
		}
		return "/models/" + m, nil
	}
	res = bookRun(t, e, spec)
	if len(fd.calls) != 0 || res.Dialogues != 0 || len(e.synth.texts()) != 7 ||
		!slices.ContainsFunc(res.Warnings, func(n core.TakeNotice) bool { return n.Code == NoticeDialogueUnavailable }) {
		t.Errorf("not downloaded: %d calls, %+v", len(fd.calls), res)
	}
	if g := res.Lines[2].Start - res.Lines[1].End; g > 0.5 {
		t.Errorf("gap in the block, line by line: %.2f s", g)
	}
}

// MOSS-TTSD speaks a block as one track, cut back into its lines; a block
// of one speaker is read line by line.
func TestBookDialogueOneTrack(t *testing.T) {
	e := newLongformEnv(t, t.TempDir(), false)
	defer e.close()
	ft := &fakeTrack{heard: map[int]string{}, tracks: map[string][]proto.Word{}}
	r := e.svc.Longform.render
	r.engine, r.recognizer, r.aligner = ft.executor, "m/asr", "m/aligner"
	r.models = func(repo string) ModelInfo {
		if repo == "m/moss" {
			return ModelInfo{Traits: TraitsFor(FamilyMossTTSD, []string{"dialogue"}), Revision: "rm"}
		}
		return kokoroInfo(repo)
	}
	d := bookOf(ferryBook + "\n\n[对话]\n伊内丝：我一个人说话。\n伊内丝：还是我。\n[/对话]")
	d.Production.Dialogue, d.Production.DialogueModel = true, "m/moss"
	spec, _ := Compile(&d, testVoices())
	spec.Audition = true
	res := bookRun(t, e, spec)
	if len(ft.calls) != 1 || len(ft.calls[0].Turns) != 4 || res.Dialogues != 1 || res.DialogueLines != 4 {
		t.Fatalf("calls %d, result %+v", len(ft.calls), res)
	}
	said := strings.Join(e.synth.texts(), "|")
	if !strings.Contains(said, "我一个人说话。") || !strings.Contains(said, "还是我。") {
		t.Errorf("the one-speaker block: %q", said)
	}
	for i := 1; i < len(res.Lines); i++ {
		if res.Lines[i].Start <= res.Lines[i-1].End {
			t.Errorf("lines %+v", res.Lines)
		}
	}
}

func TestBookLevelGains(t *testing.T) {
	line := func(voice string) LineSpec { return LineSpec{Spans: []SpanSpec{{Voice: voice}}} }
	places := []LinePlace{{Start: 0, End: 1}, {Start: 1, End: 2}, {Start: 2, End: 3}, {Start: 3, End: 4}, {Start: 4, End: 5}}
	lines := []LineSpec{line("n"), line("a"), line("b"), line("a"), line("n")}
	made := map[int]bool{1: true, 2: true, 3: true}
	g := bookLevelGains(places, lines, made, []float64{-20, -26, -21, -24, -22, math.Inf(-1)}[:5])
	// Narration median −21; a's −25 (+4 each), b's −21 (none).
	if len(g) != 2 || g[0].db != 4 || g[1].db != 4 || g[0].start != 1 || g[1].start != 3 {
		t.Errorf("gains %+v", g)
	}
	if bookLevelGains(places, lines, map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true}, []float64{-20, -26, -21, -24, -22}) != nil {
		t.Error("no narration: no gains")
	}
	big := bookLevelGains(places[:2], lines[:2], map[int]bool{1: true}, []float64{-10, -40})
	if len(big) != 1 || big[0].db != bookLevelMaxDB {
		t.Errorf("bounded %+v", big)
	}
}
