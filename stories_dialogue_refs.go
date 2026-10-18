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
	ready, missing := 0, 0
	for _, m := range cast {
		if v := a.findVoice(m.Voice); v != nil && needsReference(v) {
			missing++
		} else {
			ready++
		}
	}
	summary := i18n.T("%d ready", ready)
	if missing > 0 {
		summary = i18n.T("%d ready, %d made before rendering", ready, missing)
	}
	open := p.open["dialogue-refs"]
	collapsible(c.Key("perf-dialogue-refs"), i18n.T("Character references"), &open, func() {
		caption(c, summary).SingleLine()
	}, func() {
		for _, m := range cast {
			ui.Row(c.Key("dref-"+m.ID)).Gap(px(t, sp2)).AlignItems(ui.Start).Children(func() {
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
		if ghostButton(c.Key("dref-make-"+id), i18n.T("Make reference")).Disabled(locked).Clicked() {
			if v := a.findVoice(id); v != nil {
				a.makeReference(v, nil)
			}
		}
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
// them; then runs again. It reports whether it is making any (the caller
// waits). A voice whose reference failed is not tried again here (Make
// reference in its menu does).
func (a *app) lfMakeReferences(p *lfPage, again func()) bool {
	if p.kind != core.ProjectStory || !(p.doc.Production.Dialogue || clonesDesign(entryFor(p.doc.Model))) {
		return false
	}
	var vs []*core.Voice
	for _, t := range p.spec.Voices {
		if v := a.findVoice(t.VoiceID); needsReference(v) && a.refFailed(v.ID) == "" && a.canMakeReferenceNow(v) {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return false
	}
	n := a.ensureReferences(vs, again)
	a.toast(i18n.N(n, "Making a reference for %d voice first: the story renders after it.", "Making references for %d voices first: the story renders after them.", n))
	return true
}
