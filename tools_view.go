package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// toolsPage is /tools.
func (a *app) toolsPage(c *ui.Context) {
	t := c.Theme()
	header(c, i18n.T("Tools"), nil)
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(px(t, 20)).Gap(px(t, 16)).MaxWidth(px(t, 760)).Children(func() {
			ui.Segmented(c.Key("tools-tab"), &a.toolTab, i18n.T("A/B voices"), i18n.T("Convert"), i18n.T("Rate fit"),
				i18n.T("Direction"), i18n.T("File probe")).Label(i18n.T("Tool"))
			switch a.toolTab {
			case 0:
				a.abTool(c)
			case 1:
				a.convertTool(c)
			case 2:
				a.rateFitTool(c)
			case 3:
				a.directionTool(c)
			default:
				a.probeTool(c)
			}
		})
	})
}

func toolIntro(c *ui.Context, text string) {
	t := c.Theme()
	ui.Text(c, text).FontSize(px(t, 13)).TextColor(t.TextMuted).Wrap()
}

// runRow shows a preview's state and its Play button.
func (a *app) runRow(c *ui.Context, key, title string, run *toolRun) {
	t := c.Theme()
	ui.Row(c.Key(key)).Gap(px(t, 10)).AlignItems(ui.Center).Padding(px(t, 10), px(t, 12)).Radius(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
			ui.Text(c, title).Bold().SingleLine()
			switch {
			case run == nil:
				ui.Text(c, i18n.T("Not made yet")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			case run.busy:
				ui.Text(c, i18n.T("Generating with %s…", run.model)).FontSize(px(t, 12)).TextColor(t.TextMuted)
			case run.err != "":
				ui.Text(c, run.err).FontSize(px(t, 12)).TextColor(t.Danger).Wrap()
			default:
				ui.Text(c, run.model).FontSize(px(t, 12)).TextColor(t.TextMuted).SingleLine()
			}
		})
		if run != nil && run.busy {
			ui.Spinner(c)
		}
		label := i18n.T("Play")
		if run != nil && a.pl.Playing(run.key) {
			label = i18n.T("Stop")
		}
		if ui.Button(c.Key("play"), label).Disabled(run == nil || run.busy || run.output == "").Clicked() {
			a.playRun(run)
		}
	})
}

func (a *app) abTool(c *ui.Context) {
	t := c.Theme()
	toolIntro(c, i18n.T("The same phrase in two voices, to compare them. Nothing is kept in the History."))
	ui.Field(c, i18n.T("Phrase"), func() {
		ui.TextArea(c.Key("ab-text"), &a.ab.text).Height(px(t, 64)).Label(i18n.T("Phrase"))
	})
	labels := toolLabels(a.toolChoices())
	if a.ab.a == "" || !contains(labels, a.ab.a) {
		a.ab.a = labels[0]
	}
	if a.ab.b == "" || !contains(labels, a.ab.b) {
		a.ab.b = labels[min(1, len(labels)-1)]
	}
	ui.Row(c).Gap(px(t, 12)).Children(func() {
		ui.Field(c, i18n.T("Voice A"), func() {
			ui.Select(c.Key("ab-a"), &a.ab.a, labels).Label(i18n.T("Voice A")).Width(px(t, 240))
		})
		ui.Field(c, i18n.T("Voice B"), func() {
			ui.Select(c.Key("ab-b"), &a.ab.b, labels).Label(i18n.T("Voice B")).Width(px(t, 240))
		})
	})
	busy := a.ab.runs[0] != nil && a.ab.runs[0].busy || a.ab.runs[1] != nil && a.ab.runs[1].busy
	ui.Row(c).Children(func() {
		if ui.PrimaryButton(c.Key("ab-run"), i18n.T("Compare")).Disabled(busy || strings.TrimSpace(a.ab.text) == "").Clicked() {
			a.runAB()
		}
	})
	ui.Row(c).Gap(px(t, 12)).Children(func() {
		ui.Column(c).Grow(1).Children(func() { a.runRow(c, "ab-0", "A · "+a.ab.a, a.ab.runs[0]) })
		ui.Column(c).Grow(1).Children(func() { a.runRow(c, "ab-1", "B · "+a.ab.b, a.ab.runs[1]) })
	})
}

func (a *app) convertTool(c *ui.Context) {
	t := c.Theme()
	cv := &a.cv
	toolIntro(c, i18n.T("Say it in another voice: AudioSmith listens to a recording, then speaks what it heard in a saved voice. The take lands in the History."))
	ui.Row(c.Key("cv-file")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		name := cv.name
		if name == "" {
			name = i18n.T("No recording chosen")
		} else if cv.duration > 0 {
			name += " · " + fmt.Sprintf("%.1f s", cv.duration)
		}
		ui.Text(c, name).Grow(1).SingleLine()
		if ui.Button(c.Key("cv-choose"), i18n.T("Choose…")).Disabled(cv.stage != "").Clicked() {
			a.chooseConvertFile()
		}
	})
	labels := toolLabels(a.toolChoices())
	if cv.voice == "" || !contains(labels, cv.voice) {
		cv.voice = labels[0]
	}
	ui.Field(c, i18n.T("Voice"), func() {
		ui.Select(c.Key("cv-voice"), &cv.voice, labels).Label(i18n.T("Voice")).Width(px(t, 280))
	})
	settingRow(c, "cv-match", i18n.T("Match the recording's length"), i18n.T("OmniVoice aims at the length as it speaks; other models' takes are time-stretched to it, at the same pitch (up to twice as fast or slow)."), func() {
		ui.Switch(c.Key("cv-match-switch"), &cv.match).Label(i18n.T("Match the recording's length"))
	})
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		if ui.PrimaryButton(c.Key("cv-run"), i18n.T("Convert")).Disabled(cv.file == "" || cv.stage != "").Clicked() {
			a.runConvert()
		}
		if cv.stage != "" {
			ui.Spinner(c)
			ui.Text(c, cv.stage).TextColor(t.TextMuted)
		}
	})
	if cv.err != "" {
		ui.Text(c, cv.err).TextColor(t.Danger).Wrap()
	}
	if cv.heard != "" {
		ui.Text(c, i18n.T("What it heard")).Bold()
		ui.Text(c.Key("cv-heard"), cv.heard).Wrap().Selectable()
	}
	if cv.note != "" {
		ui.Text(c, cv.note).FontSize(px(t, 12)).TextColor(t.Warning).Wrap()
	}
	if tk := cv.take; tk != nil {
		ui.Row(c.Key("cv-take")).Gap(px(t, 8)).AlignItems(ui.Center).Padding(px(t, 10), px(t, 12)).Radius(8).Background(t.Surface).Children(func() {
			ui.Text(c, i18n.T("Take ready · %.1f s", tk.AudioSeconds)).Grow(1)
			label := i18n.T("Play")
			if a.pl.Playing(tk.ID) {
				label = i18n.T("Stop")
			}
			if ui.Button(c.Key("cv-play"), label).Clicked() {
				a.play(tk)
			}
			if ui.Button(c.Key("cv-history"), i18n.T("Open History")).Clicked() {
				a.navigate("/history")
			}
		})
	}
}

func (a *app) rateFitTool(c *ui.Context) {
	t := c.Theme()
	rf := &a.rf
	toolIntro(c, i18n.T("Will a line fit its slot when spoken? A prediction from the language's speaking rate; a language model can propose a shorter version."))
	ui.Field(c, i18n.T("Line"), func() {
		ui.TextArea(c.Key("rf-text"), &rf.text).Height(px(t, 64)).Label(i18n.T("Line"))
	})
	labels, _ := rateFitLangs()
	if rf.lang == "" {
		rf.lang = rateLangDefault()
	}
	ui.Row(c).Gap(px(t, 12)).AlignItems(ui.End).Children(func() {
		ui.Field(c, i18n.T("Language"), func() {
			ui.Select(c.Key("rf-lang"), &rf.lang, labels).Label(i18n.T("Language")).Width(px(t, 200))
		})
		ui.Field(c, i18n.T("Slot (seconds)"), func() {
			ui.NumberInput(c.Key("rf-slot"), &rf.slot, 0.1, 600, 0.1).Label(i18n.T("Slot (seconds)"))
		})
		ui.Field(c, i18n.T("Speed"), func() {
			ui.NumberInput(c.Key("rf-speed"), &rf.speed, 0.5, 2, 0.05).Label(i18n.T("Speed"))
		})
	})
	canRewrite := a.llmCan(llm.SkillFitRewrite)
	desc := i18n.T("Asks the writing-help model for a version that fits, when the line overflows.")
	if !canRewrite {
		desc = i18n.T("Set up a language model in Settings → Writing help to get shorter versions.")
	}
	settingRow(c, "rf-rewrite", i18n.T("Suggest a shorter version"), desc, func() {
		ui.Switch(c.Key("rf-rewrite-switch"), &rf.rewrite).Disabled(!canRewrite).Label(i18n.T("Suggest a shorter version"))
	})
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		if ui.PrimaryButton(c.Key("rf-run"), i18n.T("Check")).Disabled(rf.busy || strings.TrimSpace(rf.text) == "" || rf.slot <= 0).Clicked() {
			a.runRateFit()
		}
		if rf.busy {
			ui.Spinner(c)
		}
	})
	if rf.err != "" {
		ui.Text(c, rf.err).TextColor(t.Danger).Wrap()
	}
	if res := rf.res; res != nil {
		fitRow(c, "rf-fit", i18n.T("As written"), res.Fit)
		if res.Rewrite != "" {
			ui.Text(c.Key("rf-rw"), res.Rewrite).Wrap().Selectable()
			if res.RewriteFit != nil {
				fitRow(c, "rf-rw-fit", i18n.T("Shorter version"), *res.RewriteFit)
			}
		}
	}
}

// fitRow shows a prediction.
func fitRow(c *ui.Context, key, title string, f translate.Fit) {
	t := c.Theme()
	col, verdict := t.Success, i18n.T("Fits")
	switch f.Verdict {
	case translate.FitTight:
		col, verdict = t.Warning, i18n.T("Tight")
	case translate.FitOverflow:
		col, verdict = t.Danger, i18n.T("Too long")
	}
	ui.Row(c.Key(key)).Gap(px(t, 10)).AlignItems(ui.Center).Padding(px(t, 10), px(t, 12)).Radius(8).Background(t.Surface).Children(func() {
		ui.Text(c, title).Bold().Width(px(t, 130))
		ui.Text(c, verdict).TextColor(col).Bold().Width(px(t, 80))
		ui.Text(c, i18n.T("%.1f s spoken for a %.1f s slot (%.0f%%)", f.Predicted, f.Slot, f.Ratio*100)).TextColor(t.TextMuted).Grow(1)
	})
}

func (a *app) directionTool(c *ui.Context) {
	t := c.Theme()
	d := &a.dir
	toolIntro(c, i18n.T("Direct a line as you would an actor: “more urgent, a little scared”. AudioSmith turns the note into emotions, an instruction for models that take one, and rate, pitch and energy."))
	ui.Field(c, i18n.T("Note"), func() {
		ui.TextInput(c.Key("dir-note"), &d.note).Placeholder(i18n.T("warm and slow, like a bedtime story")).Label(i18n.T("Note"))
	})
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		if ui.PrimaryButton(c.Key("dir-run"), i18n.T("Read the note")).Disabled(d.busy || strings.TrimSpace(d.note) == "").Clicked() {
			a.runDirection()
		}
		if d.busy {
			ui.Spinner(c)
		}
		via := i18n.T("Keywords (set up a language model in Settings → Writing help for finer readings)")
		if a.llmCan(llm.SkillDirectionParse) {
			via = i18n.T("With the writing-help language model")
		}
		ui.Text(c, via).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap().Grow(1)
	})
	res := d.res
	if res == nil {
		return
	}
	ui.Column(c.Key("dir-res")).Gap(px(t, 6)).Padding(px(t, 12)).Radius(8).Background(t.Surface).Children(func() {
		row := func(k, label, value string) {
			ui.Row(c.Key(k)).Gap(px(t, 10)).Children(func() {
				ui.Text(c, label).TextColor(t.TextMuted).Width(px(t, 130))
				ui.Text(c, value).Wrap().Grow(1).Selectable()
			})
		}
		row("emo", i18n.T("Emotions"), orDash(strings.Join(res.Emotions, ", ")))
		row("ins", i18n.T("Instruction"), orDash(res.Instruct))
		row("rate", i18n.T("Rate"), fmt.Sprintf("%+.0f%%", res.Rate*100))
		row("pitch", i18n.T("Pitch"), fmt.Sprintf("%+.1f st", res.Pitch))
		row("energy", i18n.T("Energy"), fmt.Sprintf("%+.2f", res.Energy))
		if res.TranslateHint != "" {
			row("hint", i18n.T("For translators"), res.TranslateHint)
		}
	})
	e := directionModel(a.preset())
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		if ui.Button(c.Key("dir-preview"), i18n.T("Hear it")).Disabled(d.preview != nil && d.preview.busy || res.Instruct == "").Clicked() {
			a.previewDirection()
		}
		ui.Text(c, i18n.T("A sample line with %s", e.Label)).FontSize(px(t, 12)).TextColor(t.TextMuted)
	})
	if d.preview != nil {
		a.runRow(c, "dir-run-row", i18n.T("Sample"), d.preview)
	}
}

func (a *app) probeTool(c *ui.Context) {
	t := c.Theme()
	p := &a.prb
	toolIntro(c, i18n.T("What a media file holds: container, codec, sample rate, channels, length, video. Read by the engine's media worker."))
	ui.Row(c.Key("prb-file")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		name := p.file
		if name == "" {
			name = i18n.T("No file chosen")
		}
		ui.Text(c, name).Grow(1).SingleLine()
		if ui.Button(c.Key("prb-choose"), i18n.T("Choose…")).Disabled(p.busy).Clicked() {
			a.chooseProbeFile()
		}
	})
	if p.busy {
		ui.Spinner(c)
	}
	if p.err != "" {
		ui.Text(c, p.err).TextColor(t.Danger).Wrap()
	}
	if p.out != "" {
		a.codeBlock(c, "probe", integBlock{Title: i18n.T("Result"), Lang: "json", Code: p.out})
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
