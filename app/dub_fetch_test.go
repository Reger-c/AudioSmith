package main

import (
	"context"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// Every way to stop a dub's download — the activity card's Stop, the Dub
// page's Cancel, the Tasks page's Cancel on the job — stops the dub
// through its service (Dubs.Cancel, which cancels its jobs: what kills
// yt-dlp is services.TestDubFetchProgressAndCancel's), and none of them
// waits on the UI thread (in tests their work runs at once: a blocking
// one would hang here).
func TestDubFetchCancelEntryPoints(t *testing.T) {
	for _, entry := range []string{"card", "footer", "tasks"} {
		t.Run(entry, func(t *testing.T) {
			d := newDubTest(t, 2)
			a, tt := d.a, d.tt
			fetch := startFetch(t, d)
			// What a running fetch leaves in the project.
			a.dubSet(func(p *dub.Project) {
				p.Run = &dub.Run{Through: dub.StageReferences, At: time.Now()}
				p.SetStage(dub.StageFetch, "", dub.StageState{Status: dub.StatusRunning, Job: fetch.ID})
			})
			tt.Frame()
			done := make(chan struct{})
			go func() {
				defer close(done)
				a.post(func() {})
			}()
			<-done
			start := time.Now()
			switch entry {
			case "card":
				var card *actCard
				for i := range a.actCards {
					if a.actCards[i].key == "job-"+fetch.Owner {
						card = &a.actCards[i]
					}
				}
				if card == nil || card.action == nil {
					t.Fatalf("no card with a Stop: %+v", a.actCards)
				}
				card.action.run()
			case "footer":
				if !a.dubBusy() {
					t.Fatal("the page does not show the download")
				}
				d.click(t, "Cancel")
			case "tasks":
				a.navigate("/tasks")
				tt.Frame()
				a.cancelJobByID(a.jobList[fetch.ID])
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("%s: the UI waited %v", entry, d)
			}
			p, err := a.dubSvc.Get(context.Background(), a.dub.ID)
			if err != nil {
				t.Fatal(err)
			}
			if p.Run != nil || p.Stage(dub.StageFetch, "").Status == dub.StatusRunning {
				t.Errorf("%s did not stop the dub: run %+v, fetch %+v", entry, p.Run, p.Stage(dub.StageFetch, ""))
			}
			// The job ends as cancelled: the card goes.
			fetch.State = jobs.StateCancelled
			a.bus.Publish(jobs.Updated{Job: fetch})
			for _, id := range []string{"fetch-ingest", "fetch-separate", "fetch-transcribe", "fetch-references"} {
				j := a.jobList[id]
				j.State = jobs.StateCancelled
				a.bus.Publish(jobs.Updated{Job: j})
			}
			tt.Frame()
			for _, c := range a.actCards {
				if c.key == "job-"+fetch.Owner && !c.done {
					t.Errorf("the card stays: %+v", c)
				}
			}
			if a.dubBusy() {
				t.Error("the page still shows the download")
			}
		})
	}
}
