package settings

// M4 (thread #19): dubbing.

// Dubbing: the Dub and Batch pages' choices.
type Dubbing struct {
	// URLImport allows importing videos from links (yt-dlp, downloaded
	// when it was turned on); YtDlp is the executable.
	URLImport bool   `json:"urlImport,omitempty"`
	YtDlp     string `json:"ytdlp,omitempty"`
	// NoReview runs a new dub straight through, without stopping after
	// the transcript and the translation.
	NoReview bool `json:"noReview,omitempty"`
	// Defaults of a new dub: the TTS model (repo), target languages and
	// translator ("llm", "deepl", "google", "microsoft").
	Model      string   `json:"model,omitempty"`
	Targets    []string `json:"targets,omitempty"`
	Translator string   `json:"translator,omitempty"`
	// Open is the dub open when the app quit (reopened at the next start).
	Open string `json:"open,omitempty"`
}
