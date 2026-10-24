package main

import (
	"testing"
	"testing/fstest"

	"github.com/hehehai/actnowkit/sounds"
)

// testSoundsFS is a catalogue of a few of the family's sounds (fake
// audio), as resources/sounds holds the app's.
func testSoundsFS() fstest.MapFS {
	const cat = `{"schema": "actnow.sounds.catalog/1", "version": "test",
 "groups": [{"id": "tap", "name": {"en": "Tap", "zh": "轻触"}}, {"id": "media", "name": {"en": "Media", "zh": "媒体"}}, {"id": "notify", "name": {"en": "Notify", "zh": "通知"}}],
 "tags": [{"id": "soft", "name": {"en": "Soft", "zh": "柔和"}, "featured": true}, {"id": "short", "name": {"en": "Short", "zh": "简短"}, "featured": true}],
 "uses": [{"id": "record.start", "name": {"en": "Start recording", "zh": "开始录音"}}, {"id": "record.stop", "name": {"en": "Stop recording", "zh": "结束录音"}}, {"id": "error", "name": {"en": "Error", "zh": "错误"}}],
 "sounds": [
  {"id": "tally", "file": "sounds/tally.wav", "name": {"en": "Tally", "zh": "开录"}, "group": "media", "tags": ["short"], "duration": 0.45, "audible": 0.35, "uses": ["record.start"], "pair": "wrap"},
  {"id": "wrap", "file": "sounds/wrap.wav", "name": {"en": "Wrap", "zh": "收工"}, "group": "media", "tags": ["soft"], "duration": 0.47, "audible": 0.37, "uses": ["record.stop"], "pair": "tally"},
  {"id": "pebble", "file": "sounds/pebble.wav", "name": {"en": "Pebble", "zh": "卵石"}, "group": "tap", "tags": ["soft", "short"], "duration": 0.23, "uses": ["record.start"], "replaces": ["Pop"]},
  {"id": "tink", "file": "sounds/tink.wav", "name": {"en": "Tink", "zh": "叮"}, "group": "tap", "tags": ["short"], "duration": 0.16, "uses": ["record.stop"], "replaces": ["Tink"]},
  {"id": "shrug", "file": "sounds/shrug.wav", "name": {"en": "Shrug", "zh": "耸肩"}, "group": "notify", "tags": ["soft"], "duration": 0.5, "uses": ["error"]},
  {"id": "chime", "file": "sounds/chime.wav", "name": {"en": "Chime", "zh": "风铃"}, "group": "notify", "tags": ["soft"], "duration": 1.44}
 ]}`
	fs := fstest.MapFS{"sounds/catalog.json": {Data: []byte(cat)}}
	for _, id := range []string{"tally", "wrap", "pebble", "tink", "shrug", "chime"} {
		fs["sounds/sounds/"+id+".wav"] = &fstest.MapFile{Data: []byte("RIFF" + id)}
	}
	return fs
}

// useTestSounds gives dictation the test catalogue, a player whose
// output records what it plays, and macOS's system sounds.
func useTestSounds(t testing.TB, a *app) *sounds.Recorder {
	t.Helper()
	cat, err := sounds.Load(testSoundsFS(), "sounds")
	if err != nil {
		t.Fatal(err)
	}
	rec := &sounds.Recorder{}
	a.dictSoundCat = cat
	a.dictPlayer = sounds.NewPlayer(cat, rec)
	a.dictSysSounds, a.dictSysRead = sounds.KnownSystemSounds, true
	return rec
}
