package core

import (
	"strings"
	"time"
)

// TranscriptSource says where a transcript's audio came from.
type TranscriptSource string

const (
	SourceFile      TranscriptSource = "file"      // an audio or video file
	SourceRecording TranscriptSource = "recording" // recorded on the Transcribe page
	SourceDictation TranscriptSource = "dictation" // system-wide dictation
)

// Transcript is speech turned into text (M2): the Transcribe page's
// history and dictation's. Its audio, when kept, is a WAV in the store's
// transcript directory (store.Transcripts.AudioDir), named by Audio.
type Transcript struct {
	ID        string           `json:"id"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
	Title     string           `json:"title"` // the file's name, or "" (the UI names recordings)
	Source    TranscriptSource `json:"source"`
	// SourcePath is the file transcribed, as picked (it may move later).
	SourcePath string `json:"sourcePath,omitempty"`
	// Audio is the 16 kHz mono WAV kept with the transcript, a file name
	// in the transcript directory; "" when none is kept.
	Audio    string  `json:"audio,omitempty"`
	Mode     string  `json:"mode"`               // "fast" | "accurate"
	Model    string  `json:"model"`              // repo of the speech-to-text model
	Aligner  string  `json:"aligner,omitempty"`  // repo of the aligner that timed the words
	Language string  `json:"language,omitempty"` // BCP-47, detected or chosen
	Duration float64 `json:"duration"`           // seconds of audio
	// Segments in time order; their text, edited or not, is the transcript.
	Segments []TranscriptSegment `json:"segments"`
	// Raw is the text as the model gave it, before any edit.
	Raw string `json:"raw,omitempty"`
	// Clean is a cleaned-up version of the text (LLM clean-up of
	// fillers and self-corrections, or the dictation's polish); "" when
	// there is none. The segments keep the original.
	Clean   string `json:"clean,omitempty"`
	CleanBy string `json:"cleanBy,omitempty"` // "polish", or the LLM provider/model
	// Speakers names speaker labels ("S1" → "Ana"); labels come from
	// diarization (M4), nil until then.
	Speakers map[string]string `json:"speakers,omitempty"`
	// App is the app dictation typed into ("Notes"), for dictations.
	App string `json:"app,omitempty"`
	// Inserted reports whether a dictation was pasted (else it was copied).
	Inserted bool `json:"inserted,omitempty"`
	// Seconds is how long the model took (without loading).
	Seconds float64 `json:"seconds,omitempty"`
}

// TranscriptSegment is a stretch of speech: a sentence, or the model's
// segment when words are not timed.
type TranscriptSegment struct {
	Start   float64          `json:"start"`
	End     float64          `json:"end"`
	Text    string           `json:"text"`
	Speaker string           `json:"speaker,omitempty"` // diarization label (M4)
	Words   []TranscriptWord `json:"words,omitempty"`   // timed words, when known
	Edited  bool             `json:"edited,omitempty"`  // the text was changed by hand
}

// TranscriptWord is a timed word as it reads in the text (with its
// punctuation); one character for Chinese and Japanese.
type TranscriptWord struct {
	Text  string   `json:"text"`
	Start float64  `json:"start"`
	End   float64  `json:"end"`
	Prob  *float64 `json:"prob,omitempty"`
}

// Text is the transcript as the segments read, edits included.
func (t Transcript) Text() string {
	var b strings.Builder
	for _, s := range t.Segments {
		txt := strings.TrimSpace(s.Text)
		if txt == "" {
			continue
		}
		if b.Len() > 0 && needsSpace(b.String(), txt) {
			b.WriteByte(' ')
		}
		b.WriteString(txt)
	}
	return b.String()
}

// Preferred is the text to copy, export as text and reuse: the clean-up
// when there is one, else Text.
func (t Transcript) Preferred() string {
	if t.Clean != "" {
		return t.Clean
	}
	return t.Text()
}

// needsSpace reports whether a space goes between two pieces of text:
// not between CJK characters.
func needsSpace(before, after string) bool {
	b := []rune(before)
	a := []rune(after)
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	return !(isUnspaced(b[len(b)-1]) || isUnspaced(a[0]))
}

func isUnspaced(r rune) bool {
	return r >= 0x2E80 && r <= 0x9FFF || r >= 0xF900 && r <= 0xFAFF || r >= 0xFF00 && r <= 0xFFEF || r >= 0x3000 && r <= 0x303F
}

// TranscriptQuery filters a transcript list, newest first.
type TranscriptQuery struct {
	Search string           // text, title, language
	Source TranscriptSource // "" = every source
	Limit  int              // 0 = no limit
	Offset int
}

// TranscriptChanged is published after a transcript is saved or deleted.
type TranscriptChanged struct {
	Transcript Transcript
	Deleted    bool
}

func (TranscriptChanged) Topic() string { return "transcript.changed" }
