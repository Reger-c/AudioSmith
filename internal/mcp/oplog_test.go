package mcp

import (
	"strings"
	"sync"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/oplog"
)

// TestOpLogTools (#108): tools that change something are logged by name
// with their outcome, never their arguments; reading tools are not.
func TestOpLogTools(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	defer oplog.SetSink(func(e core.LogEntry) {
		mu.Lock()
		lines = append(lines, string(e.Level)+" "+e.Message)
		mu.Unlock()
	})()
	f := newFake(t)
	_, _, ts := serve(t, f)
	cs := connect(t, ts.URL, "claude-code", nil)
	call(t, cs, "list_voices", nil)
	call(t, cs, "generate_speech", map[string]any{"text": "A private sentence.", "voice": "af_heart"})
	call(t, cs, "delete_item", map[string]any{"kind": "take", "id": "x"}) // no confirm: refused
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(lines, "\n")
	if !strings.Contains(all, "info mcp.call tool=generate_speech via=mcp") || !strings.Contains(all, "warning mcp.call tool=delete_item via=mcp") {
		t.Errorf("lines:\n%s", all)
	}
	if strings.Contains(all, "list_voices") || strings.Contains(all, "private sentence") || strings.Contains(all, "api.call") {
		t.Errorf("logged too much:\n%s", all)
	}
}
