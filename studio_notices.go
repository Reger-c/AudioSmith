package main

// Translations of what the text pipeline and the render report by code:
// markup and lowering diagnostics (text/markup, text/plan), the render's
// warnings and Check audio's findings (core.TakeNotice).

import (
	"fmt"
	"strings"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/markup"
	"github.com/hehehai/AudioSmith/internal/text/plan"
)

// diagText explains a script diagnostic.
func diagText(d text.Diagnostic) string {
	return codeText(d.Code, d.Params)
}

// codeText explains a markup, plan or render code with its parameters.
func codeText(code string, p map[string]string) string {
	switch code {
	case markup.CodeUnknownTag:
		return i18n.T("%s is not a tag AudioSmith knows.", p["tag"])
	case markup.CodeBadPause:
		return i18n.T("%s: write a pause as [pause], [pause 500ms] or [pause 1.5s].", p["tag"])
	case markup.CodePauseClamped:
		return i18n.T("%s is longer than %s ms; it is shortened.", p["tag"], p["max_ms"])
	case markup.CodeUnclosedStyle:
		return i18n.T("[%s] is not closed: it lasts to the end.", p["style"])
	case markup.CodeStrayClose:
		return i18n.T("[/%s] closes nothing.", p["style"])
	case markup.CodeBadOverride:
		return i18n.T("%s: write a pronunciation as [[word|how to say it]].", p["tag"])
	case plan.CodeNonVerbalRemoved:
		return i18n.T("This model cannot voice %s; it is left out.", p["tag"])
	case plan.CodeTagRemoved:
		return i18n.T("%s is left out: this model would read it aloud.", p["tag"])
	case plan.CodeVoiceIgnored:
		return i18n.T("[voice:%s] switches voices in Stories; the Studio speaks with one voice.", p["voice"])
	case plan.CodeStyleIgnored:
		return i18n.T("This model has no speed control: [%s] is ignored.", p["style"])
	case plan.CodeNothingToSay:
		return i18n.T("The script has nothing to say.")
	case services.NoticePostSkipped:
		return i18n.T("The engine's file could not be read, so it was kept as it is (no volume levelling).")
	}
	return code
}

// noticeText explains a take notice: a render warning or a Check audio
// finding.
func noticeText(n core.TakeNotice) string {
	switch n.Code {
	case services.NoticeChunkShort:
		return i18n.T("Chunk %d is %.1f s long where its text needs about %s s: words may be missing.", n.Chunk, n.Value, n.Params["expected"])
	case services.NoticeChunkSilent:
		return i18n.T("Chunk %d is silent.", n.Chunk)
	case services.NoticeChunkLong:
		return i18n.T("Chunk %d is %.1f s long where its text needs about %s s: the model may have rambled.", n.Chunk, n.Value, n.Params["expected"])
	case "qc.empty":
		return i18n.T("There is no audio.")
	case "qc.silent":
		return i18n.T("The take is silent.")
	case "qc.invalid_samples":
		return i18n.T("%.0f samples are not numbers (NaN or infinite).", n.Value)
	case "qc.clipping":
		return i18n.T("Clipping: %.0f samples at full scale.", n.Value)
	case "qc.silence_gap":
		return i18n.T("A silence of %.1f s.", n.Value)
	case "qc.loudness_jump":
		if n.Value > 0 {
			return i18n.T("Suddenly %.0f LU louder.", n.Value)
		}
		return i18n.T("Suddenly %.0f LU quieter.", -n.Value)
	case "qc.dc_offset":
		return i18n.T("DC offset (%.0f dBFS).", n.Value)
	case "qc.low_level":
		return i18n.T("Very quiet: %.0f LUFS.", n.Value)
	case "qc.edge_silence":
		return i18n.T("%.1f s of silence at the edge.", n.Value)
	case "qc.click":
		return i18n.T("A click.")
	}
	if strings.HasPrefix(n.Code, "markup.") || strings.HasPrefix(n.Code, "plan.") || strings.HasPrefix(n.Code, "render.") {
		return codeText(n.Code, n.Params)
	}
	return n.Code
}

// noticeTime formats where a notice is: "0:12.3".
func noticeTime(seconds float64) string {
	m := int(seconds) / 60
	return fmt.Sprintf("%d:%04.1f", m, seconds-float64(m*60))
}
