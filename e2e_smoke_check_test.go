package main

// TestModelSmoke's checks and report: pitch, order, NaN, Chinese, level,
// similarity, memory, and the report; split from e2e_smoke_test.go.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// medianF0 estimates the median pitch of the voiced 40 ms frames
// (normalised autocorrelation, 60–400 Hz), 0 when nothing is voiced.
func medianF0(x []float64, rate int) float64 {
	frame, hop := rate*40/1000, rate*10/1000
	minLag, maxLag := rate/400, rate/60
	var f0s []float64
	for start := 0; start+frame+maxLag < len(x); start += hop {
		w := x[start : start+frame]
		if dbfs(w) < -40 {
			continue
		}
		best, bestLag := 0.0, 0
		for lag := minLag; lag <= maxLag; lag++ {
			var num, e1, e2 float64
			for i := range frame {
				a, b := x[start+i], x[start+i+lag]
				num += a * b
				e1 += a * a
				e2 += b * b
			}
			if r := num / math.Sqrt(e1*e2+1e-12); r > best {
				best, bestLag = r, lag
			}
		}
		if best > 0.6 && bestLag > 0 {
			f0s = append(f0s, float64(rate)/float64(bestLag))
		}
	}
	if len(f0s) == 0 {
		return 0
	}
	slices.Sort(f0s)
	return f0s[len(f0s)/2]
}

func ordered(ws []proto.Word) bool {
	for i, w := range ws {
		if w.End < w.Start || i > 0 && w.Start < ws[i-1].Start {
			return false
		}
	}
	return len(ws) > 0
}

func hasNaN(s []float64) bool { return slices.ContainsFunc(s, math.IsNaN) }

func hasHan(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

func dbfs(s []float64) float64 {
	if len(s) == 0 {
		return -120
	}
	var sum float64
	for _, v := range s {
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(len(s)))
	if rms < 1e-6 {
		return -120
	}
	return 20 * math.Log10(rms)
}

// similarity compares texts as words (or characters for Chinese): the
// longest common subsequence over the longer length.
func similarity(want, got string, chars bool) float64 {
	norm := func(s string) []string {
		s = strings.ToLower(s)
		var out []string
		if chars {
			for _, r := range s {
				if unicode.IsLetter(r) || unicode.IsNumber(r) {
					out = append(out, string(r))
				}
			}
			return out
		}
		for _, w := range strings.Fields(s) {
			if w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }); w != "" {
				out = append(out, w)
			}
		}
		return out
	}
	a, b := norm(want), norm(got)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	return float64(dp[len(a)][len(b)]) / float64(max(len(a), len(b)))
}

// watchRSS samples a process's resident size every 250 ms until stop,
// which returns the largest, in bytes.
func watchRSS(pid int) (stop func() int64) {
	done := make(chan struct{})
	result := make(chan int64, 1)
	go func() {
		var peak int64
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			if out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output(); err == nil {
				if kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
					peak = max(peak, kb<<10)
				}
			}
			select {
			case <-done:
				result <- peak
				return
			case <-tick.C:
			}
		}
	}()
	var got int64 = -1
	return func() int64 {
		if got < 0 {
			close(done)
			got = <-result
		}
		return got
	}
}

func writeSmokeReport(t *testing.T, path string, rows []smokeRow) {
	if len(rows) == 0 {
		return
	}
	data, _ := json.MarshalIndent(rows, "", "  ")
	os.WriteFile(strings.TrimSuffix(path, filepath.Ext(path))+".json", data, 0o644)
	var b strings.Builder
	fmt.Fprintf(&b, "# Model smoke %s\n\n| Model | Case | Result | Audio s | Gen s | RTF | MLX peak GB | RSS GB | Heard / note |\n|---|---|---|---|---|---|---|---|---|\n",
		time.Now().Format("2006-01-02 15:04"))
	for _, r := range rows {
		res := "pass"
		if !r.Pass {
			res = "**FAIL**"
		}
		note := r.Note
		if r.Heard != "" {
			note = fmt.Sprintf("%q (%.2f); %s", r.Heard, r.Score, r.Note)
		}
		if r.LoadSeconds > 0 {
			note = fmt.Sprintf("load %.1f s; %s", r.LoadSeconds, note)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %.2f | %.2f | %.2f | %.2f | %.2f | %s |\n", r.Model, r.Case, res, r.AudioSeconds, r.Seconds, r.RTF, r.PeakGB, r.RSSGB,
			strings.ReplaceAll(note, "|", "/"))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Log(err)
	} else {
		t.Logf("report: %s", path)
	}
}
