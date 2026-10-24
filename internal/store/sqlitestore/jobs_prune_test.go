package sqlitestore

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/jobs"
)

// TestPruneJobs: old finished jobs go, but not the unfinished, the recent,
// the model downloads, or those naming a custom model.
func TestPruneJobs(t *testing.T) {
	s := openTest(t, t.TempDir())
	ctx := context.Background()
	js := s.Jobs()
	old := time.Now().Add(-40 * 24 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	add := func(id, kind string, state jobs.State, finished time.Time, params string) {
		t.Helper()
		j := jobs.Job{ID: id, Spec: jobs.Spec{Kind: kind, Lane: jobs.LaneGPU, Params: json.RawMessage(params)}, State: state, Created: finished.Add(-time.Minute), Finished: finished}
		if err := js.SaveJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	add("old-tts", "tts.synthesize", jobs.StateSucceeded, old, `{"model":"known/repo","text":"long"}`)
	add("old-failed", "longform.chapter", jobs.StateFailed, old, `{"lines":["a"]}`)
	add("old-custom", "tts.synthesize", jobs.StateSucceeded, old, `{"model":"someone/custom"}`)
	add("old-download", "model.download", jobs.StateSucceeded, old, `{"repo":"someone/custom"}`)
	add("old-array", "x.y", jobs.StateCancelled, old, `[1,2]`)
	add("recent", "tts.synthesize", jobs.StateSucceeded, recent, `{"model":"known/repo"}`)
	add("queued", "tts.synthesize", jobs.StateQueued, time.Time{}, `{}`)
	n, err := s.PruneJobs(ctx, time.Now().Add(-30*24*time.Hour), []string{"model.download"}, []string{"known/repo"})
	if err != nil {
		t.Fatal(err)
	}
	all, err := js.LoadJobs(ctx, jobs.Filter{States: []jobs.State{}})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, j := range all {
		ids = append(ids, j.ID)
	}
	slices.Sort(ids)
	if want := []string{"old-custom", "old-download", "queued", "recent"}; n != 3 || !slices.Equal(ids, want) {
		t.Fatalf("removed %d, left %v, want %v", n, ids, want)
	}
}
