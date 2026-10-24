package services

// The operations log's lines for changes to the user's data (#108,
// internal/oplog): "<what>.create", ".rename", ".delete"… with the id,
// the short name, who asked ("by": app, api, lan, mcp) and the error of
// a failure. Never the text, a transcript or a prompt.

import (
	"context"
	"errors"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/oplog"
)

// logSave logs a save of what (voice, story, dub…) id: a create, a
// rename (old is the name before) or nothing for an update that kept
// the name. kv are more pairs.
func logSave(ctx context.Context, what string, created bool, old, id, name string, err error, kv ...any) {
	ev := what + ".update"
	switch {
	case created:
		ev = what + ".create"
	case old != "" && old != name:
		ev = what + ".rename"
		kv = append(kv, "from", old)
	case err == nil:
		return // the name kept: autosaves and edits are not logged
	}
	logOp(ctx, ev, id, name, err, kv...)
}

// logDelete logs a delete of what id.
func logDelete(ctx context.Context, what, id, name string, err error, kv ...any) {
	logOp(ctx, what+".delete", id, name, err, kv...)
}

// logOp logs event for id (named name when known): at Info when err is
// nil, else at Warn with the error ("not found" at Info: nothing to do).
func logOp(ctx context.Context, event, id, name string, err error, kv ...any) {
	pairs := []any{"id", id}
	if name != "" {
		pairs = append(pairs, "name", name)
	}
	pairs = append(pairs, kv...)
	pairs = append(pairs, "by", oplog.Actor(ctx))
	if err != nil {
		pairs = append(pairs, "error", err)
		if errors.Is(err, core.ErrNotFound) {
			oplog.Info(event, pairs...)
			return
		}
		oplog.Warn(event, pairs...)
		return
	}
	oplog.Info(event, pairs...)
}
