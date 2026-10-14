package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hehehai/AudioSmith/internal/jobs"
)

// sseWriter writes Server-Sent Events.
type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) *sseWriter {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	s := &sseWriter{w: w, f: f}
	s.flush()
	return s
}

func (s *sseWriter) flush() {
	if s.f != nil {
		s.f.Flush()
	}
}

// event sends one event with a JSON payload.
func (s *sseWriter) event(name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if name != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", name); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	s.flush()
	return nil
}

// comment keeps the connection alive.
func (s *sseWriter) comment() error {
	_, err := fmt.Fprint(s.w, ": keep-alive\n\n")
	s.flush()
	return err
}

// heartbeat is how often an idle event stream sends a comment.
var heartbeat = 15 * time.Second

// jobEvents is GET /api/jobs/{id}/events: the job's state as SSE, "job"
// events until it ends, then "done".
func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	x := s.w.job(id)
	defer x.stop()
	j, err := s.job(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := newSSE(w)
	if err := out.event("job", jobJSON(j, true)); err != nil {
		return
	}
	if ended(j) {
		out.event("done", jobJSON(j, true))
		return
	}
	for {
		u, ok, err := x.next(r.Context(), heartbeat)
		if err != nil {
			return
		}
		if !ok {
			if out.comment() != nil {
				return
			}
			continue
		}
		if u.Chunk != nil {
			continue
		}
		if err := out.event("job", jobJSON(u.Job, true)); err != nil {
			return
		}
		if ended(u.Job) {
			out.event("done", jobJSON(u.Job, true))
			return
		}
	}
}

func ended(j jobs.Job) bool { return j.State.Terminal() || j.State == jobs.StateInterrupted }

// allJobEvents is GET /api/jobs/events: every job's changes as SSE.
func (s *Server) allJobEvents(w http.ResponseWriter, r *http.Request) {
	x := s.w.every()
	defer x.stop()
	out := newSSE(w)
	for {
		u, ok, err := x.next(r.Context(), heartbeat)
		if err != nil {
			return
		}
		if !ok {
			if out.comment() != nil {
				return
			}
			continue
		}
		if err := out.event("job", jobJSON(u.Job, false)); err != nil {
			return
		}
	}
}
