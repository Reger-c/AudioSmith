package subtitle

import (
	"io"
	"testing"
	"time"
)

func FuzzParse(f *testing.F) {
	f.Add("1\n00:00:01,000 --> 00:00:02,000\nHi\n")
	f.Add("WEBVTT\n\n00:01.000 --> 00:02.000\n<v A>Hi <00:01.500>there\n")
	f.Fuzz(func(t *testing.T, s string) {
		_, cues, _ := Parse(s)
		WriteSRT(io.Discard, cues)
		WriteVTT(io.Discard, cues, VTTOptions{Speakers: true, Karaoke: true, IDs: true})
		WriteASS(io.Discard, cues, ASSOptions{Karaoke: true})
	})
}

func FuzzSplit(f *testing.F) {
	f.Add("Hello there. This is a test, of the splitter.", int64(5000), 20)
	f.Add("你好，世界。今天天气很好。", int64(3000), 8)
	f.Fuzz(func(t *testing.T, s string, durMs int64, width int) {
		if durMs < 0 || durMs > 1e7 {
			return
		}
		cues, _ := Split([]Segment{{Start: 0, End: time.Duration(durMs) * time.Millisecond, Text: s}},
			SegmentOptions{MaxCharsPerLine: width%60 + 1})
		for _, c := range cues {
			if c.End < c.Start {
				t.Fatalf("cue ends before it starts: %+v", c)
			}
		}
	})
}
