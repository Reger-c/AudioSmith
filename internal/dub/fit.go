package dub

import "math"

// Timing is how a dubbed line is fitted to its segment's slot.
type Timing string

const (
	// TimingLipSync (strict slot; the default before 0.4, and of projects
	// saved without a timing): the line is trimmed of
	// edge silence and time-stretched, pitch kept, to its slot; what is
	// still too long after the fastest natural speed is cut with a fade
	// (verdict trimmed).
	TimingLipSync Timing = "lipsync"
	// TimingSmart (the default, thread #41: lip sync sped 3 of 5 Chinese
	// lines up 1.2–1.4×): a line may run into the silence after its slot; only
	// what is left over is sped up (at most 1.25×). A line that still
	// does not fit keeps all its words and overlaps the next (overflow).
	TimingSmart Timing = "smart"
	// TimingConcise: no time-stretch at all; a line longer than its slot
	// and the silence after it is an overflow, for a shorter rewrite.
	TimingConcise Timing = "concise"
	// TimingStretchVideo: as smart, with at most 1.1× speed-up; the
	// video is slowed under lines that still overflow. Slowing the video
	// needs the engine's media.mux retiming (proposed); until then the
	// audio is fitted as smart and the line marked.
	TimingStretchVideo Timing = "stretch_video"
)

// Timings in the order the UI offers them.
var Timings = []Timing{TimingConcise, TimingSmart, TimingStretchVideo, TimingLipSync}

// Verdict is how a rendered line fits.
type Verdict string

const (
	VerdictOK       Verdict = "ok"       // fits as made, or within natural stretch
	VerdictTight    Verdict = "tight"    // fits, sped up noticeably (beyond 1.15×)
	VerdictOverflow Verdict = "overflow" // runs past the room it had (Over seconds)
	VerdictTrimmed  Verdict = "trimmed"  // cut to the slot (lip sync): words are lost
	VerdictVideo    Verdict = "video"    // needs the video slowed (Stretch video)
)

// Speed limits of the fitting, as time-stretch ratios (output/input).
const (
	FastestLipSync = 0.67 // 1.5× faster at most
	FastestSmart   = 0.8  // 1.25×
	FastestVideo   = 0.91 // 1.1×
	SlowestLipSync = 1.15 // a short line is slowed at most this much, then padded
	TightRatio     = 0.87 // faster than ~1.15× counts as tight
	// GapGuard is kept free before the next line when a line spills.
	GapGuard = 0.1
)

// FitIn is what fitting a line needs.
type FitIn struct {
	Rendered float64 // seconds of speech the model made (edges trimmed)
	Slot     float64 // the segment's length
	// Room is the silence after the slot, up to the next line (or the
	// end of the source); negative when lines overlap.
	Room   float64
	Timing Timing
}

// FitPlan says what to do with the line's audio.
type FitPlan struct {
	// Ratio is the time-stretch to apply (output/input; 1 = none).
	Ratio float64
	// Length is the line's length after stretching and cutting.
	Length float64
	// Cut: Length is shorter than the stretched audio (lip sync).
	Cut     bool
	Verdict Verdict
	// Over is how far the line runs past its slot and the room after it
	// (overflow), or the speech lost to the cut (trimmed).
	Over float64
	// VideoRatio is how much the video under the line should slow down
	// (Stretch video; 1 = not at all).
	VideoRatio float64
}

// Plan fits a rendered line to its slot by the timing strategy.
func Plan(in FitIn) FitPlan {
	p := FitPlan{Ratio: 1, Length: in.Rendered, Verdict: VerdictOK, VideoRatio: 1}
	if in.Rendered <= 0 || in.Slot <= 0 {
		return p
	}
	room := in.Slot + max(in.Room-GapGuard, 0)
	switch in.Timing {
	case TimingConcise:
		if in.Rendered > room {
			p.Verdict, p.Over = VerdictOverflow, round3(in.Rendered-room)
		}
	case TimingSmart, TimingStretchVideo:
		fastest := FastestSmart
		if in.Timing == TimingStretchVideo {
			fastest = FastestVideo
		}
		if in.Rendered > room {
			p.Ratio = max(room/in.Rendered, fastest)
			p.Length = in.Rendered * p.Ratio
			if p.Length > room+0.005 {
				p.Over = round3(p.Length - room)
				p.Verdict = VerdictOverflow
				if in.Timing == TimingStretchVideo {
					p.Verdict = VerdictVideo
					p.VideoRatio = round3(p.Length / room)
				}
			}
		}
	default: // lip sync
		switch {
		case in.Rendered > in.Slot:
			p.Ratio = max(in.Slot/in.Rendered, FastestLipSync)
			p.Length = in.Rendered * p.Ratio
			if p.Length > in.Slot+0.005 {
				p.Over = round3(p.Length - in.Slot)
				p.Length, p.Cut, p.Verdict = in.Slot, true, VerdictTrimmed
			}
		case in.Rendered < in.Slot*0.9:
			// Slow a short line a little; the rest is silence after it.
			p.Ratio = min(in.Slot/in.Rendered, SlowestLipSync)
			p.Length = in.Rendered * p.Ratio
		}
	}
	if p.Verdict == VerdictOK && p.Ratio < TightRatio {
		p.Verdict = VerdictTight
	}
	p.Ratio = round3(p.Ratio)
	p.Length = round3(p.Length)
	return p
}

// Room is the silence after segment i of segs (in time order) before the
// next one starts, or before duration (0 = unknown: 1 s).
func Room(segs []Segment, i int, duration float64) float64 {
	end := segs[i].End
	next := math.Inf(1)
	for j := i + 1; j < len(segs); j++ {
		if segs[j].Start >= segs[i].Start {
			next = min(next, segs[j].Start)
		}
	}
	if math.IsInf(next, 1) {
		if duration <= 0 {
			return 1
		}
		next = duration
	}
	return next - end
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }
