package main

import (
	"testing"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/services"
)

// TestStoryLanguageFollowsItsText: storyLanguage keeps its last answer
// (review #102) only while the texts are the same.
func TestStoryLanguageFollowsItsText(t *testing.T) {
	d := &services.LongformDoc{Kind: core.ProjectStory, Lines: []services.StoryLine{{Text: "Hello there."}, {Text: "How are you?"}}}
	if got := storyLanguage(d); got != "en" {
		t.Fatalf("got %q", got)
	}
	d.Lines[1].Text = "こんにちは"
	if got := storyLanguage(d); got != "ja" {
		t.Fatalf("after an edit: %q", got)
	}
	d.Lines = append(d.Lines[:1:1], services.StoryLine{Text: "你好"})
	if got := storyLanguage(d); got != "zh" {
		t.Fatalf("lines replaced: %q", got)
	}
	d.Language = "fr"
	if got := storyLanguage(d); got != "fr" {
		t.Fatalf("its own: %q", got)
	}
	b := &services.LongformDoc{Kind: core.ProjectAudiobook, Script: "# One\n\n안녕하세요"}
	if got := storyLanguage(b); got != "ko" {
		t.Fatalf("a book: %q", got)
	}
	// The four scripts start at Hangul Jamo, which the scan skips to.
	for _, tab := range []*unicode.RangeTable{unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Han} {
		for r := rune(0); r < 0x1100; r++ {
			if unicode.Is(tab, r) {
				t.Fatalf("%U is in a script the scan skips", r)
			}
		}
	}
}
