package main

// Settings → Storage: moving the data folder (the copy; the start
// finishes it, storage_move.go); split from settings_storage.go.

import (
	"context"
	"os"
	"path/filepath"

	"github.com/hehehai/AudioSmith/internal/i18n"
)

// startMove copies the data folder to dir/AudioSmith (or dir, when it is
// called AudioSmith or empty), then restarts to finish (storage_move.go).
// Checking the target, measuring the folder, stopping the Local API and
// removing a failed copy all run in the background (#101 M8): a data
// folder of many GB took seconds on the main thread. The dialog shows
// what is being done meanwhile.
func (a *app) startMove(dir string) {
	if a.moving {
		return
	}
	if len(a.activeJobs()) > 0 {
		a.toast(i18n.T("Wait for the jobs in progress to finish, or cancel them, before moving the data."))
		return
	}
	from, home := a.dataDir(), a.home()
	stopAPI := a.apiStopper()
	ctx, cancel := context.WithCancel(context.Background())
	a.moving, a.moveTo, a.moveErr, a.moveDone, a.moveTotal, a.moveCancel = true, dir, "", 0, 0, cancel
	a.moveStage = moveChecking
	a.background(func() {
		to, need, msg := planMove(ctx, from, dir, home)
		if msg != "" || ctx.Err() != nil {
			a.post(func() {
				a.moving, a.moveCancel, a.moveStage = false, nil, ""
				if msg != "" && ctx.Err() == nil {
					a.toast(msg)
				}
			})
			return
		}
		_, existed := os.Stat(to)
		if stopAPI != nil {
			stopAPI() // nothing new arrives while the files copy
		}
		a.post(func() { a.moveTo, a.moveTotal, a.moveStage = to, need, moveCopying })
		skip := func(rel string) bool { return skipData(rel, true) }
		err := copyTree(ctx, from, to, skip, func(done, total int64) {
			a.post(func() { a.moveDone, a.moveTotal = done, total })
		})
		if err == nil {
			err = writeJSON(filepath.Join(home, moveFile), pendingMove{From: from, To: to})
		}
		if err != nil {
			a.post(func() { a.moveCancel, a.moveStage = nil, moveRollingBack })
			if existed != nil && to != home { // created by the move: roll back
				os.RemoveAll(to)
			} else {
				removeDataExcept(to)
			}
		}
		a.post(func() {
			a.moveCancel, a.moveStage = nil, ""
			if err != nil {
				a.moving = false
				if ctx.Err() == nil {
					a.moveErr = i18n.T("The data was not moved: %v", err)
				}
				a.restartAPI()
				return
			}
			appRelaunch() // the start finishes the move (resolveDataDir)
		})
	})
}

// What the move dialog says it does besides copying.
const (
	moveChecking    = "checking"
	moveCopying     = "copying"
	moveRollingBack = "rolling back"
)

// planMove picks the folder a move to dir copies into and checks it: the
// target, and the room the data needs there. It returns the target, the
// size to copy, or why not (translated). Any goroutine.
func planMove(ctx context.Context, from, dir, home string) (to string, need int64, msg string) {
	to = dir
	if filepath.Base(dir) != "AudioSmith" {
		if ents, err := os.ReadDir(dir); err != nil || len(ents) > 0 {
			to = filepath.Join(dir, "AudioSmith")
		}
	}
	if filepath.Clean(to) == filepath.Clean(home) && from != home {
		// Back to the default folder: it keeps location.json.
	} else if err := checkMoveTarget(from, to); err != nil {
		return "", 0, i18n.T("Cannot move there: %v", err)
	}
	need, _ = treeSize(ctx, from, nil)
	if free := diskFree(filepath.Dir(to)); free > 0 && free < need+need/20+(256<<20) {
		return "", 0, i18n.T("Not enough space there: %s needed, %s free.", humanBytes(need), humanBytes(free))
	}
	return to, need, ""
}

// restartAPI starts the Local API again after a move that did not happen.
func (a *app) restartAPI() {
	if a.apiStarted && a.apiSrv != nil {
		a.apiStarted = false
		a.startAPI(a.apiData)
	}
}
