// Package qc is "Check audio": an advisory scan of a take or a render that
// reports problems as codes with severities and time ranges the UI can seek
// to. It streams (Checker) so hour-long renders need not sit in memory.
//
// Codes (Issue.Value meaning in brackets):
//
//	empty            error    no audio at all
//	silent           error    nothing above the silence threshold [peak dBFS]
//	invalid_samples  error    NaN or ±Inf samples (zeroed for the other checks) [count]
//	clipping         warning  runs of ≥ ClipRun samples at full scale [clipped samples]
//	silence_gap      warning  internal silence longer than MaxSilence [seconds]
//	loudness_jump    warning  speech loudness changes by ≥ JumpLU between adjacent windows [LU, signed: + = louder]
//	dc_offset        warning  a channel's mean is beyond DCOffsetDB [dBFS of |mean|]
//	low_level        warning  integrated loudness under LowLUFS [LUFS]
//	edge_silence     info     leading or trailing silence longer than MaxEdgeSilence [seconds]
//	click            info     an isolated discontinuity much sharper than its surroundings [jump size]
//
// Ranges are seconds [Start, End). Channel is the channel index, or −1 for
// all channels together.
package qc

import (
	"math"
	"slices"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
)

// Severity of an issue.
type Severity string

const (
	Info    Severity = "info"
	Warning Severity = "warning"
	Error   Severity = "error"
)

// Issue is one finding.
type Issue struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Start    float64  `json:"start"`
	End      float64  `json:"end"`
	Channel  int      `json:"channel"`
	Value    float64  `json:"value"`
	Limit    float64  `json:"limit"`
}

// Options tune the checks; zero values take the defaults.
type Options struct {
	SilenceDB      float64 // 10 ms window RMS under this is silence; default −50 dBFS
	MaxSilence     float64 // internal silence longer than this is a gap; default 2 s
	MaxEdgeSilence float64 // leading/trailing silence longer than this; default 1 s
	JumpLU         float64 // loudness jump threshold; default 8 LU
	JumpWindow     float64 // each side of a jump comparison; default 1.5 s
	ClipLevel      float64 // |sample| at or above is clipped; default 0.999 (−0.009 dBFS)
	ClipRun        int     // consecutive clipped samples that count; default 3
	DCOffsetDB     float64 // |mean| above this is DC offset; default −40 dBFS (0.01)
	LowLUFS        float64 // integrated loudness below this is low; default −35 LUFS
	MaxIssues      int     // per code; default 100 (Report.Truncated tells)
}

func (o Options) norm() Options {
	def := func(v *float64, d float64) {
		if *v == 0 {
			*v = d
		}
	}
	def(&o.SilenceDB, -50)
	def(&o.MaxSilence, 2)
	def(&o.MaxEdgeSilence, 1)
	def(&o.JumpLU, 8)
	def(&o.JumpWindow, 1.5)
	def(&o.ClipLevel, 0.999)
	def(&o.DCOffsetDB, -40)
	def(&o.LowLUFS, -35)
	if o.ClipRun <= 0 {
		o.ClipRun = 3
	}
	if o.MaxIssues <= 0 {
		o.MaxIssues = 100
	}
	return o
}

// Report is the result of a check.
type Report struct {
	Seconds   float64        `json:"seconds"`
	Rate      int            `json:"rate"`
	Channels  int            `json:"channels"`
	Loudness  loudness.Stats `json:"loudness"`
	DCOffset  []float64      `json:"dcOffset"` // per channel mean
	Issues    []Issue        `json:"issues"`   // sorted by start time
	Errors    int            `json:"errors"`
	Warnings  int            `json:"warnings"`
	Infos     int            `json:"infos"`
	OK        bool           `json:"ok"` // no errors or warnings
	Truncated bool           `json:"truncated,omitempty"`
}

// span is a frame range with a value.
type span struct {
	start, end int64
	value      float64
}

// runs collects frame ranges, merging those closer than gap frames.
type runs struct {
	gap int64
	s   []span
}

func (r *runs) add(start, end int64, v float64) {
	if n := len(r.s); n > 0 && start-r.s[n-1].end <= r.gap {
		r.s[n-1].end = max(r.s[n-1].end, end)
		r.s[n-1].value += v
		return
	}
	r.s = append(r.s, span{start, end, v})
}

// Checker scans a stream. Write chunks, then Report.
type Checker struct {
	o        Options
	rate, ch int
	pos      int64 // frames seen
	meter    *loudness.Meter
	clean    []float32

	invalid runs

	clip      runs
	clipStart []int64 // per channel: start of the current run, −1 if none

	// silence, in windows of win frames
	win          int64
	winE         float64
	winN         int64
	silThr       float64
	silStart     int64 // frame where the current silent run began, −1 if sound
	sawSound     bool
	leadSilence  int64 // frames of leading silence (set when sound first appears)
	gaps         []span
	peak         float64
	dcSum        []float64
	clickPrev    [][2]float32 // per channel, last two samples
	clickW       int64        // frames in each side window (5 ms)
	clickSum     [][]float64  // per channel, ring of running sums of |second difference|
	clickD       [][]float64  // per channel, ring of |second difference|
	clicks       runs
	clickHoldoff []int64
}

// NewChecker returns a checker for rate/channels audio.
func NewChecker(rate, channels int, o Options) *Checker {
	o = o.norm()
	c := &Checker{o: o, rate: rate, ch: channels,
		meter:        loudness.NewMeter(rate, channels, loudness.Options{}),
		invalid:      runs{gap: int64(rate / 100)},
		clip:         runs{gap: int64(rate / 20)},
		clicks:       runs{gap: int64(rate / 50)},
		clipStart:    make([]int64, channels),
		win:          max(int64(rate/100), 1),
		silThr:       math.Pow(10, o.SilenceDB/10),
		silStart:     0,
		dcSum:        make([]float64, channels),
		clickPrev:    make([][2]float32, channels),
		clickW:       max(int64(rate/200), 2),
		clickHoldoff: make([]int64, channels),
	}
	ring := 2*c.clickW + 4
	for range channels {
		c.clickSum = append(c.clickSum, make([]float64, ring))
		c.clickD = append(c.clickD, make([]float64, ring))
	}
	for i := range c.clipStart {
		c.clipStart[i] = -1
	}
	return c
}

// Write scans interleaved samples (whole frames).
func (c *Checker) Write(samples []float32) {
	n := len(samples) / c.ch
	c.clean = append(c.clean[:0], samples[:n*c.ch]...)
	clipLvl := float32(c.o.ClipLevel)
	for i := range n {
		f := c.pos + int64(i)
		fr := c.clean[i*c.ch : (i+1)*c.ch]
		bad := false
		var e float64
		for k, v := range fr {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				bad = true
				v, fr[k] = 0, 0
			}
			a := v
			if a < 0 {
				a = -a
			}
			c.peak = max(c.peak, float64(a))
			c.dcSum[k] += float64(v)
			e += float64(v) * float64(v)
			// Clipping runs per channel.
			if a >= clipLvl {
				if c.clipStart[k] < 0 {
					c.clipStart[k] = f
				}
			} else if c.clipStart[k] >= 0 {
				c.endClip(k, f)
			}
			c.click(k, f, v)
		}
		if bad {
			c.invalid.add(f, f+1, 1)
		}
		// Silence windows.
		c.winE += e
		c.winN++
		if c.winN == c.win {
			c.endWindow(f + 1)
		}
	}
	c.pos += int64(n)
	c.meter.Write(c.clean)
}

func (c *Checker) endClip(k int, end int64) {
	if s := c.clipStart[k]; end-s >= int64(c.o.ClipRun) {
		c.clip.add(s, end, float64(end-s))
	}
	c.clipStart[k] = -1
}

// click flags an isolated sharp discontinuity: a second difference over
// 0.05 and over 12× the mean second difference of the 5 ms before it and
// of the 5 ms after it (skipping the two samples the spike itself
// disturbs). Judging against both sides is what keeps speech onsets out:
// a plosive after silence is sharp against the silence before it, but the
// speech after it is just as busy, while a click stands alone. Candidates
// are judged 5 ms late, once the window after them has arrived.
func (c *Checker) click(k int, f int64, v float32) {
	p := &c.clickPrev[k]
	d := math.Abs(float64(v - 2*p[1] + p[0]))
	p[0], p[1] = p[1], v
	if f < 2 {
		d = 0
	}
	sum, ds := c.clickSum[k], c.clickD[k]
	L := int64(len(sum))
	prev := 0.0
	if f > 0 {
		prev = sum[(f-1)%L]
	}
	sum[f%L] = prev + d
	ds[f%L] = d
	w := c.clickW
	m := f - w - 2 // the candidate: its window after is m+3 … f
	if m < w+2 {
		return
	}
	before := (sum[(m-1)%L] - sum[(m-w-1)%L]) / float64(w)
	after := (sum[f%L] - sum[(m+2)%L]) / float64(w)
	dm := ds[m%L]
	if dm > 0.05 && dm > 12*max(before, after) && m >= c.clickHoldoff[k] {
		c.clicks.add(m, m+1, dm)
		c.clickHoldoff[k] = m + int64(c.rate/100)
	}
}

// endWindow closes the silence window ending at frame end.
func (c *Checker) endWindow(end int64) {
	silent := c.winE/float64(c.winN*int64(c.ch)) < c.silThr
	start := end - c.winN
	c.winE, c.winN = 0, 0
	switch {
	case silent && c.silStart < 0:
		c.silStart = start
	case !silent:
		if c.silStart >= 0 {
			if !c.sawSound {
				c.leadSilence = start - c.silStart
			} else {
				c.gaps = append(c.gaps, span{c.silStart, start, 0})
			}
		}
		c.sawSound = true
		c.silStart = -1
	}
}

// Report finishes the scan and returns the findings. Call it once, at the end.
func (c *Checker) Report() Report {
	if c.winN > 0 {
		c.endWindow(c.pos)
	}
	for k := range c.clipStart {
		if c.clipStart[k] >= 0 {
			c.endClip(k, c.pos)
		}
	}
	o := c.o
	sec := func(f int64) float64 { return float64(f) / float64(c.rate) }
	r := Report{Seconds: sec(c.pos), Rate: c.rate, Channels: c.ch, Loudness: c.meter.Stats(), DCOffset: make([]float64, c.ch)}
	counts := map[string]int{}
	add := func(is Issue) {
		if counts[is.Code] >= o.MaxIssues {
			r.Truncated = true
			return
		}
		counts[is.Code]++
		r.Issues = append(r.Issues, is)
	}
	whole := func(code string, sev Severity, v, lim float64) Issue {
		return Issue{Code: code, Severity: sev, Start: 0, End: r.Seconds, Channel: -1, Value: v, Limit: lim}
	}
	if c.pos == 0 {
		add(Issue{Code: "empty", Severity: Error, Channel: -1})
		return r.finish()
	}
	for _, s := range c.invalid.s {
		add(Issue{"invalid_samples", Error, sec(s.start), sec(s.end), -1, s.value, 0})
	}
	if !c.sawSound {
		add(whole("silent", Error, floorDB(dsp.DB(c.peak)), o.SilenceDB))
		return r.finish()
	}
	for _, s := range c.clip.s {
		add(Issue{"clipping", Warning, sec(s.start), sec(s.end), -1, s.value, float64(o.ClipRun)})
	}
	for _, g := range c.gaps {
		if d := sec(g.end - g.start); d > o.MaxSilence {
			add(Issue{"silence_gap", Warning, sec(g.start), sec(g.end), -1, d, o.MaxSilence})
		}
	}
	if d := sec(c.leadSilence); d > o.MaxEdgeSilence {
		add(Issue{"edge_silence", Info, 0, d, -1, d, o.MaxEdgeSilence})
	}
	if c.silStart >= 0 {
		if d := sec(c.pos - c.silStart); d > o.MaxEdgeSilence {
			add(Issue{"edge_silence", Info, sec(c.silStart), r.Seconds, -1, d, o.MaxEdgeSilence})
		}
	}
	for k, s := range c.dcSum {
		m := s / float64(c.pos)
		r.DCOffset[k] = m
		if db := dsp.DB(math.Abs(m)); db > o.DCOffsetDB {
			add(Issue{"dc_offset", Warning, 0, r.Seconds, k, db, o.DCOffsetDB})
		}
	}
	if in := r.Loudness.Integrated; in < o.LowLUFS {
		add(whole("low_level", Warning, in, o.LowLUFS))
	}
	for _, j := range c.jumps() {
		add(j)
	}
	for _, s := range c.clicks.s {
		// Clipped corners are clicks too, but clipping already says so.
		if !slices.ContainsFunc(c.clip.s, func(cl span) bool { return s.start < cl.end+c.clip.gap && s.end > cl.start-c.clip.gap }) {
			add(Issue{"click", Info, sec(s.start), sec(s.end), -1, s.value, 12})
		}
	}
	return r.finish()
}

func floorDB(v float64) float64 { return math.Max(v, loudness.Floor) }

func (r Report) finish() Report {
	slices.SortStableFunc(r.Issues, func(a, b Issue) int {
		switch {
		case a.Start < b.Start:
			return -1
		case a.Start > b.Start:
			return 1
		}
		return 0
	})
	for _, is := range r.Issues {
		switch is.Severity {
		case Error:
			r.Errors++
		case Warning:
			r.Warnings++
		default:
			r.Infos++
		}
	}
	r.OK = r.Errors == 0 && r.Warnings == 0
	if r.Issues == nil {
		r.Issues = []Issue{}
	}
	return r
}

// jumps compares the speech loudness of the JumpWindow before and after
// boundaries every 0.5 s. Only 400 ms blocks that are speech (above −50 LUFS
// and within 20 LU of the integrated loudness) count, and each side needs
// half its blocks to be speech, so pauses and breaths are not jumps.
// Adjacent flagged boundaries merge into one issue holding the largest jump.
func (c *Checker) jumps() []Issue {
	mom := c.meter.MomentarySeries() // block i covers [i, i+4) tenths of a second
	integ := c.meter.Integrated()
	if math.IsInf(integ, -1) || len(mom) == 0 {
		return nil
	}
	gate := math.Max(integ-20, -50)
	w := max(int(math.Round(c.o.JumpWindow*10)), 4) // window in 100 ms steps
	// side returns the power mean of speech blocks lying inside [lo, lo+w).
	side := func(lo int) (float64, bool) {
		var s float64
		var n int
		for i := lo; i <= lo+w-4; i++ {
			if mom[i] > gate {
				s += math.Pow(10, mom[i]/10)
				n++
			}
		}
		if n == 0 || n*2 < w-3 {
			return 0, false
		}
		return 10 * math.Log10(s/float64(n)), true
	}
	var out []Issue
	merging := false
	for b := w; b+w-4 < len(mom); b += 5 {
		a, okA := side(b - w)
		z, okB := side(b)
		d := z - a
		if !okA || !okB || math.Abs(d) < c.o.JumpLU {
			merging = false
			continue
		}
		at := float64(b) / 10
		start, end := at-c.o.JumpWindow, at+c.o.JumpWindow
		if merging {
			last := &out[len(out)-1]
			last.End = end
			if math.Abs(d) > math.Abs(last.Value) {
				last.Value = d
			}
			continue
		}
		out = append(out, Issue{"loudness_jump", Warning, math.Max(start, 0), end, -1, d, c.o.JumpLU})
		merging = true
	}
	return out
}

// Check scans a whole buffer.
func Check(b dsp.Buffer, o Options) Report {
	c := NewChecker(b.Rate, b.Channels, o)
	c.Write(b.Samples)
	return c.Report()
}
