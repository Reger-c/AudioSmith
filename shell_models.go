package main

// The Models page: every catalogue entry with what of it is on disk, and
// download (resumable, cancellable), verify, delete; the licence dialog
// that gates models whose licence needs accepting. The page's view is
// shell_models_view.go.
//
// Downloads are "model.download" jobs on the net lane (hub.DownloadSpec):
// the page submits them, follows them through jobs.Updated
// (downloadUpdated) and cancels them by id. A download the app quit in
// the middle of continues at the next start, where it stopped.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/licence"
)

// modelsState is the Models page's view state.
type modelsState struct {
	models    map[string]*modelState // by modelKey
	deleteAsk bool
	deleting  catalog.Entry
	// deleteShared are the other ActNow apps that may use the model
	// being deleted (hub.Family): its files stay (spec §7.10.3).
	deleteShared []string
	// modelsFind is the search (friendly and technical names, tags);
	// modelsDetail the cards whose details are open, by id.
	modelsFind   string
	modelsDetail map[string]bool
}

// modelState is what the page knows of one entry.
type modelState struct {
	entry       catalog.Entry
	local       hub.Local
	known       bool // local was read
	downloading bool // its download job is queued or running
	jobID       string
	verifying   bool
	progress    hub.Progress
	cancelling  bool
	err         string
}

// licenceDialog is the licence acceptance dialog's state.
type licenceDialog struct {
	licenceOpen  bool
	licenceAgree bool
	licenceEntry catalog.Entry
	licenceThen  func()
}

// hubClient is the downloader for the current settings.
func (a *app) hubClient() *hub.Client { return a.lib.hub() }

// resetHub makes the next download use the settings as they are now;
// downloads running keep their client.
func (a *app) resetHub() { a.lib.resetHub() }

// modelKey identifies an entry on the page: its catalogue id, or its repo
// for one outside the catalogue (the Studio's custom repo).
func modelKey(e catalog.Entry) string {
	if e.ID == "" || e.ID == "custom" {
		return "repo:" + e.Repo
	}
	return e.ID
}

func (a *app) model(e catalog.Entry) *modelState {
	k := modelKey(e)
	m := a.models[k]
	if m == nil {
		m = &modelState{entry: e}
		a.models[k] = m
	}
	return m
}

func (a *app) downloadsRunning() int {
	n := 0
	for _, m := range a.models {
		if m.downloading {
			n++
		}
	}
	return n
}

// refreshModels reads what of every entry is on disk, and what the
// removed models left there (models_removed.go), in the background.
func (a *app) refreshModels() {
	client := a.hubClient()
	entries := catalog.Builtin().Entries
	a.async(func() {
		for _, e := range entries {
			l, err := client.Status(context.Background(), e)
			a.post(func() { a.setLocal(e, l, err) })
		}
	})
	a.refreshRemoved()
}

// refreshModel reads what of e is on disk, in the background.
func (a *app) refreshModel(e catalog.Entry) {
	client := a.hubClient()
	a.async(func() {
		l, err := client.Status(context.Background(), e)
		a.post(func() { a.setLocal(e, l, err) })
	})
}

func (a *app) setLocal(e catalog.Entry, l hub.Local, err error) {
	m := a.model(e)
	m.local, m.known = l, true
	if err != nil {
		m.err = err.Error()
	}
	a.localLLMChanged(e) // local_llm.go
}

// download queues the download of e, asking for its licence first when it
// needs one.
func (a *app) download(e catalog.Entry) {
	m := a.model(e)
	if m.downloading {
		return
	}
	if licence.Required(e) != nil && !licence.Covers(a.accepted, e) {
		a.askLicence(e, func() { a.download(e) })
		return
	}
	m.downloading, m.cancelling, m.err, m.progress, m.jobID = true, false, "", hub.Progress{Repo: e.Repo}, ""
	sched := a.lib.sched
	spec := hub.DownloadSpec(e, i18n.T("Download %s", modelRef(e)))
	a.logs.Append(core.LogEntry{Source: "hub", Message: "downloading " + e.Repo})
	a.async(func() {
		id, err := sched.Submit(context.Background(), spec)
		a.post(func() {
			m := a.model(e)
			if err != nil {
				m.downloading, m.err = false, err.Error()
				return
			}
			if m.jobID == "" {
				m.jobID = id
			}
			if m.cancelling {
				a.cancelDownload(e)
			}
		})
	})
}

// downloadUpdated follows a download job; it runs on the main thread.
func (a *app) downloadUpdated(j jobs.Job) {
	var p hub.DownloadParams
	if json.Unmarshal(j.Params, &p) != nil {
		return
	}
	e, err := hub.EntryOf(p)
	if err != nil {
		return
	}
	m := a.model(e)
	if m.jobID != "" && m.jobID != j.ID && m.downloading {
		return // an older job of the same model
	}
	m.jobID = j.ID
	switch j.State {
	case jobs.StateWaiting, jobs.StateQueued, jobs.StateRunning:
		m.downloading = true
		m.progress = hub.Progress{Repo: e.Repo, Bytes: j.Progress.Bytes, TotalBytes: j.Progress.TotalBytes, Mirror: j.Progress.Message}
		if j.Progress.Stage == hub.StageWaiting {
			m.progress.Mirror, m.progress.Waiting, m.progress.WaitingFor = "", true, j.Progress.Message
		}
		return
	}
	if !m.downloading {
		return // already handled
	}
	m.downloading, m.cancelling = false, false
	a.refreshModel(e)
	lg := a.logs
	switch j.State {
	case jobs.StateSucceeded:
		lg.Append(core.LogEntry{Source: "hub", Level: core.SeveritySuccess, Message: "downloaded " + e.Repo})
		a.notify(core.SeveritySuccess, i18n.T("%s is downloaded", modelRef(e)), "", "/models")
		if a.needDownload != nil && a.needDownload.Repo == e.Repo {
			a.needDownload, a.jobError = nil, ""
		}
	case jobs.StateCancelled:
		lg.Append(core.LogEntry{Source: "hub", Message: "cancelled " + e.Repo})
		a.toast(i18n.T("Download of %s cancelled; it resumes where it stopped.", modelRef(e)))
	default: // failed, interrupted
		err := hub.ErrorOf(j.Err)
		lg.Append(core.LogEntry{Source: "hub", Level: core.SeverityError, Message: e.Repo + ": " + err.Error()})
		msg, link := downloadError(err)
		m.err = msg
		a.notify(core.SeverityError, i18n.T("Could not download %s", modelRef(e)), msg, link)
	}
}

// downloadError explains a download failure and links to where it is fixed.
func downloadError(err error) (msg, link string) {
	switch {
	case errors.Is(err, hub.ErrGated):
		return i18n.T("The model is gated: accept its terms on huggingface.co, then add an access token."), "/settings/models"
	case errors.Is(err, hub.ErrDiskSpace):
		return i18n.T("Not enough free disk space. %v", err), "/settings/models"
	case errors.Is(err, hub.ErrOffline):
		return i18n.T("Could not reach Hugging Face or its mirror. Check the network and the proxy."), "/settings/models"
	case errors.Is(err, hub.ErrChecksum):
		return i18n.T("A file arrived damaged. Download again to retry."), ""
	}
	return err.Error(), ""
}

// cancelDownload cancels the download job of e by its id; the files
// fetched so far stay for resuming.
func (a *app) cancelDownload(e catalog.Entry) {
	m := a.model(e)
	if !m.downloading {
		return
	}
	m.cancelling = true
	if m.jobID == "" {
		return // cancelled when Submit returns the id
	}
	sched, id := a.lib.sched, m.jobID
	a.async(func() { sched.Cancel(context.Background(), id) })
}

func (a *app) verifyModel(e catalog.Entry) {
	m := a.model(e)
	if m.verifying || m.downloading {
		return
	}
	m.verifying = true
	client := a.hubClient()
	a.async(func() {
		bad, err := client.Verify(context.Background(), e)
		l, _ := client.Status(context.Background(), e)
		a.post(func() {
			m.verifying, m.local = false, l
			switch {
			case err != nil:
				msg, link := downloadError(err)
				a.notify(core.SeverityError, i18n.T("Could not check %s", modelRef(e)), msg, link)
			case len(bad) == 0:
				a.toast(i18n.T("Every file of %s checks out", modelRef(e)))
			default:
				a.notify(core.SeverityWarning, i18n.T("%s is damaged", modelRef(e)),
					i18n.N(len(bad), "%d file is missing or damaged; download again to repair it.", "%d files are missing or damaged; download again to repair them.", len(bad)), "/models")
			}
		})
	})
}

// askDelete asks before deleting e, after finding out (LaunchServices,
// in the background) whether other ActNow apps may use it.
func (a *app) askDelete(e catalog.Entry) {
	fam := a.hubClient().Options().Family
	a.async(func() {
		users := fam.UsersOf(e.Repo)
		a.post(func() { a.deleting, a.deleteShared, a.deleteAsk = e, users, true })
	})
}

// deleteModel deletes e's files, unless another ActNow app may use them
// (hub.Client.Remove keeps them).
func (a *app) deleteModel(e catalog.Entry) {
	client := a.hubClient()
	m := a.model(e)
	a.async(func() {
		r, err := client.Remove(context.Background(), e)
		l, _ := client.Status(context.Background(), e)
		a.post(func() {
			m.local, m.known = l, true
			holder, busy := hub.Holder(err)
			switch {
			case busy:
				a.toast(busyText(holder)) // models_removed.go
			case err != nil:
				a.toast(i18n.T("Could not delete %s: %v", modelRef(e), err))
			case len(r.Kept) > 0:
				a.logs.Append(core.LogEntry{Source: "hub", Message: "kept " + e.Repo + ": used by " + strings.Join(r.Kept, ", ")})
				a.toast(sharedText(r.Kept, m.local.Bytes)) // installed since the dialog
			default:
				a.toast(i18n.T("Deleted %s", modelRef(e)))
			}
		})
	})
}

// askLicence shows the licence dialog for e; then (may be nil) runs after
// the user accepted.
func (a *app) askLicence(e catalog.Entry, then func()) {
	a.licenceEntry, a.licenceThen, a.licenceAgree, a.licenceOpen = e, then, false, true
}

// licenceModal is the licence acceptance dialog.
func (a *app) licenceModal(c *ui.Context) {
	t := c.Theme()
	e := a.licenceEntry
	lic := e.Licence
	if ui.Modal(c, &a.licenceOpen, func() {
		ui.Column(c).Width(px(t, 440)).Gap(px(t, sp3)).Children(func() {
			sectionHeading(c, i18n.T("Licence of %s", modelRef(e)))
			fillCard(c).Gap(px(t, sp2)).Children(func() {
				ui.Text(c, i18n.T("%s's weights are licensed under %s.", modelRef(e), lic.Name))
				if !lic.Commercial {
					ui.Text(c, i18n.T("You may use the model, and what you make with it, for non-commercial purposes only. AudioSmith runs it only after you accept.")).
						FontSize(px(t, fsCaption)).TextColor(tok(t).Text2)
				}
				if lic.URL != "" {
					ui.Link(c, i18n.T("Read the licence"), lic.URL)
				}
			})
			ui.Checkbox(c.Key("licence-agree"), &a.licenceAgree, i18n.T("I have read the licence and will use this model only as it allows."))
			ui.Row(c).Gap(px(t, sp2)).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("licence-cancel"), i18n.T("Cancel")).Clicked() {
					a.licenceOpen, a.licenceThen = false, nil
				}
				if primaryButton(c.Key("licence-accept"), i18n.T("Accept")).Disabled(!a.licenceAgree).Clicked() {
					a.acceptLicence(e, a.licenceThen)
					a.licenceOpen, a.licenceThen = false, nil
				}
			})
		})
	}).Dismissed() {
		a.licenceThen = nil
	}
}

// acceptLicence records the acceptance of e's licence for e, then runs then.
func (a *app) acceptLicence(e catalog.Entry, then func()) {
	gate := a.licences
	acc := core.LicenceAcceptance{Licence: e.Licence.ID, Model: e.Repo}
	a.async(func() {
		err := gate.Accept(context.Background(), acc)
		list, _ := gate.List(context.Background())
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not save the acceptance: %v", err))
				return
			}
			a.accepted = list
			if then != nil {
				then()
			}
		})
	})
}
