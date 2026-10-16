package main

// Workflow texts: the errors and warnings of steps and runs; split from
// workflow.go.

import (
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// wfErrorText explains a failed step or run.
func wfErrorText(code, msg string) string {
	switch code {
	case "":
		return msg
	case "model_not_downloaded":
		return i18n.T("A model this workflow uses is not downloaded: download it on the Models page.")
	case "licence_required":
		return i18n.T("A model this workflow uses needs its licence accepted on the Models page.")
	case services.CodeEngineUnavailable:
		return i18n.T("The speech engine is not running. Start it on the Engine page.")
	case services.WorkflowCodeNoSTT:
		return i18n.T("No speech-to-text model is available: download one on the Models page.")
	case services.WorkflowCodeNoSpeech:
		return i18n.T("No speech was found.")
	case services.WorkflowCodeVoice:
		return i18n.T("The voice can't be used: %s", msg)
	case core.CodeNeedsReference:
		return needsReferenceText(modelInMessage(msg))
	case services.WorkflowCodeEncode:
		return i18n.T("This engine can't write M4A: choose WAV.")
	case services.WorkflowCodeFile:
		return i18n.T("A file could not be read or written: %s", msg)
	case services.CodeNothingToSay:
		return i18n.T("There is nothing to say.")
	case translate.CodeNoLLM, translate.CodeNoKey, translate.CodeConfig:
		return i18n.T("Translating needs a translation engine: a local model (Ollama, LM Studio) or a provider in Settings → Writing help.")
	case translate.CodeLineFailed, translate.CodeMissing:
		return i18n.T("Some text could not be translated.")
	}
	return msg
}

// wfWarningText explains a step's warning.
func wfWarningText(code string) string {
	switch code {
	case services.WorkflowWarnSilent:
		return i18n.T("The audio is silent: kept as it is.")
	case services.WorkflowWarnNoFolder:
		return i18n.T("No folder: kept in the Results list.")
	case services.WarnNoWordTimes:
		return i18n.T("Without word timings: subtitles are timed from the text.")
	}
	return code
}
