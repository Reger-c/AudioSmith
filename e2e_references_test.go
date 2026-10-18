package main

// TestReferencesAB (thread #83) makes the new references on the real
// engine, through the app's own code, and renders the kitten scene of
// #81 with whole dialogue (SoulX-Podcast) from old and new references:
//
//   - two designed voices (#81's child and mother, 1.7B VoiceDesign):
//     old = their greeting sample (the product before #83), new = their
//     reference (makeReference: three takes, picked);
//   - two built-in speakers (0.6B CustomVoice Vivian and Serena): old =
//     the portable line said once, new = their portable copy
//     (makeSpeakerPortable: three takes, picked).
//
// It writes every reference, the four scenes, the checker's measures and
// each line's measures (abMeasure, abListen) to AUDIOSMITH_REFS_AB:
//
//	AUDIOSMITH_REFS_AB=/tmp/refs go test -run TestReferencesAB -v -timeout 2h .

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/refcheck"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// refsOldPortableLine is what portable copies said before #83.
const refsOldPortableLine = "这是一段用来克隆音色的参考录音。我会用平稳自然的语气，把这几句话清楚地读完，让每个字的声音都能被听见。"

// refsKitten is #81's kitten scene (k1): the child's and the mother's lines.
var refsKitten = [][2]string{
	{"kid", "我想领养一只小猫。"}, {"kid", "妈妈，我想领养一只小猫，可以吗？"},
	{"mom", "领养是好事，可是养猫要花很多时间照顾它。你能一直照顾它吗？"}, {"kid", "我能！我每天喂它，给它换水，铲猫砂。"},
	{"mom", "谁带它去打针、看病呢？"}, {"kid", "我和你一起去。我用零花钱买猫粮。"},
	{"kid", "我同学小雨家有一只猫，很可爱。"}, {"mom", "那我们先去救助站看看吧。你要负责，不能刚开始很热情，很快就没兴趣了。"},
}

func TestReferencesAB(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_REFS_AB")
	if out == "" {
		t.Skip("set AUDIOSMITH_REFS_AB to a directory")
	}
	os.MkdirAll(filepath.Join(out, "refs"), 0o755)
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: set AUDIOSMITH_ENGINE")
	}
	// A Chinese user: built-in speakers without a language of their own
	// (CustomVoice's) get Chinese reference texts, as in the app.
	i18n.SetLocale("zh-Hans")
	defer i18n.SetLocale("en")
	lib := openTestLibrary(t)
	h := &engineHost{bin: bin, logDir: t.TempDir()}
	a := newApp(h, lib)
	a.autoplay = false
	events := make(chan func(), 1<<16)
	a.enginePost = func(fn func()) { events <- fn }
	a.postQueue = a.enginePost
	h.sup = engine.NewSupervisor(engine.Config{Bin: bin, Bus: a.bus, LogDir: h.logDir, Offline: true, CacheDir: modelCache(a.prefs),
		OnDemand: []proto.Role{proto.RoleListen, proto.RoleMedia},
		Log: func(e core.LogEntry) {
			if e.Level != core.SeverityInfo && e.Level != core.SeverityDebug {
				t.Logf("%s %s: %s", e.Level, e.Source, e.Message)
			}
		}, Logf: t.Logf})
	a.connectEngine(h)
	defer h.Stop()
	waitFor := func(what string, timeout time.Duration, done func() bool) {
		t.Helper()
		deadline := time.After(timeout)
		for !done() {
			if a.engState == engineFailed {
				t.Fatalf("engine failed: %s\n%s", a.engError, a.engTail)
			}
			select {
			case fn := <-events:
				fn()
			case <-time.After(200 * time.Millisecond):
			case <-deadline:
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	a.startEngine()
	waitFor("ready", time.Minute, func() bool { return a.engState == engineReady })
	waitFor("voices", time.Minute, func() bool { return a.voicesLoaded })
	ctx := context.Background()
	vdm, _ := catalog.Builtin().ByID("qwen3-tts-1.7b-voicedesign-4bit")
	cv, _ := catalog.Builtin().ByID("qwen3-tts-0.6b-customvoice-8bit")
	base, _ := catalog.Builtin().ByID("qwen3-tts-1.7b-base-8bit")
	soulx, _ := dialogueEntry()
	started := time.Now()
	logStep := func(s string) { t.Logf("[%5.0f s] %s", time.Since(started).Seconds(), s) }
	// gen renders a take through TakeService (nothing plays).
	made := map[string]core.Take{}
	core.On(a.bus, func(ev core.TakeCreated) { tc := ev.Take; events <- func() { made[tc.ID] = tc } })
	gen := func(what string, tk core.Take) core.Take {
		tk.ID = core.NewID(time.Now())
		if _, err := lib.svc.Takes.Generate(ctx, core.TakeRequest{Recipe: tk}); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		waitFor(what, 10*time.Minute, func() bool { _, ok := made[tk.ID]; return ok })
		return made[tk.ID]
	}

	// The designed voices: a sample (the greeting), then a reference.
	designs := []struct{ key, desc string }{
		{"kid", "一个七岁左右的小女孩，声音清脆稚嫩，音调偏高，活泼可爱。"},
		{"mom", "一位三十多岁的年轻妈妈，声音温柔柔和，语速平稳，略带疲惫。"},
	}
	seeds := map[string]int64{"kid": 22, "mom": 11}
	designed := map[string]string{}
	for _, d := range designs {
		attrs, _ := json.Marshal(vd.Parse(d.desc).Attrs.Canonical())
		v, err := lib.svc.Voices.SaveFiles(ctx, core.Voice{Name: "design-" + d.key, Kind: core.VoiceDesigned, Design: d.desc, DesignAttrs: attrs,
			Seed: seeds[d.key], Language: "zh", CreatedWith: vdm.Repo}, core.VoiceFiles{})
		if err != nil {
			t.Fatal(err)
		}
		a.voiceChanged(core.VoiceChanged{Voice: v})
		designed[d.key] = v.ID
		// Its sample, as Design saves it (renderSample would play it).
		low, err := vd.Lower(designTarget(vdm), voiceRecipe(&v), vd.Options{Lang: "zh"})
		if err != nil {
			t.Fatal(err)
		}
		st := gen("sample "+d.key, core.Take{Text: sampleLine("zh"), Model: vdm.Repo, Voice: low.Voice, Seed: v.Seed, Language: modelLanguage(vdm, "zh"), VoiceID: v.ID})
		v.SampleText = st.Text
		if v, err = lib.svc.Voices.SaveFiles(ctx, v, core.VoiceFiles{Sample: a.takeStore.AudioPath(st)}); err != nil {
			t.Fatal(err)
		}
		a.voiceChanged(core.VoiceChanged{Voice: v})
		logStep("sample " + d.key)
		a.makeReference(a.findVoice(v.ID), nil)
		waitFor("reference "+d.key, 20*time.Minute, func() bool { return a.refMaking(v.ID) == nil })
		if e := a.refFailed(v.ID); e != "" {
			t.Fatalf("reference %s: %s", d.key, e)
		}
		logStep("reference " + d.key)
	}

	// The built-in speakers: the old portable line, and the new copy.
	speakers := map[string]string{"kid": "Vivian", "mom": "Serena"}
	oldPortable, newPortable := map[string]string{}, map[string]string{}
	for _, key := range []string{"kid", "mom"} {
		sp := speakers[key]
		ot := gen("old portable "+sp, core.Take{Text: refsOldPortableLine, Model: cv.Repo, Voice: sp, Language: modelLanguage(cv, "zh"),
			Render: &core.TakeRender{Bits: 24, Master: services.MasterDefault}})
		lvl := filepath.Join(t.TempDir(), "old-"+sp+".wav")
		b, _, _ := wav.ReadFile(a.takeStore.AudioPath(ot))
		nb, _ := refcheck.Tidy(b, []dsp.Range{}) // levelled only, as before
		wav.WriteFile(lvl, nb, wav.PCM16, wav.Options{})
		v, err := lib.svc.Voices.SaveFiles(ctx, core.Voice{Name: "old-portable-" + sp, Kind: core.VoiceCloned, Language: "zh", RefText: refsOldPortableLine},
			core.VoiceFiles{Reference: lvl})
		if err != nil {
			t.Fatal(err)
		}
		a.voiceChanged(core.VoiceChanged{Voice: v})
		oldPortable[key] = v.ID
		logStep("old portable " + sp)
		a.makeSpeakerPortable(cv, sp)
		waitFor("portable "+sp, 20*time.Minute, func() bool {
			for _, v := range a.voices {
				if v.Origin == "portable:speaker:"+cv.ID+":"+sp {
					newPortable[key] = v.ID
					return true
				}
			}
			return a.refMakes["portable:speaker:"+cv.ID+":"+sp] != nil && a.refMakes["portable:speaker:"+cv.ID+":"+sp].err != ""
		})
		if newPortable[key] == "" {
			t.Fatalf("portable %s: %s", sp, a.refMakes["portable:speaker:"+cv.ID+":"+sp].err)
		}
		logStep("new portable " + sp)
	}

	// A0: the designed voices' greeting samples, as clones (the product
	// before #83 cloned them so).
	greeting := map[string]string{}
	for key, id := range designed {
		d := a.findVoice(id)
		v, err := lib.svc.Voices.SaveFiles(ctx, core.Voice{Name: "greeting-" + key, Kind: core.VoiceCloned, Language: "zh", RefText: d.SampleText},
			core.VoiceFiles{Reference: a.voicePath(d, d.Sample)})
		if err != nil {
			t.Fatal(err)
		}
		a.voiceChanged(core.VoiceChanged{Voice: v})
		greeting[key] = v.ID
	}

	// Every reference, measured by the checker.
	var report []string
	report = append(report, "# #83 references and the kitten scene "+time.Now().Format("2006-01-02 15:04"), "",
		"## References", "", "| reference | text | s | speech s | SNR dB | LUFS | F0 Hz | F0 range st | chars/s | pauses /10 s | longest pause s | grade | hints |",
		"|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	refRow := func(name string, v *core.Voice, path, text string) {
		b, _, err := wav.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		services.CopyFile(ctx, path, filepath.Join(out, "refs", name+".wav"))
		r := refcheck.Measure(b, textOptions(text))
		var hs []string
		for _, h := range r.Hints(refcheck.UseDialogue) {
			hs = append(hs, h.Code)
		}
		report = append(report, fmt.Sprintf("| %s | %s | %.1f | %.1f | %.0f | %.1f | %.0f | %.1f | %.1f | %.1f | %.2f | %s | %s |", name, text, r.Seconds, r.Speech,
			r.SNR, r.LUFS, r.F0, r.F0Range, r.Rate, r.PausesPer10, r.LongestPause, r.Grade(refcheck.UseDialogue), strings.Join(hs, ", ")))
	}
	for _, key := range []string{"kid", "mom"} {
		d := a.findVoice(designed[key])
		refRow("design-"+key+"-old-greeting", d, a.voicePath(d, d.Sample), d.SampleText)
		refRow("design-"+key+"-new-reference", d, a.voicePath(d, d.RefAudio), d.RefText)
		o, n := a.findVoice(oldPortable[key]), a.findVoice(newPortable[key])
		refRow("builtin-"+speakers[key]+"-old-portable", o, a.voicePath(o, o.RefAudio), o.RefText)
		refRow("builtin-"+speakers[key]+"-new-portable", n, a.voicePath(n, n.RefAudio), n.RefText)
	}

	// The scene, four ways.
	report = append(report, "")
	for _, sc := range []struct {
		name, model string
		voices      map[string]string
	}{
		{"design-old", base.Repo, greeting},
		{"design-new", vdm.Repo, designed},
		{"builtin-old", base.Repo, oldPortable},
		{"builtin-new", base.Repo, newPortable},
	} {
		doc := services.NewLongformDoc(core.ProjectStory, sc.model)
		doc.Title, doc.Language, doc.Seed = "我想领养一只小猫", "zh", 20261018
		doc.Cast = []services.CastMember{{ID: "kid", Name: "孩子", Voice: sc.voices["kid"]}, {ID: "mom", Name: "妈妈", Voice: sc.voices["mom"]}}
		doc.Voice = sc.voices["mom"]
		for i, l := range refsKitten {
			doc.Lines = append(doc.Lines, services.StoryLine{ID: fmt.Sprintf("l%d", i+1), Character: l[0], Text: l[1]})
		}
		doc.Production = services.DefaultStoryProduction()
		doc.Production.Dialogue, doc.Production.DialogueModel = true, soulx.Repo
		spec, probs := services.Compile(&doc, lfResolverOf(&doc, a.voices, a.fillVoice, lib.svc.Voices.FilePath))
		for _, p := range probs {
			if p.Blocking {
				t.Fatalf("%s: %+v", sc.name, p)
			}
		}
		spec.Audition = true
		begun := time.Now()
		r, err := lib.svc.Longform.Start(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		var body services.RenderBody
		waitFor(sc.name, 40*time.Minute, func() bool {
			rr, b, err := lib.svc.Longform.Get(ctx, r.ID)
			body = b
			return err == nil && rr.State != core.RenderRunning
		})
		if len(body.Chapters) != 1 || body.Chapters[0].State != "done" {
			t.Fatalf("%s: %+v %+v", sc.name, body.Chapters, body.Error)
		}
		wavPath := filepath.Join(out, "kitten-"+sc.name+".wav")
		if err := services.CopyFile(ctx, body.Chapters[0].File, wavPath); err != nil {
			t.Fatal(err)
		}
		res, _ := services.ReadChapter(body.Chapters[0].File)
		lines := spec.Chapters[0].Lines
		md := abMeasure(t, wavPath, res, lines) + abListen(t, ctx, h.sup, lib, wavPath, res, lines)
		head := fmt.Sprintf("## kitten-%s\n\n%.1f s audio in %.0f s; dialogue segments %d, lines %d, redone %d, on the story's model %d\n",
			sc.name, res.AudioSeconds, time.Since(begun).Seconds(), res.Dialogues, res.DialogueLines, res.DialogueRedone, res.DialogueFailed)
		logStep(head)
		report = append(report, head, md)
	}
	os.WriteFile(filepath.Join(out, "measures.md"), []byte(strings.Join(report, "\n")+"\n"), 0o644)
}
