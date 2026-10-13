# Engine protocol v2

How AudioSmith (Go) talks to `audiosmith-engine` (Swift). This spec is the contract between the two sides.

| Side | Code |
|---|---|
| Go wire types | [`internal/engine/proto`](../internal/engine/proto) |
| Go supervisor (processes, handshake, routing, restarts) and job executors | [`internal/engine`](../internal/engine) (`supervise.go`, `executors.go`) |
| Swift wire types (`Codable`, no dependencies) | [`engine/Sources/AudioSmithProtocol`](../engine/Sources/AudioSmithProtocol) |
| Swift engine (session, jobs, models) | [`engine/Sources/AudioSmithEngine`](../engine/Sources/AudioSmithEngine) (`Server.swift`, `Synthesizer.swift`) |
| Golden transcripts, decoded by both sides | [`engine/Fixtures/`](../engine/Fixtures) |

Protocol v1 (the MVP: flat `cmd`/`event` objects, one job, cancel without an id) is gone from both sides since M0; §9 explains how a stale v1 engine is recognised.

## 1. Goals

- **Many jobs, addressed by id.** Every long-running request is a job. Its id is the request id; progress, streamed audio, its answer and cancellation all refer to that id.
- **Fail loudly on mismatch.** A handshake checks the protocol version and the role before anything else runs.
- **Typed errors** the app can act on: download the model, accept a licence, retry, report a bug.
- **Streaming.** Audio chunks arrive while a take is still generating.
- **Stateless engine.** The engine holds models and runs computations. Queues, persistence, licences and the library live in Go.

## 2. Transport

| Item | Rule |
|---|---|
| Process | One Swift binary started as `audiosmith-engine --role gpu\|listen\|media`. Without `--role`, the role is `gpu`. The app starts `gpu` with the window, and `listen` and `media` with the first request for them (`engine.Config.OnDemand`). One-shot CLI flags (`--text …`) still work for smoke tests. |
| Channels | The app writes requests to the engine's **stdin**. The engine writes responses and notifications to its **stdout**. **stderr** is free-form text that the app appends to `engine-<role>.log` and keeps the last 40 lines of, for crash reports. |
| Framing | One JSON object per line, UTF-8, ended by `\n`. The JSON encoder must escape newlines inside strings, which every JSON encoder does by default. Max line: 4 MiB (`proto.MaxLine`). |
| stdout hygiene | stdout carries only protocol lines. The engine dups the real stdout for itself and points fd 1 at stderr, so stray prints from libraries can't corrupt the stream (the v1 engine already does this). |
| Paths | Files are passed as absolute paths. The app owns every directory it names. Inputs are read-only; outputs are written to a temporary name in the same directory, then renamed. |
| End | When stdin closes, the engine cancels its jobs and exits 0. `engine.shutdown` does the same after it answers. If it does not exit within 5 s, the app kills it. |
| Environment | The app may add variables (`engine.Config.Env`), e.g. proxies. `HF_TOKEN` is used for downloads while the engine still downloads (§4.7). `AUDIOSMITH_MAX_MODELS` caps resident models (default 3). |

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
   - **Job requests** (`models.load`, `tts.synthesize`, `tts.encodeVoice`, `stt.transcribe`, `stt.align`, `vad.detect`, `enhance.run`, `media.decode`, `capture.start`; `proto.JobMethods`) run long, may send notifications, and can be cancelled.
   - **Control requests** (all others) answer promptly, even while jobs run, and cannot be cancelled.
3. **Concurrency.** The engine runs at most `hello.result.concurrency` jobs at once (`gpu` 1, `listen` 1, `media` 2, matching the scheduler's lanes). Another job request gets `busy`, which is retryable.
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
   - A job ends only when its computation has: the engine must not take the next job while a cancelled one still runs on the GPU. Models that make a whole take in one call (Kokoro, Kitten) run in the job's own task, so cancelling them waits for the take (about 2 s for 25 s of audio on an M4). Streaming models stop at their next step; the engine waits 200 ms for it.
   - The app's supervisor gives a cancelled job 5 s to end, then kills and restarts the worker.
7. **Models.** Job methods load the model they need if it is not resident, reporting stage `loading`.
   - With `hello.offline: true` they never download: missing files fail with `model_not_downloaded`, and the Go hub owns downloads.
   - The app sends `offline: true` (since integration I5). The engine executors fill each job's local snapshot directory (`models.load` `path`, `tts.synthesize` `modelPath`, from `hub.Status` over the catalogue's pinned commit) as the job starts; a model that is not downloaded fails the job with `model_not_downloaded` before the engine sees it.
   - `models.load` with a `path` (and `tts.synthesize` with a `modelPath`) loads that snapshot directory.
   - Families mlx-audio-swift cannot load from a directory (Marvis, OmniVoice, Spark in the pin) fall back to resolving the repo id in `hello.cacheDir`, where the hub put the same snapshot (standard layout; for a pinned commit it also writes `refs/main`, so swift-huggingface finds it without the network). Auxiliary repos (Kokoro's G2P) are always resolved that way.
   - Without a path, the engine resolves the repo in `hello.cacheDir` (it sets `HF_HUB_CACHE` to it). A repo counts as present when `<cacheDir>/mlx-audio/<owner>_<name>/config.json` exists or `<cacheDir>/models--<owner>--<name>/snapshots/` has a non-empty snapshot.
   - The engine keeps several models resident (`AUDIOSMITH_MAX_MODELS`, least recently used first out). The supervisor polls `engine.stats` while jobs run and after each, and unloads the least recently used idle models when all workers' models together exceed `engine.Config.MemoryBudget`.

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
- `families` are the model families it can load. They match `catalog.Entry.Family`. The M0 engine reports `kokoro`, `kitten`, `qwen3_tts`, `qwen3`, `marvis`, `soprano`, `omnivoice`, `chatterbox`, `pocket_tts`, `llama_tts`, `fish_speech`, `echo_tts`, `irodori_tts`, `moss_tts`, `moss_tts_nano`, `indextts`, `spark`, `breeze` (role `gpu`; other roles report none).
- Roles: every role implements `engine.hello`, `engine.stats`, `engine.flush`, `engine.shutdown` and `job.cancel`. On top of those (`proto.RoleOf`):
  - `gpu`: `models.*`, `tts.synthesize`, `tts.encodeVoice`, `stt.transcribe`, `stt.align`, `vad.detect`, `enhance.run` — everything that runs an MLX model;
  - `media`: `media.probe`, `media.decode` — AVFoundation, no MLX; concurrency 2;
  - `listen`: `capture.devices`, `capture.start`, `capture.stop` — the microphone (M2 adds streaming ASR here).
  A method sent to another role is answered `method_not_found`.
- **Why capture lives in `listen`.** A recording is long and interactive. In the `listen` process it survives the gpu worker being restarted (OOM, a stuck cancel) or busy with a long render, and M2's dictation, which uses the same microphone, runs in the same process. `media` does batch file work that may run two at a time; mixing a live device into it would make its concurrency mean nothing.

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
→ {"id":"01JA…","method":"tts.synthesize","params":{"model":"mlx-community/Kokoro-82M-bf16","modelPath":"/…/snapshots/a71e4d38…","text":"Hello from AudioSmith.","voice":"af_heart","speed":1.25,
   "output":{"path":"/…/clips/01JA….wav","format":"wav","bits":16},"stream":true}}
← job.progress {stage:"loading"} … {stage:"generating", fraction, audioSeconds} … {stage:"writing"}
← tts.chunk {seq:0 …} … {seq:n, final:true}          (only with "stream":true)
← {"id":"01JA…","result":{"output":"…","model":"…","sampleRate":24000,"channels":1,"frames":19200,"audioSeconds":0.8,"seconds":0.2,"rtf":4}}
```

Parameters left unset use the model's defaults:
- `speed`, `temperature`, `topP` and `seed` are pointers on the Go side;
- `maxTokens`, `steps`, `instruct` and `duration` count as unset when absent.

OmniVoice takes its own parameters through the same fields: `steps` (diffusion steps, default 32), `duration` (target seconds; the take comes out that long), `speed`, `seed`; `voice` is a design description ("female, young adult, british accent"), and `refAudio`/`refText` clone. It runs in the job's task and stops at the next diffusion step when cancelled.

What `voice`, `instruct` and the clone fields mean, by model (catalogue capabilities in brackets):

| Field | Meaning |
|---|---|
| `voice` | A built-in speaker id (Kokoro `af_heart`, CustomVoice `Vivian`). For design models [`design`, `voiceStyle`] — Qwen3-TTS VoiceDesign (free text, English or Chinese) and OmniVoice (attribute tags) — the voice description, as `voicedesign.Lowered.Voice` makes it. CustomVoice also takes `"Speaker, instruction"`. |
| `instruct` | A style or emotion instruction [`instruct`]. CustomVoice appends it to the speaker (`Vivian` + `用非常开心的语气说`); design models use it as the description when `voice` is empty. |
| `language` | The catalogue's `Language.Value` for the model ("Chinese", "en"…). |
| `refAudio`, `refText` | Clone reference and its transcript [`clone`]. Any format AVFoundation reads. |
| `voicePrompt` | A file `tts.encodeVoice` wrote for this model [`voicePrompt`]. It wins over `refAudio`. If it cannot be used (made for another model, an older format), the engine logs a warning and uses `refAudio`; without `refAudio` it fails with `bad_request`. The app therefore sends both. |
| `guidance` | Classifier-free guidance scale [`guidance`]: OmniVoice `guidance_scale`, Chatterbox `cfg_weight`. |
| `exaggeration` | Chatterbox's emotion exaggeration [`exaggeration`]. |
| `steps`, `duration` | Diffusion steps [`steps`], target seconds [`duration`]. |

**Text limits.** A model whose input is bounded per request (Kokoro: 510 phonemes) fails an over-long text with `input_too_long`, `data: {"limit", "actual", "unit": "phonemes"|"chars"|"tokens"}`. The app splits scripts into chunks below `catalog.Entry.MaxChars` (measured with `catalog.TextLength`), so this error means a chunk was still too long.

A model that cannot honour a parameter ignores it, unless that parameter is the point of the request: `refAudio` on a model that cannot clone fails with `unsupported`, `data.capability:"clone"`.

`result.seed` is the seed actually used, so the take can be reproduced: the engine seeds MLX's generator for every take, with a random seed when `seed` is unset.

`output.bits` is 16 or 24 (PCM) or 32 (IEEE float); the file is mono WAV. With `stream`, chunks hold up to 1 s of audio each, and a last empty chunk carries `final:true` (the engine only knows the take ended after the last audio). The chunks' samples add up to `result.frames`.

### `job.cancel` (control)

Takes `{job}` and returns `{cancelled:bool}`. See §4.6.

## 5.2 Methods added in M1

Types: [`m1.go`](../internal/engine/proto/m1.go) (Go), [`WireM1.swift`](../engine/Sources/AudioSmithProtocol/WireM1.swift). Fixtures: `stt.jsonl`, `analysis.jsonl`, `media.jsonl`, `capture.jsonl`, `voice.jsonl`.

Shared rules:
- **Inputs** (`audio`, `input`, `refAudio`) are absolute paths to anything AVFoundation reads: WAV, AIFF, CAF, MP3, M4A/AAC, ALAC, FLAC, and the audio of MOV/MP4/M4V. The engine converts to what the model needs. A missing file fails with `file_not_found` (`data.path`), an unreadable one with `unsupported_media` (`data.path`).
- **Outputs** are WAV (`output` as for `tts.synthesize`: `path`, `bits` 16/24/32f), written to a temporary name and renamed. Results describe them with `output`, `sampleRate`, `channels`, `frames`, `audioSeconds` (`proto.AudioOut`).
- **Times** are seconds from the start of the input.
- **Language codes are BCP-47** (`internal/lang`: `en`, `zh`, `yue`, `pt-BR`); the engine maps them to the model's names. (`tts.*` keep the catalogue's `Language.Value`.)
- **Model jobs** take `model` and `modelPath` like `tts.synthesize`; the executors fill `modelPath` and check licences, and the engine reports stage `loading` when it loads.
- **Stages** of `job.progress`: `transcribing`, `aligning`, `detecting`, `enhancing`, `decoding`, `encoding`, `recording` (besides `loading`, `generating`, `writing`).
- **In the app**, the job methods are job kinds (`engine.ExecutorKinds`) submitted on the lane named like their role: `jobs.Lane(proto.RoleOf(kind))`. `media.probe`, `capture.devices` and recordings bypass the queue: `engine.Probe`, `engine.Devices`, `engine.StartCapture`.

### `tts.encodeVoice` (job, gpu)

Encodes a clone reference once (speaker embedding, codec tokens… whatever the model conditions on) into a file, so later takes skip that step.

```json
→ {"id":"01JB…","method":"tts.encodeVoice","params":{"model":"mlx-community/Qwen3-TTS-12Hz-1.7B-Base-8bit","modelPath":"/…","refAudio":"/…/reference.wav","refText":"This is what my voice sounds like.","language":"English","promptPath":"/…/voices/01JB…/qwen3-tts-1.7b-base-8bit.voiceprompt"}}
← job.progress {stage:"loading"} … {stage:"encoding"}
← {"id":"01JB…","result":{"voicePrompt":"/…/qwen3-tts-1.7b-base-8bit.voiceprompt","model":"…","format":"qwen3_tts/1","bytes":182400,"audioSeconds":12.5,"seconds":0.6}}
```

- The file's content is the engine's business; `format` names the family and version. Its metadata records the model, so a prompt is never applied to another one (see `voicePrompt` above).
- The app keeps prompts next to the voice, one per model, and makes them again after a model update.
- A model that cannot (catalogue capability `voicePrompt` unset) answers `unsupported`, `data:{"capability":"voicePrompt"}`. Cloning then sends `refAudio` every time.

### `stt.transcribe` (job, gpu)

```json
→ {"id":"01JB…","method":"stt.transcribe","params":{"model":"mlx-community/whisper-large-v3-turbo-4bit","modelPath":"/…","audio":"/…/reference.wav","language":"auto","prompt":"AudioSmith, MLX"}}
← job.progress {stage:"transcribing", fraction, audioSeconds} …
← {"id":"01JB…","result":{"model":"…","text":"This is what my voice sounds like. …","language":"en",
   "segments":[{"start":0,"end":30,"text":"This is what my voice sounds like."},…],"audioSeconds":42.5,"seconds":3.1,"rtf":13.7}}
```

| Param | Meaning |
|---|---|
| `language` | BCP-47, or `""`/`"auto"` to detect. `result.language` is the one detected (or given). |
| `task` | `"transcribe"` (default) or `"translate"` (into English; Whisper only, else `unsupported` with capability `translate`). |
| `words` | Ask for word timings. Only models with catalogue `wordTimes` give them (`segment.words`: `text`, `start`, `end`, optional `prob`); others ignore it: run `stt.align` on the text. |
| `prompt` | Context or vocabulary (Whisper's initial prompt, Qwen3-ASR's context); ignored by models without one. |

Segments are in time order and cover `text`. Whisper gives about one per 30 s window, Qwen3-ASR one per utterance.

### `stt.align` (job, gpu)

`{model, modelPath, audio, text, language?}` → `{model, words:[{text,start,end}], audioSeconds, seconds}`. Qwen3-ForcedAligner: words in order, one per character for Chinese and Japanese; punctuation is not a word. The app maps words back onto its lines (subtitles, script chunks).

### `vad.detect` (job, gpu)

`{model, modelPath, audio, threshold?, minSpeech?, minSilence?, pad?}` → `{model, regions:[{start,end,prob}], audioSeconds, seconds}`. Silero VAD; the defaults are threshold 0.5, minSpeech 0.25 s, minSilence 0.1 s, pad 0.03 s. Regions are in order and do not overlap. M1 uses it to pick the best 5–15 s reference window; M2's dictation runs its own VAD in `listen`.

### `enhance.run` (job, gpu)

`{model, modelPath, audio, output, strength?}` → `{output, sampleRate, channels, frames, audioSeconds, model, seconds}`. DeepFilterNet v3 (the `v3/` folder of `mlx-community/DeepFilterNet-mlx`). The output is mono at the model's rate (48 kHz); the app resamples if it needs another. `strength` (0–1, default 1) mixes the enhanced signal over the original, to keep some room tone.

### `media.probe` (control, media)

```json
→ {"id":"c2","method":"media.probe","params":{"input":"/Users/me/Movies/interview.mov"}}
← {"id":"c2","result":{"format":"mov","duration":754.32,"hasAudio":true,"hasVideo":true,"audioTracks":1,"codec":"aac","sampleRate":48000,"channels":2,"bitRate":256000,
   "video":{"codec":"hevc","width":3840,"height":2160,"fps":29.97}}}
```

Audio fields describe the first audio track; `bits` is set for PCM only.

### `media.decode` (job, media)

`{input, output, sampleRate?, channels?, start?, duration?, track?}` → `{output, sampleRate, channels, frames, audioSeconds, source, seconds}`. Decodes a window (`start`, `duration`; 0 = to the end) of an audio track (`track`, default 0) to WAV at `sampleRate`/`channels` (0 keeps the source's; 1 mixes down, 2 duplicates mono). `source` is the input's `media.probe` result. Progress stage `decoding`.

### `capture.devices` (control, listen)

Returns `{devices:[{id, name, channels, sampleRate, default?, transport?}]}`, the system default first. `id` is the Core Audio device UID (stable across launches and reconnections); `transport` is `builtin`, `usb`, `bluetooth`, `aggregate`, `virtual` or absent.

### `capture.start` (job, listen) → `capture.level` … → `capture.stop` (control)

```json
→ {"id":"01JB…","method":"capture.start","params":{"device":"AppleUSBAudioEngine:…","inputChannels":[2],"channels":1,"sampleRate":48000,"output":{"path":"/…/recordings/01JB….wav","bits":24},"maxSeconds":120}}
← {"method":"job.progress","params":{"job":"01JB…","stage":"recording"}}
← {"method":"capture.level","params":{"job":"01JB…","seconds":0.05,"rms":-52.3,"peak":-40.1}}   … about 20 per second
→ {"id":"c3","method":"capture.stop","params":{"job":"01JB…"}}
← {"id":"01JB…","result":{"output":"/…/01JB….wav","sampleRate":48000,"channels":1,"frames":9600,"audioSeconds":0.2,"device":"AppleUSBAudioEngine:…","peak":-0.0001,"clipped":3,"reason":"stopped"}}
← {"id":"c3","result":{"stopped":true}}
```

- **A recording is a job** whose response comes when it ends, so its meter (`capture.level`, which names the job) and its result follow the usual job rules: notifications first, nothing after the response.
- **Params.** `device` is a device `id` (`""` = the system default input). `inputChannels` are the device's channels numbered from 1, as in Audio MIDI Setup (default `[1]`, or `[1,2]` for stereo); with `channels` 1 several are mixed down. `sampleRate` is the file's (default: the device's). `maxSeconds` stops it by itself (0 = no limit).
- **Start.** `job.progress {stage:"recording"}` is sent once the device runs. The first time, macOS asks for microphone access before that; refusal fails the job with `permission_denied`, `data:{"permission":"microphone"}`. An unknown or busy device fails with `device_unavailable`, `data:{"device"}`.
- **Meter.** `capture.level` about 20 times a second: `seconds` recorded so far, `rms` and `peak` in dBFS (at least −120) over the recorded channels, `clipped` when a sample reached full scale since the last one.
- **End.** `capture.stop {job}` finalises the file; the job answers with the result **before** the stop request is answered `{stopped:true}` (`false` if the job had already ended). It also ends by itself: `reason` is `stopped`, `maxSeconds`, or `deviceLost` (unplugged; what was recorded is kept). `job.cancel` discards the file and the job ends `cancelled`.
- **Result.** The `proto.AudioOut` fields, the `device` used, the loudest `peak` (dBFS) and `clipped`, the number of samples at full scale.
- **In the app**, `engine.StartCapture` returns once `recording` arrives; `Recording.Stop` sends `capture.stop` and returns the job's result; `Recording.Cancel` cancels.

## 6. Notifications (engine → app)

| Method | Params | Notes |
|---|---|---|
| `job.progress` | `job`, `stage`, `fraction?`, `audioSeconds?`, `message?` | `stage` is an open set: `loading`, `generating`, `writing`, … Omit `fraction` when it is unknown; the UI then shows an indeterminate bar. `message` is English, for logs and the status line. At most about 10 per second per job. |
| `tts.chunk` | `job`, `seq`, `sampleRate`, `channels`, `encoding`, `data`, `final?` | `encoding` is `"pcm16le"` (the only one in M0). `data` is base64 (Go `[]byte`, Swift `Data`). Send chunks of up to about 1 s of audio. The last chunk has `final:true` and may be empty. |
| `log` | `level`, `message`, `job?` | Structured log line for the Logs panel. `level` is `debug`, `info`, `warning` or `error`. It may name any job. |
| `capture.level` | `job`, `seconds`, `rms`, `peak`, `clipped?` | The input meter of a `capture.start` job, about 20 per second (§5.2). |

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
| `input_too_long` (M1) | The text is over the model's per-request limit | `{"limit","actual","unit"}` | split it (the app's chunker should have) |
| `file_not_found` (M1) | An input path does not exist | `{"path"}` | say which file is gone |
| `unsupported_media` (M1) | An input exists but cannot be decoded | `{"path"}` | "convert it to WAV, M4A or MP3" |
| `permission_denied` (M1) | macOS refused a permission | `{"permission":"microphone"}` | point to System Settings → Privacy & Security |
| `device_unavailable` (M1) | The capture device is unknown, gone or busy | `{"device"}` | refresh the device list |

The app treats an unknown code as `internal` (so an older app reads the M1 codes as `internal`). `proto.Codes` lists them all; `proto.Retryable(code)` the retryable ones, and the job scheduler uses it. `jobErrorText` (enginelink.go) translates them for the UI.

## 8. Reserved methods (later milestones)

These names are taken; `proto.Reserved` lists them. An engine that does not implement one answers `method_not_found`. Each one gets params, results and fixtures in the milestone that adds it.

| Family | Methods | Role | Milestone |
|---|---|---|---|
| TTS | `tts.batch` (many segments, one warm model) | gpu | M3 |
| STT | streaming `stt.stream.open/push/close` → `stt.partial` / `stt.final` | listen | M2 |
| Analysis | `diarize.run`, `lid.detect`, `separate.run` | gpu | M2–M4 |
| LLM | `llm.generate`, `translate.run` | gpu | M2 / M4 |
| Media | `media.encode`, `media.mux`, `media.timePitch`, `media.thumbnail`, `media.ocr`, `doc.extractText` | media | M3 / M4 |
| Capture | `capture.pcm` (raw input for streaming ASR) | listen | M2 |

M1 implemented `tts.encodeVoice`, `stt.transcribe`, `stt.align`, `vad.detect`, `enhance.run`, `media.probe`, `media.decode` and `capture.devices/start/stop/level` (§5.2).

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

**Swift side:** [`FixtureTests.swift`](../engine/Tests/AudioSmithEngineTests/FixtureTests.swift) decodes every `msg` with the Swift `Codable` types, re-encodes it and compares it with the fixture, so the Swift types carry every field. Run it with `scripts/test-engine.sh` (xcodebuild, on the Mac).

**Supervisor:** `TestSupervisorReplaysFixtures` (`go test ./internal/engine`) runs a scripted engine ([`enginetest`](../internal/engine/enginetest)) that replays `tts_stream`, `tts_clone`, `models`, `cancel`, `stt`, `analysis` and `voice`, and checks that `Supervisor.Call` returns the fixture's results, errors and notifications in order; the cancel transcript is driven by cancelling the call's context.

**Real engine:** `AUDIOSMITH_E2E=1 go test -run TestRealEngineConformance ./internal/engine` speaks raw protocol 2 with the built engine: handshake errors, every error code it can produce without special models, `busy`, control requests during a job, cancel by id, gapless chunks, shutdown and stdin EOF.

`TestFixturesCoverMethods` fails when a method has no fixture.

**Fake engine for app tests:** besides replaying, [`enginetest`](../internal/engine/enginetest) serves every method in the role it is started with, reading and writing real WAV files, so the app's code can be tested end to end on Linux (`TestM1Executors`, `TestCapture`). [`fake_m1.go`](../internal/engine/enginetest/fake_m1.go) says what each method does with its inputs (e.g. a transcript comes from `<audio>.txt`). View tests use the lighter fakes in `enginefake_test.go`, where the test finishes jobs itself.

Every engine change that touches the wire adds or updates a fixture in the same commit.

Changing a fixture is changing the contract: both test suites must pass.
