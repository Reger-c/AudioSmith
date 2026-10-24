package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// TestDubEngine checks the M4 engine methods on a Mac on a 30 s clip of
// two speakers (macOS voices) over synthetic music:
//   - separate.run (HTDemucs; Mel-RoFormer was removed in thread #46):
//     the vocals stem must be much closer to the speech than the mix is
//     (scale-invariant SDR), the background close to the music;
//   - diarize.run (Sortformer): two speakers, turns matching who spoke;
//   - media.mux: an MP4 of the clip's video (copied) with the original
//     audio, two dubbed tracks and two SRT subtitle tracks, read back by
//     ffprobe and by AVFoundation (languages, titles, default/enabled);
//   - media.thumbnail; media.transcode of an HEVC source (and a VP9 WebM,
//     which must be unsupported_media).
//
// It needs ffmpeg (Homebrew) and the network for the models, which it
// deletes afterwards unless they were there (AUDIOSMITH_SMOKE_KEEP keeps them).
//
//	AUDIOSMITH_E2E=1 go test -run TestDubEngine -v -timeout 40m .
func TestDubEngine(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := engine.Find("../resources")
	ff := lookTool("ffmpeg")
	fp := lookTool("ffprobe")
	if bin == "" || ff == "" || fp == "" {
		t.Fatalf("needs the engine (%q), ffmpeg (%q) and ffprobe (%q)", bin, ff, fp)
	}
	ctx := context.Background()
	dir := t.TempDir()
	if d := os.Getenv("AUDIOSMITH_DUB_DIR"); d != "" {
		dir = d
		os.MkdirAll(dir, 0o755)
	}
	sh := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}

	// The clip: two macOS voices taking turns, and music.
	const rate = 44100
	lines := []struct{ voice, text string }{
		{"Samantha", "Good morning, and welcome back to the show. Today we are talking about trains."},
		{"Daniel", "Thanks for having me. I have loved trains since I was a little boy."},
		{"Samantha", "What is the longest journey you have ever taken?"},
		{"Daniel", "Probably the night train from London to the far north of Scotland."},
		{"Samantha", "That sounds wonderful. Did you sleep well on the way, or did you stay up watching the hills?"},
		{"Daniel", "I stayed up most of the night. The hills in the moonlight were too beautiful to miss, and the tea was good."},
	}
	var speech []float64
	type turn struct {
		start, end float64
		who        int
	}
	var truth []turn
	speech = append(speech, make([]float64, rate/2)...)
	for i, l := range lines {
		aiff := filepath.Join(dir, fmt.Sprintf("line%d.aiff", i))
		wav := filepath.Join(dir, fmt.Sprintf("line%d.wav", i))
		sh("say", "-v", l.voice, "-o", aiff, l.text)
		sh("afconvert", "-f", "WAVE", "-d", "LEI16@44100", "-c", "1", aiff, wav)
		a, err := enginetest.ReadWAV(wav)
		if err != nil {
			t.Fatal(err)
		}
		start := float64(len(speech)) / rate
		speech = append(speech, a.Samples...)
		truth = append(truth, turn{start, float64(len(speech)) / rate, i % 2})
		speech = append(speech, make([]float64, rate*7/10)...)
	}
	seconds := float64(len(speech)) / rate
	music := synthMusic(len(speech), rate)
	stereo := func(m []float64, gain float64) []float64 {
		out := make([]float64, 2*len(m))
		for i, v := range m {
			out[2*i], out[2*i+1] = v*gain, v*gain
		}
		return out
	}
	speechS, musicS := stereo(speech, 0.9), music
	mix := make([]float64, len(speechS))
	for i := range mix {
		mix[i] = speechS[i] + musicS[i]
	}
	write := func(name string, s []float64) string {
		p := filepath.Join(dir, name)
		if err := enginetest.WriteWAV(p, enginetest.Audio{Rate: rate, Channels: 2, Samples: s}, 16); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("speech.wav", speechS)
	write("music.wav", musicS)
	mixPath := write("mix.wav", mix)
	t.Logf("clip: %.1f s, %d turns, speech %.1f dBFS, music %.1f dBFS", seconds, len(truth), dbfs(speechS), dbfs(musicS))

	// The engine and the models.
	client := hub.New(hub.Options{CacheDir: hub.CacheDir()})
	keep := os.Getenv("AUDIOSMITH_SMOKE_KEEP") != ""
	ensure := func(id string) (catalog.Entry, string) {
		t.Helper()
		e, ok := catalog.Builtin().ByID(id)
		if !ok {
			t.Fatalf("no entry %s", id)
		}
		l, err := client.Status(ctx, e)
		if err == nil && l.State != hub.Downloaded {
			if err = client.Download(ctx, e, nil); err == nil && !keep {
				t.Cleanup(func() { client.Delete(context.Background(), e) })
			}
			l, _ = client.Status(ctx, e)
		}
		if err != nil || l.State != hub.Downloaded {
			t.Fatalf("%s: %v %+v", id, err, l)
		}
		return e, l.Snapshot
	}
	sup := engine.NewSupervisor(engine.Config{Bin: bin, Bus: core.NewBus(), OnDemand: []proto.Role{proto.RoleMedia}, LogDir: dir, Offline: true,
		CacheDir: hub.CacheDir(), StatsEvery: -1, Logf: t.Logf})
	sup.Start(ctx)
	defer sup.Stop()
	run := func(method string, params, out any) (time.Duration, error) {
		raw, _ := json.Marshal(params)
		start := time.Now()
		res, err := engine.KindExecutor(sup, method).Run(ctx, jobs.Job{ID: core.NewID(time.Now()), Spec: jobs.Spec{Params: raw}}, func(jobs.Progress) {}, nil)
		if err == nil && out != nil {
			err = json.Unmarshal(res, out)
		}
		return time.Since(start), err
	}

	// Separation.
	speechMono := speech
	musicMono := make([]float64, len(speech))
	for i := range musicMono {
		musicMono[i] = musicS[2*i]
	}
	mixMono := make([]float64, len(speech))
	for i := range mixMono {
		mixMono[i] = mix[2*i]
	}
	base := siSDR(speechMono, mixMono)
	t.Logf("mix vs speech: %.1f dB; mix vs music %.1f dB", base, siSDR(musicMono, mixMono))
	sepModels := []string{catalog.DefaultSeparate}
	var vocalsPath string
	for _, id := range sepModels {
		e, snap := ensure(id)
		v, b := filepath.Join(dir, id+"-vocals.wav"), filepath.Join(dir, id+"-background.wav")
		var sr proto.SeparateResult
		took, err := run(proto.MethodSeparateRun, proto.SeparateParams{Model: e.Repo, ModelPath: snap, Audio: mixPath,
			Vocals: proto.Output{Path: v}, Background: proto.Output{Path: b}}, &sr)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		va, _ := enginetest.ReadWAV(v)
		ba, _ := enginetest.ReadWAV(b)
		vs, bs := siSDR(speechMono, mono(va)), siSDR(musicMono, mono(ba))
		t.Logf("%s: %.1f s in %.1f s (%.1f× real time), %d frames (input %d): vocals vs speech %.1f dB, background vs music %.1f dB",
			id, sr.AudioSeconds, took.Seconds(), sr.AudioSeconds/took.Seconds(), sr.Frames, len(speech), vs, bs)
		if math.Abs(float64(sr.Frames-int64(len(speech)))) > 1 || vs < 8 || vs < base+6 || bs < 3 {
			t.Errorf("%s: separation too weak or misaligned (frames %d vs %d)", id, sr.Frames, len(speech))
		}
		if id == catalog.DefaultSeparate {
			vocalsPath = v
		}
		run(proto.MethodModelsUnload, proto.UnloadParams{}, nil)
	}

	// Diarization of the vocals.
	if vocalsPath == "" {
		vocalsPath = write("speech-only.wav", speechS)
	}
	e, snap := ensure(catalog.DefaultDiarize)
	var dr proto.DiarizeResult
	took, err := run(proto.MethodDiarizeRun, proto.DiarizeParams{Model: e.Repo, ModelPath: snap, Audio: vocalsPath}, &dr)
	if err != nil {
		t.Fatalf("diarize: %v", err)
	}
	t.Logf("diarize: %d speakers in %.1f s: %+v", dr.Speakers, took.Seconds(), dr.Turns)
	// Map labels to the truth by overlap, then score the time agreed.
	overlap := func(a0, a1, b0, b1 float64) float64 { return math.Max(0, math.Min(a1, b1)-math.Max(a0, b0)) }
	votes := map[string][2]float64{}
	for _, tr := range dr.Turns {
		for _, g := range truth {
			v := votes[tr.Speaker]
			v[g.who] += overlap(tr.Start, tr.End, g.start, g.end)
			votes[tr.Speaker] = v
		}
	}
	var agree, total float64
	for _, g := range truth {
		total += g.end - g.start
		for _, tr := range dr.Turns {
			v := votes[tr.Speaker]
			who := 0
			if v[1] > v[0] {
				who = 1
			}
			if who == g.who {
				agree += overlap(tr.Start, tr.End, g.start, g.end)
			}
		}
	}
	t.Logf("diarize: %.0f%% of the speech labelled with the right speaker", 100*agree/total)
	if dr.Speakers != 2 || agree/total < 0.8 {
		t.Errorf("diarize: %d speakers, %.2f agreement", dr.Speakers, agree/total)
	}
	var unsup *jobs.Error
	if _, err := run(proto.MethodDiarizeRun, proto.DiarizeParams{Model: e.Repo, ModelPath: snap, Audio: vocalsPath, Speakers: 6}, &dr); !errors.As(err, &unsup) || unsup.Code != proto.CodeUnsupported {
		t.Errorf("6 speakers: %v", err)
	}

	// Video: the clip as an H.264 MOV with its mix as AAC.
	mov := filepath.Join(dir, "clip.mov")
	sh(ff, "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=640x360:rate=25:duration=%.2f", seconds), "-i", mixPath,
		"-c:v", "h264_videotoolbox", "-b:v", "1M", "-c:a", "aac", "-shortest", mov)
	dubs := map[string]string{"es": "Monica", "fr": "Thomas"}
	tracks := []proto.MuxTrack{}
	for _, lang := range []string{"es", "fr"} {
		aiff := filepath.Join(dir, lang+".aiff")
		text := map[string]string{"es": "Buenos días y bienvenidos de nuevo al programa. Hoy hablamos de trenes.", "fr": "Bonjour et bienvenue dans l'émission. Aujourd'hui, nous parlons de trains."}[lang]
		sh("say", "-v", dubs[lang], "-o", aiff, text)
		tracks = append(tracks, proto.MuxTrack{Path: aiff, Language: lang, Title: map[string]string{"es": "Español", "fr": "Français"}[lang], Default: lang == "es"})
	}
	srt := func(name, a, b string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("1\n00:00:00,500 --> 00:00:04,000\n"+a+"\n\n2\n00:00:05,000 --> 00:00:09,000\n<i>"+b+"</i>\n"), 0o644)
		return p
	}
	subs := []proto.MuxSubtitle{
		{Path: srt("es.srt", "Buenos días.", "Hablamos de trenes."), Language: "es", Title: "Español", Default: true},
		{Path: srt("en.srt", "Good morning.", "We talk about trains."), Language: "en", Title: "English"},
	}
	outMP4 := filepath.Join(dir, "dubbed.mp4")
	var mr proto.MuxResult
	took, err = run(proto.MethodMediaMux, proto.MuxParams{Video: mov, KeepAudio: true, Audio: tracks, Subtitles: subs, Output: proto.Output{Path: outMP4, Format: "mp4"}}, &mr)
	if err != nil {
		t.Fatalf("mux: %v", err)
	}
	t.Logf("mux: %+v in %.1f s", mr, took.Seconds())
	probe := ffprobeStreams(t, fp, outMP4)
	var kinds []string
	for _, s := range probe {
		kinds = append(kinds, fmt.Sprintf("%s:%s lang=%s title=%q default=%d", s.Type, s.Codec, s.Tags["language"], s.Tags["title"]+s.Tags["handler_name"], s.Disposition["default"]))
	}
	t.Logf("ffprobe: %s", strings.Join(kinds, "; "))
	count := func(typ string) int {
		n := 0
		for _, s := range probe {
			if s.Type == typ {
				n++
			}
		}
		return n
	}
	if mr.Tracks != 3 || count("video") != 1 || count("audio") != 3 || count("subtitle") != 2 {
		t.Errorf("mux streams: %v", kinds)
	}
	for _, s := range probe {
		if s.Type == "video" && s.Codec != "h264" {
			t.Errorf("video re-encoded? %s", s.Codec)
		}
		if s.Type == "audio" && s.Tags["language"] == "spa" && s.Disposition["default"] != 1 {
			t.Errorf("Spanish audio should be the default: %+v", s)
		}
		if s.Type == "audio" && s.Tags["language"] == "fra" && s.Disposition["default"] != 0 {
			t.Errorf("French audio should not be the default: %+v", s)
		}
	}
	if !slices.ContainsFunc(probe, func(s ffStream) bool { return s.Type == "subtitle" && s.Tags["language"] == "spa" }) {
		t.Errorf("no Spanish subtitles: %v", kinds)
	}
	// As AVFoundation (QuickTime, the app's player) reads it.
	av := avTracks(t, dir, outMP4)
	t.Logf("AVFoundation: %s", av)
	if !strings.Contains(av, "soun es-ES") && !strings.Contains(av, "soun es ") {
		t.Errorf("AVFoundation does not see the Spanish track: %s", av)
	}

	// MOV keeps track titles (QuickTime user data; MP4 has no place AVFoundation writes them).
	outMOV := filepath.Join(dir, "dubbed.mov")
	if _, err := run(proto.MethodMediaMux, proto.MuxParams{Video: mov, KeepAudio: true, Audio: tracks, Subtitles: subs, Output: proto.Output{Path: outMOV, Format: "mov"}}, &mr); err != nil {
		t.Fatalf("mux mov: %v", err)
	}
	avm := avTracks(t, dir, outMOV)
	t.Logf("AVFoundation (MOV): %s", avm)
	if !strings.Contains(avm, "name=Español") || !strings.Contains(avm, "name=Français") {
		t.Errorf("MOV track titles: %s", avm)
	}

	// Thumbnails.
	poster := filepath.Join(dir, "poster.jpg")
	var th proto.ThumbnailResult
	if _, err := run(proto.MethodMediaThumbnail, proto.ThumbnailParams{Input: mov, Times: []float64{1, 1000}, Outputs: []string{poster, filepath.Join(dir, "last.png")}, Width: 320}, &th); err != nil {
		t.Fatalf("thumbnail: %v", err)
	}
	t.Logf("thumbnail: %+v", th.Frames)
	if len(th.Frames) != 2 || th.Frames[0].Width != 320 || th.Frames[0].Height != 180 || th.Frames[1].Time > seconds {
		t.Errorf("thumbnail %+v", th.Frames)
	}

	// Transcode an HEVC source; a VP9 WebM is unsupported.
	hevc := filepath.Join(dir, "clip-hevc.mov")
	sh(ff, "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=1920x1080:rate=25:duration=3", "-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-c:v", "hevc_videotoolbox", "-tag:v", "hvc1", "-b:v", "4M", "-c:a", "aac", hevc)
	var tc proto.TranscodeResult
	took, err = run(proto.MethodMediaTranscode, proto.TranscodeParams{Input: hevc, Output: proto.Output{Path: filepath.Join(dir, "preview.mp4"), Format: "mp4"}, MaxHeight: 720}, &tc)
	if err != nil {
		t.Fatalf("transcode: %v", err)
	}
	t.Logf("transcode: %+v in %.1f s", tc, took.Seconds())
	tp := ffprobeStreams(t, fp, tc.Output)
	if tc.Height != 720 || tc.Width != 1280 || !slices.ContainsFunc(tp, func(s ffStream) bool { return s.Type == "video" && s.Codec == "h264" }) {
		t.Errorf("transcode: %+v %+v", tc, tp)
	}
	webm := filepath.Join(dir, "clip.webm")
	if out, err := exec.Command(ff, "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=1", "-c:v", "libvpx-vp9", webm).CombinedOutput(); err == nil {
		var je *jobs.Error
		if _, err := run(proto.MethodMediaTranscode, proto.TranscodeParams{Input: webm, Output: proto.Output{Path: filepath.Join(dir, "w.mp4")}}, &tc); !errors.As(err, &je) || je.Code != proto.CodeUnsupportedMedia {
			t.Errorf("webm: %v", err)
		}
	} else {
		t.Logf("no VP9 encoder: %s", out)
	}
}

func lookTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if p := "/opt/homebrew/bin/" + name; fileExistsPath(p) {
		return p
	}
	return ""
}

func fileExistsPath(p string) bool { _, err := os.Stat(p); return err == nil }

func mono(a enginetest.Audio) []float64 { return a.Mono() }

// siSDR is the scale-invariant SDR of est against ref, in dB.
func siSDR(ref, est []float64) float64 {
	n := min(len(ref), len(est))
	var dot, rr float64
	for i := range n {
		dot += ref[i] * est[i]
		rr += ref[i] * ref[i]
	}
	alpha := dot / math.Max(rr, 1e-12)
	var sig, noise float64
	for i := range n {
		t := alpha * ref[i]
		sig += t * t
		d := est[i] - t
		noise += d * d
	}
	return 10 * math.Log10(sig/math.Max(noise, 1e-12))
}

// synthMusic is stereo "music": a chord progression with vibrato, a bass
// line and noise-burst drums at 120 bpm, about −18 dBFS.
func synthMusic(frames, rate int) []float64 {
	r := rand.New(rand.NewPCG(3, 4))
	chords := [][]float64{{261.6, 329.6, 392.0}, {220.0, 261.6, 329.6}, {174.6, 220.0, 261.6}, {196.0, 246.9, 293.7}}
	out := make([]float64, 2*frames)
	beat := rate / 2
	for i := range frames {
		t := float64(i) / float64(rate)
		ch := chords[(i/(rate*2))%len(chords)]
		var v float64
		for k, f := range ch {
			v += 0.05 * math.Sin(2*math.Pi*f*t+0.3*math.Sin(2*math.Pi*5*t)) * (1 + 0.2*float64(k))
		}
		v += 0.08 * math.Sin(2*math.Pi*ch[0]/2*t)
		if p := i % beat; p < rate/20 {
			v += 0.12 * r.NormFloat64() * math.Exp(-float64(p)/float64(rate/200))
		}
		out[2*i] = v * (0.9 + 0.1*math.Sin(t))
		out[2*i+1] = v * (0.9 - 0.1*math.Sin(t))
	}
	return out
}

type ffStream struct {
	Type        string            `json:"codec_type"`
	Codec       string            `json:"codec_name"`
	Tags        map[string]string `json:"tags"`
	Disposition map[string]int    `json:"disposition"`
}

func ffprobeStreams(t *testing.T, fp, path string) []ffStream {
	t.Helper()
	out, err := exec.Command(fp, "-v", "error", "-show_streams", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var v struct {
		Streams []ffStream `json:"streams"`
	}
	json.Unmarshal(out, &v)
	return v.Streams
}

// avTracks lists the tracks of path as AVFoundation reads them: media
// type, extended language tag (or language code), enabled, name.
func avTracks(t *testing.T, dir, path string) string {
	t.Helper()
	src := filepath.Join(dir, "avtracks.swift")
	os.WriteFile(src, []byte(`import AVFoundation
let a = AVURLAsset(url: URL(fileURLWithPath: CommandLine.arguments[1]))
for tr in try await a.load(.tracks) {
    let lang = (try? await tr.load(.extendedLanguageTag)) ?? nil
    let code = (try? await tr.load(.languageCode)) ?? nil
    let enabled = (try? await tr.load(.isEnabled)) ?? false
    var name = ""
    for item in (try? await tr.load(.metadata)) ?? [] where item.identifier == .quickTimeUserDataTrackName {
        name = ((try? await item.load(.stringValue)) ?? nil) ?? ""
    }
    print("\(tr.mediaType.rawValue) \(lang ?? code ?? "und") enabled=\(enabled) name=\(name)")
}
`), 0o644)
	exe := filepath.Join(dir, "avtracks")
	if out, err := exec.Command("xcrun", "swiftc", "-O", src, "-o", exe).CombinedOutput(); err != nil {
		t.Fatalf("swiftc: %v %s", err, out)
	}
	out, err := exec.Command(exe, path).CombinedOutput()
	if err != nil {
		t.Fatalf("avtracks: %v %s", err, out)
	}
	return strings.ReplaceAll(strings.TrimSpace(string(out)), "\n", "; ")
}
