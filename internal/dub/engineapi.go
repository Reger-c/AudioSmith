package dub

// Engine methods the dubbing pipeline uses. They were proposed here and
// are protocol 2 since M4 (docs/protocol-v2.md §5.3; internal/engine/proto
// m4.go): the names and types below are aliases of proto's. Only
// media.timePitch stays a proposal (reserved): the pipeline stretches in
// Go (internal/dsp/stretch).

import "github.com/hehehai/AudioSmith/internal/engine/proto"

// Methods. All are job requests.
const (
	MethodSeparate  = proto.MethodSeparateRun    // gpu: voices / background (HTDemucs; Mel-RoFormer for quality)
	MethodDiarize   = proto.MethodDiarizeRun     // gpu: who speaks when (Sortformer ≤ 4)
	MethodMux       = proto.MethodMediaMux       // media: MP4 with the source's video, N audio tracks, soft subtitles
	MethodEncode    = proto.MethodMediaEncode    // media: AAC (M4A)
	MethodTimePitch = "media.timePitch"          // media: reserved, not implemented (see TimePitchParams)
	MethodThumbnail = proto.MethodMediaThumbnail // media: video frames as images
	MethodTranscode = proto.MethodMediaTranscode // media: an H.264 preview of a video the web view cannot play
)

// RoleOf is the role a method runs in: proto.RoleOf, plus the reserved
// media.timePitch.
func RoleOf(method string) proto.Role {
	if method == MethodTimePitch {
		return proto.RoleMedia
	}
	return proto.RoleOf(method)
}

// Progress stages (besides loading/decoding/encoding/writing):
// "separating", "diarizing", "muxing", "transcoding".

// The methods' params and results: see internal/engine/proto/m4.go.
type (
	SeparateParams  = proto.SeparateParams
	SeparateResult  = proto.SeparateResult
	DiarizeParams   = proto.DiarizeParams
	DiarizeResult   = proto.DiarizeResult
	Turn            = proto.Turn // a stretch of one speaker's speech, as diarization finds it
	MuxParams       = proto.MuxParams
	MuxTrack        = proto.MuxTrack
	MuxSubtitle     = proto.MuxSubtitle
	Retime          = proto.Retime
	MuxResult       = proto.MuxResult
	EncodeParams    = proto.EncodeParams
	EncodeResult    = proto.EncodeResult
	TimePitchParams = proto.TimePitchParams
	ThumbnailParams = proto.ThumbnailParams
	ThumbnailResult = proto.ThumbnailResult
	Frame           = proto.Frame
	TranscodeParams = proto.TranscodeParams
	TranscodeResult = proto.TranscodeResult
)
