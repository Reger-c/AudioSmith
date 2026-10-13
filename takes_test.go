package main

import (
	"os"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store/historyjson"
)

func TestHistoryReuseAndDelete(t *testing.T) {
	a, _, tt := newTestApp(t)
	for i, text := range []string{"First take", "Second take"} {
		at := time.Now().Add(time.Duration(i) * time.Second)
		addTake(t, a, core.Take{
			ID: historyjson.NewTakeID(at), CreatedAt: at,
			Text: text, Model: "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit", Voice: "Vivian, cheerful",
			Language: "English", Temperature: 0.4, TopP: 0.8, MaxTokens: 900, AudioSeconds: 1,
		}, 0.3)
	}
	a.page = "history"
	tt.Frame()
	if !tt.HasText("2 takes") || !tt.HasText("First take") || !tt.HasText("Second take") {
		t.Fatalf("history: %q", tt.Texts())
	}

	second := &a.takes[0]
	a.reuse(second)
	if a.page != "studio" || a.text != "Second take" || a.modelLabel != "Qwen3-TTS 0.6B CustomVoice" ||
		a.voice != "Vivian, cheerful" || a.modelDefaults || a.temperature != 0.4 || a.maxTokens != 900 {
		t.Errorf("reuse: %+v", a)
	}

	a.page = "history"
	tt.Frame()
	path := a.takeStore.AudioPath(*second)
	a.deleteClip(second)
	tt.Frame()
	if len(a.takes) != 1 || a.takes[0].Text != "First take" {
		t.Fatalf("after delete: %v", a.takes)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("audio of a deleted take remains: %v", err)
	}
}

func TestEmptyHistory(t *testing.T) {
	a, _, tt := newTestApp(t)
	a.page = "history"
	tt.Frame()
	if !tt.HasText("No takes yet") {
		t.Errorf("texts %q", tt.Texts())
	}
}

func TestExportNames(t *testing.T) {
	if got := exportName(&core.Take{Text: "What: a/test? of the export name, really long"}); got != "What- a-test- of the export name,.wav" {
		t.Errorf("export name %q", got)
	}
	if got := exportName(&core.Take{Text: "你好，世界"}); got != "你好，世界.wav" {
		t.Errorf("export name %q", got)
	}
}
