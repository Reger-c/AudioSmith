package proto

// Params and results of the methods M4 (dubbing) adds: docs/protocol-v2.md
// §5.3. The JSON is the one thread #19 proposed (internal/dub/engineapi.go),
// so the dub package can alias these types. Shared rules as §5.2:
// absolute paths, inputs anything AVFoundation reads, outputs written to a
// temporary name beside output.path and renamed (a cancel removes it),
// file_not_found / unsupported_media with data.path, times in seconds,
// languages BCP-47.

// ---- separate.run (job, gpu) ----

// SeparateParams split speech from everything else. The stems are WAV at
// the model's rate (44.1 kHz stereo for HTDemucs and Mel-RoFormer), the
// same length as the input and aligned with it; a zero Path skips a stem.
type SeparateParams struct {
	Model      string `json:"model"`
	ModelPath  string `json:"modelPath,omitempty"`
	Audio      string `json:"audio"`
	Vocals     Output `json:"vocals"`
	Background Output `json:"background"`
	// Shifts are HTDemucs' random shifts (0 = 1); Overlap the windows'
	// overlap (0 = 0.25). Other models ignore them.
	Shifts  int     `json:"shifts,omitempty"`
	Overlap float64 `json:"overlap,omitempty"`
}

// SeparateResult describes the stems written.
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

// ---- diarize.run (job, gpu) ----

// DiarizeParams ask who speaks when. Speakers 0 detects how many; a model
// that cannot tell that many apart fails with unsupported, data
// {"capability":"speakers","max":4}. MinSpeakers/MaxSpeakers bound
// detection (0 = none).
type DiarizeParams struct {
	Model       string `json:"model"`
	ModelPath   string `json:"modelPath,omitempty"`
	Audio       string `json:"audio"`
	Speakers    int    `json:"speakers,omitempty"`
	MinSpeakers int    `json:"minSpeakers,omitempty"`
	MaxSpeakers int    `json:"maxSpeakers,omitempty"`
}

// DiarizeResult: turns sorted by start (they may overlap: crosstalk);
// labels are opaque, stable within one result; Speakers = distinct labels.
type DiarizeResult struct {
	Turns        []Turn  `json:"turns"`
	Speakers     int     `json:"speakers"`
	Model        string  `json:"model"`
	AudioSeconds float64 `json:"audioSeconds"`
	Seconds      float64 `json:"seconds"`
}

// Turn is one speaker speaking.
type Turn struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker"`
}

// ---- media.mux (job, media) ----

// MuxParams make an MP4 (or MOV) of Video's video track, copied as it is,
// with the source's audio (KeepAudio) and N dubbed audio tracks (AAC), and
// soft subtitle tracks from SRT/WebVTT files.
type MuxParams struct {
	Video     string        `json:"video"`
	KeepAudio bool          `json:"keepAudio,omitempty"`
	Audio     []MuxTrack    `json:"audio"`
	Subtitles []MuxSubtitle `json:"subtitles,omitempty"`
	Output    Output        `json:"output"` // format "mp4" (default) or "mov"
	// Retime slows parts of the video (later: unsupported, capability "retime").
	Retime []Retime `json:"retime,omitempty"`
}

// MuxTrack is a dubbed audio track.
type MuxTrack struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"` // BCP-47
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Bitrate  int    `json:"bitrate,omitempty"` // b/s; 0 = 192 kb/s
}

// MuxSubtitle is a soft subtitle track.
type MuxSubtitle struct {
	Path     string `json:"path"` // SRT or WebVTT
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
}

// Retime is a stretch of video to slow down by Factor (> 1).
type Retime struct {
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Factor float64 `json:"factor"`
}

// MuxResult describes the file written; Tracks counts its audio tracks.
type MuxResult struct {
	Output    string  `json:"output"`
	Bytes     int64   `json:"bytes"`
	Duration  float64 `json:"duration"`
	Tracks    int     `json:"tracks"`
	Reencoded bool    `json:"reencoded,omitempty"`
}

// ---- media.thumbnail (job, media) ----

// ThumbnailParams: one image per time (seconds) into Outputs (JPEG, or
// PNG by extension), Width wide (0 = the video's), height in proportion.
type ThumbnailParams struct {
	Input   string    `json:"input"`
	Times   []float64 `json:"times"`
	Outputs []string  `json:"outputs"`
	Width   int       `json:"width,omitempty"`
}

// ThumbnailResult lists the images written, in order.
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

// ---- media.transcode (job, media) ----

// TranscodeParams: an H.264 + AAC MP4 at most MaxHeight high (0 = 720),
// for previews of videos the web view cannot play.
type TranscodeParams struct {
	Input     string `json:"input"`
	Output    Output `json:"output"` // format "mp4"
	MaxHeight int    `json:"maxHeight,omitempty"`
	NoAudio   bool   `json:"noAudio,omitempty"`
}

// TranscodeResult describes the file written.
type TranscodeResult struct {
	Output   string  `json:"output"`
	Bytes    int64   `json:"bytes"`
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
}

// ---- media.timePitch (reserved; optional) ----

// TimePitchParams change tempo (Rate > 1 is faster) and pitch
// (semitones). Not implemented: the app stretches in Go.
type TimePitchParams struct {
	Input  string  `json:"input"`
	Output Output  `json:"output"`
	Rate   float64 `json:"rate"`
	Pitch  float64 `json:"pitch,omitempty"`
}
