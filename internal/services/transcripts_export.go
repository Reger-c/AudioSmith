package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/doc/subtitle"
)

// Transcript export formats.
const (
	ExportTXT  = "txt"
	ExportSRT  = "srt"
	ExportVTT  = "vtt"
	ExportJSON = "json"
)

// ExportFormats are the formats Export writes, in menu order.
var ExportFormats = []string{ExportTXT, ExportSRT, ExportVTT, ExportJSON}

// Export writes a transcript as format: plain text (the preferred text:
// the clean-up when there is one), SubRip or WebVTT subtitles cut to
// Netflix-style limits for its language (speaker names as VTT voices), or
// JSON with every segment and timed word.
func Export(t core.Transcript, format string) ([]byte, error) {
	switch format {
	case ExportTXT:
		return []byte(t.Preferred() + "\n"), nil
	case ExportSRT, ExportVTT:
		cues := Cues(t)
		var b bytes.Buffer
		var err error
		if format == ExportSRT {
			err = subtitle.WriteSRT(&b, cues)
		} else {
			err = subtitle.WriteVTT(&b, cues, subtitle.VTTOptions{Speakers: true})
		}
		return b.Bytes(), err
	case ExportJSON:
		return exportJSON(t)
	}
	return nil, fmt.Errorf("export: unknown format %q", format)
}

// Cues are the transcript's subtitles: its segments split into readable
// cues by their word timings (estimated from the text when unknown).
func Cues(t core.Transcript) []subtitle.Cue {
	var segs []subtitle.Segment
	for _, s := range t.Segments {
		txt := strings.TrimSpace(s.Text)
		if txt == "" {
			continue
		}
		seg := subtitle.Segment{Start: secs(s.Start), End: secs(s.End), Text: txt, Speaker: speakerName(t, s.Speaker)}
		if !s.Edited {
			for _, w := range s.Words {
				seg.Words = append(seg.Words, subtitle.Word{Text: w.Text, Start: secs(w.Start), End: secs(w.End)})
			}
		}
		segs = append(segs, seg)
	}
	cues, _ := subtitle.Split(segs, subtitle.Defaults(t.Language))
	return cues
}

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// speakerName is a speaker label's name, or the label.
func speakerName(t core.Transcript, label string) string {
	if n := t.Speakers[label]; n != "" {
		return n
	}
	return label
}

type jsonWord struct {
	Text  string   `json:"text"`
	Start float64  `json:"start"`
	End   float64  `json:"end"`
	Prob  *float64 `json:"prob,omitempty"`
}

type jsonSegment struct {
	Start   float64    `json:"start"`
	End     float64    `json:"end"`
	Text    string     `json:"text"`
	Speaker string     `json:"speaker,omitempty"`
	Words   []jsonWord `json:"words,omitempty"`
}

type jsonTranscript struct {
	ID       string        `json:"id"`
	Title    string        `json:"title,omitempty"`
	Created  time.Time     `json:"created"`
	Language string        `json:"language,omitempty"`
	Duration float64       `json:"duration"`
	Model    string        `json:"model"`
	Aligner  string        `json:"aligner,omitempty"`
	Text     string        `json:"text"`
	Clean    string        `json:"clean,omitempty"`
	Segments []jsonSegment `json:"segments"`
}

func exportJSON(t core.Transcript) ([]byte, error) {
	out := jsonTranscript{ID: t.ID, Title: t.Title, Created: t.CreatedAt, Language: t.Language, Duration: round3(t.Duration),
		Model: t.Model, Aligner: t.Aligner, Text: t.Text(), Clean: t.Clean, Segments: []jsonSegment{}}
	for _, s := range t.Segments {
		js := jsonSegment{Start: round3(s.Start), End: round3(s.End), Text: s.Text, Speaker: speakerName(t, s.Speaker)}
		if !s.Edited {
			for _, w := range s.Words {
				js.Words = append(js.Words, jsonWord{Text: w.Text, Start: round3(w.Start), End: round3(w.End), Prob: w.Prob})
			}
		}
		out.Segments = append(out.Segments, js)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	return append(b, '\n'), err
}

func round3(f float64) float64 { return float64(int64(f*1000+0.5)) / 1000 }

// ExportAll writes transcripts as one text file, newest first: a header
// line (date, language, title) above each text, "---" between them.
// date formats the header's time (the UI's localized format).
func ExportAll(ts []core.Transcript, date func(time.Time) string) []byte {
	var b strings.Builder
	for i, t := range ts {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		head := "[" + date(t.CreatedAt) + "]"
		if t.Language != "" {
			head += " (" + t.Language + ")"
		}
		if t.Title != "" {
			head += " " + t.Title
		}
		b.WriteString(head + "\n" + t.Preferred() + "\n")
	}
	return []byte(b.String())
}
