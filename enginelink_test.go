package main

import (
	"os"
	"testing"

	"github.com/hehehai/AudioSmith/internal/engine"
)

func TestErrorsAndCancel(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	tt.Click("Generate")
	id := eng.last(t).ID

	a.handleEvent(engine.Event{ID: id, Event: "error", Message: "Unsupported model type: nil"})
	tt.Frame()
	if a.job != nil || !tt.HasText("Unsupported model type: nil") {
		t.Fatalf("error not shown: %q", tt.Texts())
	}

	tt.Click("Generate")
	id = eng.last(t).ID
	tt.Frame()
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if eng.last(t).Cmd != "cancel" {
		t.Fatalf("Cancel sent %+v", eng.last(t))
	}
	a.handleEvent(engine.Event{ID: id, Event: "cancelled"})
	if a.job != nil {
		t.Fatal("job survived cancellation")
	}
}

func TestEngineCrash(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	tt.Click("Generate")
	a.engineExited(os.ErrClosed, "fatal: Failed to load the default metallib")
	a.showPage("engine")
	tt.Frame()
	if a.job != nil || a.engState != engineFailed {
		t.Fatalf("state %v job %v", a.engState, a.job)
	}
	if !tt.HasText("Engine stopped") || !tt.HasText("fatal: Failed to load the default metallib") {
		t.Errorf("crash not shown: %q", tt.Texts())
	}
	if err := tt.Click("Restart engine"); err != nil {
		t.Fatal(err)
	}
	if eng.restarts != 1 || a.engState != engineStarting {
		t.Errorf("restarts %d state %v", eng.restarts, a.engState)
	}
}

func TestPreloadAndUnload(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	if err := tt.Click("Preload"); err != nil {
		t.Fatal(err)
	}
	req := eng.last(t)
	if req.Cmd != "load" || req.Model != kokoro {
		t.Fatalf("Preload sent %+v", req)
	}
	a.handleEvent(engine.Event{ID: req.ID, Event: "loaded", Model: req.Model, Seconds: 1.5})
	tt.Frame()
	if a.job != nil || !tt.HasText("Loaded") {
		t.Fatalf("after load: job %v texts %q", a.job, tt.Texts())
	}
	tt.Click("Unload")
	if eng.last(t).Cmd != "unload" {
		t.Fatalf("Unload sent %+v", eng.last(t))
	}
	a.handleEvent(engine.Event{ID: eng.last(t).ID, Event: "unloaded"})
	if a.loadedModel != "" {
		t.Error("model still loaded")
	}
}
