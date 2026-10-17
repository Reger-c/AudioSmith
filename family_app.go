package main

// The ActNow family, phase 1 (internal/family; family spec v1.0.4): the
// manifest in $FAMILY/apps, discovery of the other family apps, and the
// claims on the models AudioSmith uses, so that the other apps keep them.
// The UI names other apps only when a release build of another family
// app is installed (famOthers, internal/family OthersForUI; spec §7.10):
// otherwise there is no family wording anywhere, deletes ask as before.

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/family"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/settings"
	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
)

// familyState is the app's handle on the family directory; nil in tests
// and in the MCP relay.
type familyState struct {
	fam *family.Family
	// famDirty asks the family worker to compare the manifest with the
	// settings again (one pending request is enough).
	famDirty chan struct{}
	// famOthers are the other family apps the UI may name (OthersForUI):
	// empty, no family wording. famDeleted maps a catalogue repo (lower
	// case) to the app that deleted it for all apps (the Models page's
	// "Deleted in Dicta · Download again").
	famOthers  []string
	famDeleted map[string]string
}

// familyView is what the UI shows of the family, read on a goroutine.
type familyView struct {
	others  []string
	deleted map[string]string
}

func readFamilyView(fam *family.Family) familyView {
	v := familyView{others: fam.OthersForUI(), deleted: map[string]string{}}
	for _, e := range catalog.Builtin().Entries {
		if name := fam.DeletedIn(e.Repo); name != "" {
			v.deleted[strings.ToLower(e.Repo)] = name
		}
	}
	return v
}

// showFamily takes v on the main thread.
func (a *app) showFamily(v familyView) {
	a.famOthers, a.famDeleted = v.others, v.deleted
	if len(a.famOthers) == 0 {
		a.famDeleted = nil
	}
}

// connectFamily makes AudioSmith's Family (no file touched yet) and has
// downloads claim what they finish. main calls it once the
// single-instance lock is held.
func (a *app) connectFamily() {
	a.fam = family.New(family.Options{})
	a.lib.setFamily(a.fam)
	a.connectFamilyRuntime() // family_coord.go (thread #73)
	a.connectKeys()          // family_keys.go (thread #74)
}

// startFamily sets AudioSmith up in the family directory in the
// background (family.Start: migration claims, then the manifest), then
// follows the settings the manifest names. main calls it in WhenReady,
// after openWindow (the version) and startAPI.
func (a *app) startFamily(dataDir string) {
	fam, lib, version := a.fam, a.lib, a.version
	if fam == nil {
		return
	}
	inputs := func() family.Inputs { return familyInputs(lib.settings.Get(), version, dataDir) }
	fam.OnChange(func(topics []string) {
		a.familyChanged(topics) // family_coord.go: memory requests, shortcut conflicts
		v := readFamilyView(fam)
		a.post(func() {
			a.showFamily(v)
			if slices.Contains(topics, actnow.TopicModels) {
				a.refreshModels() // another app downloaded or deleted a model
			}
			if slices.Contains(topics, actnow.TopicCredentials) || slices.Contains(topics, actnow.TopicApps) {
				a.reloadKeys() // family_keys.go: a key shared, changed or removed
			}
		})
	})
	mygo.App.OnDidBecomeActive(func() { go fam.Reread() }) // spec §5.4: notifications are only hints
	a.famDirty = make(chan struct{}, 1)
	dirty := a.famDirty
	core.On(lib.bus, func(settings.Changed) {
		select {
		case dirty <- struct{}{}:
		default:
		}
	})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := fam.Start(ctx, inputs(), lib.familyWanted)
		cancel()
		if err != nil {
			log.Printf("family: %v (no manifest this time; deletes write no claims)", err)
		}
		v := readFamilyView(fam)
		a.post(func() { a.showFamily(v) })
		a.familyStarted() // family_coord.go
		for range dirty {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			if err := fam.Update(ctx, inputs(), lib.familyWanted); err != nil {
				log.Printf("family: update: %v", err)
			}
			cancel()
		}
	}()
}

// closeFamily stops listening to the family directory (at quit).
func (a *app) closeFamily() {
	if a.fam != nil {
		a.fam.Close()
	}
}

// familyInputs are what AudioSmith's manifest says, from the settings.
func familyInputs(s settings.Settings, version, dataDir string) family.Inputs {
	return family.Inputs{
		Version: version, Beta: betaUpdates(s, version), Hub: modelCache(s), DataDir: dataDir,
		Shortcut: s.Dictation.Shortcut, ShortcutHold: s.Dictation.Mode == settings.DictationHold, ShortcutEnabled: s.Dictation.Enabled,
		ShortcutSince: s.Dictation.EnabledAt.UTC(), APIEnabled: s.API.Enabled, APIPort: s.API.LoopbackPort(), APIKey: s.API.RequireKey,
	}
}

// setFamily makes the next downloads claim what they finish
// (hub.Options.Claimed) and name AudioSmith's running bundle id in their
// lock owner files.
func (l *library) setFamily(fam *family.Family) {
	l.hubMu.Lock()
	l.fam, l.hubCli = fam, nil
	l.hubMu.Unlock()
}

// familyOptions fill the family's part of the downloader's options; the
// caller holds hubMu.
func (l *library) familyOptions(o *hub.Options) {
	fam := l.fam
	if fam == nil {
		return
	}
	o.Family.BundleID = fam.BundleID
	o.Family.Decider = fam.Decider(o.CacheDir)
	o.Claimed = func(ctx context.Context, cacheDir string, d hub.Finished) error {
		return fam.Claimed(ctx, cacheDir, family.ClaimOf(d.Entry, d.Repo, d.Commit, d.Files, d.Aux, models.OriginDownloaded))
	}
}

// familyWanted are the models AudioSmith claims when it first uses the
// model folder hub (family.Migrate): the catalogue's, and the custom
// repos it knows of — the Studio's and those of the downloads it still
// remembers (finished model.download jobs).
func (l *library) familyWanted(cacheDir string) []models.Claim {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var custom []string
	var d studioDraft
	if ok, err := l.svc.Drafts.Load(ctx, studioDraftKey, &d); err == nil && ok && d.CustomRepo != "" {
		custom = append(custom, d.CustomRepo)
	}
	done, err := l.sched.List(ctx, jobs.Filter{Kind: hub.KindDownload, States: []jobs.State{jobs.StateSucceeded}})
	if err != nil {
		log.Printf("family: downloads: %v", err)
	}
	for _, j := range done {
		var p hub.DownloadParams
		if json.Unmarshal(j.Params, &p) == nil && p.Repo != "" {
			if _, builtin := catalog.Builtin().ByID(p.ID); !builtin && !slices.Contains(custom, p.Repo) {
				custom = append(custom, p.Repo)
			}
		}
	}
	return family.Wanted(cacheDir, catalog.Builtin().Entries, custom)
}

// familyErase forgets AudioSmith's claims in cacheDir after "Erase
// everything" deleted its models (the next start claims what other apps
// kept on disk).
func (a *app) familyErase(cacheDir string) {
	if a.fam == nil {
		return
	}
	if err := a.fam.ForgetAll(cacheDir); err != nil {
		log.Printf("family: erase: %v", err)
	}
}

// familyNames are the apps the UI may name among users (spec §7.10):
// none unless another family app's release build is installed.
func (a *app) familyNames(users []models.User) []string {
	if len(a.famOthers) == 0 {
		return nil
	}
	return family.VisibleNames(users...)
}
