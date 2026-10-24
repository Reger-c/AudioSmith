package main

// The render the Studio follows: which chunk it makes (RenderProgress),
// and its streaming preview. A render job streams the audio joined so far
// as its chunks (proto.ChunkParams, pcm16le); the Studio collects them in
// an audio.Stream and, when autoplay is on, plays it from the first
// chunk while the rest renders. The finished take then replaces the
// preview (the player switches to the file when the preview has ended or
// is paused).

import (
	"encoding/json"
	"strings"

	"github.com/hehehai/AudioSmith/internal/audio"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
)

// renderState is the Studio's view of its render.
type renderState struct {
	renderTake   string // the take being rendered ("" when none)
	renderChunk  int    // chunk being made, 1-based; 0 = finishing or unknown
	renderChunks int
	renderCached int
	preview      *audio.Stream // the joined audio so far; nil until the first chunk
	previewID    string        // the take the preview is of
}

// startRender resets the render state for a new take (the one generate
// is about to ask for).
func (a *app) startRender() {
	a.renderChunk, a.renderChunks, a.renderCached = 0, 0, 0
	a.renderTake = ""
	if a.preview != nil && !a.pl.Streaming(a.previewID) {
		a.preview.Close()
	}
	a.preview, a.previewID = nil, ""
}

// followRenders subscribes (once, in openTakes) to the renders' progress
// and streamed audio, for the take the Studio waits for.
func (a *app) followRenders() {
	core.On(a.lib.bus, func(p services.RenderProgress) {
		id, ok := strings.CutPrefix(p.Owner, takeOwner)
		if !ok {
			return
		}
		a.post(func() {
			if id != a.awaitTake {
				return
			}
			a.renderTake = id
			a.renderChunk, a.renderChunks, a.renderCached = p.Chunk, p.Chunks, p.Cached
		})
	})
	core.On(a.lib.bus, func(u jobs.Updated) {
		if u.Job.Kind != services.KindRender {
			return
		}
		if strings.HasPrefix(u.Job.Owner, "preview:") && u.Chunk == nil && (u.Job.State.Terminal() || u.Job.State == jobs.StateInterrupted) {
			// A pronunciation test (Settings → Pronunciation).
			j := u.Job
			why := ""
			if j.State == jobs.StateFailed || j.State == jobs.StateInterrupted {
				why = jobErrorText(j.Err)
			}
			a.post(func() { a.previewDone(j.ID, j.State == jobs.StateSucceeded, why) })
			return
		}
		if u.Chunk == nil {
			return
		}
		id, ok := strings.CutPrefix(u.Job.Owner, takeOwner)
		if !ok {
			return
		}
		var c proto.ChunkParams
		if json.Unmarshal(u.Chunk, &c) != nil || c.Encoding != "pcm16le" {
			return
		}
		a.post(func() { a.previewChunk(id, c) })
	})
}

// previewChunk adds streamed audio of take id to its preview, and starts
// playing it when autoplay is on.
func (a *app) previewChunk(id string, c proto.ChunkParams) {
	if id != a.awaitTake && id != a.previewID {
		return
	}
	if a.preview == nil || a.previewID != id {
		if c.SampleRate <= 0 {
			return
		}
		a.preview = audio.NewStream(c.SampleRate, max(c.Channels, 1), 0)
		a.previewID = id
	}
	a.preview.AppendPCM16(c.Data)
	if c.Final {
		a.preview.Finish()
	}
	if a.autoplay && !a.previewStarted() && a.preview.Seconds() > 0 {
		a.playPreview()
	}
}

// previewStarted reports whether the preview plays or played.
func (a *app) previewStarted() bool { return a.pl.Current() == a.previewID && a.previewID != "" }

// playPreview plays the preview from its start.
func (a *app) playPreview() {
	if a.preview == nil {
		return
	}
	a.pl.PlayStream(a.previewID, a.preview)
}

// previewAvailable reports whether the Studio's render has preview audio
// to play now.
func (a *app) previewAvailable() bool {
	return a.preview != nil && a.previewID == a.awaitTake && a.preview.Seconds() > 0
}
