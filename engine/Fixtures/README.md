# Protocol v2 fixtures

Golden transcripts of the engine protocol: the contract between the Go app and the Swift engine. See [docs/protocol-v2.md](../../docs/protocol-v2.md) §10 for the format and what the tests check.

| File | Shows |
|---|---|
| `handshake.jsonl` | `engine.hello` |
| `tts_stream.jsonl` | `tts.synthesize` with progress and streamed `tts.chunk`s |
| `tts_clone.jsonl` | cloning with sampling parameters and a seed |
| `cancel.jsonl` | `job.cancel` of a running job, and of a finished one |
| `models.jsonl` | `models.load` / `loaded` / `unload`, `engine.stats`, `flush`, `shutdown` |
| `errors.jsonl` | the M0 error codes, and `input_too_long` |
| `voice.jsonl` | `tts.encodeVoice`, a take from a voice prompt, VoiceDesign, CustomVoice + instruct, Chatterbox parameters |
| `stt.jsonl` | `stt.transcribe` (auto language, word timings, translate refused, missing file), `stt.align` |
| `analysis.jsonl` | `vad.detect`, `enhance.run`, `unsupported_media` |
| `media.jsonl` | a `media` worker: hello, `media.probe` (video, WAV, unreadable), `media.decode` of a window, `file_not_found`, a gpu method refused |
| `capture.jsonl` | a `listen` worker: `capture.devices`, a recording with its meter stopped by `capture.stop`, refusals (device, permission), cancel, `maxSeconds` |
| `logs.jsonl` | structured `log` notifications |
| `legacy_v1.jsonl` | the first line of a protocol-1 engine (detected, refused) |

Go: `go test ./internal/engine/proto` (and `TestFixturesCoverMethods`: every method has a fixture). Swift: `FixtureTests.swift` (`scripts/test-engine.sh`).
