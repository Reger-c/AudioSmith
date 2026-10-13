package core

import "errors"

// Sentinel errors. Implementations wrap them (fmt.Errorf("take %s: %w", id,
// ErrNotFound)) so the UI and the Local API can map them to messages and
// HTTP statuses without parsing text.
var (
	ErrNotFound        = errors.New("not found")            // 404
	ErrExists          = errors.New("already exists")       // 409
	ErrInvalid         = errors.New("invalid request")      // 400
	ErrBusy            = errors.New("busy")                 // 409 / 503
	ErrCancelled       = errors.New("cancelled")            // 499
	ErrLicenceRequired = errors.New("licence not accepted") // 451: see LicenceService
	ErrNotDownloaded   = errors.New("model not downloaded") // 424: see hub
	ErrEngine          = errors.New("engine unavailable")   // 503: worker down or restarting
	ErrUnsupported     = errors.New("not supported")        // 501: model/engine/store lacks the capability
)
