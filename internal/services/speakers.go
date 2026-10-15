package services

// Transcription with speakers (thread #48): MOSS-Transcribe-Diarize
// transcribes and labels who speaks in one pass (proto.Segment.Speaker),
// for the dub's source analysis and Transcribe's Speakers option.
//
// The engine decodes what it gets in one pass; the app keeps each pass
// at most SpeakersWindowSeconds long. A pass slows down and grows with
// its length (Dicta's evaluation: 34 minutes ran at real time and peaked
// at 7.7 GB, 10-minute windows at 2.4× and 3.2 GB), and the model
// numbers speakers per pass, so longer audio goes in windows that
// overlap by SpeakersOverlapSeconds, and who speaks when in the overlap
// maps each window's labels onto the file's (Dicta 48d0bb8: linked
// windows kept speakers as well as one pass, unlinked ones lost them half
// the time).
//
// Its decoder is a chat model: given audio without speech it answers
// ("I'm sorry, I can't assist with that request."). Windows without
// speech are not sent, and such answers, and a copy of the vocabulary
// prompt, are dropped.

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

const (
	// SpeakersWindowSeconds is the longest audio a speakers model hears
	// in one pass; longer audio goes in windows of at most this length.
	SpeakersWindowSeconds = 600
	// SpeakersOverlapSeconds is what consecutive windows share: both
	// transcribe it, and its speakers link the two windows' labels.
	SpeakersOverlapSeconds = 60
)

// The window and overlap TranscribeSpeakers uses (tests shorten them).
var speakersWindow, speakersOverlap = float64(SpeakersWindowSeconds), float64(SpeakersOverlapSeconds)

// IsSpeakersModel reports whether model (a repo or id) transcribes with
// speaker labels: MOSS-Transcribe-Diarize (catalogue capability
// "speakers").
func IsSpeakersModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "moss-transcribe-diarize")
}

// speakerWindow is a stretch of the audio one pass hears, in seconds.
type speakerWindow struct{ start, end float64 }

// speakerWindows cuts seconds of audio into passes: one when it fits in
// window, else equal windows of at most window seconds, each sharing
// overlap seconds with the next.
func speakerWindows(seconds, window, overlap float64) []speakerWindow {
	if seconds <= window || window <= overlap {
		return []speakerWindow{{0, seconds}}
	}
	n := int(math.Ceil((seconds - overlap) / (window - overlap)))
	step := (seconds - overlap) / float64(n)
	out := make([]speakerWindow, n)
	for k := range out {
		out[k] = speakerWindow{float64(k) * step, float64(k)*step + step + overlap}
	}
	out[n-1].end = seconds
	return out
}

// TranscribeSpeakers transcribes base.Audio (a WAV) of seconds with a
// speakers model through call: in one pass, or in linked windows (each
// cut into a WAV of its own next to the audio) when it is longer than
// SpeakersWindowSeconds. call gets the window's index and the number of
// windows, for progress. Windows without speech are not sent. The
// result's segments are in file time, labelled S1, S2… in order of
// appearance through the whole file; its text joins theirs.
func TranscribeSpeakers(ctx context.Context, call func(k, n int, p *proto.TranscribeParams, out *proto.TranscribeResult) error,
	base proto.TranscribeParams, seconds float64) (proto.TranscribeResult, error) {
	audio, _, rerr := wav.ReadFile(base.Audio)
	if rerr == nil {
		seconds = audio.Seconds()
	}
	wins := speakerWindows(seconds, speakersWindow, speakersOverlap)
	if rerr != nil {
		wins = wins[:1] // not a WAV Go reads: the engine hears it whole
		wins[0].end = max(seconds, 0)
	}
	terms := hotwords(base.Prompt)
	res := proto.TranscribeResult{Model: base.Model, AudioSeconds: seconds}
	var prev *linkedWindow
	next := 1
	langs := map[string]int{}
	for k, w := range wins {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		p := base
		var own []proto.Segment
		if rerr == nil {
			piece := audio.Slice(int(math.Round(w.start*float64(audio.Rate))), int(math.Round(w.end*float64(audio.Rate))))
			if !audibleSpeech(piece) {
				prev = &linkedWindow{end: w.end, labels: prevLabels(prev)}
				continue
			}
			if len(wins) > 1 {
				p.Audio = fmt.Sprintf("%s.speakers%d.wav", strings.TrimSuffix(base.Audio, ".wav"), k)
				if err := wav.WriteFile(p.Audio, piece, wav.PCM16, wav.Options{}); err != nil {
					return res, err
				}
			}
		}
		var tr proto.TranscribeResult
		err := call(k, len(wins), &p, &tr)
		if p.Audio != base.Audio {
			os.Remove(p.Audio)
		}
		if err != nil {
			return res, err
		}
		res.Model, res.Seconds = firstNonEmpty(tr.Model, res.Model), res.Seconds+tr.Seconds
		for _, s := range tr.Segments {
			s.Text = strings.TrimSpace(s.Text)
			if s.Text == "" || isAssistantReply(s.Text) || isPromptEcho(s.Text, terms) {
				continue
			}
			// Untagged text between windows is not speech the model heard
			// (a window's tail of silence gets a chat answer).
			if len(wins) > 1 && s.Speaker == "" {
				continue
			}
			s.Start, s.End = s.Start+w.start, max(s.End, s.Start)+w.start
			own = append(own, s)
			if tr.Language != "" {
				langs[tr.Language] += len([]rune(s.Text))
			}
		}
		// This window's labels → the file's, through the overlap.
		labels := linkLabels(prev, own, w.start)
		for _, s := range own {
			if s.Speaker != "" && labels[s.Speaker] == "" {
				labels[s.Speaker] = strconv.Itoa(next)
				next++
			}
		}
		// Each stretch from one window: the overlap is split in the middle.
		from, to := math.Inf(-1), math.Inf(1)
		if k > 0 {
			from = w.start + speakersOverlap/2
		}
		if k < len(wins)-1 {
			to = w.end - speakersOverlap/2
		}
		for _, s := range own {
			if mid := (s.Start + s.End) / 2; mid < from || mid >= to {
				continue
			}
			if s.Speaker != "" {
				s.Speaker = labels[s.Speaker]
			}
			s.Start, s.End = min(s.Start, seconds), min(s.End, seconds)
			res.Segments = append(res.Segments, s)
		}
		prev = &linkedWindow{end: w.end, segments: own, labels: labels}
	}
	relabelSpeakers(res.Segments)
	texts := make([]string, len(res.Segments))
	for i, s := range res.Segments {
		texts[i] = s.Text
	}
	res.Text = joinTexts(texts)
	best := 0
	for l, n := range langs {
		if n > best || n == best && l < res.Language {
			res.Language, best = l, n
		}
	}
	if res.Language == "" {
		res.Language = base.Language
	}
	if res.Seconds > 0 {
		res.RTF = seconds / res.Seconds
	}
	return res, nil
}

// linkedWindow is a window done: where it ends, its segments (file time,
// the model's own labels) and what those labels are in the file.
type linkedWindow struct {
	end      float64
	segments []proto.Segment
	labels   map[string]string
}

func prevLabels(w *linkedWindow) map[string]string {
	if w == nil {
		return map[string]string{}
	}
	return w.labels
}

// linkLabels maps a window's labels (own: its segments, file time, its
// labels) onto the file's: the pairs that speak at the same time longest
// in the overlap with the window before (from start to its end), each
// label once; and when one speaker is left on each side, they are the
// same person.
func linkLabels(prev *linkedWindow, own []proto.Segment, start float64) map[string]string {
	labels := map[string]string{}
	if prev == nil {
		return labels
	}
	taken := map[string]bool{}
	for _, pr := range coincidence(prev.segments, own, start, prev.end) {
		id := prev.labels[pr.a]
		if labels[pr.b] != "" || id == "" || taken[id] {
			continue
		}
		labels[pr.b], taken[id] = id, true
	}
	var mine, theirs []string
	for _, s := range own {
		if s.Speaker != "" && labels[s.Speaker] == "" && !slices.Contains(mine, s.Speaker) {
			mine = append(mine, s.Speaker)
		}
	}
	for _, id := range prev.labels {
		if !taken[id] && !slices.Contains(theirs, id) {
			theirs = append(theirs, id)
		}
	}
	if len(mine) == 1 && len(theirs) == 1 {
		labels[mine[0]] = theirs[0]
	}
	return labels
}

type labelPair struct {
	a, b    string
	seconds float64
}

// coincidence is how long each pair of labels (a's, b's) speaks at the
// same time within [from, to], pairs of at least a second, longest first.
func coincidence(a, b []proto.Segment, from, to float64) []labelPair {
	t := map[[2]string]float64{}
	for _, x := range a {
		for _, y := range b {
			if x.Speaker == "" || y.Speaker == "" {
				continue
			}
			if o := min(x.End, y.End, to) - max(x.Start, y.Start, from); o > 0 {
				t[[2]string{x.Speaker, y.Speaker}] += o
			}
		}
	}
	var out []labelPair
	for k, v := range t {
		if v >= 1 {
			out = append(out, labelPair{k[0], k[1], v})
		}
	}
	slices.SortFunc(out, func(x, y labelPair) int {
		if x.seconds != y.seconds {
			return int(math.Copysign(1, y.seconds-x.seconds))
		}
		return strings.Compare(x.a+x.b, y.a+y.b)
	})
	return out
}

// relabelSpeakers names the labels S1, S2… in order of appearance.
func relabelSpeakers(segs []proto.Segment) {
	names := map[string]string{}
	for i := range segs {
		l := segs[i].Speaker
		if l == "" {
			continue
		}
		if names[l] == "" {
			names[l] = "S" + strconv.Itoa(len(names)+1)
		}
		segs[i].Speaker = names[l]
	}
}

// joinTexts joins segment texts: no space next to CJK characters.
func joinTexts(texts []string) string {
	var b strings.Builder
	for _, t := range texts {
		if t == "" {
			continue
		}
		if b.Len() > 0 && spaced(b.String(), t) {
			b.WriteByte(' ')
		}
		b.WriteString(t)
	}
	return b.String()
}

// audibleSpeech reports whether audio (any rate) holds speech-level
// sound: 30 ms frames louder than both -60 dBFS and the quietest tenth
// of the frames plus 12 dB, 0.3 s of them in all (Dicta 65fe7ac).
func audibleSpeech(b dsp.Buffer) bool {
	frame := max(b.Rate*30/1000, 1) * b.Channels
	if len(b.Samples) < frame {
		return false
	}
	var levels []float64
	for i := 0; i+frame <= len(b.Samples); i += frame {
		var sum float64
		for _, v := range b.Samples[i : i+frame] {
			sum += float64(v) * float64(v)
		}
		levels = append(levels, 10*math.Log10(max(sum/float64(frame), 1e-12)))
	}
	sorted := slices.Clone(levels)
	slices.Sort(sorted)
	threshold := max(-60, sorted[len(sorted)/10]+12)
	n := 0
	for _, l := range levels {
		if l > threshold {
			n++
		}
	}
	return n >= 10
}

// assistantReply matches what the model's chat decoder says instead of a
// transcript ("I'm sorry, I can't assist with that request. I'm a Qwen-1
// model…"): the answers seen so far.
var assistantReply = regexp.MustCompile(`(?i)(can'?t|cannot) assist with (that|this)|i'?m a qwen|as an ai (language )?model|我无法协助|我是通义|热词提示`)

func isAssistantReply(text string) bool { return assistantReply.MatchString(text) }

// hotwords are the terms of a vocabulary prompt, lower-case: separated
// by commas, 、, semicolons or new lines (as the engine reads them).
func hotwords(prompt string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(prompt, func(r rune) bool { return strings.ContainsRune("\n,，、;；", r) }) {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// isPromptEcho reports whether text is only the vocabulary copied: two
// or more of its terms, and nothing else but separators.
func isPromptEcho(text string, terms []string) bool {
	if len(terms) < 2 {
		return false
	}
	// Terms may hold spaces ("API latency"): split on separators only.
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return strings.ContainsRune("\n,，、;；", r) })
	n := 0
	for _, p := range parts {
		p = strings.TrimFunc(p, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
		if p == "" {
			continue
		}
		if !slices.Contains(terms, p) {
			return false
		}
		n++
	}
	return n >= 2
}
