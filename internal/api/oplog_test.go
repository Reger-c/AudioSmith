package api

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/oplog"
)

// TestOpLogCalls (#108): requests that change something are logged with
// their method, path, caller and status; reads are not, nor the body.
func TestOpLogCalls(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	defer oplog.SetSink(func(e core.LogEntry) {
		mu.Lock()
		lines = append(lines, string(e.Level)+" "+e.Message)
		mu.Unlock()
	})()
	f := newFake(t)
	_, ts := f.server()
	do(t, ts, "GET", "/api/voices", nil, nil)
	do(t, ts, "POST", "/api/voices/design", map[string]any{"description": "a warm secret-sauce voice", "name": "Grandpa"}, nil)
	do(t, ts, "DELETE", "/api/voices/nope", nil, nil)
	req, _ := http.NewRequest("DELETE", ts.URL+"/api/voices/x", nil) // no CSRF header: refused before the route
	http.DefaultClient.Do(req)
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(lines, "\n")
	for _, want := range []string{
		"info api.call method=POST path=/api/voices/design via=api status=201",
		"warning api.call method=DELETE path=/api/voices/nope via=api status=404",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "GET") || strings.Contains(all, "secret-sauce") {
		t.Errorf("logged a read or a body:\n%s", all)
	}
}
