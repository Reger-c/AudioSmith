package settings

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestM2Defaults(t *testing.T) {
	d := Defaults()
	if !d.Dictation.Enabled || d.Dictation.Shortcut != DefaultShortcut || d.Dictation.Mode != DictationToggle ||
		d.Dictation.Output != OutputPaste || !d.Dictation.Cleanup || !d.Dictation.Sounds || d.Transcribe.Mode != TranscribeAccurate {
		t.Fatalf("defaults %+v %+v", d.Dictation, d.Transcribe)
	}
	// A document from before M2 gains them.
	s, err := Migrate([]byte(`{"version":1,"general":{"language":"en"}}`))
	if err != nil || s.Dictation != d.Dictation || s.Transcribe != d.Transcribe {
		t.Fatalf("old document: %+v %v", s.Dictation, err)
	}
	// A partial section keeps what it has.
	s, _ = Migrate([]byte(`{"dictation":{"mode":"hold"}}`))
	if s.Dictation.Mode != DictationHold || s.Dictation.Shortcut != DefaultShortcut || !s.Dictation.Enabled {
		t.Fatalf("partial: %+v", s.Dictation)
	}
}

func TestM2Normalize(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	long := strings.Repeat("词", MaxVocabulary+20)
	err := s.Update(ctx, func(s *Settings) {
		s.Dictation.Mode = "sometimes"
		s.Dictation.Output = "fax"
		s.Dictation.Vocabulary = "  " + long
		s.Dictation.Shortcut = " Alt+D "
		s.Transcribe.Mode = "slow"
		s.LLM.Providers = []LLMProvider{{ID: "ollama", Headers: map[string]string{"a": "b"}}}
		s.LLM.Skills = map[string][]LLMRoute{"dictation_cleanup": {{Provider: "ollama"}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	g := s.Get()
	if g.Dictation.Mode != DictationToggle || g.Dictation.Output != OutputPaste || g.Dictation.Shortcut != "Alt+D" ||
		utf8.RuneCountInString(g.Dictation.Vocabulary) != MaxVocabulary || g.Transcribe.Mode != TranscribeAccurate {
		t.Fatalf("normalized %+v %+v", g.Dictation, g.Transcribe)
	}
	// Get returns deep copies.
	g.LLM.Providers[0].Headers["a"] = "x"
	g.LLM.Skills["dictation_cleanup"][0].Provider = "x"
	again := s.Get()
	if again.LLM.Providers[0].Headers["a"] != "b" || again.LLM.Skills["dictation_cleanup"][0].Provider != "ollama" {
		t.Fatal("Get shares LLM state")
	}
}
