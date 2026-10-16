package main

// Failed jobs in the log: the jobs scheduler keeps a job's error in the
// database and shows it where the job was started, but nothing reached
// the log file, so Settings → Logs said nothing about a failed dub or
// render. A job that failed only because a job it depends on failed, or
// was cancelled, is left out: the job that failed first says why.

import (
	"log"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// logJobFailures writes one line for every job that fails on its own.
func logJobFailures(bus *core.Bus) {
	core.On(bus, func(u jobs.Updated) {
		j := u.Job
		if u.Chunk != nil || j.State != jobs.StateFailed || j.Err == nil {
			return
		}
		switch j.Err.Code {
		case jobs.CodeDependency, jobs.CodeCancelled:
			return
		}
		log.Printf("job %s (%s, %s) failed: %s: %s", j.ID, j.Kind, j.Owner, j.Err.Code, j.Err.Message)
	})
}
