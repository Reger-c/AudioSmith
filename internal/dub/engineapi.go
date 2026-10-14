package dub

// Engine methods the dubbing pipeline needs that protocol 2 does not have
// yet: a proposal to the engine thread (#14), in the style of
// docs/protocol-v2.md §5.2 (absolute paths, outputs written to a
// temporary name and renamed, proto.Output for files, BCP-47 language
// codes, progress stages, typed errors). The names are already reserved
// in proto.Reserved. The Go side runs against dubtest's fake of them;
// until the engine has one, the pipeline skips what needs it (separation,
// diarization) or the UI marks what needs it (MP4, M4A, preview proxies,
// posters) as "needs the engine".

import "github.com/hehehai/AudioSmith/internal/engine/proto"

// Proposed methods. All are job requests.
const (
	MethodSeparate  = "separate.run"    // gpu: voices / background (HTDemucs; Mel-RoFormer for quality)
	MethodDiarize   = "diarize.run"     // gpu: who speaks when (Sortformer ≤ 4; FluidAudio pyannote later)
	MethodMux       = "media.mux"       // media: MP4 with the source's video and N audio tracks
	MethodEncode    = "media.encode"    // media: AAC (M4A); M3's M4B uses the same method
	MethodTimePitch = "media.timePitch" // media: tempo change keeping pitch (AVAudioUnitTimePitch)
	MethodThumbnail = "media.thumbnail" // media: video frames as images
	MethodTranscode = "media.transcode" // media: an H.264 preview of a video the web view cannot play
)

// RoleOf is the role a proposed method runs in (proto.RoleOf for the
// others).
func RoleOf(method string) proto.Role {
	switch method {
	case MethodMux, MethodEncode, MethodTimePitch, MethodThumbnail, MethodTranscode,
		proto.MethodMediaDecode, proto.MethodMediaProbe:
		return proto.RoleMedia
	case MethodSeparate, MethodDiarize:
		return proto.RoleGPU
	}
	return proto.RoleOf(method)
}

// Progress stages of the proposed methods (besides loading/decoding/
// encoding/writing): "separating", "diarizing", "muxing", "transcoding".

// SeparateParams: separate.run. The input is any file the engine reads
// (it decodes it); the stems are written as WAV at the model's rate
// (44.1 kHz stereo for HTDemucs and RoFormer).
type SeparateParams struct {
	Model     string `json:"model"`
	ModelPath string `json:"modelPath,omitempty"`
	Audio     string `json:"audio"`
	// Vocals and Background are the stems to write; a zero Path skips one.
	Vocals     proto.Output `json:"vocals"`
	Background proto.Output `json:"background"`
	// Shifts (HTDemucs' random shifts, 0 = the model's default 1) trade
	// time for quality; Overlap is the window overlap (0 = 0.25).
	Shifts  int     `json:"shifts,omitempty"`
	Overlap float64 `json:"overlap,omitempty"`
}

// SeparateResult: the stems (proto.AudioOut fields describe them).
type SeparateResult struct {
	Vocals       string  `json:"vocals,omitempty"`
	Background   string  `json:"background,omitempty"`
	SampleRate   int     `json:"sampleRate"`
	Channels     int     `json:"channels"`
	Frames       int64   `json:"frames"`
	AudioSeconds float64 `json:"audioSeconds"`
	Model        string  `json:"model"`
	Seconds      float64 `json:"seconds"`
}

// DiarizeParams: diarize.run. Speakers 0 detects how many; a model that
// cannot tell that many apart fails with unsupported, data
// {"capability":"speakers","max":4}.
type DiarizeParams struct {
	Model       string `json:"model"`
	ModelPath   string `json:"modelPath,omitempty"`
	Audio       string `json:"audio"`
	Speakers    int    `json:"speakers,omitempty"`
	MinSpeakers int    `json:"minSpeakers,omitempty"`
	MaxSpeakers int    `json:"maxSpeakers,omitempty"`
}

// DiarizeResult: turns in time order (they may overlap: crosstalk);
// labels are the model's ("0", "1"… or "SPEAKER_00"), stable within one
// result.
type DiarizeResult struct {
	Turns        []Turn  `json:"turns"`
	Speakers     int     `json:"speakers"`
	Model        string  `json:"model"`
	AudioSeconds float64 `json:"audioSeconds"`
	Seconds      float64 `json:"seconds"`
}

// MuxParams: media.mux. The video track of Video is copied as it is (no
// re-encode: AVMutableComposition + passthrough export), unless Retime
// asks to slow parts of it (Stretch video), which re-encodes it. Audio
// tracks are AAC (WAV inputs are encoded; AAC/M4A inputs copied), each
// with its language (BCP-47 → ISO 639-2/T in the file) and title;
// Default marks the track players pick first. Subtitles are soft tracks
// (tx3g / mov_text) from SRT or WebVTT files.
type MuxParams struct {
	Video string `json:"video"`
	// KeepAudio puts the source's own audio tracks first, as they are.
	KeepAudio bool          `json:"keepAudio,omitempty"`
	Audio     []MuxTrack    `json:"audio"`
	Subtitles []MuxSubtitle `json:"subtitles,omitempty"`
	Output    proto.Output  `json:"output"` // format "mp4" (default) or "mov"
	// Retime slows the video between Start and End by Factor (> 1) —
	// the audio given is already laid out on the retimed timeline.
	Retime []Retime `json:"retime,omitempty"`
}

// MuxTrack is an audio track of an MP4.
type MuxTrack struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"` // BCP-47
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Bitrate  int    `json:"bitrate,omitempty"` // bits/s; 0 = 192 kb/s stereo
}

// MuxSubtitle is a soft subtitle track.
type MuxSubtitle struct {
	Path     string `json:"path"` // SRT or WebVTT
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
}

// Retime is a stretch of video to slow down.
type Retime struct {
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Factor float64 `json:"factor"`
}

// MuxResult: the file written.
type MuxResult struct {
	Output   string  `json:"output"`
	Bytes    int64   `json:"bytes"`
	Duration float64 `json:"duration"`
	Tracks   int     `json:"tracks"` // audio tracks in the file
	// Reencoded: the video was re-encoded (Retime), not copied.
	Reencoded bool `json:"reencoded,omitempty"`
}

// EncodeParams: media.encode, as thread #18 (M3) proposed it for M4B
// (services.EncodeParams); dubbing uses format "m4a" without chapters.
type EncodeParams struct {
	Input      string       `json:"input"`
	Output     proto.Output `json:"output"` // format "m4a" ("m4b", later "mp3")
	Codec      string       `json:"codec,omitempty"`
	Bitrate    int          `json:"bitrate,omitempty"`
	SampleRate int          `json:"sampleRate,omitempty"`
}

// EncodeResult: as M3's.
type EncodeResult struct {
	Output   string  `json:"output"`
	Format   string  `json:"format"`
	Bytes    int64   `json:"bytes"`
	Duration float64 `json:"duration"`
	Bitrate  int     `json:"bitrate,omitempty"`
}

// TimePitchParams: media.timePitch. Rate > 1 is faster (AVAudioUnitTimePitch
// rate, 1/32–32); Pitch shifts by semitones (0 = none). The pipeline
// stretches in Go (internal/dsp/stretch) and needs this only if the
// engine's sounds better on the Mac.
type TimePitchParams struct {
	Input  string       `json:"input"`
	Output proto.Output `json:"output"`
	Rate   float64      `json:"rate"`
	Pitch  float64      `json:"pitch,omitempty"`
}

// ThumbnailParams: media.thumbnail. One image per time (seconds) in
// Outputs (JPEG or PNG by extension), Width wide (0 = the video's, kept
// in proportion); the nearest keyframe is fine (AVAssetImageGenerator
// with tolerance).
type ThumbnailParams struct {
	Input   string    `json:"input"`
	Times   []float64 `json:"times"`
	Outputs []string  `json:"outputs"`
	Width   int       `json:"width,omitempty"`
}

// ThumbnailResult: the images written, in order.
type ThumbnailResult struct {
	Frames []Frame `json:"frames"`
}

// Frame is an image of a video.
type Frame struct {
	Time   float64 `json:"time"`
	Path   string  `json:"path"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
}

// TranscodeParams: media.transcode. An H.264 + AAC MP4 at most MaxHeight
// high (0 = 720) for the web view's preview. AVFoundation reads H.264,
// HEVC, ProRes and (M3+) AV1 in MP4/MOV; VP9 and WebM/MKV need ffmpeg
// (unsupported_media until it is provided).
type TranscodeParams struct {
	Input     string       `json:"input"`
	Output    proto.Output `json:"output"` // format "mp4"
	MaxHeight int          `json:"maxHeight,omitempty"`
	NoAudio   bool         `json:"noAudio,omitempty"`
}

// TranscodeResult: the file written.
type TranscodeResult struct {
	Output   string  `json:"output"`
	Bytes    int64   `json:"bytes"`
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
}
