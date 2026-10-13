package core

// Domain events published by the services after the change is durable
// (committed to the store). Other packages define their own:
//
//	jobs.Updated            "job.updated"       internal/jobs
//	engine.StatusChanged    "engine.status"     internal/engine
//	settings.Changed        "settings.changed"  internal/settings
//	hub.DownloadProgress    "model.download"    internal/hub
//
// Add an event type next to the service that publishes it; keep the topic
// prefix equal to the owner's name.

// TakeCreated: a take finished and was saved.
type TakeCreated struct{ Take Take }

// TakeUpdated: a take's library state (star, relations) changed.
type TakeUpdated struct{ Take Take }

// TakeDeleted: a take and its audio are gone.
type TakeDeleted struct{ ID string }

// VoiceChanged: a voice profile was created, edited or deleted.
type VoiceChanged struct {
	Voice   Voice
	Deleted bool
}

// ProjectChanged: a project was created, saved, renamed or deleted.
type ProjectChanged struct {
	Project Project
	Deleted bool
}

// DraftSaved: DraftService wrote a draft (for an "autosaved" hint).
type DraftSaved struct{ Key string }

// LicenceChanged: the user accepted or revoked a model licence.
type LicenceChanged struct {
	Acceptance LicenceAcceptance
	Accepted   bool
}

// NotificationPosted: something for the notification centre / a toast.
type NotificationPosted struct{ Notification Notification }

// LogAppended: a line for the logs panel.
type LogAppended struct{ Entry LogEntry }

func (TakeCreated) Topic() string        { return "take.created" }
func (TakeUpdated) Topic() string        { return "take.updated" }
func (TakeDeleted) Topic() string        { return "take.deleted" }
func (VoiceChanged) Topic() string       { return "voice.changed" }
func (ProjectChanged) Topic() string     { return "project.changed" }
func (DraftSaved) Topic() string         { return "draft.saved" }
func (LicenceChanged) Topic() string     { return "licence.changed" }
func (NotificationPosted) Topic() string { return "notification.posted" }
func (LogAppended) Topic() string        { return "log.appended" }
