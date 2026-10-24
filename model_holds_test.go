package main

import (
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/oplog"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// #109: a live call holds the gpu and listen workers' models; its end or
// deletion releases them.
func TestLiveCallHoldsModels(t *testing.T) {
	a, eng, _ := newTestApp(t)
	c := &services.Call{ID: "c1", Phase: services.CallLive}
	a.callChanged(services.CallChanged{ID: "c1", Call: c})
	a.callChanged(services.CallChanged{ID: "c1", Call: c}) // an update: one hold
	if h := eng.held(); len(h) != 2 {
		t.Fatalf("held %v", h)
	}
	ended := *c
	ended.Phase = services.CallEnded
	a.callChanged(services.CallChanged{ID: "c1", Call: &ended})
	if h := eng.held(); len(h) != 0 {
		t.Fatalf("held after the call %v", h)
	}
	a.callChanged(services.CallChanged{ID: "c2", Call: &services.Call{ID: "c2", Phase: services.CallDialing}})
	a.callChanged(services.CallChanged{ID: "c2", Deleted: true})
	if h := eng.held(); len(h) != 0 {
		t.Fatalf("held after deleting %v", h)
	}
}

// #109: the settings' migration is in the operations log.
func TestSettingsMigrationLogged(t *testing.T) {
	var lines []string
	defer oplog.SetSink(func(e core.LogEntry) { lines = append(lines, e.Message) })()
	logSettingsMigrations([]settings.Migration{{Field: "models.autoUnload", From: 0, To: 30}})
	if len(lines) != 1 || lines[0] != "settings.migrate field=models.autoUnload from=0 to=30" {
		t.Fatalf("logged %q", lines)
	}
}
