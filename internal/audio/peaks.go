package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/egoist/mygo/audio"
)

// clip is one generated take: its audio file and the settings that made it.
type clip struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	File      string    `json:"file"` // name in the clips directory

	Text        string  `json:"text"`
	Model       string  `json:"model"`
	Voice       string  `json:"voice,omitempty"`
	Language    string  `json:"language,omitempty"`
	Speed       float64 `json:"speed,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"topP,omitempty"`
	MaxTokens   int     `json:"maxTokens,omitempty"`
	RefAudio    string  `json:"refAudio,omitempty"`
	RefText     string  `json:"refText,omitempty"`

	SampleRate   int     `json:"sampleRate"`
	AudioSeconds float64 `json:"audioSeconds"`
	GenSeconds   float64 `json:"genSeconds"`

	peaks []float32 // waveform, loaded on demand
}

func (c *clip) RTF() float64 {
	if c.GenSeconds <= 0 {
		return 0
	}
	return c.AudioSeconds / c.GenSeconds
}

// library stores clips under dir: dir/clips/*.wav and dir/history.json.
type library struct {
	dir   string
	clips []*clip // newest first
}

func (l *library) clipsDir() string    { return filepath.Join(l.dir, "clips") }
func (l *library) indexPath() string   { return filepath.Join(l.dir, "history.json") }
func (l *library) path(c *clip) string { return filepath.Join(l.clipsDir(), c.File) }

func loadLibrary(dir string) (*library, error) {
	l := &library{dir: dir}
	if err := os.MkdirAll(l.clipsDir(), 0o755); err != nil {
		return l, err
	}
	data, err := os.ReadFile(l.indexPath())
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	if err := json.Unmarshal(data, &l.clips); err != nil {
		return l, err
	}
	// Drop entries whose audio is gone.
	kept := l.clips[:0]
	for _, c := range l.clips {
		if _, err := os.Stat(l.path(c)); err == nil {
			kept = append(kept, c)
		}
	}
	l.clips = kept
	sort.SliceStable(l.clips, func(i, j int) bool { return l.clips[i].CreatedAt.After(l.clips[j].CreatedAt) })
	return l, nil
}

func (l *library) save() error {
	data, err := json.MarshalIndent(l.clips, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.indexPath())
}

func (l *library) add(c *clip) error {
	l.clips = append([]*clip{c}, l.clips...)
	return l.save()
}

func (l *library) remove(id string) error {
	for i, c := range l.clips {
		if c.ID == id {
			os.Remove(l.path(c))
			l.clips = append(l.clips[:i:i], l.clips[i+1:]...)
			return l.save()
		}
	}
	return nil
}

func (l *library) find(id string) *clip {
	for _, c := range l.clips {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// newClipID is sortable and doubles as the file name.
func newClipID(now time.Time) string {
	return now.Format("20060102-150405.000")
}

// wavePeaks reads an audio file and returns n peak magnitudes (0–1),
// one per equal slice of the file, for drawing its waveform.
func wavePeaks(path string, n int) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d, _, err := audio.NewDecoder(f)
	if err != nil {
		return nil, err
	}
	ch := max(d.Channels(), 1)
	var samples []float32
	buf := make([]float32, 8192*ch)
	for {
		k, err := d.Read(buf)
		for i := 0; i+ch <= k; i += ch {
			var m float32
			for j := range ch {
				m = max(m, float32(math.Abs(float64(buf[i+j]))))
			}
			samples = append(samples, m)
		}
		if err != nil || k == 0 {
			break
		}
	}
	return peaksOf(samples, n), nil
}

func peaksOf(samples []float32, n int) []float32 {
	if n <= 0 || len(samples) == 0 {
		return nil
	}
	out := make([]float32, n)
	var top float32
	for i := range n {
		lo := i * len(samples) / n
		hi := max((i+1)*len(samples)/n, lo+1)
		var m float32
		for _, s := range samples[lo:min(hi, len(samples))] {
			m = max(m, s)
		}
		out[i] = m
		top = max(top, m)
	}
	if top > 0 {
		for i := range out {
			out[i] /= top
		}
	}
	return out
}
