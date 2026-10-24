package main

// The Calls texts: errors, outcomes, phases, states and the call clock;
// split from calls.go.

import (
	"errors"
	"fmt"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// callErrorText explains a failure to place or control a call.
func callErrorText(err error) string {
	var ae *telephony.APIError
	var lic *core.LicenceRequiredError
	switch {
	case errors.Is(err, services.ErrCallVoice):
		return i18n.T("Only designed voices and voices with a verified consent recording can make calls.")
	case errors.Is(err, services.ErrCallsBusy):
		return i18n.T("Another call is running. Wait for it to end.")
	case telephony.Unauthorized(err):
		return i18n.T("Twilio did not accept the Account SID or auth token.")
	case errors.As(err, &ae) && ae.Code == 21219:
		return i18n.T("Trial accounts can only call numbers verified in the Twilio Console.")
	case errors.As(err, &ae):
		return i18n.T("Twilio: %s", ae.Message)
	case errors.Is(err, core.ErrNotDownloaded):
		return i18n.T("The voice's speech model is not downloaded yet.")
	case errors.As(err, &lic):
		return i18n.T("The voice's speech model needs its licence accepted in Settings → Models & network.")
	case errors.Is(err, core.ErrUnsupported):
		return i18n.T("Finish the setup steps above to place calls.")
	}
	return err.Error()
}

// callOutcomeText names an outcome.
func callOutcomeText(o string) string {
	switch o {
	case telephony.OutcomeBooked:
		return i18n.T("Booked")
	case telephony.OutcomeDone:
		return i18n.T("Done")
	case telephony.OutcomeNotDone:
		return i18n.T("Not done")
	case telephony.OutcomeNeedsYou:
		return i18n.T("Needs you")
	case telephony.OutcomeFailed:
		return i18n.T("Failed")
	}
	return i18n.T("No outcome")
}

// callPhaseText names a phase.
func callPhaseText(p string) string {
	switch p {
	case services.CallDialing:
		return i18n.T("Dialing")
	case services.CallRinging:
		return i18n.T("Ringing")
	case services.CallLive:
		return i18n.T("In call")
	case services.CallFailed:
		return i18n.T("Not connected")
	}
	return i18n.T("Ended")
}

// callStateText names the agent's state.
func callStateText(s string) string {
	switch s {
	case telephony.StateThinking:
		return i18n.T("Thinking")
	case telephony.StateSpeaking:
		return i18n.T("Speaking")
	}
	return i18n.T("Listening")
}

// callClock is m:ss.
func callClock(d time.Duration) string {
	s := int(d.Seconds())
	if s < 0 {
		s = 0
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
