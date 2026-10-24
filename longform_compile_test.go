package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"
)

// TestLargeManuscriptCompilesOncePaused: a large manuscript is compiled,
// and its draft handed over, once typing pauses, not at every key (review
// #102); the results land with the cast, and quitting saves a pending
// draft.
func TestLargeManuscriptCompilesOncePaused(t *testing.T) {
	a, _, tt := newTestApp(t)
	lfDeferAlways = true
	t.Cleanup(func() { lfDeferAlways = false })
	q := make(chan func(), 64)
	a.postQueue = func(fn func()) { q <- fn }
	drain := func() {
		for {
			select {
			case fn := <-q:
				fn()
			default:
				return
			}
		}
	}
	a.navigate("/audiobook")
	p := a.lfBook
	para := strings.Repeat("他也说过，这个城市里有很多米粉店，西边那家最好吃。", 20)
	ms := "# 第一章\n\n" + strings.Repeat(para+"\n\n", 20) + "# 第二章\n\n" + strings.Repeat(para+"\n\n", 20)
	a.setManuscript(p, ms)
	tt.Frame()
	if !p.lfc.compiled || p.stats.Chapters != 2 {
		t.Fatalf("a new manuscript compiles at once: %+v", p.stats)
	}
	before := p.stats.Chars
	bookCast(p)
	// A chapter and a voice typed: nothing compiles while typing goes on.
	at := len(p.ed.Buffer.String())
	p.ed.Buffer.Edit(editor.Edit{Start: at, End: at, Text: "\n\n# 第三章\n\n[voice:小明] 你好。"})
	tt.Frame()
	if p.stats.Chapters != 2 || !p.lfc.stale(p.ed.Buffer.Version()) {
		t.Fatalf("compiled at the key: %+v", p.stats)
	}
	if c := bookCast(p); len(c) != 0 {
		t.Fatalf("cast found at the key: %v", c)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.lfc.stale(p.ed.Buffer.Version()) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		drain()
	}
	tt.Frame()
	if p.stats.Chapters != 3 || p.stats.Chars <= before {
		t.Fatalf("not compiled after the pause: %+v", p.stats)
	}
	if c := bookCast(p); len(c) != 1 || c[0] != "小明" {
		t.Fatalf("cast %v", c)
	}
	var d lfDraft
	if err := a.lib.svc.Drafts.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.lib.svc.Drafts.Load(context.Background(), p.draftKey(), &d); err != nil || !ok || !strings.Contains(d.Doc.Script, "第三章") {
		t.Fatalf("draft not saved with the compile: ok=%v err=%v", ok, err)
	}
	// Typed, then quit before the pause: the draft has it.
	at = len(p.ed.Buffer.String())
	p.ed.Buffer.Edit(editor.Edit{Start: at, End: at, Text: "最后一句。"})
	tt.Frame()
	a.flushLongform()
	if err := a.lib.svc.Drafts.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.lib.svc.Drafts.Load(context.Background(), p.draftKey(), &d); err != nil || !ok || !strings.HasSuffix(d.Doc.Script, "最后一句。") {
		t.Fatalf("quitting lost the pending draft: ok=%v err=%v", ok, err)
	}
	time.Sleep(lfCompileDelay + 50*time.Millisecond)
	drain()
}
