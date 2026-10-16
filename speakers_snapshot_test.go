package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// fakeOnDisk puts an entry's files (empty, of their sizes) in the test
// app's model folder at its revision and rereads the Models page.
func fakeOnDisk(t *testing.T, a *app, e catalog.Entry) {
	t.Helper()
	snap := filepath.Join(hub.RepoDir(modelCache(a.prefs), e.Repo), "snapshots", e.Revision)
	for _, f := range e.Files {
		p := filepath.Join(snap, filepath.FromSlash(f.Path))
		os.MkdirAll(filepath.Dir(p), 0o755)
		fh, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		fh.Truncate(f.Size)
		fh.Close()
	}
	a.refreshModels()
}

// speakersTranscript is a three-person interview, one of them named.
func speakersTranscript(at time.Time) core.Transcript {
	segs := []core.TranscriptSegment{
		{Start: 0.4, End: 3.9, Speaker: "S1", Text: "今天我们请到了两位嘉宾，聊聊本地配音工具。"},
		{Start: 4.2, End: 7.7, Speaker: "S2", Text: "谢谢邀请。我们团队去年开始把字幕和配音都放到 Mac 上做。"},
		{Start: 8.1, End: 11.2, Speaker: "S3", Text: "最大的变化是不用再把素材上传到云端了。"},
		{Start: 11.6, End: 14.0, Speaker: "S1", Text: "那准确率怎么样？"},
		{Start: 14.3, End: 18.9, Speaker: "S2", Text: "中文基本不用改，说话人也能分清楚，多人对话省了很多时间。"},
		{Start: 19.2, End: 22.5, Speaker: "S3", Text: "英文术语偶尔要手动修正，加了热词以后好多了。"},
		{Start: 22.9, End: 25.0, Speaker: "S1", Text: "好，我们下期再见。"},
	}
	return core.Transcript{ID: "spk1", CreatedAt: at, UpdatedAt: at, Title: "访谈-第12期.m4a", Source: core.SourceFile, Mode: "accurate",
		Model: "vanch007/mlx-MOSS-Transcribe-Diarize-8bit", Aligner: "mlx-community/Qwen3-ForcedAligner-0.6B-4bit", Language: "zh",
		Duration: 25.4, Audio: "spk1.wav", Segments: segs, Speakers: map[string]string{"S1": "主持人"}}
}

// TestSnapshotsSpeakers renders transcription with speakers (thread #48)
// at the review's sizes (b1Runs): Transcribe with Speakers on and the
// model missing, a transcript with three speakers and the rename
// popover; the dub's source step recommending the model, and its
// transcript step labelled by it.
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/spk AUDIOSMITH_B1_SIZES=1100,min125 go test -run TestSnapshotsSpeakers .
func TestSnapshotsSpeakers(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	var only, sizes []string
	if v := os.Getenv("AUDIOSMITH_B1_ONLY"); v != "" {
		only = strings.Split(v, ",")
	}
	if v := os.Getenv("AUDIOSMITH_B1_SIZES"); v != "" {
		sizes = strings.Split(v, ",")
	}
	moss, _ := catalog.Builtin().SpeakersModel()
	for _, run := range b1Runs {
		if len(sizes) > 0 && !strings.Contains(","+strings.Join(sizes, ",")+",", ","+run.size+",") {
			continue
		}
		s := &b1Shooter{t: t, out: out, run: run, only: only}
		if s.want("spk-transcribe") {
			i18n.SetLocale("en")
			m := newM2Test(t)
			a, tt := m.a, m.tt
			s.setup(a, tt)
			save := func(name string) {
				a.toasts = nil
				s.save(tt, name)
			}
			a.updateSettings(func(st *settings.Settings) { st.Transcribe.Speakers = true })
			a.navigate("/transcripts")
			save("spk-transcribe-need")
			fakeOnDisk(t, a, moss)
			putTranscript(t, a, speakersTranscript(time.Now().Add(-2*time.Hour)))
			a.selectTranscript("spk1")
			save("spk-transcribe-detail")
			a.trRename, a.trRenBuf, a.trRenOpen = "S2", "", true
			save("spk-transcribe-rename")
			a.trRename, a.trRenOpen = "", false
		}
		if s.want("spk-dub") {
			i18n.SetLocale("en")
			d := newDubTest(t, 6)
			a, tt := d.a, d.tt
			s.setup(a, tt)
			save := func(name string) {
				a.toasts = nil
				s.save(tt, name)
			}
			a.navigate("/dub")
			a.dubSourceFile(d.src)
			a.dubSet(func(p *dub.Project) { p.Targets = []string{"zh"} })
			a.refreshModels()
			save("spk-dub-source-hint")
			d.fe.Speakers = 3
			fakeOnDisk(t, a, moss)
			a.dubStart()
			a.dubStep = dubStepTranscribe
			save("spk-dub-transcript")
			p := a.dub
			if p.Stage(dub.StageTranscribe, "").Status != dub.StatusDone || len(p.SpeakerLabels()) != 3 {
				t.Errorf("dub: %+v, speakers %v", p.Stage(dub.StageTranscribe, ""), p.SpeakerLabels())
			}
			if n := d.fe.Count(dub.MethodDiarize); n != 0 {
				t.Errorf("diarize.run called %d times", n)
			}
		}
	}
}
