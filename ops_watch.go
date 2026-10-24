package main

// The operations log's watcher (#108, internal/oplog): jobs' lives,
// downloads and settings changes, read from the bus, one line each.
// The services log the changes to the user's data themselves; the app
// logs toasts (toast), its start and its quit.

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/logs"
	"github.com/hehehai/AudioSmith/internal/oplog"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// useOpLog sends the operations log to ring and watches bus. It returns
// a function that stops watching.
func useOpLog(ring *logs.Ring, bus *core.Bus) (stop func()) {
	oplog.SetSink(ring.Append)
	w := &opWatch{seen: map[string]jobs.State{}}
	offJobs := core.On(bus, w.job)
	offSettings := core.On(bus, func(ev settings.Changed) { logSettingsChange(ev.Old, ev.New) })
	return func() {
		offJobs()
		offSettings()
	}
}

// opWatch follows the jobs' states, to log each change once (progress
// updates are not logged).
type opWatch struct {
	mu   sync.Mutex
	seen map[string]jobs.State // jobs not finished yet, by id
}

func (w *opWatch) job(ev jobs.Updated) {
	j := ev.Job
	w.mu.Lock()
	was, known := w.seen[j.ID]
	if known && was == j.State {
		w.mu.Unlock()
		return
	}
	if j.State.Terminal() || j.State == jobs.StateInterrupted {
		delete(w.seen, j.ID)
	} else {
		w.seen[j.ID] = j.State
	}
	w.mu.Unlock()
	logJob(j, was, known)
}

// logJob writes the line for a job that is now in j.State (was before,
// when known). Downloads are "download.*" lines with the repo.
func logJob(j jobs.Job, was jobs.State, known bool) {
	ev, kv := "job.", []any{"id", j.ID, "kind", j.Kind}
	if j.Kind == hub.KindDownload {
		ev, kv = "download.", []any{"job", j.ID, "repo", strings.TrimPrefix(j.Owner, hub.OwnerPrefix)}
	} else if j.Owner != "" {
		kv = append(kv, "owner", j.Owner)
	}
	ran := func() time.Duration {
		if j.Started.IsZero() || j.Finished.IsZero() {
			return 0
		}
		return j.Finished.Sub(j.Started)
	}
	switch j.State {
	case jobs.StateWaiting, jobs.StateQueued:
		switch {
		case !known && j.Attempt == 0:
			oplog.Info(ev+"submit", append(kv, "lane", string(j.Lane))...)
		case was == jobs.StateRunning && j.Err != nil:
			oplog.Warn(ev+"retry", append(kv, "attempt", j.Attempt, "code", j.Err.Code, "error", j.Err.Message)...)
		case !known || was.Terminal() || was == jobs.StateInterrupted:
			oplog.Info(ev+"requeue", append(kv, "attempt", j.Attempt)...)
		}
	case jobs.StateRunning:
		wait := time.Duration(0)
		if !j.Started.IsZero() && !j.Created.IsZero() {
			wait = j.Started.Sub(j.Created)
		}
		oplog.Info(ev+"start", append(kv, "attempt", j.Attempt, "wait_ms", wait)...)
	case jobs.StateSucceeded:
		oplog.Info(ev+"done", append(kv, "ms", ran())...)
	case jobs.StateFailed:
		code, msg := "", ""
		if j.Err != nil {
			code, msg = j.Err.Code, j.Err.Message
		}
		oplog.Warn(ev+"fail", append(kv, "code", code, "error", msg, "ms", ran())...)
	case jobs.StateCancelled:
		oplog.Info(ev+"cancel", append(kv, "by", cancelledBy(j), "ms", ran())...)
	case jobs.StateInterrupted:
		oplog.Warn(ev+"interrupted", append(kv, "ms", ran())...)
	}
}

// cancelledBy is who cancelled j (jobs.WithActor): "app" when the
// window did, as it does not say.
func cancelledBy(j jobs.Job) string {
	if j.Err != nil && len(j.Err.Data) > 0 {
		var d struct{ By string }
		if json.Unmarshal(j.Err.Data, &d) == nil && d.By != "" {
			return d.By
		}
	}
	return "app"
}

// quietSettings change too often to log: what the app records for
// itself, not what the user chose.
var quietSettings = []string{"updates.lastCheck"}

// logSettingsChange logs the names of the settings that changed, never
// their values (keys, tokens, proxies' passwords).
func logSettingsChange(old, new settings.Settings) {
	var names []string
	diffNames("", reflect.ValueOf(old), reflect.ValueOf(new), &names)
	names = slices.DeleteFunc(names, func(n string) bool { return slices.Contains(quietSettings, n) })
	if len(names) == 0 {
		return
	}
	const most = 12
	more := 0
	if len(names) > most {
		names, more = names[:most], len(names)-most
	}
	kv := []any{"fields", strings.Join(names, ",")}
	if more > 0 {
		kv = append(kv, "more", more)
	}
	oplog.Info("settings.change", kv...)
}

// diffNames adds to names the JSON paths of the fields that differ in a
// and b (structs: by field; anything else: as a whole).
func diffNames(path string, a, b reflect.Value, names *[]string) {
	if a.Kind() == reflect.Struct && a.Type() == b.Type() {
		t := a.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if path != "" {
				name = path + "." + name
			}
			diffNames(name, a.Field(i), b.Field(i), names)
		}
		return
	}
	if !reflect.DeepEqual(a.Interface(), b.Interface()) {
		*names = append(*names, path)
	}
}
