package main

import (
	"context"
	"log"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/hub"
)

// jobsKeepDays is how long finished jobs stay in the library: the Tasks
// page's history, and their errors, which say why something failed.
const jobsKeepDays = 30

// pruneJobs removes the jobs that finished more than jobsKeepDays ago,
// once at startup (review #102: the table only grew, and a book's
// chapter jobs hold its text). The model downloads stay, and the jobs
// naming a custom model: they are how AudioSmith knows it used them
// (usedRepos, family_app.go).
func (l *library) pruneJobs(ctx context.Context) {
	var known []string
	for _, e := range catalog.Builtin().Entries {
		if e.Repo != "" {
			known = append(known, e.Repo)
		}
	}
	n, err := l.store.PruneJobs(ctx, time.Now().AddDate(0, 0, -jobsKeepDays), []string{hub.KindDownload}, known)
	if err != nil {
		log.Printf("jobs: prune: %v", err)
	} else if n > 0 {
		log.Printf("jobs: removed %d finished more than %d days ago", n, jobsKeepDays)
	}
}
