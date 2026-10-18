package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// dialogueBook is the snapshots' manuscript: a passage of three speakers
// with narration, a stage direction and a direction, in the language.
func dialogueBook(zh bool) string {
	if zh {
		return "# 第三章 渡口\n\n雾还没有散，渡口只有一盏灯。\n\n[对话]\n伊内丝：这是去哈罗角的船吗？\n托马斯：唯一的一班。\n他把缆绳抛上岸，指了指灯下的长椅。\n" +
			"托马斯（低声）：随便坐吧，要两个钟头。\n伊内丝：两个钟头？地图上看着挺近的。\n（远处传来汽笛声。）\n船长：地图总是骗人的。\n[/对话]\n\n她选了灯下的长椅，看着小镇的灯一盏一盏熄灭。\n\n" +
			"“你也去哈罗角？”她问邻座的老人。\n\n“回家。”老人说。"
	}
	return "# Chapter Three: The Ferry\n\nThe fog had not lifted; one lamp lit the pier.\n\n[dialogue]\nInes: Is this the boat to Harrow Point?\nTomas: The only one.\nHe threw the rope ashore and pointed at the bench.\n" +
		"Tomas (quietly): Sit anywhere. Two hours.\nInes: Two hours? It looked close on the map.\n(A horn sounds far away.)\nCaptain: Maps always lie.\n[/dialogue]\n\nShe chose the bench under the lamp.\n\n" +
		"\"Going to Harrow Point too?\" she asked the old man beside her.\n\n\"Home,\" the old man said."
}

// TestSnapshotsBookDialogue renders the Audiobook's dialogue passages
// (thread #88): the editor with a passage and the inspector's 对话段
// section (ready; the model not downloaded; a speaker without a voice;
// a Mac with 8 GB) and the toast after wrapping a selection (the Insert
// menu is native: the tester does not draw it; audiobook_dialogue_test.go
// chooses its item); English and Chinese (and Chinese dark), at 1100×720 and at
// the minimum with 125 % text:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsBookDialogue .
func TestSnapshotsBookDialogue(t *testing.T) { runSnapshots(t, snapshotsBookDialogue) }

func snapshotsBookDialogue(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	stubMachine(t, 16, true)
	for _, run := range []struct {
		lang string
		dark bool
	}{{"zh-Hans", false}, {"en", false}, {"zh-Hans", true}} {
		for _, sz := range []struct {
			name  string
			w, h  int
			scale float64
		}{{"1100", 1100, 720, 1}, {"min125", 820, 520, 1.25}} {
			i18n.SetLocale("en")
			a, _, tt := newTestApp(t)
			lang, scale := run.lang, sz.scale
			a.updateSettings(func(s *settings.Settings) {
				if lang != "en" {
					s.General.Language = lang
				}
				s.Appearance.Scale = scale
			})
			tt.SetSize(sz.w, sz.h)
			tt.SetDark(run.dark)
			tt.SetScale(2)
			suffix := sz.name + "-light"
			if run.dark {
				suffix = sz.name + "-dark"
			}
			if run.lang != "en" {
				suffix += "-" + run.lang
			}
			save := func(name string) {
				tt.Frame()
				tt.Frame()
				f, err := os.Create(filepath.Join(out, fmt.Sprintf("%s-%s.png", name, suffix)))
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err := png.Encode(f, tt.Image()); err != nil {
					t.Fatal(err)
				}
			}
			a.sys = &sysInfo{Memory: 16 << 30}
			zh := run.lang != "en"
			a.navigate("/audiobook")
			p := a.lfBook
			p.doc.Title = i18n.T("The Night Ferry")
			p.doc.Voice = "speaker:af_heart"
			a.setManuscript(p, dialogueBook(zh))
			tt.Frame()
			a.bookDialogueOn(p)
			e, _, _ := dialogueChoice(16, &p.doc)
			m := a.model(e)
			m.known, m.local.State = true, hub.Downloaded
			names := [3]string{"伊内丝", "托马斯", "船长"}
			if !zh {
				names = [3]string{"Ines", "Tomas", "Captain"}
			}
			p.doc.VoiceCast = map[string]string{names[0]: "speaker:af_bella", names[1]: "speaker:am_adam", names[2]: "speaker:bm_george"}
			p.open["setup"], p.open["cast"], p.open["dialogue"] = false, false, true
			save("dlg-book-editor")
			// The inspector's states (a drawer at the minimum size).
			p.panel.set, p.panel.open = true, true
			// Not downloaded.
			m.local.State = hub.Missing
			save("dlg-book-inspector-needdl")
			m.local.State = hub.Downloaded
			// A speaker without a voice.
			delete(p.doc.VoiceCast, names[2])
			save("dlg-book-inspector-uncast")
			p.doc.VoiceCast[names[2]] = "speaker:bm_george"
			// 8 GB.
			a.sys = &sysInfo{Memory: 8 << 30}
			save("dlg-book-inspector-8gb")
			a.sys = &sysInfo{Memory: 16 << 30}
			p.panel = panelState{}
			// Wrapping the last two paragraphs: the toast with Undo.
			s := p.ed.Buffer.String()
			p.ed.Select(len(s)-len(lastTwo(zh)), len(s))
			a.insertDialogue(p)
			save("dlg-book-wrap-toast")
		}
	}
}

func lastTwo(zh bool) string {
	if zh {
		return "“你也去哈罗角？”她问邻座的老人。\n\n“回家。”老人说。"
	}
	return "\"Going to Harrow Point too?\" she asked the old man beside her.\n\n\"Home,\" the old man said."
}
