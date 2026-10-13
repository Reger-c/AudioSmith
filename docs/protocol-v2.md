# Engine protocol v2

How AudioSmith (Go) talks to `audiosmith-engine` (Swift). This spec is the contract between the two sides. The Go types are in [`internal/engine/proto`](../internal/engine/proto). The golden transcripts in [`engine/Fixtures/`](../engine/Fixtures) are decoded by the Go tests now, and the Swift tests must decode them too. Thread B implements the Swift side and the Go supervisor.

Protocol v1 (the MVP: flat `cmd`/`event` objects, one job, cancel without an id) stays in use until the Studio runs on jobs. §9 explains how the two versions are told apart.

## 1. Goals

- **Many jobs, addressed by id.** Every long-running request is a job. Its id is the request id; progress, streamed audio, its answer and cancellation all refer to that id.
- **Fail loudly on mismatch.** A handshake checks the protocol version and the role before anything else runs.
- **Typed errors** the app can act on: download the model, accept a licence, retry, report a bug.
- **Streaming.** Audio chunks arrive while a take is still generating.
- **Stateless engine.** The engine holds models and runs computations. Queues, persistence, licences and the library live in Go.

## 2. Transport

| Item | Rule |
|---|---|
| Process | One Swift binary started as `audiosmith-engine --role gpu\|listen\|media`. In M0 only `gpu` runs. Without `--role`, the role is `gpu`. One-shot CLI flags (`--text …`) still work for smoke tests. |
| Channels | The app writes requests to the engine's **stdin**. The engine writes responses and notifications to its **stdout**. **stderr** is free-form text that the app appends to `engine-<role>.log` and keeps the last 40 lines of, for crash reports. |
| Framing | One JSON object per line, UTF-8, ended by `\n`. The JSON encoder must escape newlines inside strings, which every JSON encoder does by default. Max line: 4 MiB (`proto.MaxLine`). |
| stdout hygiene | stdout carries only protocol lines. The engine dups the real stdout for itself and points fd 1 at stderr, so stray prints from libraries can't corrupt the stream (the v1 engine already does this). |
| Paths | Files are passed as absolute paths. The app owns every directory it names. Inputs are read-only; outputs are written to a temporary name in the same directory, then renamed. |
| End | When stdin closes, the engine cancels its jobs and exits 0. `engine.shutdown` does the same after it answers. If it does not exit within 5 s, the app kills it. |

## 3. Messages

There are three shapes, all JSON-RPC 2.0 in form:

```text
request       {"id":"…","method":"…","params":{…}}          app → engine
notification  {"method":"…","params":{…}}                   engine → app (none app → engine in M0)
response      {"id":"…","result":{…}}                       engine → app
              {"id":"…","error":{"code":"…","message":"…","data":{…}}}
```

Differences from JSON-RPC 2.0:
- there is no `"jsonrpc"` member;
- **ids are strings**;
- **error codes are strings** (§7);
- the engine never sends requests.

`params` may be omitted when empty. `result` is always present on success; it is `{}` when there is nothing to return.

Ids are chosen by the app and are unique among the requests in flight to one process.
- **Job requests** use the scheduler's job id, a ULID, so logs, notifications and the database all correlate.
- **Control requests** use `c1`, `c2`, …

Unknown members must be ignored by both sides, so optional fields can be added within protocol 2. Missing optional members take their documented default.

## 4. Session and sequencing

1. **Handshake.** The app sends `engine.hello` right after spawning. Until it is answered, every other request fails with `not_initialized`.
   - If `params.protocol` is a version the engine does not speak, the engine answers `protocol_mismatch` with `data.supported`.
   - The app refuses an engine whose `result.protocol` ≠ 2 or whose `result.role` differs from the role it spawned. It shows "Engine version mismatch: rebuild audiosmith-engine" and does not restart it.
   - The handshake has a 30 s timeout. The first launch compiles Metal shaders.
2. **Two kinds of request.**
   - **Job requests** (`models.load`, `tts.synthesize`, and later the task methods of §8) run long, may send notifications, and can be cancelled.
   - **Control requests** (all others) answer promptly, even while jobs run, and cannot be cancelled.
3. **Concurrency.** The engine runs at most `hello.result.concurrency` jobs at once (`gpu`: 1). Another job request gets `busy`, which is retryable.
   - The Go scheduler is the only queue: it never sends more jobs than the engine can run.
   - The engine does not queue jobs.
4. **Ordering.**
   - All of a job's notifications are written **before** its response.
   - Nothing about a job is written after its response.
   - Responses to different ids may come in any order.
   - `tts.chunk` `seq` starts at 0 and has no gaps.
5. **Exactly one response.** Every request gets exactly one response, unless the process dies first. When it dies, the app fails all calls in flight as *interrupted* (`engine.ErrWorkerExited`).
6. **Cancellation.** `job.cancel {job}` answers at once with `{cancelled: true}` if the job was running, or `false` if it is unknown or already finished.
   - The job itself then ends with error `cancelled`.
   - The engine checks for cancellation between generation chunks, as v1 does.
   - The app's supervisor gives a cancelled job 5 s to end, then kills and restarts the worker.
7. **Models.** Job methods load the model they need if it is not resident, reporting stage `loading`.
   - With `hello.offline: true` they never download: missing files fail with `model_not_downloaded`, and the Go hub owns downloads.
   - The app sends `offline: true` once the hub's downloads are wired in (integration I4 in [architecture.md](architecture.md)). Until then it sends `false`, and the engine downloads on first use as v1 does.
   - `models.load` with a `path` loads that snapshot directory.
   - Without a path, the engine resolves the repo in `hello.cacheDir`.

## 5. Requests

Every field is listed in [`methods.go`](../internal/engine/proto/methods.go). The fixtures show every method on the wire.

### `engine.hello` (control)

```json
→ {"id":"c1","method":"engine.hello","params":{"protocol":2,"app":"0.2.0","role":"gpu","cacheDir":"/Users/me/.cache/huggingface/hub","offline":true,"logLevel":"info"}}
← {"id":"c1","result":{"protocol":2,"version":"0.2.0","role":"gpu","pid":4242,"concurrency":1,
   "methods":["engine.hello","engine.stats","engine.flush","engine.shutdown","models.load","models.unload","models.loaded","tts.synthesize","job.cancel"],
   "families":["kokoro","qwen3_tts","marvis","soprano"],
   "device":{"name":"Apple M2 Pro","memory":34359738368,"os":"macOS 15.1","mlx":"0.31.6"}}}
```

- `methods` lists the requests this build and role implement. The app hides features whose methods are missing.
- `families` are the model families it can load. They match `catalog.Entry.Family`.

### `engine.stats` (control)

Returns:
- MLX memory (`active`, `cache`, `peak`, in bytes);
- the resident models, each with `idleSeconds` (for LRU eviction);
- the ids of running jobs.

The supervisor polls it while jobs run, and on demand for the ⌘E switcher.

### `engine.flush` (control)

Drops MLX caches (`MLX.GPU.clearCache()`) and returns `{}`.

### `engine.shutdown` (control)

Cancels running jobs, answers `{}`, then exits 0.

### `models.load` (job)

```json
→ {"id":"01JA…","method":"models.load","params":{"model":"mlx-community/Kokoro-82M-bf16","path":"/…/snapshots/0123abcd","task":"tts"}}
← {"method":"job.progress","params":{"job":"01JA…","stage":"loading","message":"Loading Kokoro-82M-bf16"}}
← {"id":"01JA…","result":{"model":"…","task":"tts","sampleRate":24000,"seconds":1.5,"bytes":390000000,"voices":["af_heart",…],"capabilities":["speed","stream"]}}
```

The result confirms or extends the catalogue entry: sample rate, the voices and languages the model reports, and capabilities named as in `catalog.Capability`.

### `models.unload` (control)

Takes `{model}` to unload one model, or `{}` to unload all. Returns `{unloaded:[…]}`.

### `models.loaded` (control)

Returns `{models:[…]}`.

### `tts.synthesize` (job)

```json
→ {"id":"01JA…","method":"tts.synthesize","params":{"model":"mlx-community/Kokoro-82M-bf16","text":"Hello from AudioSmith.","voice":"af_heart","speed":1.25,
   "output":{"path":"/…/clips/01JA….wav","format":"wav","bits":16},"stream":true}}
← job.progress {stage:"loading"} … {stage:"generating", fraction, audioSeconds} … {stage:"writing"}
← tts.chunk {seq:0 …} … {seq:n, final:true}          (only with "stream":true)
← {"id":"01JA…","result":{"output":"…","model":"…","sampleRate":24000,"channels":1,"frames":19200,"audioSeconds":0.8,"seconds":0.2,"rtf":4}}
```

Parameters left unset use the model's defaults:
- `speed`, `temperature`, `topP` and `seed` are pointers on the Go side;
- `maxTokens`, `steps`, `instruct` and `duration` count as unset when absent.

A model that cannot honour a parameter ignores it, unless that parameter is the point of the request: `refAudio` on a model that cannot clone fails with `unsupported`, `data.capability:"clone"`.

`result.seed` is the seed actually used, so the take can be reproduced.

### `job.cancel` (control)

Takes `{job}` and returns `{cancelled:bool}`. See §4.6.

## 6. Notifications (engine → app)

| Method | Params | Notes |
|---|---|---|
| `job.progress` | `job`, `stage`, `fraction?`, `audioSeconds?`, `message?` | `stage` is an open set: `loading`, `generating`, `writing`, … Omit `fraction` when it is unknown; the UI then shows an indeterminate bar. `message` is English, for logs and the status line. At most about 10 per second per job. |
| `tts.chunk` | `job`, `seq`, `sampleRate`, `channels`, `encoding`, `data`, `final?` | `encoding` is `"pcm16le"` (the only one in M0). `data` is base64 (Go `[]byte`, Swift `Data`). Send chunks of up to about 1 s of audio. The last chunk has `final:true` and may be empty. |
| `log` | `level`, `message`, `job?` | Structured log line for the Logs panel. `level` is `debug`, `info`, `warning` or `error`. It may name any job. |

At 24 kHz mono, PCM16 in base64 is about 64 KB/s. If profiling ever shows that to be costly, a binary side channel (an fd 3 pipe) can be added in a later minor change.

## 7. Errors

| Code | When | `data` | App reaction |
|---|---|---|---|
| `bad_request` | Params malformed or missing | — | bug: report |
| `method_not_found` | Unknown method, or not in this role | — | hide the feature |
| `not_initialized` | Request before `engine.hello` | — | bug |
| `protocol_mismatch` | `hello.protocol` not spoken | `{"supported":[2]}` | "rebuild the engine" |
| `unsupported` | The model lacks the capability | `{"capability":"clone"}` | explain, disable the control |
| `model_not_downloaded` | Offline and files missing | `{"model","missing":[…]}` | offer the download (hub) |
| `model_load_failed` | Files present but unloadable | — | show the message; offer Verify/Repair |
| `licence_required` | Reserved: the app gates licences before sending | `{"licence"}` | licence dialog |
| `oom` | Out of memory | `{"active":…}` | **retryable** after unloading or a restart |
| `busy` | Over `concurrency` | — | **retryable** (scheduler bug if seen) |
| `cancelled` | The job was cancelled | — | none |
| `internal` | Anything else | — | show the message, keep the stderr tail |

The app treats an unknown code as `internal`. `proto.Retryable(code)` lists the retryable ones, and the job scheduler uses it.

## 8. Reserved methods (later milestones)

These names are taken; `proto.Reserved` lists them. An engine that does not implement one answers `method_not_found`. Each one gets params, results and fixtures in the milestone that adds it.

| Family | Methods | Role | Milestone |
|---|---|---|---|
| TTS | `tts.encodeVoice` (clone reference → cached prompt file), `tts.batch` (many segments, one warm model) | gpu | M1 / M3 |
| STT | `stt.transcribe` (segments + words + language), `stt.align`; streaming `stt.stream.open/push/close` → `stt.partial` / `stt.final` | gpu / listen | M2 |
| Analysis | `vad.detect`, `diarize.run`, `lid.detect`, `enhance.run`, `separate.run` | gpu | M1–M4 |
| LLM | `llm.generate`, `translate.run` | gpu | M2 / M4 |
| Media | `media.probe`, `media.decode`, `media.encode`, `media.mux`, `media.timePitch`, `media.thumbnail`, `media.ocr`, `doc.extractText` | media | M3 / M4 |
| Capture | `capture.devices`, `capture.start` → `capture.level` / `capture.pcm`, `capture.stop` | listen | M1 / M2 |

## 9. Versions and compatibility

- **Within protocol 2**, changes are additive only: new methods (announced in `hello.methods`), new optional fields and new notification methods. A peer ignores what it does not know.
- Any other change is **protocol 3**: new `proto.Version`, new fixtures, and the engine answers `protocol_mismatch` to the other version. App and engine ship together in one bundle, so a mismatch only happens with `AUDIOSMITH_ENGINE` overrides or stale dev builds.
- **Telling a v1 engine apart.** A v1 engine writes `{"event":"ready","protocolVersion":1,…}` first, without being asked. `proto.LegacyReady` recognises that line; the supervisor then reports "engine speaks protocol 1: rebuild it" instead of waiting for the hello timeout. `engine/Fixtures/legacy_v1.jsonl` pins this line.

### v1 → v2 mapping

| v1 | v2 |
|---|---|
| `ready` (unsolicited) | `engine.hello` request/response |
| `ping` / `pong` | `engine.stats` |
| `load` → `loaded` | `models.load` (job) → result |
| `synthesize` → `status` / `progress` / `done` | `tts.synthesize` → `job.progress` / `tts.chunk` → result |
| `cancel` (whatever runs) | `job.cancel {job}` |
| `cancelled` event | error `cancelled` on the job's id |
| `unload` → `unloaded` | `models.unload` → result |
| `error {message}` (id optional) | error response with a code, always on an id |
| `shutdown` | `engine.shutdown` |

## 10. Fixtures

[`engine/Fixtures/*.jsonl`](../engine/Fixtures) are golden transcripts. Each line is one of:

```json
{"to":"engine","msg":{…one wire message…}}
{"to":"app","msg":{…}}
```

`legacy_v1.jsonl` instead uses `{"to":"app","line":{…}}` for a raw v1 line.

`go test ./internal/engine/proto` (`TestFixtures`) runs these checks:
- every `msg` is a valid shape for its direction;
- params and results decode into the Go types **with unknown fields rejected**, and re-encode to equal JSON, so the Go types carry every field the fixture has;
- every request is answered exactly once;
- a job's notifications come before its response, and nothing comes after it;
- chunk `seq` has no gaps, `fraction` is within 0–1, and error codes are known;
- unknown methods are answered `method_not_found`.

**Swift side (thread B):**
- add `engine/Tests/AudioSmithEngineTests/FixtureTests.swift` that decodes every `msg` with the Swift `Codable` types and re-encodes it;
- every engine change that touches the wire adds or updates a fixture in the same commit.

Changing a fixture is changing the contract: both test suites must pass.
