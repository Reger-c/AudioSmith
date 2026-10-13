package core

import "time"

// Take is one generated piece of speech: its audio file and the recipe
// that made it, so it can be replayed, reused or regenerated.
//
// The JSON names are the history.json format of the MVP and stay stable:
// the SQLite store keeps the recipe in the same shape (takes.recipe JSON)
// and the history.json importer decodes old files with this type.
type Take struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	// File is the audio's name inside the store's audio directory; see
	// store.Takes.AudioPath. Never an absolute path.
	File string `json:"file"`

	// The recipe. Zero values mean "the model's default" and were not sent.
	Text        string  `json:"text"`
	Model       string  `json:"model"` // Hugging Face repo id (catalog.Entry.Repo)
	Voice       string  `json:"voice,omitempty"`
	Language    string  `json:"language,omitempty"`
	Speed       float64 `json:"speed,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"topP,omitempty"`
	MaxTokens   int     `json:"maxTokens,omitempty"`
	RefAudio    string  `json:"refAudio,omitempty"` // reference clip for cloning (absolute path)
	RefText     string  `json:"refText,omitempty"`
	Seed        int64   `json:"seed,omitempty"` // M1: the seed used, so the take can be reproduced
	Duration    float64 `json:"duration,omitempty"` // target length in seconds (OmniVoice); 0 = natural
	Steps       int     `json:"steps,omitempty"`    // diffusion steps (OmniVoice); 0 = the model's

	// Relations (M1+). Empty for MVP takes.
	VoiceID   string `json:"voiceId,omitempty"`   // Voice profile it was spoken with
	ProjectID string `json:"projectId,omitempty"` // project (story, audiobook, dub) it belongs to
	JobID     string `json:"jobId,omitempty"`     // job that produced it

	// Library state (M1+).
	Starred bool `json:"starred,omitempty"`

	// The result.
	SampleRate   int     `json:"sampleRate"`
	AudioSeconds float64 `json:"audioSeconds"`
	GenSeconds   float64 `json:"genSeconds"`
}

// RTF is how many seconds of audio one second of generation made.
func (t *Take) RTF() float64 {
	if t.GenSeconds <= 0 {
		return 0
	}
	return t.AudioSeconds / t.GenSeconds
}

// TakeQuery selects takes. The zero value is every take, newest first.
type TakeQuery struct {
	Search    string // full-text over the script (SQLite FTS5); "" matches all
	Model     string
	VoiceID   string
	ProjectID string
	Starred   bool // only starred takes
	Limit     int  // 0 = no limit
	Offset    int
}
