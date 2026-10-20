package main

// Whole dialogue's references (thread #83): the dialogue model clones
// each character from a reference, and copies its noise, loudness,
// intonation and pace. Under the switch, folded: each character's
// reference and what the checker says about it for dialogue. A designed
// voice without a reference gets one before the story renders
// (lfMakeReferences); the render then starts by itself.

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/refcheck"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
)

// storyDialogueRefs lists the characters' references.
func (a *app) storyDialogueRefs(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	var cast []services.CastMember
	for _, m := range p.doc.Cast {
		if m.Voice != "" {
			cast = append(cast, m)
		}
	}
	if len(cast) == 0 {
		return
	}
	missing := 0
	for _, m := range cast {
		if v := a.findVoice(m.Voice); v != nil && needsReference(v) {
			missing++
		}
	}
	var trailing func()
	if missing > 0 {
		trailing = func() { caption(c, i18n.T("%d to make", missing)).SingleLine().Shrink(0) }
	}
	open := p.open["dialogue-refs"]
	collapsible(c.Key("perf-dialogue-refs"), i18n.T("Character references"), &open, trailing, func() {
		for _, m := range cast {
			ui.Row(c.Key("dref-" + m.ID)).Gap(px(t, sp2)).AlignItems(ui.Start).Children(func() {
				ui.Text(c, m.Name).FontSize(px(t, fsCaption)).FontWeight(600).Width(px(t, 96)).SingleLine().Ellipsis("…").Shrink(0)
				ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
					a.dialogueRefStatus(c, m.Voice, locked)
				})
			})
		}
	})
	p.open["dialogue-refs"] = open
}

// dialogueRefStatus says where a voice's dialogue reference comes from.
func (a *app) dialogueRefStatus(c *ui.Context, ref string, locked bool) {
	if strings.HasPrefix(ref, services.SpeakerRef) {
		caption(c, i18n.T("A short sample in this voice is made first."))
		return
	}
	v := a.findVoice(ref)
	switch {
	case v == nil:
		caption(c, i18n.T("Missing voice"))
	case v.Kind == core.VoiceBuiltin:
		caption(c, i18n.T("A short sample in this voice is made first."))
	case a.refMaking(v.ID) != nil:
		done, total := a.refMaking(v.ID).progress()
		ui.Row(c).Gap(px(c.Theme(), sp1)).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			caption(c, i18n.T("Making a reference… %d of %d takes", done, total))
		})
	case needsReference(v):
		caption(c, i18n.T("No reference yet: it is made before the story renders."))
		if err := a.refFailed(v.ID); err != "" {
			caption(c, i18n.T("Could not make the reference: %s", err)).TextColor(c.Theme().Warning)
		}
		id := v.ID
		ui.Row(c).Children(func() {
			if ghostButton(c.Key("dref-make-"+id), i18n.T("Make reference")).Disabled(locked).Clicked() {
				if v := a.findVoice(id); v != nil {
					a.makeReference(v, nil)
				}
			}
		})
	case v.RefCheck != nil:
		refCheckView(c, "dref-check-"+v.ID, v.RefCheck, refcheck.UseDialogue)
	case v.Locked:
		caption(c, i18n.T("Its locked take."))
	default:
		caption(c, i18n.T("Its own clip."))
	}
}

// lfMakeReferences makes the references a story's designed voices lack
// before it renders, when whole dialogue or the story's model clones
// them, and has those made from a voice heard once (#93); then runs
// again. It reports whether it is making or checking any (the caller
// waits). A voice whose reference failed is not tried again here (Make
// reference in its menu does).
func (a *app) lfMakeReferences(p *lfPage, again func()) bool {
	book := p.kind == core.ProjectAudiobook && p.doc.Production.Dialogue && len(p.blocks) > 0 // its passages' speakers (thread #88)
	if !book && (p.kind != core.ProjectStory || !(p.doc.Production.Dialogue || clonesDesign(entryFor(p.doc.Model)))) {
		return false
	}
	speakers := map[string]bool{}
	for _, ch := range p.spec.Chapters {
		for _, l := range ch.Lines {
			if l.Block > 0 && l.Character != "" && len(l.Spans) > 0 {
				speakers[l.Spans[0].Voice] = true
			}
		}
	}
	var vs []*core.Voice
	for key, t := range p.spec.Voices {
		if book && !speakers[key] {
			continue
		}
		if v := a.findVoice(t.VoiceID); a.refWork(v) {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return false
	}
	n, checking := a.ensureReferences(vs, again)
	if n == 0 {
		return checking > 0 // heard first (#93): quick, no note
	}
	if book {
		a.toast(i18n.N(n, "Making a reference for %d voice first: the book renders after it.", "Making references for %d voices first: the book renders after them.", n))
		return true
	}
	a.toast(i18n.N(n, "Making a reference for %d voice first: the story renders after it.", "Making references for %d voices first: the story renders after them.", n))
	return true
}
