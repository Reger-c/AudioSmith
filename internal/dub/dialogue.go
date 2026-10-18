package dub

// Dub × dialogue model (plan/dialogue-dub-audiobook-plan.md §2, thread
// #89): the conversations of a track are spoken by a dialogue model
// (SoulX-Podcast), each line hearing the lines before it, and every line
// is then fitted to its own slot as any other (Plan) and placed at its
// source start, so the source's gaps and overlaps stay. What is pure is
// here: when it applies, the stretches (internal/dialogue's DubRules over
// the track's lines in time order), the lines a speaker's reference is
// cut from, and the render keys. The engine side is
// internal/services/dub_dialogue*.go.

import (
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
	"math"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/dialogue"
)

// Dialogue modes (Settings.Dialogue).
const (
	DialogueAuto = "auto"
	DialogueOff  = "off"
)

// Why a track is not spoken by the dialogue model, as far as the project
// says (DialogueBlocked); the app adds this Mac's memory and the
// model's download.
const (
	DialogueWhyOff        = "off"         // the project's switch
	DialogueWhyVoiceMatch = "voice_match" // per-line voice match: each line clones its own clip
	DialogueWhyLanguage   = "language"    // the model does not speak the track's language
	DialogueWhySpeakers   = "speakers"    // fewer than two speakers
)

// DialogueOn reports whether the project asks for the dialogue model.
func (s Settings) DialogueOn() bool { return s.Dialogue == DialogueAuto }

// DialogueSpeaks reports whether a model speaking langs (base codes: zh,
// en) speaks the target language lang: Chinese in either script is read
// as Mandarin.
func DialogueSpeaks(langs []string, lang string) bool {
	b := base(lang)
	if b == "cmn" {
		b = "zh"
	}
	return slices.Contains(langs, b)
}

// DialogueBlocked says why lang's track is not spoken by a dialogue model
// that speaks langs ("" when, as far as the project goes, it is).
func (p *Project) DialogueBlocked(lang string, langs []string) string {
	switch {
	case !p.Settings.DialogueOn():
		return DialogueWhyOff
	case p.Settings.VoiceMatch == MatchLine:
		return DialogueWhyVoiceMatch
	case len(p.SpeakerLabels()) < 2:
		return DialogueWhySpeakers
	case !DialogueSpeaks(langs, lang):
		return DialogueWhyLanguage
	}
	return ""
}

// Stretch is a conversation of a track that the dialogue model speaks in
// one job.
type Stretch struct {
	N      int      // numbered from 1 in the track, in time order
	IDs    []string // its lines' segment ids, in time order
	Voices []string // its voices (DialogueVoice), by first line
}

// Index is line id's position in the stretch, or -1.
func (st Stretch) Index(id string) int { return slices.Index(st.IDs, id) }

// DialogueVoice is the voice a segment speaks with, as the stretches
// count voices: the speaker's own clone ("auto:" and the label) or the
// voice cast. Two labels cast with one voice are one voice.
func (p *Project) DialogueVoice(s Segment) string {
	ref := p.VoiceOf(s)
	if ref == "" || ref == VoiceAuto {
		return "auto:" + s.Speaker
	}
	return ref
}

// DialogueStretches are lang's stretches (dialogue.DubRules within lim):
// runs of turns in time order, a line being a turn when it has text in
// lang and speaks lang (Segment.Lang) and is not kept out (NoDialogue);
// a line without text is not said and ends nothing, any other ends the
// run.
func (p *Project) DialogueStretches(lang string, lim dialogue.Limits) []Stretch {
	return p.stretches(lang, lim, false)
}

// DialoguePreview are lang's stretches as they will be: the source text
// stands in for the lines not translated yet (the Cast step, before the
// translation).
func (p *Project) DialoguePreview(lang string, lim dialogue.Limits) []Stretch {
	return p.stretches(lang, lim, true)
}

// DialogueShare is the share of lang's lines (those with source text)
// that the dialogue model would speak, the source text standing in for
// the lines not translated yet (the translation's fit, before there is
// a translation).
func (p *Project) DialogueShare(lang string, lim dialogue.Limits) float64 {
	total := 0
	for _, s := range p.Segments {
		if strings.TrimSpace(s.Text) != "" {
			total++
		}
	}
	if total == 0 {
		return 0
	}
	n := 0
	for _, st := range p.DialoguePreview(lang, lim) {
		n += len(st.IDs)
	}
	return float64(n) / float64(total)
}

func (p *Project) stretches(lang string, lim dialogue.Limits, draft bool) []Stretch {
	order := make([]int, len(p.Segments))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return cmpFloat(p.Segments[a].Start, p.Segments[b].Start)
	})
	items := make([]dialogue.Item, len(order))
	for k, i := range order {
		s := p.Segments[i]
		txt := strings.TrimSpace(p.TextFor(s, lang))
		if txt == "" && draft {
			txt = strings.TrimSpace(s.Text)
		}
		it := dialogue.Item{Voice: p.DialogueVoice(s), Text: txt, Start: s.Start, End: s.End}
		switch {
		case txt == "":
			it.Aside = true // not said: ends nothing
		case s.NoDialogue || s.Lang != "" && !sameLanguage(s.Lang, lang):
		default:
			it.Turn = true
		}
		items[k] = it
	}
	var out []Stretch
	for _, g := range dialogue.Group(items, lim, dialogue.DubRules) {
		st := Stretch{N: len(out) + 1}
		for _, k := range g {
			s := p.Segments[order[k]]
			st.IDs = append(st.IDs, s.ID)
			if v := items[k].Voice; !slices.Contains(st.Voices, v) {
				st.Voices = append(st.Voices, v)
			}
		}
		out = append(out, st)
	}
	return out
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// StretchOf is the stretch line id is in, or false.
func StretchOf(sts []Stretch, id string) (Stretch, bool) {
	for _, st := range sts {
		if st.Index(id) >= 0 {
			return st, true
		}
	}
	return Stretch{}, false
}

// DialogueKey is a dialogue line's render key: its own fingerprint and
// its stretch's key (the model, the seed, the references and the
// members, not the other lines' text), so that editing a line makes only
// it stale and changing a voice the whole stretch (§2.5).
func DialogueKey(fingerprint, stretchKey string) string {
	h := sha256.Sum256([]byte(fingerprint + "\x1edlg\x1e" + stretchKey))
	return hex.EncodeToString(h[:12])
}

// A speaker's dialogue reference lasts DialogueRefMin–DialogueRefMax
// seconds (#81, #83: the dialogue models clone best from 10–14 s), cut
// from several of their lines, DialogueRefGap apart.
const (
	DialogueRefMin = 10.0
	DialogueRefMax = 14.0
	DialogueRefGap = 0.25
	// A line shorter than dialogueRefLine adds little but an edge.
	dialogueRefLine = 0.8
)

// DialogueRefLines are the lines a speaker's dialogue reference is cut
// from (indices of segs, in time order): first those of their clone
// reference ref (the references stage chose it as their best window),
// then their other lines, the longest first, until DialogueRefMin
// seconds and never past DialogueRefMax. Only lines of theirs alone
// (no other speaker over them, not merged) with text count; none:
// nil (the caller keeps ref).
func DialogueRefLines(segs []Segment, speaker string, ref *Reference) []int {
	var inRef, rest []int
	for i, s := range segs {
		d := s.Duration()
		if s.Speaker != speaker || len(s.Parts) > 0 || strings.TrimSpace(s.Text) == "" || d < dialogueRefLine || d > RefMax ||
			overlapsOther(segs, speaker, s.Start, s.End) {
			continue
		}
		if ref != nil && s.Start >= ref.Start-0.05 && s.End <= ref.End+0.05 {
			inRef = append(inRef, i)
		} else {
			rest = append(rest, i)
		}
	}
	slices.SortStableFunc(rest, func(a, b int) int { return cmpFloat(segs[b].Duration(), segs[a].Duration()) })
	var out []int
	total := 0.0
	for _, i := range append(inRef, rest...) {
		if total >= DialogueRefMin {
			break
		}
		add := segs[i].Duration()
		if len(out) > 0 {
			add += DialogueRefGap
		}
		if total+add > DialogueRefMax {
			continue
		}
		out = append(out, i)
		total += add
	}
	slices.Sort(out)
	return out
}

// DialogueRefText is the transcript of a reference cut from lines.
func DialogueRefText(segs []Segment, lines []int) string {
	out := ""
	for _, i := range lines {
		out = joinText(out, strings.TrimSpace(segs[i].Text))
	}
	return out
}

// DialogueSeed is the seed of a stretch's take (Track.DialogueTakes),
// by its first line's id: a stretch keeps it when others come and go.
func DialogueSeed(first string, take int) int64 {
	h := fnv.New32a()
	h.Write([]byte(first))
	return int64(h.Sum32()%1_000_000) + 7919*int64(take)
}

// FitRate is how much longer than the translation's estimate a track's
// lines last when a dialogue model that speaks rate times the estimate
// (raw / predicted: SoulX-Podcast 1.01–1.12× in P0, thread #86) speaks
// share of them; the translation then aims a little shorter.
func FitRate(rate, share float64) float64 {
	if rate <= 0 || math.IsNaN(rate) {
		return 1
	}
	return 1 + (rate-1)*min(max(share, 0), 1)
}
