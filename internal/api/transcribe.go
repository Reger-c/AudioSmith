package api

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// MaxUpload bounds an uploaded file (audio, video, a voice reference).
const MaxUpload = 1 << 30 // 1 GiB

// TranscribeParams is a transcription as clients ask for it.
type TranscribeParams struct {
	// File is a readable path; it is not removed (uploads are, by the
	// handlers that saved them).
	File  string
	Title string
	// Model: a catalogue id, repo or label, or OpenAI's names.
	Model    string
	Language string // BCP-47; "" detects it
	Prompt   string
	// Words asks for word timings (the forced aligner runs when the model
	// gives none).
	Words bool
	// Speakers asks who says what: the speakers model (thread #48,
	// MOSS-Transcribe-Diarize) transcribes, whatever Model says, and the
	// segments carry speaker labels. Naming that model, or OpenAI's
	// gpt-4o-transcribe-diarize, does the same.
	Speakers bool
}

// Transcription is a finished transcript.
type Transcription struct {
	Transcript core.Transcript
	Warnings   []string
	// Replaced is set when the request named a removed model.
	Replaced *Replacement
}

// Transcribe transcribes a file through the queue and waits for it. The
// transcript is kept in the library (the Transcribe page lists it).
func (s *Server) Transcribe(ctx context.Context, p TranscribeParams) (Transcription, error) {
	if err := s.engineGate(); err != nil {
		return Transcription{}, err
	}
	e, rep, err := s.resolveSTT(p.Model, p.Language)
	if p.Speakers || isSpeakersAlias(p.Model) {
		var ok bool
		if e, ok = catalog.Builtin().SpeakersModel(); !ok {
			return Transcription{}, errInvalid("speakers", "This build has no model that tells speakers apart.")
		}
		rep, err = nil, nil
	}
	if err != nil {
		return Transcription{}, err
	}
	if p.File == "" {
		return Transcription{}, errInvalid("file", "There is no file to transcribe.")
	}
	mode := "accurate"
	if isSTTAlias(p.Model) && fastSTTAlias(p.Model) {
		mode = "fast" // gpt-4o-mini-transcribe: Transcribe's Fast mode
	}
	req := services.TranscribeRequest{ID: core.NewID(time.Now()), Input: p.File, Source: core.SourceFile,
		Title: p.Title, Mode: mode, Model: e.Repo, Words: p.Words, Language: strings.TrimSpace(p.Language), Prompt: p.Prompt}
	if p.Words && !e.Has(catalog.CapWordTimes) {
		if al, ok := catalog.Builtin().ByID(catalog.DefaultAlign); ok {
			req.Aligner = al.Repo
		}
	}
	x := s.w.owner(services.TranscriptOwner + req.ID)
	defer x.stop()
	jobID, err := s.b.Transcribe(ctx, req)
	if err != nil {
		return Transcription{}, err
	}
	job, err := waitJob(ctx, s.sched, x, jobID, s.queueTimeout(), nil)
	if err != nil {
		return Transcription{}, err
	}
	var res services.TranscribeResult
	if err := jobOutcome(job, &res); err != nil {
		return Transcription{}, err
	}
	return Transcription{Transcript: res.Transcript, Warnings: res.Warnings, Replaced: rep}, nil
}

// TranslateToEnglish translates a transcript's segments into English with
// Settings → Writing help's language model (speech translation models
// are not in the catalogue yet). An English transcript is kept as it is.
func (s *Server) TranslateToEnglish(ctx context.Context, t core.Transcript) (core.Transcript, error) {
	if strings.HasPrefix(strings.ToLower(t.Language), "en") {
		return t, nil
	}
	req := translate.Request{Source: t.Language, Target: "en"}
	for i, seg := range t.Segments {
		if strings.TrimSpace(seg.Text) == "" {
			continue
		}
		req.Segments = append(req.Segments, translate.Segment{ID: itoa(i), Text: seg.Text})
	}
	if len(req.Segments) == 0 {
		return t, nil
	}
	res, err := s.b.Translate(ctx, req)
	if err != nil {
		if errors.Is(err, core.ErrUnsupported) || translate.Code(err) == translate.CodeNoLLM {
			return t, &Error{Status: http.StatusNotImplemented, Code: CodeTranslationNotReady,
				Message: "Translating speech needs a language model: set one up in AudioSmith → Settings → Writing help (a local Ollama works)."}
		}
		return t, err
	}
	out := t
	out.Segments = make([]core.TranscriptSegment, len(t.Segments))
	copy(out.Segments, t.Segments)
	for _, tr := range res.Segments {
		i := atoi(tr.ID)
		if i < 0 || i >= len(out.Segments) || tr.Text == "" {
			continue
		}
		out.Segments[i].Text = tr.Text
		out.Segments[i].Words = nil // they time the original words
	}
	out.Language, out.Clean = "en", ""
	return out, nil
}

// saveUpload copies a multipart file into the temporary folder and
// returns its path (the caller removes it).
func (s *Server) saveUpload(f multipart.File, h *multipart.FileHeader) (string, error) {
	dir := filepath.Join(s.b.TempDir(), "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(h.Filename))
	if len(ext) > 8 || strings.ContainsAny(ext, `/\`) {
		ext = ""
	}
	out, err := os.CreateTemp(dir, "upload-*"+ext)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, io.LimitReader(f, MaxUpload+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if fi, serr := os.Stat(out.Name()); serr == nil && fi.Size() > MaxUpload {
			err = &Error{Status: http.StatusRequestEntityTooLarge, Code: CodeTooLarge, Message: "The file is over 1 GiB."}
		}
	}
	if err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

// formFile reads the multipart form's file field into a temporary file.
func (s *Server) formFile(r *http.Request, field string) (path, name string, err error) {
	r.Body = http.MaxBytesReader(nil, r.Body, MaxUpload+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return "", "", errInvalid(field, "Send the file as multipart/form-data (%v).", err)
	}
	f, h, err := r.FormFile(field)
	if err != nil {
		return "", "", errInvalid(field, "The form has no %q file.", field)
	}
	defer f.Close()
	path, err = s.saveUpload(f, h)
	return path, filepath.Base(h.Filename), err
}

func itoa(i int) string { return strconv.Itoa(i) }

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// formBool reads a form field as a boolean: "true", "1", "yes", "on".
func formBool(r *http.Request, field string) bool {
	switch strings.ToLower(strings.TrimSpace(r.FormValue(field))) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
