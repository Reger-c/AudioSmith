package main

// The Models page: every catalogue entry with what of it is on disk, and
// download (resumable, cancellable), verify, delete; the licence dialog
// that gates models whose licence needs accepting.
//
// Downloads are "model.download" jobs on the net lane (hub.DownloadSpec):
// the page submits them, follows them through jobs.Updated
// (downloadUpdated) and cancels them by id. A download the app quit in
// the middle of continues at the next start, where it stopped.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo"
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

// refreshModels reads what of every entry is on disk, in the background.
func (a *app) refreshModels() {
	client := a.hubClient()
	entries := catalog.Builtin().Entries
	a.async(func() {
		for _, e := range entries {
			l, err := client.Status(context.Background(), e)
			a.post(func() { a.setLocal(e, l, err) })
		}
	})
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
	spec := hub.DownloadSpec(e, i18n.T("Download %s", e.Label))
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
		a.notify(core.SeveritySuccess, i18n.T("%s is downloaded", e.Label), "", "/models")
		if a.needDownload != nil && a.needDownload.Repo == e.Repo {
			a.needDownload, a.jobError = nil, ""
		}
	case jobs.StateCancelled:
		lg.Append(core.LogEntry{Source: "hub", Message: "cancelled " + e.Repo})
		a.toast(i18n.T("Download of %s cancelled; it resumes where it stopped.", e.Label))
	default: // failed, interrupted
		err := hub.ErrorOf(j.Err)
		lg.Append(core.LogEntry{Source: "hub", Level: core.SeverityError, Message: e.Repo + ": " + err.Error()})
		msg, link := downloadError(err)
		m.err = msg
		a.notify(core.SeverityError, i18n.T("Could not download %s", e.Label), msg, link)
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
				a.notify(core.SeverityError, i18n.T("Could not check %s", e.Label), msg, link)
			case len(bad) == 0:
				a.toast(i18n.T("Every file of %s checks out", e.Label))
			default:
				a.notify(core.SeverityWarning, i18n.T("%s is damaged", e.Label),
					i18n.N(len(bad), "%d file is missing or damaged; download again to repair it.", "%d files are missing or damaged; download again to repair them.", len(bad)), "/models")
			}
		})
	})
}

func (a *app) deleteModel(e catalog.Entry) {
	client := a.hubClient()
	m := a.model(e)
	a.async(func() {
		err := client.Delete(context.Background(), e)
		l, _ := client.Status(context.Background(), e)
		a.post(func() {
			m.local, m.known = l, true
			if err != nil {
				a.toast(i18n.T("Could not delete %s: %v", e.Label, err))
			} else {
				a.toast(i18n.T("Deleted %s", e.Label))
			}
		})
	})
}

func (a *app) modelsPage(c *ui.Context) {
	t := c.Theme()
	header(c, i18n.T("Models"), func() {
		if ui.Button(c.Key("models-refresh"), i18n.T("Refresh")).Clicked() {
			a.refreshModels()
		}
		if ui.Button(c.Key("models-folder"), i18n.T("Open model folder")).Clicked() {
			mygo.Shell.OpenPath(a.hubClient().Options().CacheDir)
		}
		if ui.Button(c.Key("models-settings"), i18n.T("Download settings")).Clicked() {
			a.navigate("/settings/models")
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(px(t, 16), px(t, 20)).Gap(px(t, 10)).MaxWidth(px(t, 860)).Children(func() {
			for _, e := range catalog.Builtin().Entries {
				a.modelCard(c, e)
			}
		})
	})
}

func (a *app) modelCard(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	m := a.model(e)
	eng := a.engineSummary()
	loaded := false
	for _, r := range eng.Loaded {
		loaded = loaded || r == e.Repo
	}
	ui.Row(c.Key("model-"+e.ID)).Gap(px(t, 16)).Padding(px(t, 12)).Radius(10).Border(1, t.Border).Background(t.Surface).AlignItems(ui.Start).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 5)).Children(func() {
			a.modelInfo(c, e, m, loaded)
		})
		ui.Column(c).Shrink(0).Gap(px(t, 8)).AlignItems(ui.End).Children(func() {
			ui.Text(c, a.modelStateText(e, m)).FontSize(px(t, 12)).TextColor(t.TextMuted)
			ui.Row(c).Gap(px(t, 6)).Children(func() {
				a.modelActions(c, e, m, loaded)
			})
		})
	})
}

// modelInfo describes an entry: name, what it does, its size and licence,
// and the download in progress.
func (a *app) modelInfo(c *ui.Context, e catalog.Entry, m *modelState, loaded bool) {
	t := c.Theme()
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, e.Label).FontSize(px(t, 14)).Bold()
		if e.Quantization != "" {
			ui.Badge(c, e.Quantization)
		}
		if e.Hidden {
			ui.Badge(c, i18n.T("Preview"))
		}
		if loaded {
			ui.Badge(c, i18n.T("In memory"))
		}
	})
	ui.Text(c, i18n.T(e.About)).FontSize(px(t, 12)).TextColor(t.TextMuted)
	var facts []string
	if s := catalog.HumanSize(e.Size); s != "" {
		facts = append(facts, s)
	}
	if n := len(e.Languages); n > 0 {
		facts = append(facts, i18n.N(n, "%d language", "%d languages", n))
	}
	if n := len(e.Voices); n > 0 {
		facts = append(facts, i18n.N(n, "%d voice", "%d voices", n))
	}
	if e.MinMemoryGB > 0 {
		facts = append(facts, i18n.T("needs %g GB of memory", e.MinMemoryGB))
	}
	facts = append(facts, e.Repo)
	ui.Text(c, strings.Join(facts, " · ")).FontSize(px(t, 11)).TextColor(t.TextMuted).Selectable()

	// The licence.
	ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
		lic := e.Licence
		use := i18n.T("commercial use allowed")
		color := t.Success
		if !lic.Commercial {
			use, color = i18n.T("non-commercial use only"), t.Warning
		}
		ui.Box(c).Size(px(t, 6), px(t, 6)).Radius(px(t, 3)).Background(color)
		ui.Text(c, lic.Name+" · "+use).FontSize(px(t, 12))
		if lic.RequiresAcceptance {
			if licence.Covers(a.accepted, e) {
				ui.Text(c, "· "+i18n.T("accepted")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			} else if ui.Button(c.Key("licence"), i18n.T("Review licence…")).Clicked() {
				a.askLicence(e, nil)
			}
		}
		if lic.URL != "" {
			ui.Link(c, i18n.T("Model card"), lic.URL).FontSize(px(t, 12))
		}
	})

	if m.downloading {
		frac := -1.0
		if m.progress.TotalBytes > 0 {
			frac = float64(m.progress.Bytes) / float64(m.progress.TotalBytes)
		}
		ui.Progress(c, frac).Label(i18n.T("Downloading %s", e.Label))
		info := i18n.T("%s of %s", mb(m.progress.Bytes), mb(m.progress.TotalBytes))
		if m.progress.Mirror != "" {
			info += " · " + strings.TrimPrefix(m.progress.Mirror, "https://")
		}
		ui.Text(c, info).FontSize(px(t, 11)).TextColor(t.TextMuted)
	}
	if m.err != "" && !m.downloading {
		ui.Text(c, m.err).FontSize(px(t, 12)).TextColor(t.Danger).Selectable()
	}
}

// modelActions are the buttons of an entry for its state.
func (a *app) modelActions(c *ui.Context, e catalog.Entry, m *modelState, loaded bool) {
	st := m.local.State
	switch {
	case m.downloading:
		if ui.Button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
			a.cancelDownload(e)
		}
	case st == hub.Partial:
		if ui.PrimaryButton(c.Key("download"), i18n.T("Resume")).Clicked() {
			a.download(e)
		}
	case st != hub.Downloaded:
		if ui.PrimaryButton(c.Key("download"), i18n.T("Download")).Disabled(!m.known).Clicked() {
			a.download(e)
		}
	}
	if st == hub.Downloaded && !m.downloading {
		if ui.Button(c.Key("verify"), i18n.T("Verify")).Disabled(m.verifying).Tooltip(i18n.T("Check every file against its hash")).Clicked() {
			a.verifyModel(e)
		}
		if ui.Button(c.Key("reveal"), i18n.T("Reveal")).Tooltip(i18n.T("Show in Finder")).Clicked() {
			mygo.Shell.ShowItemInFolder(m.local.Snapshot)
		}
	}
	if (st == hub.Downloaded || st == hub.Partial) && !m.downloading {
		del := ui.Button(c.Key("delete"), i18n.T("Delete")).Disabled(loaded)
		if loaded {
			del.Tooltip(i18n.T("Unload it first"))
		}
		if del.Clicked() {
			a.deleting, a.deleteAsk = e, true
		}
	}
}

// modelStateText says what of an entry is on disk.
func (a *app) modelStateText(e catalog.Entry, m *modelState) string {
	switch {
	case m.downloading:
		if m.progress.TotalBytes > 0 {
			return i18n.T("Downloading · %d%%", int(100*m.progress.Bytes/m.progress.TotalBytes))
		}
		return i18n.T("Starting the download…")
	case m.verifying:
		return i18n.T("Checking…")
	case !m.known:
		return i18n.T("Checking…")
	case m.local.State == hub.Downloaded:
		return i18n.T("Downloaded · %s", mb(m.local.Bytes))
	case m.local.State == hub.Partial:
		return i18n.T("Incomplete · %s on disk", mb(m.local.Bytes))
	}
	return i18n.T("Not downloaded")
}

// mb formats bytes as the Models page shows them.
func mb(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.2f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.0f KB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d B", n)
}

// modelsAlerts asks before deleting a model.
func (a *app) modelsAlerts(c *ui.Context) {
	e := a.deleting
	switch ui.AlertDialog(c, &a.deleteAsk, i18n.T("Delete %s?", e.Label),
		i18n.T("Its files leave the model folder. You can download it again."), i18n.T("Cancel"), i18n.T("Delete")) {
	case 1:
		a.deleteModel(e)
	}
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
		ui.Column(c).Width(px(t, 440)).Gap(px(t, 12)).Children(func() {
			ui.Text(c, i18n.T("Licence of %s", e.Label)).FontSize(px(t, 15)).Bold()
			ui.Text(c, i18n.T("%s's weights are licensed under %s.", e.Label, lic.Name))
			if !lic.Commercial {
				ui.Text(c, i18n.T("You may use the model, and what you make with it, for non-commercial purposes only. AudioSmith runs it only after you accept.")).TextColor(t.TextMuted)
			}
			if lic.URL != "" {
				ui.Link(c, i18n.T("Read the licence"), lic.URL)
			}
			ui.Checkbox(c.Key("licence-agree"), &a.licenceAgree, i18n.T("I have read the licence and will use this model only as it allows."))
			ui.Row(c).Gap(px(t, 8)).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("licence-cancel"), i18n.T("Cancel")).Clicked() {
					a.licenceOpen, a.licenceThen = false, nil
				}
				if ui.PrimaryButton(c.Key("licence-accept"), i18n.T("Accept")).Disabled(!a.licenceAgree).Clicked() {
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
