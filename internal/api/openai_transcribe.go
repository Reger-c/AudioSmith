package api

// OpenAI's audio API (/v1): transcriptions and translations; split from
// openai.go.

import (
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/services"
)

// v1Transcriptions is POST /v1/audio/transcriptions.
func (s *Server) v1Transcriptions(w http.ResponseWriter, r *http.Request) {
	s.v1Transcribe(w, r, false)
}

// v1Translations is POST /v1/audio/translations: speech in any language
// to English text.
func (s *Server) v1Translations(w http.ResponseWriter, r *http.Request) { s.v1Transcribe(w, r, true) }

func (s *Server) v1Transcribe(w http.ResponseWriter, r *http.Request, translateIt bool) {
	path, name, err := s.formFile(r, "file")
	if err != nil {
		writeError(w, err)
		return
	}
	defer os.Remove(path)
	if v := r.FormValue("stream"); v == "true" || v == "1" {
		writeError(w, errInvalid("stream", "Streaming transcription is not supported: send stream=false."))
		return
	}
	format := strings.ToLower(r.FormValue("response_format"))
	if format == "" {
		format = "json"
	}
	if !slices.Contains([]string{"json", "text", "verbose_json", "diarized_json", "srt", "vtt"}, format) {
		writeError(w, &Error{Status: http.StatusBadRequest, Code: CodeUnsupportedFormat, Param: "response_format",
			Message: "response_format is json, text, verbose_json, diarized_json, srt or vtt."})
		return
	}
	grans := append(r.Form["timestamp_granularities[]"], r.Form["timestamp_granularities"]...)
	words := format == "verbose_json" && slices.Contains(grans, "word")
	// Who says what: AudioSmith's speakers=true, OpenAI's
	// gpt-4o-transcribe-diarize, or its diarized_json format.
	speakers := formBool(r, "speakers") || format == "diarized_json"
	if speakers && translateIt {
		writeError(w, errInvalid("speakers", "Translations do not tell speakers apart."))
		return
	}
	p := TranscribeParams{File: path, Title: name + " (API)", Model: r.FormValue("model"), Prompt: r.FormValue("prompt"), Words: words, Speakers: speakers}
	if !translateIt {
		p.Language = r.FormValue("language")
	}
	tr, err := s.Transcribe(r.Context(), p)
	if err != nil {
		writeError(w, err)
		return
	}
	if r := tr.Replaced; r != nil {
		w.Header().Set("X-AudioSmith-Replaced-By", r.ReplacedBy)
		w.Header().Set("X-AudioSmith-Warning", r.Warning)
	}
	t := tr.Transcript
	task := "transcribe"
	if translateIt {
		task = "translate"
		if t, err = s.TranslateToEnglish(r.Context(), t); err != nil {
			writeError(w, err)
			return
		}
	}
	writeTranscript(w, t, format, task, words, slices.Contains(grans, "segment") || len(grans) == 0)
}

// writeTranscript answers a transcript in an OpenAI response format.
func writeTranscript(w http.ResponseWriter, t core.Transcript, format, task string, words, segments bool) {
	text := t.Text()
	usage := map[string]any{"type": "duration", "seconds": int(t.Duration + 0.999)}
	switch format {
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, text+"\n")
	case "srt", "vtt":
		data, err := services.Export(t, format)
		if err != nil {
			writeError(w, err)
			return
		}
		ct := "application/x-subrip"
		if format == "vtt" {
			ct = "text/vtt"
		}
		w.Header().Set("Content-Type", ct+"; charset=utf-8")
		w.Write(data)
	case "verbose_json":
		out := map[string]any{"task": task, "language": languageName(t.Language), "duration": t.Duration, "text": text, "usage": usage}
		if segments {
			segs := []map[string]any{}
			for i, s := range t.Segments {
				seg := map[string]any{"id": i, "seek": 0, "start": s.Start, "end": s.End, "text": s.Text,
					"tokens": []int{}, "temperature": 0.0, "avg_logprob": 0.0, "compression_ratio": 0.0, "no_speech_prob": 0.0}
				if s.Speaker != "" {
					seg["speaker"] = speakerName(t, s.Speaker)
				}
				segs = append(segs, seg)
			}
			out["segments"] = segs
		}
		if words {
			ws := []WordJSON{}
			for _, s := range t.Segments {
				for _, wd := range s.Words {
					word := strings.TrimFunc(wd.Text, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) })
					if word == "" {
						continue
					}
					ws = append(ws, WordJSON{Word: word, Start: wd.Start, End: wd.End})
				}
			}
			out["words"] = ws
		}
		writeJSON(w, http.StatusOK, out)
	case "diarized_json":
		// OpenAI's shape: one segment per turn, with its speaker.
		segs := []map[string]any{}
		for i, s := range t.Segments {
			segs = append(segs, map[string]any{"type": "transcript.text.segment", "id": "seg_" + itoa(i), "start": s.Start, "end": s.End,
				"text": s.Text, "speaker": speakerName(t, s.Speaker)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"task": task, "duration": t.Duration, "text": text, "segments": segs, "usage": usage})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"text": text, "usage": usage})
	}
}
