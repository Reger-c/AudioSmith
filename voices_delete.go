package main

// Deleting a voice (#108): the confirmation says where the voice is
// used, the delete can be undone from its toast for a few seconds (the
// voice's files wait in the library's trash, services/voices_trash.go),
// and the places that named it show it as deleted, to choose another.

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/oplog"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// undoTime is how long a deleted voice's toast offers Undo (the time
// stops while the pointer rests on it or the window is in the back).
const undoTime = 10 * time.Second

// voiceUsage is where a voice is used, for the delete's confirmation.
type voiceUsage struct {
	id    string
	takes int
	// places name where the voice is chosen: stories, books, dubs and
	// workflows by title, the Studio, the batch queue, calls, agents.
	places []string
}

// askDeleteVoice asks before deleting a voice, saying where it is used
// once that is known.
func (a *app) askDeleteVoice(id string) {
	a.deleteVoice, a.deleteOpen, a.deleteUsage = id, true, nil
	a.loadDeleteUsage(id)
}

// loadDeleteUsage finds where voice id is used, in the background.
func (a *app) loadDeleteUsage(id string) {
	var here []string
	if a.studioVoice == id {
		here = append(here, i18n.T("Studio"))
	}
	if a.batch.settings.Voice == id {
		here = append(here, i18n.T("Batch dubbing"))
	}
	if a.prefs.Calls.Voice == id {
		here = append(here, i18n.T("Phone calls"))
	}
	for _, b := range a.prefs.API.Bindings {
		if b.Voice == id {
			here = append(here, i18n.T("Agent “%s”", firstOf(b.Label, b.Client)))
		}
	}
	voices, dubs, wfs := a.lib.svc.Voices, a.dubSvc, a.wfSvc
	a.background(func() {
		ctx := context.Background()
		u := voiceUsage{id: id}
		if vu, err := voices.Usage(ctx, id); err == nil {
			u.takes = vu.Takes
			for _, p := range vu.Projects {
				u.places = append(u.places, quoteTitle(p.Title))
			}
		}
		if dubs != nil {
			if recs, err := dubs.List(ctx, store.DubQuery{AllBatches: true, Limit: 500}); err == nil {
				for _, r := range recs {
					if p, err := dubs.Get(ctx, r.ID); err == nil && dubUsesVoice(p.Cast, p.Segments, id) {
						u.places = append(u.places, quoteTitle(firstOf(p.Title, i18n.T("Untitled"))))
					}
				}
			}
		}
		if wfs != nil {
			if recs, err := wfs.List(ctx); err == nil {
				for _, r := range recs {
					if w, _, err := wfs.Get(ctx, r.ID); err == nil && slices.ContainsFunc(w.Nodes, func(n workflow.Node) bool { return n.P.Voice == id }) {
						u.places = append(u.places, quoteTitle(firstOf(w.Name, i18n.T("Untitled workflow"))))
					}
				}
			}
		}
		u.places = append(u.places, here...)
		a.post(func() {
			if a.deleteVoice == id {
				a.deleteUsage = &u
			}
		})
	})
}

// quoteTitle quotes a title as the language does: “A”, 《A》.
func quoteTitle(s string) string { return i18n.T("“%s”", s) }

// usageText says where a voice is used, for its delete's confirmation:
// "" when nowhere.
func usageText(u *voiceUsage) string {
	if u == nil || len(u.places) == 0 {
		return ""
	}
	const most = 3
	names := u.places
	more := ""
	if len(names) > most {
		names, more = names[:most], "…"
	}
	return i18n.N(len(u.places), "It is used in %[2]s%[3]s. After it is deleted, choose another voice there.",
		"It is used in %[1]d places: %[2]s%[3]s. After it is deleted, choose another voice there.",
		len(u.places), strings.Join(names, i18n.T(", ")), more)
}

func (a *app) voiceDeleteAlert(c *ui.Context) {
	v := a.findVoice(a.deleteVoice)
	if v == nil {
		a.deleteOpen = false
		return
	}
	msg := i18n.T("Its reference, samples and portrait are deleted too. Takes made with it stay.")
	switch u := a.deleteUsage; {
	case u == nil || u.id != v.ID:
		msg += "\n\n" + i18n.T("Checking where it is used…")
	case usageText(u) != "":
		msg = usageText(u) + "\n\n" + msg
	}
	if ui.AlertDialog(c, &a.deleteOpen, i18n.T("Delete “%s”?", v.Name), msg, i18n.T("Cancel"), i18n.T("Delete voice")) == 1 {
		places := 0
		if u := a.deleteUsage; u != nil && u.id == v.ID {
			places = len(u.places)
		}
		oplog.Info("voice.delete.ask", "id", v.ID, "name", v.Name, "places", places)
		a.deleteVoiceNow(v.ID)
	}
}

// deleteVoiceNow deletes a voice in the background; VoiceChanged updates
// the views. Its toast offers Undo for a few seconds.
func (a *app) deleteVoiceNow(id string) {
	if a.pl.Current() == "voice:"+id {
		a.pl.Stop()
	}
	svc := a.lib.svc.Voices
	a.background(func() {
		err := svc.Trash(context.Background(), id)
		a.post(func() {
			if err != nil {
				a.toastWarn(i18n.T("Could not delete: %v", err))
				return
			}
			a.toastAction(ui.Toast{
				ID: "voice-deleted-" + id, Title: i18n.T("Voice deleted"), Timeout: undoTime,
				Action: i18n.T("Undo"), OnAction: func() { a.undoDeleteVoice(id) },
				OnClose: func() { a.purgeDeletedVoice(id) },
			})
		})
	})
}

// undoDeleteVoice brings a deleted voice back from the trash.
func (a *app) undoDeleteVoice(id string) {
	a.undoing[id] = true
	svc := a.lib.svc.Voices
	a.background(func() {
		v, err := svc.Restore(context.Background(), id)
		a.post(func() {
			delete(a.undoing, id)
			if err != nil {
				a.toastWarn(i18n.T("Could not restore the voice: %v", err))
				return
			}
			a.toast(i18n.T("Restored “%s”", v.Name))
		})
	})
}

// purgeDeletedVoice removes a deleted voice's files for good once its
// toast closed, unless Undo is bringing it back.
func (a *app) purgeDeletedVoice(id string) {
	if a.undoing[id] {
		return
	}
	svc := a.lib.svc.Voices
	a.background(func() { svc.Purge(id) })
}

// dubUsesVoice reports whether a dub's cast or one of its lines names
// voice id.
func dubUsesVoice(cast map[string]string, segs []dub.Segment, id string) bool {
	for _, v := range cast {
		if v == id {
			return true
		}
	}
	return slices.ContainsFunc(segs, func(s dub.Segment) bool { return s.Voice == id })
}

// voiceGone names a voice reference whose saved voice is no longer in
// the library: the places that named it show this, to choose another.
func voiceGone() string { return i18n.T("Voice deleted") }
