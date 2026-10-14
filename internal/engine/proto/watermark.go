package proto

// The invisible watermark (docs/protocol-v2.md §5.5): Meta's AudioSeal,
// a 16-bit message embedded in speech and detected per sample, served by
// the media worker. Model files are the app's conversion of
// facebook/audioseal to safetensors (internal/torchpt): modelPath is a
// folder with generator.safetensors and detector.safetensors.

// Methods added for the watermark.
const (
	MethodWatermarkEmbed  = "watermark.embed"  // request, job (media)
	MethodWatermarkDetect = "watermark.detect" // request, job (media)
)

// WatermarkBits is the length of the message.
const WatermarkBits = 16

// WatermarkEmbedParams are watermark.embed's params. The input (any
// format the engine reads) is written to Output at its own rate and
// channels with the watermark added: AudioSeal works at 16 kHz mono, so the
// watermark is computed on a 16 kHz mix and resampled to the input's rate.
type WatermarkEmbedParams struct {
	Model     string `json:"model"`
	ModelPath string `json:"modelPath,omitempty"`
	Input     string `json:"input"`
	Output    Output `json:"output"`
	// Message is the 16 bits to embed (0–65535; bit 15, the most
	// significant, is the model's first bit).
	Message int `json:"message"`
	// Strength scales the watermark (AudioSeal's alpha; 0 = 1). Lower is
	// less audible and less robust.
	Strength float64 `json:"strength,omitempty"`
}

// WatermarkEmbedResult describes the watermarked file (proto.AudioOut
// fields) and how loud the watermark is against the audio.
type WatermarkEmbedResult struct {
	Output       string  `json:"output"`
	SampleRate   int     `json:"sampleRate"`
	Channels     int     `json:"channels"`
	Frames       int64   `json:"frames"`
	AudioSeconds float64 `json:"audioSeconds"`
	Model        string  `json:"model"`
	Message      int     `json:"message"`
	// SNR is the audio's power over the watermark's, in dB.
	SNR     float64 `json:"snr"`
	Seconds float64 `json:"seconds"`
}

// WatermarkDetectParams are watermark.detect's params.
type WatermarkDetectParams struct {
	Model     string `json:"model"`
	ModelPath string `json:"modelPath,omitempty"`
	Input     string `json:"input"`
	// Threshold is the per-sample probability over which a sample counts
	// as watermarked (0 = 0.5).
	Threshold float64 `json:"threshold,omitempty"`
}

// WatermarkDetectResult: Probability is the fraction of samples found
// watermarked (AudioSeal's detection score); Detected is Probability ≥
// 0.5. Message is decoded whether or not the audio is watermarked (random
// when it is not); Bits are the probabilities of each bit being 1, most
// significant first, and Confidence their mean |2p − 1|. Frames give the
// probability per second, to locate watermarked parts.
type WatermarkDetectResult struct {
	Probability  float64          `json:"probability"`
	Detected     bool             `json:"detected"`
	Message      int              `json:"message"`
	Bits         []float64        `json:"bits"`
	Confidence   float64          `json:"confidence"`
	Frames       []WatermarkFrame `json:"frames,omitempty"`
	AudioSeconds float64          `json:"audioSeconds"`
	Model        string           `json:"model"`
	Seconds      float64          `json:"seconds"`
}

// WatermarkFrame is the detection over one stretch of the input.
type WatermarkFrame struct {
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	Probability float64 `json:"probability"`
}
