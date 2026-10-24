package oplog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
)

func TestLine(t *testing.T) {
	got := Line("voice.delete", "id", "01J", "name", "Mara Lee", "ok", true, "n", 3, "ms", 1500*time.Millisecond,
		"error", errors.New("disk full\nnow"), "text", Msg(`say "hi"`), "odd")
	want := `voice.delete id=01J name="Mara Lee" ok=true n=3 ms=1500 error="disk full now" text="say \"hi\"" odd=(missing)`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if l := Line("x", "name", strings.Repeat("长", 100)); !strings.HasSuffix(l, "…") || len([]rune(l)) != len("x name=")+81 {
		t.Errorf("not cut: %d %s", len([]rune(l)), l)
	}
}

func TestSinkAndActor(t *testing.T) {
	var got []core.LogEntry
	restore := SetSink(func(e core.LogEntry) { got = append(got, e) })
	defer restore()
	Info("a", "k", 1)
	Warn("b")
	Err("c", nil)
	Err("d", errors.New("bad"))
	if len(got) != 4 || got[0].Message != "a k=1" || got[1].Level != core.SeverityWarning || got[2].Level != core.SeverityInfo ||
		got[3].Message != `d error=bad` || got[3].Level != core.SeverityWarning || got[0].Source != Source {
		t.Errorf("%+v", got)
	}
	ctx := context.Background()
	if Actor(ctx) != "app" || Actor(WithActor(ctx, "mcp")) != "mcp" {
		t.Error("actor")
	}
}
