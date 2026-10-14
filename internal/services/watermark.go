package services

// The invisible watermark at the end of every render (takes, and so the
// dub lines, workflow Speak steps and Local API speech that render
// through the take pipeline; longform chapters in longform_render.go):
// Config.Watermark marks the finished WAV in place. A failure never fails
// the render: the take keeps a notice and stays unmarked.

import (
	"context"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
)

// NoticeWatermarkFailed: the take could not be watermarked (params: error).
const NoticeWatermarkFailed = "render.watermark_failed"

// WatermarkFunc marks the WAV at path (bits: its bit depth, 32 = float)
// in place. It reports false, without error, when the watermark is off or
// cannot run now (its model is not downloaded).
type WatermarkFunc func(ctx context.Context, path string, bits int) (bool, error)

// watermark marks a finished render's file; it returns whether it did,
// and a notice when it failed.
func (r *renderer) watermarkOutput(ctx context.Context, path string, bits int) (bool, *core.TakeNotice) {
	if r.watermark == nil || path == "" {
		return false, nil
	}
	if bits == 0 {
		bits = 16
	}
	started := time.Now()
	ok, err := r.watermark(ctx, path, bits)
	if err != nil {
		if ctx.Err() != nil {
			return false, nil
		}
		r.logf("render: watermark %s: %v", path, err)
		return false, &core.TakeNotice{Code: NoticeWatermarkFailed, Severity: "info", Params: map[string]string{"error": err.Error()}}
	}
	if ok {
		r.logf("render: watermarked %s in %d ms", path, time.Since(started).Milliseconds())
	}
	return ok, nil
}
