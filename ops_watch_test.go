package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/oplog"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestOpWatch (#108): a job's states are logged once each (progress is
// not), downloads by repo, cancels with who cancelled.
func TestOpWatch(t *testing.T) {
	var lines []string
	defer oplog.SetSink(func(e core.LogEntry) { lines = append(lines, string(e.Level)+" "+e.Message) })()
	w := &opWatch{seen: map[string]jobs.State{}}
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	j := jobs.Job{ID: "J1", Spec: jobs.Spec{Kind: hub.KindDownload, Lane: jobs.LaneNet, Owner: hub.OwnerPrefix + "mlx-community/Kokoro"}, Created: at}
	send := func(mod func(*jobs.Job)) {
		mod(&j)
		w.job(jobs.Updated{Job: j})
	}
	send(func(j *jobs.Job) { j.State = jobs.StateQueued })
	send(func(j *jobs.Job) { j.State, j.Attempt, j.Started = jobs.StateRunning, 1, at.Add(time.Second) })
	for range 5 {
		send(func(j *jobs.Job) { j.Progress.Fraction += 0.1 })
	}
	send(func(j *jobs.Job) {
		j.State, j.Finished = jobs.StateCancelled, at.Add(3*time.Second)
		j.Err = &jobs.Error{Code: jobs.CodeCancelled, Data: json.RawMessage(`{"by":"api"}`)}
	})
	want := []string{
		"info download.submit job=J1 repo=mlx-community/Kokoro lane=net",
		"info download.start job=J1 repo=mlx-community/Kokoro attempt=1 wait_ms=1000",
		"info download.cancel job=J1 repo=mlx-community/Kokoro by=api ms=2000",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if len(w.seen) != 0 {
		t.Errorf("finished jobs kept: %v", w.seen)
	}

	// Settings: names only.
	lines = nil
	old := settings.Defaults()
	nw := old
	nw.Models.Proxy = "http://u:hunter2-secret@proxy:8080"
	nw.Updates.LastCheck = at
	logSettingsChange(old, nw)
	if len(lines) != 1 || lines[0] != "info settings.change fields=models.proxy" {
		t.Errorf("settings: %q", lines)
	}
}
