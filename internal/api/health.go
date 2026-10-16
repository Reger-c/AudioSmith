package api

// GET /health, and where AudioSmith's source code is: the server tells
// whoever uses it over the network where to get the source and under
// which licence (GNU AGPL-3.0, section 13).

import (
	"net/http"
	"strconv"
)

// AudioSmith's source code and licence (an SPDX identifier). The About
// page shows them too.
const (
	SourceURL = "https://github.com/hehehai/AudioSmith"
	License   = "AGPL-3.0-only"
)

// Health is GET /health's answer.
type Health struct {
	Status  string `json:"status"` // "ok", "starting", "unavailable"
	Version string `json:"version"`
	Engine  Status `json:"engine"`
	// SourceURL and License: where to get AudioSmith's source, and its
	// licence (AGPL-3.0 §13).
	SourceURL string `json:"source_url"`
	License   string `json:"license"`
}

// Health reports whether requests can run now.
func (s *Server) Health() Health {
	st := s.b.Status()
	h := Health{Status: "ok", Version: s.o.Version, Engine: st, SourceURL: SourceURL, License: License}
	switch st.State {
	case StateStarting, StateRestarting:
		h.Status = "starting"
	case StateFailed, StateStopped:
		h.Status = "unavailable"
	}
	return h
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	h := s.Health()
	status := http.StatusOK
	if h.Status != "ok" {
		status = http.StatusServiceUnavailable
		retry := h.Engine.RetryAfter
		if retry <= 0 {
			retry = 5
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
	}
	writeJSON(w, status, h)
}
