package main

// Stories → Advanced: the experimental switches of the emotion
// experiment's second round (thread #78; services story_track.go,
// story_steer.go): a character's part said in one go, punctuation that
// follows the emotion, and steadier sampling.

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
)

func (a *app) storyExperiments(c *ui.Context, d *services.LongformDoc, locked bool) {
	formRow(c, "perf-tracks-row", i18n.T("Each part in one go"), i18n.T("Experimental. Each character's lines are said in one synthesis per stretch between their turns, in their baseline feeling, so the voice carries on from line to line; the forced aligner (%s) cuts them apart. A stretch that cannot be cut cleanly is made line by line.", modelRefID(catalog.DefaultAlign)), func() {
		ui.Switch(c.Key("perf-tracks"), &d.Production.Tracks).Label(i18n.T("Each part in one go")).Disabled(locked)
	})
	formRow(c, "perf-steer-row", i18n.T("Punctuation follows the emotion"), i18n.T("Experimental. Lines are synthesized with punctuation that follows their emotion: a calm line without its “!”, a hesitant one with “…”. The words never change, and the story shows the lines as written."), func() {
		ui.Switch(c.Key("perf-steer"), &d.Production.Steer).Label(i18n.T("Punctuation follows the emotion")).Disabled(locked)
	})
	steady := d.Production.Steadier()
	formRow(c, "perf-steady-row", i18n.T("Steadier delivery"), i18n.T("Experimental. Tighter sampling (temperature %.2f, top-p %.2f) and a seed of each character's own: lines are less often overplayed, and a render made again sounds the same.", services.StoryTemperature, services.StoryTopP), func() {
		if ui.Switch(c.Key("perf-steady"), &steady).Label(i18n.T("Steadier delivery")).Disabled(locked).Changed() {
			d.Production.SetSteadier(steady)
		}
	})
}
