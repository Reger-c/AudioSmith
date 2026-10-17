package main

// ActNow family apps that share the model folder (the family spec's
// phase 0; internal/hub/family.go): what Models → Delete, Clean up and
// Settings → Storage → Erase everything keep for them, and the words for
// a model another app is downloading.

import (
	"cmp"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// keptReason says in the log why Clean up kept l.
func keptReason(l hub.Leftover) string {
	if l.Busy {
		return "its lock is held by " + cmp.Or(l.BusyBy, "another process")
	}
	return "used by " + strings.Join(l.UsedBy, ", ")
}

// busyText says another app holds a model's lock (hub.Holder): it
// downloads or deletes the model now.
func busyText(holder string) string {
	if holder == "" {
		holder = i18n.T("Another app")
	}
	return i18n.T("%s is downloading or using this model. Try again later.", holder)
}

// catalogRepos are the repos of the catalogue's models and their
// auxiliary repos.
func catalogRepos() []string {
	var out []string
	for _, e := range catalog.Builtin().Entries {
		for _, r := range append([]string{e.Repo}, auxRepos(e)...) {
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

func auxRepos(e catalog.Entry) []string {
	var out []string
	for _, x := range e.Aux {
		out = append(out, x.Repo)
	}
	return out
}

// eraseKeeps finds, in the background, the models on disk that other
// ActNow apps may use, which "Erase everything" with the models keeps,
// for its confirmation.
func (a *app) eraseKeeps() {
	opts := a.hubClient().Options()
	a.resetKeeps = ""
	a.async(func() {
		ls, _ := hub.Leftovers(opts.CacheDir, catalogRepos(), opts.Family)
		var names []string
		var bytes int64
		for _, l := range ls {
			if len(l.UsedBy) == 0 {
				continue
			}
			bytes += l.Bytes
			if name := catalog.Builtin().DisplayName(l.Repo); !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
		text := ""
		if len(names) > 0 {
			text = i18n.T("These models are also used by other ActNow apps and will be kept: %s (%s)", listJoin(names), humanBytes(bytes))
		}
		a.post(func() { a.resetKeeps = text })
	})
}

// waitingText says a download waits for another app that downloads a
// file it needs (spec §7.10.4).
func waitingText(p hub.Progress) string {
	pct := 0
	if p.TotalBytes > 0 {
		pct = int(100 * p.Bytes / p.TotalBytes)
	}
	app := p.WaitingFor
	if app == "" {
		app = i18n.T("another app")
	}
	return i18n.T("Downloading in %s… %d%%", app, pct)
}
