package dub

// How far a yt-dlp download is: Download asks yt-dlp for machine-readable
// lines (progressArgs) and progressParser turns them into one DownloadProgress
// for the whole download, over the separate video and audio streams that
// ffmpeg merges at the end.

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
)

// DownloadPhase is what a download is doing.
type DownloadPhase string

const (
	DownloadFetching  DownloadPhase = "download" // fetching the streams
	DownloadMerging   DownloadPhase = "merge"    // ffmpeg joins video and audio
	DownloadFinishing DownloadPhase = "finish"   // the last steps (captions, moving the file)
)

// DownloadProgress is how far a download is.
type DownloadProgress struct {
	Phase DownloadPhase
	// Fraction of the whole download, 0–1; -1 while not known (the size
	// not known yet, or merging). It never goes back during a download.
	Fraction float64
	// Done and Total bytes of the whole download (Total 0: not known).
	Done, Total int64
	// ETA is the seconds left (0: not known).
	ETA float64
	// Stream is the stream being fetched (1-based) of Streams (0: not known).
	Stream, Streams int
	// Title is the video's title, once yt-dlp said it.
	Title string
}

// The markers of Download's machine-readable lines.
const (
	progInfo = "ASI " // before the download: format ids, approximate size, title
	progLine = "ASP " // each progress report
	progFile = "ASF " // after the download: the file
)

// progressArgs make yt-dlp print what progressParser reads: the progress
// (to stdout, even though --print makes it quiet otherwise) at most about
// four times a second, and before the download the formats chosen (one
// id per stream, joined by "+"), their approximate size and the title.
func progressArgs() []string {
	return []string{
		"--progress", "--newline", "--progress-delta", "0.25",
		"--progress-template", "download:" + progLine + "%(progress.status)s %(progress.downloaded_bytes)s %(progress.total_bytes)s " +
			"%(progress.total_bytes_estimate)s %(progress.speed)s %(progress.eta)s %(progress.filename)s",
		"--print", "before_dl:" + progInfo + "%(format_id)s %(filesize,filesize_approx)s %(title)j",
	}
}

// subtitleExts are the caption files yt-dlp fetches beside the video:
// their progress is not the video's.
var subtitleExts = map[string]bool{".vtt": true, ".srt": true, ".ttml": true, ".srv1": true, ".srv2": true, ".srv3": true,
	".json3": true, ".ass": true, ".lrc": true, ".sbv": true, ".dfxp": true}

// progressParser follows one download's lines. Its zero value is ready.
type progressParser struct {
	p        DownloadProgress
	approx   int64  // the formats' approximate size, from the info line
	cur      string // the stream being fetched
	curDone  int64
	curTotal int64
	finished int64 // bytes of the streams fetched before cur
	curEnded bool  // cur reported "finished"
}

// line reads one line of yt-dlp's output and reports whether it was one
// of progressArgs's (then p is the download's progress).
func (pp *progressParser) line(l string) (DownloadProgress, bool) {
	if rest, ok := strings.CutPrefix(l, progInfo); ok {
		pp.info(rest)
		return pp.out(), true
	}
	rest, ok := strings.CutPrefix(l, progLine)
	if !ok {
		return DownloadProgress{}, false
	}
	f := strings.SplitN(rest, " ", 7)
	if len(f) < 7 {
		return DownloadProgress{}, false
	}
	status, file := f[0], strings.TrimSpace(f[6])
	if subtitleExts[strings.ToLower(filepath.Ext(file))] {
		return pp.out(), true
	}
	if file != pp.cur {
		if pp.cur != "" {
			pp.finished += max(pp.curDone, pp.curTotal*boolInt(pp.curEnded))
		}
		pp.cur, pp.curDone, pp.curTotal, pp.curEnded = file, 0, 0, false
		pp.p.Stream++
		if pp.p.Streams > 0 && pp.p.Stream > pp.p.Streams {
			pp.p.Streams = pp.p.Stream
		}
	}
	done := num(f[1])
	total := num(f[2])
	if total <= 0 {
		total = num(f[3])
	}
	if done > 0 {
		pp.curDone = done
	}
	if total > 0 {
		pp.curTotal = max(total, pp.curDone)
	}
	pp.p.Phase = DownloadFetching
	if status == "finished" {
		pp.curEnded = true
		if pp.curTotal > 0 {
			pp.curDone = pp.curTotal
		} else {
			pp.curTotal = pp.curDone
		}
		if pp.p.Streams == 0 || pp.p.Stream >= pp.p.Streams {
			pp.p.Phase = DownloadFinishing
			if pp.p.Streams > 1 {
				pp.p.Phase = DownloadMerging
			}
		}
	}
	pp.measure(num(f[4]), num(f[5]))
	return pp.out(), true
}

// info reads the line printed before the download.
func (pp *progressParser) info(s string) {
	f := strings.SplitN(s, " ", 3)
	if len(f) > 0 && f[0] != "NA" && f[0] != "" {
		pp.p.Streams = strings.Count(f[0], "+") + 1
	}
	if len(f) > 1 {
		pp.approx = max(num(f[1]), 0)
	}
	if len(f) > 2 {
		var t string
		if json.Unmarshal([]byte(f[2]), &t) == nil {
			pp.p.Title = strings.TrimSpace(t)
		}
	}
}

// measure works out the whole download's fraction, size and time left.
func (pp *progressParser) measure(speed, eta int64) {
	p := &pp.p
	done := pp.finished + pp.curDone
	last := p.Streams <= 1 || p.Stream >= p.Streams
	var total int64
	switch {
	case last && pp.curTotal > 0:
		total = pp.finished + pp.curTotal // exact now
	case pp.approx > 0:
		total = max(pp.approx, pp.finished+pp.curTotal)
	case p.Streams <= 1:
		total = pp.curTotal
	}
	p.Done, p.Total = done, total
	frac := -1.0
	switch {
	case total > 0:
		frac = float64(done) / float64(total)
	case p.Streams > 1 && pp.curTotal > 0:
		// No size for the whole: each stream an equal share.
		frac = (float64(p.Stream-1) + float64(pp.curDone)/float64(pp.curTotal)) / float64(p.Streams)
	}
	if frac >= 0 {
		frac = min(frac, 1)
		p.Fraction = max(frac, p.Fraction) // never back
	}
	p.ETA = 0
	switch {
	case p.Phase != DownloadFetching:
	case speed > 0 && total > done:
		p.ETA = float64(total-done) / float64(speed)
	case last && eta > 0:
		p.ETA = float64(eta)
	}
}

// out is the progress to report: indeterminate while merging, or before
// any size is known.
func (pp *progressParser) out() DownloadProgress {
	p := pp.p
	if p.Phase == "" {
		p.Phase = DownloadFetching
	}
	if p.Phase != DownloadFetching || p.Fraction <= 0 && p.Total == 0 {
		p.Fraction = -1
	}
	return p
}

// num reads one of yt-dlp's numbers ("NA" and the like: -1).
func num(s string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f != f {
		return -1
	}
	return int64(f)
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
