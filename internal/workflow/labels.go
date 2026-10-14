package workflow

import (
	"strings"

	"github.com/hehehai/AudioSmith/internal/i18n"
)

// Label is a kind's name in the interface's language.
func (k Kind) Label() string {
	switch k {
	case KindStart:
		return i18n.T("Start")
	case KindSource:
		return i18n.T("Source clips")
	case KindSpeak:
		return i18n.T("Speak")
	case KindTranscribe:
		return i18n.T("Transcribe")
	case KindTranslate:
		return i18n.T("Translate")
	case KindConvert:
		return i18n.T("Convert voice")
	case KindNormalize:
		return i18n.T("Normalize audio")
	case KindCondition:
		return i18n.T("Condition")
	case KindExport:
		return i18n.T("Export")
	}
	return string(k)
}

// About says what a kind does.
func (k Kind) About() string {
	switch k {
	case KindStart:
		return i18n.T("Scripts to run, one item each.")
	case KindSource:
		return i18n.T("Audio files to run, one item each.")
	case KindSpeak:
		return i18n.T("Text to speech in a voice.")
	case KindTranscribe:
		return i18n.T("Speech to a timed transcript.")
	case KindTranslate:
		return i18n.T("Text into another language.")
	case KindConvert:
		return i18n.T("The same words in another voice.")
	case KindNormalize:
		return i18n.T("Even loudness or peak level.")
	case KindCondition:
		return i18n.T("Yes or no, by the text.")
	case KindExport:
		return i18n.T("Files in a folder.")
	}
	return ""
}

// Label names a value type.
func (t Type) Label() string {
	switch t {
	case TypeText:
		return i18n.T("Text")
	case TypeAudio:
		return i18n.T("Audio")
	case TypeTranscript:
		return i18n.T("Transcript")
	}
	return string(t)
}

// Label names a comparison.
func (m Match) Label() string {
	switch m {
	case MatchEquals:
		return i18n.T("Is exactly")
	case MatchPrefix:
		return i18n.T("Starts with")
	case MatchSuffix:
		return i18n.T("Ends with")
	}
	return i18n.T("Contains")
}

// PortLabel names an output port ("yes", "no"); "" for the others.
func PortLabel(port string) string {
	switch port {
	case PortYes:
		return i18n.T("yes")
	case PortNo:
		return i18n.T("no")
	}
	return ""
}

// TemplateName and TemplateAbout translate a template's texts.
func TemplateName(id string) string {
	switch id {
	case "narration":
		return i18n.T("Batch narration")
	case "narration-normalize":
		return i18n.T("Narration + normalize")
	case "transcript":
		return i18n.T("Audio to transcript")
	case "change-voice":
		return i18n.T("Change the voice")
	case "translate-narrate":
		return i18n.T("Translate and narrate")
	case "subtitles":
		return i18n.T("Transcript → subtitles")
	}
	return id
}

func TemplateAbout(id string) string {
	switch id {
	case "narration":
		return i18n.T("Scripts → speech, one file each.")
	case "narration-normalize":
		return i18n.T("Scripts → speech at an even loudness.")
	case "transcript":
		return i18n.T("Recordings → text files.")
	case "change-voice":
		return i18n.T("Recordings → the same words in another voice.")
	case "translate-narrate":
		return i18n.T("Recordings → transcribed, translated and spoken.")
	case "subtitles":
		return i18n.T("Recordings → SubRip subtitles.")
	}
	return ""
}

// Text is the diagnostic's message in the interface's language. Codes
// the app adds (Check) are not known here: their Arg is the message.
func (d Diagnostic) Text() string {
	switch d.Code {
	case CodeEmpty:
		return i18n.T("Add a Start or Source clips step to begin.")
	case CodeNoInput:
		return i18n.T("Add a Start or Source clips step: it brings the items to run.")
	case CodeNoSteps:
		return i18n.T("Add a step after the input.")
	case CodeExtraInput:
		return i18n.T("A workflow takes one input step: remove this one.")
	case CodeUnconnected:
		return i18n.T("Connect this step's input.")
	case CodeUnreachable:
		return i18n.T("No path from the input reaches this step.")
	case CodeCycle:
		return i18n.T("This link closes a loop.")
	case CodeType:
		return i18n.T("This step can't take %s.", Type(d.Arg).Label())
	case CodeMergeTypes:
		return i18n.T("Links into one input must carry the same kind of value.")
	case CodeMerge:
		return i18n.T("Only the yes and no branches of a condition may join into one input.")
	case CodeStartEmpty:
		return i18n.T("Add a script.")
	case CodeStartTooMany:
		return i18n.T("At most 50 scripts: this has %s.", d.Arg)
	case CodeStartTooLong:
		return i18n.T("Script %s is over 20,000 characters.", d.Arg)
	case CodeSourceEmpty:
		return i18n.T("Add audio clips.")
	case CodeSourceTooMany:
		return i18n.T("At most 50 clips: this has %s.", d.Arg)
	case CodeSourceLarge:
		return i18n.T("%s is over 64 MiB.", d.Arg)
	case CodeSourceZero:
		return i18n.T("%s is empty.", d.Arg)
	case CodeNoModel:
		return i18n.T("Choose a voice model.")
	case CodeNoTarget:
		return i18n.T("Choose the language to translate into.")
	case CodeSameLanguage:
		return i18n.T("The source and target languages are the same.")
	case CodeNoMatchText:
		return i18n.T("Enter the text to look for.")
	case CodeFormat:
		return i18n.T("This value can't be saved as %s.", strings.ToUpper(d.Arg))
	case CodeTemplate:
		return i18n.T("The file name can't be empty or hold / \\ or :.")
	case CodeNoFolder:
		return i18n.T("No folder: the results stay in the Results list.")
	}
	return d.Arg
}
