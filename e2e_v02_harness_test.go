package main

// TestV02's harness: the steps' rows, waits, jobs and models, the checks
// on audio and transcripts, ffprobe, and the memory sampler; split from
// e2e_v02_test.go.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

func (v *v02) row(s string) {
	v.mu.Lock()
	v.rows = append(v.rows, s)
	v.mu.Unlock()
}

// wait applies events until done, failing the (sub)test after timeout.
func (v *v02) wait(t *testing.T, what string, timeout time.Duration, done func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for !done() {
		if v.a.engState == engineFailed && !v.restarting {
			t.Fatalf("%s: engine failed: %s\n%s", what, v.a.engError, v.a.engTail)
		}
		select {
		case fn := <-v.events:
			fn()
		case <-tick.C:
		case <-deadline:
			t.Fatalf("timed out waiting for %s (studio job %+v, error %q)", what, v.a.job, v.a.jobError)
		}
	}
}

// drain applies the events already queued.
func (v *v02) drain() {
	for {
		select {
		case fn := <-v.events:
			fn()
		default:
			return
		}
	}
}

// do runs fn on a goroutine (it may need the app, e.g. an HTTP call the
// API answers) and applies events until it returns.
func (v *v02) do(t *testing.T, what string, timeout time.Duration, fn func() error) error {
	t.Helper()
	var err error
	done := make(chan struct{})
	go func() { err = fn(); close(done) }()
	v.wait(t, what, timeout, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	return err
}

// job waits for a scheduler job to end and returns it.
func (v *v02) job(t *testing.T, id string, timeout time.Duration) jobs.Job {
	t.Helper()
	var j jobs.Job
	v.wait(t, "job "+id, timeout, func() bool {
		var err error
		j, err = v.a.lib.sched.Get(context.Background(), id)
		return err == nil && j.State.Terminal()
	})
	return j
}

func (v *v02) mlxPeak() float64 {
	var p float64
	for _, w := range v.a.workers {
		if w.Role == proto.RoleGPU && w.Stats != nil {
			p = float64(w.Stats.Memory.Peak) / (1 << 30)
		}
	}
	return p
}

func (v *v02) model(t *testing.T, id string) catalog.Entry {
	t.Helper()
	e, ok := catalog.Builtin().ByID(id)
	if !ok {
		t.Fatalf("no catalogue entry %s", id)
	}
	if ready, _ := v.a.modelOnDisk(e); !ready {
		t.Fatalf("%s is not downloaded", id)
	}
	return e
}

// generate makes a Studio take and returns it.
func (v *v02) generate(t *testing.T, what string, timeout time.Duration) *core.Take {
	t.Helper()
	a := v.a
	before := a.lastClipID
	a.generate()
	if a.job == nil {
		t.Fatalf("%s did not start: %q (download %v)", what, a.jobError, a.needDownload)
	}
	v.wait(t, what, timeout, func() bool { return a.job == nil && a.awaitTake == "" })
	if a.jobError != "" {
		t.Fatalf("%s: %s", what, a.jobError)
	}
	take := a.lastClip()
	if take == nil || a.lastClipID == before {
		t.Fatalf("%s: no take", what)
	}
	return take
}

// sttCheck transcribes a WAV with the default STT model through the
// voice pages' job (as cloning does) and returns the text.
func (v *v02) sttCheck(t *testing.T, path, lang string) string {
	t.Helper()
	var text string
	var err error
	done := false
	v.a.transcribeClip(path, lang, func(s string, e error) { text, err, done = s, e, true })
	v.wait(t, "stt "+filepath.Base(path), 10*time.Minute, func() bool { return done })
	if err != nil {
		t.Errorf("stt %s: %v", path, err)
	}
	return text
}

// say makes a WAV of macOS speech (rate Hz, mono 16-bit).
func say(t *testing.T, dir, name, voice, text string, rate int) string {
	t.Helper()
	aiff := filepath.Join(dir, name+".aiff")
	out := filepath.Join(dir, name+".wav")
	for _, c := range [][]string{{"say", "-v", voice, "-o", aiff, text}, {"afconvert", "-f", "WAVE", "-d", "LEI16@" + strconv.Itoa(rate), "-c", "1", aiff, out}} {
		if b, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", c, err, b)
		}
	}
	os.Remove(aiff)
	return out
}

// wavBits reads a WAV's bits per sample from its fmt chunk.
func wavBits(path string) int {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 44 {
		return 0
	}
	for i := 12; i+8 < len(b); {
		id, n := string(b[i:i+4]), int(uint32(b[i+4])|uint32(b[i+5])<<8|uint32(b[i+6])<<16|uint32(b[i+7])<<24)
		if id == "fmt " && i+24 <= len(b) {
			return int(uint16(b[i+22]) | uint16(b[i+23])<<8)
		}
		i += 8 + n + n%2
	}
	return 0
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '\'')
	})
}

// overlap is the share of want's words found in got.
func overlap(want, got string) float64 {
	w, g := words(want), words(got)
	if len(w) == 0 {
		return 0
	}
	n := 0
	for _, x := range w {
		if slices.Contains(g, x) {
			n++
		}
	}
	return float64(n) / float64(len(w))
}

var hanRE = regexp.MustCompile(`\p{Han}`)

// ffprobeJSON runs ffprobe with args and decodes its JSON.
func ffprobeJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	fp := lookTool("ffprobe")
	if fp == "" {
		t.Log("no ffprobe: not checked")
		return nil
	}
	out, err := exec.Command(fp, append([]string{"-v", "error", "-of", "json"}, args...)...).Output()
	if err != nil {
		t.Errorf("ffprobe %v: %v", args, err)
		return nil
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	return m
}

// --- memory -------------------------------------------------------------------------

// memSampler polls the resident memory of the engine workers and of this
// process every half second; peak is the highest sum since reset, in GB.
type memSampler struct {
	mu   sync.Mutex
	max  int64
	quit chan struct{}
}

func newMemSampler() *memSampler {
	m := &memSampler{quit: make(chan struct{})}
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-m.quit:
				return
			case <-tick.C:
				m.sample()
			}
		}
	}()
	return m
}

func (m *memSampler) sample() {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=,comm=").Output()
	if err != nil {
		return
	}
	self := os.Getpid()
	var sum int64
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		kb, _ := strconv.ParseInt(f[2], 10, 64)
		// This process and its engine workers (not an AudioSmith the
		// user runs at the same time).
		if pid == self || ppid == self && strings.HasSuffix(f[len(f)-1], "audiosmith-engine") {
			sum += kb << 10
		}
	}
	m.mu.Lock()
	m.max = max(m.max, sum)
	m.mu.Unlock()
}

func (m *memSampler) reset() { m.mu.Lock(); m.max = 0; m.mu.Unlock() }
func (m *memSampler) peak() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return float64(m.max) / (1 << 30)
}
func (m *memSampler) stop() { close(m.quit) }
