package main

// Stories → inspector → Performance: how the story's dialogue flows
// (story_pacing.go in services): the pacing preset and merged turns, the
// scene, the AI director, and what the chosen model does with them (its
// continuity level). Personas sit on the cast cards, directions on the
// lines (stories_view.go).

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/services"
)

// pacingLabels are the pacing presets' names, in services.Pacings order.
func pacingLabels() []string {
	return []string{i18n.T("Dialogue (tight)"), i18n.T("Reading (standard)"), i18n.T("Relaxed"), i18n.T("Fixed gap")}
}

// pacingLabel is a document's pacing's name.
func pacingLabel(pacing string) string {
	i := slices.Index(services.Pacings, pacing)
	if i < 0 {
		i = len(services.Pacings) - 1
	}
	return pacingLabels()[i]
}

// storyContinuity is what the story's model does with scene, personas
// and directions (services.Continuity*).
func storyContinuity(model string) string {
	return services.ContinuityOf(modelInfo(model).Traits)
}

// continuityLabel names a continuity level; continuityTip explains it.
func continuityLabel(level string) string {
	switch level {
	case services.ContinuityInstruct:
		return i18n.T("Directed delivery")
	case services.ContinuityIntensity:
		return i18n.T("Emotion intensity")
	}
	return i18n.T("Line by line")
}

func continuityTip(level string) string {
	switch level {
	case services.ContinuityInstruct:
		return i18n.T("This model hears the scene, each character's persona and each line's direction as an instruction, so the lines carry the moment's feeling. Never spoken.")
	case services.ContinuityIntensity:
		return i18n.T("This model takes no instructions: a line's direction only sets how strong its emotion is (Chatterbox's exaggeration). Pacing and merged turns still apply.")
	}
	return i18n.T("This model says each line as written: scene, personas and directions are kept but not heard. Pacing and merged turns still apply. For directed delivery, use Qwen3-TTS CustomVoice or VoiceDesign.")
}

// storyPerformance is the Performance section.
func (a *app) storyPerformance(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	k := tok(t)
	d := &p.doc
	level := storyContinuity(d.Model)
	ui.Row(c.Key("perf-level")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
		col := k.Text2
		ic := icons.Dialog2
		if level == services.ContinuityInstruct {
			col, ic = k.AccentInk, icons.MagicStick3
		}
		statusMark(c, ic, col, continuityLabel(level)).Shrink(1)
		infoTip(c.Key("perf-level-tip"), continuityTip(level))
	})
	if level == services.ContinuityLines && storyDirected(d) {
		caption(c.Key("perf-advice"), i18n.T("Switch to Qwen3-TTS CustomVoice or VoiceDesign (Setup) to hear the directions.")).TextColor(k.Text3)
	}

	// Pacing.
	labels := pacingLabels()
	cur := pacingLabel(d.Production.Pacing)
	fieldInfo(c, i18n.T("Pacing"), i18n.T("How long the silences between lines are: shorter when a character goes on or answers a question, a little longer after a statement, shortest when a line is cut off (… —). Fixed gap: one gap for every line, as stories made before pacing have. A line's own pause after it wins."), func() {
		if ui.Select(c.Key("perf-pacing"), &cur, labels).Label(i18n.T("Pacing")).Disabled(locked).Changed() {
			if i := slices.Index(labels, cur); i >= 0 {
				d.Production.Pacing = services.Pacings[i]
			}
		}
	})
	// Scene and the AI director: only for models that hear them.
	if level == services.ContinuityLines {
		return
	}
	fieldInfo(c, i18n.T("Scene"), i18n.T("Where and when, who these people are to each other, the mood. Models that take instructions hear it with every line; it is never spoken."), func() {
		ui.TextArea(c.Key("perf-scene"), &d.Scene).Placeholder(i18n.T("Evening at home: the child wants a kitten, the mother hesitates but softens")).
			Label(i18n.T("Scene")).Lines(2, 5).MinHeight(0).Disabled(locked)
	})
	ui.Row(c.Key("perf-director")).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
		label := i18n.T("AI director")
		tip := i18n.T("A language model reads the whole story and fills in the scene, a persona for each character and a direction for each line. You can change any of them.")
		disabled := locked || p.directing || len(storyScript(d, nil).Lines) == 0
		if storyDirected(d) {
			dropButton(c.Key("perf-direct"), icons.MagicStick3, label).Tooltip(tip).Disabled(disabled).Menu(func(m *ui.Menu) {
				if m.Item(i18n.T("Fill in what is empty")).Chosen() {
					a.directStory(p, false)
				}
				if m.Item(i18n.T("Direct again (replace all)")).Chosen() {
					a.directStory(p, true)
				}
				m.Separator()
				if m.Item(i18n.T("Clear scene, personas and directions")).Chosen() {
					a.clearDirection(p)
				}
			})
		} else if secondaryIconButton(c.Key("perf-direct"), icons.MagicStick3, label).Label(label).Tooltip(tip).Disabled(disabled).Clicked() {
			a.directStory(p, false)
		}
		if p.directing {
			ui.Spinner(c)
			caption(c, i18n.T("Directing…"))
		} else if n := directedLines(d); n > 0 {
			caption(c, i18n.N(n, "%d line directed", "%d lines directed", n)).TextColor(k.Text3)
		}
	})
}

// directedLines counts the lines with a direction.
func directedLines(d *services.LongformDoc) int {
	n := 0
	for _, l := range d.Lines {
		if l.Direction != "" && !l.Chapter {
			n++
		}
	}
	return n
}

// clearDirection empties the scene, personas and directions, with Undo.
func (a *app) clearDirection(p *lfPage) {
	before := p.doc
	before.Lines = slices.Clone(p.doc.Lines)
	before.Cast = slices.Clone(p.doc.Cast)
	p.doc.Scene = ""
	for i := range p.doc.Cast {
		p.doc.Cast[i].Persona = ""
	}
	for i := range p.doc.Lines {
		p.doc.Lines[i].Direction, p.doc.Lines[i].Intensity = "", 0
	}
	a.toastUndo(i18n.T("Cleared the scene, personas and directions"), func() {
		p.doc.Scene, p.doc.Lines, p.doc.Cast = before.Scene, before.Lines, before.Cast
	})
}

// intensityLabels are a direction's intensities (0 = not set).
func intensityLabels() []string {
	return []string{i18n.T("Intensity: —"), i18n.T("Restrained"), i18n.X("intensity", "Clear"), i18n.T("Strong")}
}

// directionChip is a line's direction as a chip ("pleading · strong"),
// greyed when the model does not hear it.
func directionChip(c *ui.Context, l *services.StoryLine, heard bool) ui.Element {
	t := c.Theme()
	k := tok(t)
	text := l.Direction
	if l.Intensity == services.IntensityLight || l.Intensity == services.IntensityStrong {
		text += " · " + intensityLabels()[l.Intensity] // "clear" is the usual: unsaid
	}
	col, bg := k.AccentInk, k.AccentSoft
	tip := i18n.T("Direction: %s", text)
	if !heard {
		col, bg = k.Text3, k.Hover
		tip = i18n.T("Direction: %s (this model does not hear it)", text)
	}
	return ui.Text(c, text).FontSize(px(t, fsMicro)).TextColor(col).SingleLine().Ellipsis("…").Padding(px(t, 1), px(t, 6)).
		Radius(px(t, 4)).Background(bg).Shrink(1).MinWidth(0).MaxWidth(px(t, 180)).Tooltip(tip)
}

// lineDirection is the direction row of a line's options.
func (a *app) lineDirection(c *ui.Context, p *lfPage, l *services.StoryLine, locked bool) {
	t := c.Theme()
	formRow(c, "opt-direction", i18n.X("story", "Direction"), i18n.T("How this line is said: “pleading”, “softly, holding back tears”. Models that take instructions hear it; it is never spoken."), func() {
		ui.TextInput(c.Key("line-direction"), &l.Direction).Placeholder(i18n.T("e.g. pleading")).Label(i18n.X("story", "Direction")).
			Width(px(t, 150)).Shrink(1).MinWidth(px(t, 90)).Disabled(locked)
		labels := intensityLabels()
		cur := labels[min(max(l.Intensity, 0), len(labels)-1)]
		if ui.Select(c.Key("line-intensity"), &cur, labels).Label(i18n.T("Intensity")).Width(px(t, 110)).Shrink(1).MinWidth(px(t, 80)).Disabled(locked || l.Direction == "").Changed() {
			l.Intensity = slices.Index(labels, cur)
		}
	})
}

// personaSummary is the Performance section's summary: the pacing.
func personaSummary(d *services.LongformDoc) string { return pacingLabel(d.Production.Pacing) }
