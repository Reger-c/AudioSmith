package ffmpeg

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // thumbnails' sizes
	_ "image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ---- probe -----------------------------------------------------------------

// Probe describes a media file (ffprobe).
type Probe struct {
	Format   string  // container names, "matroska,webm"
	Duration float64 // seconds
	Audio    []Stream
	Video    []Stream
}

// Stream is one audio or video stream.
type Stream struct {
	Codec      string
	SampleRate int
	Channels   int
	Bits       int // PCM bit depth (0 for compressed audio)
	BitRate    int
	Width      int
	Height     int
	FPS        float64
}

// Probe asks ffprobe about path.
func (t *Tool) Probe(ctx context.Context, path string) (Probe, error) {
	if t == nil || t.FFmpeg == "" {
		return Probe{}, ErrNotFound
	}
	if t.FFprobe == "" {
		return Probe{}, ErrNoProbe
	}
	out, err := output(ctx, t.FFprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return Probe{}, err
	}
	return parseProbe([]byte(out))
}

func parseProbe(data []byte) (Probe, error) {
	var raw struct {
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type       string `json:"codec_type"`
			Codec      string `json:"codec_name"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
			Bits       string `json:"bits_per_raw_sample"`
			BitsSample int    `json:"bits_per_sample"`
			BitRate    string `json:"bit_rate"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			Rate       string `json:"avg_frame_rate"`
			Duration   string `json:"duration"`
			Default    struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Probe{}, fmt.Errorf("ffprobe: %w", err)
	}
	p := Probe{Format: raw.Format.Name}
	p.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	for _, s := range raw.Streams {
		st := Stream{Codec: s.Codec, Channels: s.Channels, Width: s.Width, Height: s.Height}
		st.SampleRate, _ = strconv.Atoi(s.SampleRate)
		st.BitRate, _ = strconv.Atoi(s.BitRate)
		if strings.HasPrefix(s.Codec, "pcm_") {
			st.Bits, _ = strconv.Atoi(s.Bits)
			if st.Bits == 0 {
				st.Bits = s.BitsSample
			}
		}
		if n, d, ok := strings.Cut(s.Rate, "/"); ok {
			nf, _ := strconv.ParseFloat(n, 64)
			df, _ := strconv.ParseFloat(d, 64)
			if df > 0 {
				st.FPS = nf / df
			}
		}
		if p.Duration == 0 {
			p.Duration, _ = strconv.ParseFloat(s.Duration, 64)
		}
		switch s.Type {
		case "audio":
			p.Audio = append(p.Audio, st)
		case "video":
			if s.Default.AttachedPic == 0 { // cover art is not video
				p.Video = append(p.Video, st)
			}
		}
	}
	return p, nil
}

// ---- audio -----------------------------------------------------------------

// DecodeOptions decode (a window of) a file's audio to WAV.
type DecodeOptions struct {
	Input, Output string
	SampleRate    int     // 0 keeps the source's
	Channels      int     // 0 keeps the source's
	Bits          int     // 16 (default), 24, 32 (float)
	Start         float64 // seconds
	Duration      float64 // seconds, 0 = to the end
	Track         int     // the audio stream's index
	Total         float64 // the source's length, for progress (0 = unknown)
}

// DecodeAudio writes the audio of o.Input as WAV.
func (t *Tool) DecodeAudio(ctx context.Context, o DecodeOptions, progress func(float64)) error {
	var args []string
	if o.Start > 0 {
		args = append(args, "-ss", secs(o.Start))
	}
	if o.Duration > 0 {
		args = append(args, "-t", secs(o.Duration))
	}
	args = append(args, "-i", o.Input, "-map", fmt.Sprintf("0:a:%d", max(o.Track, 0)), "-vn", "-sn", "-dn")
	if o.SampleRate > 0 {
		args = append(args, "-ar", strconv.Itoa(o.SampleRate))
	}
	if o.Channels > 0 {
		args = append(args, "-ac", strconv.Itoa(o.Channels))
	}
	codec := "pcm_s16le"
	switch o.Bits {
	case 24:
		codec = "pcm_s24le"
	case 32:
		codec = "pcm_f32le"
	}
	args = append(args, "-c:a", codec, "-rf64", "auto", "-f", "wav")
	length := o.Duration
	if length == 0 && o.Total > 0 {
		length = o.Total - o.Start
	}
	return t.run(ctx, args, o.Output, length, progress)
}

// Chapter is a chapter mark, seconds.
type Chapter struct {
	Title      string
	Start, End float64
}

// EncodeOptions encode a WAV into Format.
type EncodeOptions struct {
	Input, Output string
	Format        string // MP3, Opus, FLAC, OGG, M4A, WAV
	Bitrate       int    // bits per second, 0 = the format's default
	SampleRate    int    // 0 keeps the input's (Opus resamples to 48 kHz)
	Channels      int    // 0 keeps the input's
	// Metadata: "title", "artist", "album", "album_artist", "composer",
	// "date", "genre", "comment".
	Metadata map[string]string
	Chapters []Chapter
	Cover    string  // PNG or JPEG (MP3, M4A, FLAC)
	Duration float64 // the input's length, for progress
}

// EncodeAudio writes o.Input in o.Format.
func (t *Tool) EncodeAudio(ctx context.Context, o EncodeOptions, progress func(float64)) error {
	codec, copts := t.audioCodec(o.Format, o.Bitrate)
	if codec == "" {
		return &NoEncoderError{Format: o.Format}
	}
	args := []string{"-i", o.Input}
	next := 1
	meta := -1
	if len(o.Metadata) > 0 || len(o.Chapters) > 0 {
		f, err := os.CreateTemp("", "audiosmith-meta-*.txt")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		f.WriteString(ffmetadata(o.Metadata, o.Chapters))
		f.Close()
		args = append(args, "-f", "ffmetadata", "-i", f.Name())
		meta, next = next, next+1
	}
	cover := -1
	if o.Cover != "" && (o.Format == MP3 || o.Format == M4A || o.Format == FLAC) {
		if _, err := os.Stat(o.Cover); err == nil {
			args = append(args, "-i", o.Cover)
			cover = next
		}
	}
	args = append(args, "-map", "0:a:0")
	if cover >= 0 {
		args = append(args, "-map", strconv.Itoa(cover)+":v:0", "-c:v", "copy", "-disposition:v:0", "attached_pic")
		if o.Format == MP3 {
			args = append(args, "-metadata:s:v", "title=Album cover", "-metadata:s:v", "comment=Cover (front)")
		}
	}
	if meta >= 0 {
		args = append(args, "-map_metadata", strconv.Itoa(meta), "-map_chapters", strconv.Itoa(meta))
	}
	args = append(args, "-c:a", codec)
	args = append(args, copts...)
	if o.SampleRate > 0 && o.Format != Opus {
		args = append(args, "-ar", strconv.Itoa(o.SampleRate))
	}
	if o.Channels > 0 {
		args = append(args, "-ac", strconv.Itoa(o.Channels))
	}
	switch o.Format {
	case MP3:
		args = append(args, "-id3v2_version", "3", "-write_id3v1", "1", "-f", "mp3")
	case Opus:
		args = append(args, "-f", "ogg")
	case OGG:
		args = append(args, "-f", "ogg")
	case FLAC:
		args = append(args, "-f", "flac")
	case M4A:
		args = append(args, "-movflags", "+faststart", "-f", "mp4")
	case WAV:
		args = append(args, "-f", "wav")
	}
	return t.run(ctx, args, o.Output, o.Duration, progress)
}

// ffmetadata writes FFmpeg's metadata file: tags, then chapters.
func ffmetadata(tags map[string]string, chapters []Chapter) string {
	esc := strings.NewReplacer(`\`, `\\`, "=", `\=`, ";", `\;`, "#", `\#`, "\n", "\\\n")
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")
	for _, k := range []string{"title", "artist", "album", "album_artist", "composer", "date", "genre", "comment"} {
		if v := strings.TrimSpace(tags[k]); v != "" {
			b.WriteString(k + "=" + esc.Replace(v) + "\n")
		}
	}
	for _, c := range chapters {
		fmt.Fprintf(&b, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n", int64(c.Start*1000+0.5), int64(c.End*1000+0.5), esc.Replace(c.Title))
	}
	return b.String()
}

// ---- video -----------------------------------------------------------------

// TranscodeOptions make an H.264 + AAC MP4 preview.
type TranscodeOptions struct {
	Input, Output string
	MaxHeight     int // 0 = 720
	NoAudio       bool
	Duration      float64 // for progress
}

// Transcode writes a preview MP4 the web view plays.
func (t *Tool) Transcode(ctx context.Context, o TranscodeOptions, progress func(float64)) error {
	h := o.MaxHeight
	if h <= 0 {
		h = 720
	}
	args := []string{"-i", o.Input, "-map", "0:v:0"}
	if !o.NoAudio {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-vf", fmt.Sprintf("scale=-2:min(%d\\,ih),format=yuv420p", h))
	args = append(args, t.videoCodec()...)
	if !o.NoAudio {
		args = append(args, "-c:a", "aac", "-b:a", "128k")
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-sn", "-movflags", "+faststart", "-f", "mp4")
	return t.run(ctx, args, o.Output, o.Duration, progress)
}

// Frame is a thumbnail written.
type Frame struct {
	Time          float64
	Path          string
	Width, Height int
}

// Thumbnails writes one image per time into outputs (JPEG or PNG by
// extension), width wide (0 = the video's).
func (t *Tool) Thumbnails(ctx context.Context, input string, times []float64, outputs []string, width int) ([]Frame, error) {
	if len(times) != len(outputs) {
		return nil, fmt.Errorf("ffmpeg: %d times for %d outputs", len(times), len(outputs))
	}
	var frames []Frame
	for i, at := range times {
		args := []string{"-ss", secs(max(at, 0)), "-i", input, "-map", "0:v:0", "-frames:v", "1", "-an", "-sn"}
		if width > 0 {
			args = append(args, "-vf", fmt.Sprintf("scale=%d:-2", width))
		}
		if strings.EqualFold(filepath.Ext(outputs[i]), ".png") {
			args = append(args, "-f", "image2", "-c:v", "png")
		} else {
			args = append(args, "-f", "image2", "-c:v", "mjpeg", "-q:v", "3")
		}
		if err := t.run(ctx, args, outputs[i], 0, nil); err != nil {
			return frames, err
		}
		f := Frame{Time: at, Path: outputs[i]}
		if r, err := os.Open(outputs[i]); err == nil {
			if cfg, _, err := image.DecodeConfig(r); err == nil {
				f.Width, f.Height = cfg.Width, cfg.Height
			}
			r.Close()
		}
		frames = append(frames, f)
	}
	return frames, nil
}

// Track is an audio track to add to a video.
type Track struct {
	Path     string
	Language string // ISO 639-2 ("spa"), as MP4 wants
	Title    string
	Default  bool
	Bitrate  int // b/s, 0 = 192 kb/s
}

// SubtitleTrack is a soft subtitle track (SRT or WebVTT).
type SubtitleTrack struct {
	Path     string
	Language string // ISO 639-2
	Title    string
	Default  bool
}

// MuxOptions make an MP4 of Video's video with audio tracks and soft
// subtitles.
type MuxOptions struct {
	Video     string
	KeepAudio bool // the source's own audio, first
	// CopyVideo keeps the video as it is (H.264/HEVC); otherwise it is
	// encoded to H.264 (VP9, AV1 and others in an MP4 play nowhere).
	CopyVideo bool
	Audio     []Track
	Subtitles []SubtitleTrack
	Output    string
	Duration  float64
}

// Mux writes o.Output.
func (t *Tool) Mux(ctx context.Context, o MuxOptions, progress func(float64)) error {
	args := []string{"-i", o.Video}
	for _, a := range o.Audio {
		args = append(args, "-i", a.Path)
	}
	for _, s := range o.Subtitles {
		args = append(args, "-i", s.Path)
	}
	args = append(args, "-map", "0:v:0")
	n := 0 // audio streams out
	var meta []string
	if o.KeepAudio {
		args = append(args, "-map", "0:a:0?")
		meta = append(meta, "-disposition:a:0", "0")
		n++
	}
	for i, a := range o.Audio {
		args = append(args, "-map", strconv.Itoa(1+i)+":a:0")
		if a.Language != "" {
			meta = append(meta, fmt.Sprintf("-metadata:s:a:%d", n), "language="+a.Language)
		}
		if a.Title != "" {
			meta = append(meta, fmt.Sprintf("-metadata:s:a:%d", n), "title="+a.Title)
		}
		disp := "0"
		if a.Default {
			disp = "default"
		}
		meta = append(meta, fmt.Sprintf("-disposition:a:%d", n), disp)
		n++
	}
	for j, s := range o.Subtitles {
		args = append(args, "-map", strconv.Itoa(1+len(o.Audio)+j)+":s:0")
		if s.Language != "" {
			meta = append(meta, fmt.Sprintf("-metadata:s:s:%d", j), "language="+s.Language)
		}
		if s.Title != "" {
			meta = append(meta, fmt.Sprintf("-metadata:s:s:%d", j), "title="+s.Title)
		}
		disp := "0"
		if s.Default {
			disp = "default"
		}
		meta = append(meta, fmt.Sprintf("-disposition:s:%d", j), disp)
	}
	if o.CopyVideo {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, "-vf", "format=yuv420p")
		args = append(args, t.videoCodec()...)
	}
	br := 192000
	if len(o.Audio) > 0 && o.Audio[0].Bitrate > 0 {
		br = o.Audio[0].Bitrate
	}
	args = append(args, "-c:a", "aac", "-b:a", strconv.Itoa(br/1000)+"k")
	if len(o.Subtitles) > 0 {
		args = append(args, "-c:s", "mov_text")
	}
	args = append(args, meta...)
	args = append(args, "-movflags", "+faststart", "-f", "mp4")
	return t.run(ctx, args, o.Output, o.Duration, progress)
}

// BurnOptions make an MP4 with subtitles drawn into the picture.
type BurnOptions struct {
	Video string
	// Audio replaces the video's sound ("" keeps it).
	Audio string
	// Subtitles is an SRT or ASS file for libass; Cues the same lines,
	// drawn by AudioSmith when this ffmpeg has no libass.
	Subtitles string
	Cues      []Cue
	// Fonts to draw Cues with, in order of preference (TrueType or
	// OpenType files or collections); a built-in Latin font comes last.
	Fonts    []string
	Output   string
	Duration float64 // the video's length (seconds)
	Width    int     // the video's size, for the drawn captions
	Height   int
}

// Burn writes o.Output: the video re-encoded with its subtitles drawn in,
// and the chosen audio in AAC.
func (t *Tool) Burn(ctx context.Context, o BurnOptions, progress func(float64)) error {
	args := []string{"-i", o.Video}
	audio := "0:a:0?"
	next := 1
	if o.Audio != "" {
		args = append(args, "-i", o.Audio)
		audio = strconv.Itoa(next) + ":a:0"
		next++
	}
	var filter, cwd string
	switch {
	case t.HasFilter("subtitles") && o.Subtitles != "" && !forceDraw:
		// libass reads the file by a name in the filtergraph, whose
		// escaping rules differ from the shell's: a copy with a plain
		// name in a folder of its own, which ffmpeg runs in.
		dir, err := os.MkdirTemp("", "audiosmith-subs-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		data, err := os.ReadFile(o.Subtitles)
		if err != nil {
			return err
		}
		name := "subs" + strings.ToLower(filepath.Ext(o.Subtitles))
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
		cwd = dir
		filter = "[0:v]subtitles=filename=" + name + ":force_style='FontSize=22,Outline=2,Shadow=0,MarginV=28',format=yuv420p[v]"
	case t.HasFilter("overlay") && len(o.Cues) > 0:
		if o.Width <= 0 || o.Height <= 0 {
			return fmt.Errorf("ffmpeg: the video's size is needed to draw subtitles")
		}
		dir, err := os.MkdirTemp("", "audiosmith-captions-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		list, h, err := RenderCaptions(o.Cues, o.Width, o.Height, o.Duration, o.Fonts, dir)
		if err != nil {
			return err
		}
		args = append(args, "-f", "concat", "-safe", "0", "-i", list)
		filter = fmt.Sprintf("[%d:v]format=rgba[s];[0:v][s]overlay=0:H-%d:eof_action=pass:format=auto,format=yuv420p[v]", next, h)
	default:
		return &NoEncoderError{Format: "burnt-in subtitles"}
	}
	args = append(args, "-filter_complex", filter, "-map", "[v]", "-map", audio)
	args = append(args, t.videoCodec()...)
	args = append(args, "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", "-f", "mp4")
	return t.runIn(ctx, cwd, args, o.Output, o.Duration, progress)
}

// forceDraw makes Burn draw captions even with libass (tests).
var forceDraw = false
