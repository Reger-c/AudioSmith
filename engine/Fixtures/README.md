# Protocol v2 fixtures

Golden transcripts of the engine protocol: the contract between the Go app and the Swift engine. See [docs/protocol-v2.md](../../docs/protocol-v2.md) §10 for the format and what the tests check.

| File | Shows |
|---|---|
| `handshake.jsonl` | `engine.hello` |
| `tts_stream.jsonl` | `tts.synthesize` with progress and streamed `tts.chunk`s |
| `tts_clone.jsonl` | cloning with sampling parameters and a seed |
| `cancel.jsonl` | `job.cancel` of a running job, and of a finished one |
| `models.jsonl` | `models.load` / `loaded` / `unload`, `engine.stats`, `flush`, `shutdown` |
| `errors.jsonl` | every error code the engine sends |
| `logs.jsonl` | structured `log` notifications |
| `legacy_v1.jsonl` | the first line of a protocol-1 engine (detected, refused) |

Go: `go test ./internal/engine/proto`. Swift: `FixtureTests.swift` (thread B).
