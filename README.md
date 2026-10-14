# AudioSmith

A local speech workbench for macOS on Apple silicon: text to speech with saved, cloned and designed voices, stories and audiobooks, video dubbing, transcription and system-wide dictation, a local OpenAI-compatible API and MCP server, and workflows. Every model runs on the Mac's GPU with MLX; nothing leaves the Mac unless you ask.

- **UI**: [MyGo](https://mygo.egoist.dev/) native UI, written in Go (`*.go`).
- **Speech**: `audiosmith-engine`, a Swift helper on [mlx-audio-swift](https://github.com/Blaizzy/mlx-audio-swift) in `engine/`, which runs the models with MLX.

## Features

| Area | What it does |
|---|---|
| Studio | Script editor with markup (pauses, emphasis, `[voice:…]`, non-verbal tags) and document import; model / voice / language / speed / sampling / duration controls that follow the model; long scripts in chunks with a streaming preview and a chunk cache; mastering (loudness, “Even out volume”), pronunciation dictionary, audio check (clipping, clicks, silences); Generate (⌘↩) and Cancel; the output panel (waveform, Play, Save as WAV 16/24/32-bit — and MP3, FLAC, Opus with ffmpeg — drag out, Show in Finder) |
| Voices, Design, Gallery | A voice library: clone from a recording or a file (with a consent statement), design a voice from a description (Qwen3-TTS VoiceDesign, OmniVoice tags), ~1,100 gallery archetypes with previews; portraits, tags, favourites; `.asvoice` files (and VoiceStudio `.ovsvoice` import) |
| Stories, Audiobook, Projects | Multi-voice scripts with a cast; audiobooks from TXT/MD/EPUB/DOCX with chapters, per-chapter loudness, read-along, M4B with chapters and cover (MP3/Opus with chapters with ffmpeg), stems; the Projects library of everything made and exported |
| Dub, Batch | Video and audio dubbing: transcribe, translate (local or cloud language models, DeepL, Google, Microsoft), speaker separation and diarization, voice-cloned lines fitted to the timing (WSOLA, or OmniVoice's target duration), a timeline editor, A/B mixer, video preview; exports WAV, M4A, MP4 with dubbed tracks and soft subtitles, SRT/VTT/ASS, stems — MP3 and burnt-in subtitles with ffmpeg; batches and a watch folder; links via yt-dlp (opt-in) |
| Transcribe, Dictation | Transcripts with word times and speakers (Whisper, Qwen3-ASR, Parakeet), exports SRT/VTT/TXT/DOCX; writing help with a language model; system-wide dictation (⌘⇧Space, toggle or hold) that types into any app, with a pill, the menu bar icon and close-to-tray |
| Local API, MCP, Integrations, Tools | An OpenAI-compatible `/v1` (speech in wav/pcm/aac — mp3/opus/flac with ffmpeg —, transcriptions, translations, models, voices), a native `/api` with job events and WebSocket streaming, MCP over HTTP and stdio, setup pages for Claude Code, Cursor, Codex, n8n, the OpenAI SDKs; tools (A/B, convert, direction, watermark detection, file probe) |
| Workflows | A canvas of steps (sources, TTS, transcription, translation, effects, exports) run as resumable jobs, with templates |
| Models | A pinned catalogue (TTS, speech recognition, alignment, VAD, separation, diarization, enhancement, local language models, watermark) downloaded from huggingface.co or hf-mirror.com (the fastest wins), through the proxy, resumable, checked; licences reviewed before gated ones; any Hugging Face repo mlx-audio-swift loads |
| Shell | Sidebar in sections — Create, Voices, Library, Automate, Utilities, System — resizable, collapsible to an icon rail; ⌘1–9: Studio, Stories, Audiobook, Dub, Voices, Design, Gallery, Projects, Transcribe; command palette (⌘K) with every page; shortcut sheet (?); status bar with the engine, the job queue and downloads; ⌘E quick switch; notifications; logs page and panel (⇧⌘L); `audiosmith://` deep links |
| First run | A setup wizard: a check of this Mac (macOS, chip, memory, disk, network or mirror, engine, ffmpeg, permissions), a model pack chosen by memory (Fast, Balanced, Quality, Max), privacy, dictation; then the Studio says hello with Kokoro. Settings → General runs it again |
| Settings | General, appearance (theme, accent colour, size 90–125 %, sidebar, reduce motion), models and downloads, Studio, engine (memory budget, idle unload), pronunciation, dictation, writing help, permissions, dubbing, Local API, audio tools (ffmpeg), network (mirror, proxy, connection test), privacy (no analytics; local-only language models; watermark; history cap), storage (what takes room, clear caches, move the data folder, reset or erase), usage (statistics computed on the Mac), diagnostics and about (self-check, diagnostic bundle, bug report) |

All long work runs as jobs in one persistent queue: generation, renders, dubs, transcripts, workflows, downloads. A job the app quit in the middle of continues at the next start. The engine never touches the network: it loads the snapshots the app downloaded.

**ffmpeg is optional.** macOS's AVFoundation reads and writes WAV, AIFF, M4A/AAC, MP3 (read), MOV and MP4. With an ffmpeg (Homebrew's, or a file chosen in Settings → Audio tools), AudioSmith also exports MP3, Opus and FLAC, reads WebM, MKV, VP9 and AV1, and burns subtitles into videos; it runs ffmpeg as a separate program. Without it, those options say what they need.

## Data

| What | Where |
|---|---|
| Library: takes, voices, projects, renders, dubs, transcripts, workflows, settings, licences, jobs, drafts, notifications | `~/Library/Application Support/AudioSmith/audiosmith.db` (SQLite) |
| Files of the library | `…/AudioSmith/` `clips/` (takes), `voices/`, `references/`, `renders/` and `covers/` (stories, audiobooks), `dubs/`, `transcripts/`, `dictation/`, `workflows/`, `tools/` (yt-dlp, ffmpeg) |
| Data folder elsewhere | Settings → Storage → Move… copies the folder, checks the copy, and switches at the next start; `…/AudioSmith/location.json` then points to it |
| Caches | `~/Library/Caches/AudioSmith/` (waveforms, rendered chunks, voice previews; Settings → Storage → Clear) |
| Logs | `~/Library/Logs/AudioSmith/audiosmith.log` (app) and `engine-*.log` (engine workers) |
| Models | the Hugging Face cache, `~/.cache/huggingface/hub`, shared with the Python tools (`HF_HUB_CACHE` / `HF_HOME`, or Settings → Models and downloads) |
| Secrets | the login Keychain, service "AudioSmith": Hugging Face token (or `$HF_TOKEN`, or `huggingface-cli login`'s file), API key, language-model and translation keys |

## Architecture

```
┌──────────────── AudioSmith.app ─────────────────────────┐
│ Go + MyGo native UI (Metal)                              │
│  *.go               views and their state (main)         │
│  internal/services  takes, voices, projects, drafts      │
│  internal/store     SQLite (sqlitestore)                 │
│  internal/jobs      persistent job queue (lanes, DAG)    │
│  internal/engine    supervisor + executors, protocol 2   │──stdin──▶ ┌───────────────────────────┐
│  internal/hub       model downloads (HF API, mirrors)    │◀─stdout── │ audiosmith-engine (Swift) │
│  internal/catalog, licence, settings, i18n, notify,      │           │ mlx-audio-swift + MLX     │
│  logs, audio, text, doc, lang, dsp, voicedesign,         │           │ offline, writes WAV takes │
│  gallery, llm, translate                                 │           │                           │
└──────────────────────────────────────────────────────────┘           └───────────────────────────┘
```

[docs/architecture.md](docs/architecture.md) maps the packages, the data flow, the threading rules, the seams between UI areas and the data on disk. The two halves share only the engine protocol, [protocol 2](docs/protocol-v2.md): JSON-RPC-shaped lines with a handshake, job ids, cancellation by id, progress and streamed audio chunks, typed errors. Golden fixtures in `engine/Fixtures/` are decoded by both sides' tests. The Swift package has three targets: `AudioSmithProtocol` (the wire types, no MLX), `AudioSmithEngine` (the executable) and `AudioSmithEngineTests` (the fixtures).

The engine exits when the app closes its stdin. Its stdout carries only the protocol: it redirects everything else to stderr, which the app logs.

Why a helper process: MyGo is pure Go without cgo, and MLX is Swift/C++/Metal. A child process keeps the Go build cgo-free, keeps the UI alive when a model crashes or runs out of memory, and gives Unload a way to free the GPU memory.

## Build and run

Runs on macOS 15 or later on Apple silicon (M1 or later; 16 GB of memory recommended for the larger models). Building needs Xcode 26 (Swift 6.2+) and Go 1.27.1 (`GOTOOLCHAIN=auto` downloads it).

```sh
scripts/build-engine.sh         # xcodebuild → resources/darwin-arm64/engine/  (about 4 min the first time)
go tool mygo dev                # run with live reload
go tool mygo build              # build/darwin-arm64/AudioSmith.app and a .dmg (bundles the SQLite library)
```

The engine has to be built with `xcodebuild`, not `swift build`: only Xcode compiles MLX's Metal kernels into `mlx-swift_Cmlx.bundle/…/default.metallib`. `build-engine.sh` stages the executable and its resource bundles, and MyGo copies them into `AudioSmith.app/Contents/Resources/engine/` and signs them.

Smoke-test the engine without the app:

```sh
resources/darwin-arm64/engine/audiosmith-engine --text "Hello" --voice af_heart --output /tmp/hello.wav
```

`AUDIOSMITH_ENGINE=/path/to/audiosmith-engine` makes the app use another engine build.

### Developing from Linux

The UI and its tests are plain Go and run anywhere:

```sh
CGO_ENABLED=0 go test ./...     # views run headless in ui.Tester; the engine is faked or scripted
go tool mygo vet .              # go vet ./... + MyGo UI-lifetime checks
```

`scripts/mac.sh` mirrors the project to a Mac over SSH (`MAC_HOST`, default `mac-air`; `MAC_DIR`, default `code/AudioSmith`) and runs a command there:

```sh
scripts/mac.sh scripts/build-engine.sh
scripts/mac.sh go tool mygo build
```

## Versions

The engine builds on upstream mlx-audio-swift with AudioSmith's patch series, locked in `engine/Package.resolved` (builds use it strictly):

| Dependency | Pin |
|---|---|
| [Blaizzy/mlx-audio-swift](https://github.com/Blaizzy/mlx-audio-swift) | `244458d` (2026-10-10) + `engine/patches/mlx-audio-swift/*.patch`, applied by `scripts/fetch-engine-deps.sh` into `engine/.build/deps/` |
| mlx-swift | `0.32.3` |
| mlx-swift-lm | `3.32.3` |
| swift-transformers / swift-huggingface | `1.3.5` / `0.13.0` |

The patches (see `engine/patches/README.md`): Kokoro evaluates its audio before yielding it (mlx-swift 0.32 makes default streams thread-local: a lazy array yielded from a model's own `Task` crashes with `There is no Stream(gpu, N) in current thread`); Whisper detects the language and takes a task; Chatterbox spells spaces and takes a language (Cangjie for Chinese). **Moving a pin, or adding a patch, needs the per-model Mac smoke suite** (`scripts/smoke-models.sh`): every catalogue model passes it before it is listed.

MyGo is pinned in `go.mod`; `go tool mygo` runs the CLI at that version. Its SQLite plugin loads a prebuilt library: `go test` downloads it once (sha256-checked) into `~/.cache/mygo/natives`, `MYGO_SQLITE_LIBRARY` points it at a local copy (CI does this), and `mygo build` bundles it into the app.

## Tests

| Command | Where | What it covers |
|---|---|---|
| `CGO_ENABLED=0 go test ./...` | anywhere | Views in `ui.Tester` with fake engines (generate, cancel, errors, crash + restart, licence and download refusals, per-model controls, preload/unload, history, settings, Models page, jobs, notifications); the real scheduler and supervisor against a scripted protocol-2 engine; the SQLite store; the hub against a fake Hugging Face; the protocol fixtures; the catalogue; i18n key parity |
| `go test -race ./...` | anywhere (cgo) | the same, for data races |
| `go tool mygo vet .` | anywhere | `go vet` + MyGo UI-lifetime checks |
| `AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .` | anywhere | Renders every page and overlay, light and dark, English and Chinese, to PNG |
| `scripts/test-engine.sh` | Mac | The Swift protocol types against `engine/Fixtures` |
| `scripts/smoke-engine.sh` | Mac | The engine binary alone: three Kokoro takes |
| `scripts/smoke-models.sh [ids]` | Mac + network | **The model gate.** Each catalogue model (or the listed ids) is downloaded, run offline and checked: TTS takes read back by Whisper (and design tags by pitch), transcripts, word times, VAD, denoising; reports RTF and memory to `/tmp/audiosmith-smoke/report.md` |
| `AUDIOSMITH_E2E=1 go test -run TestRealEngineMedia -v ./internal/engine` | Mac | The media and listen workers: probe and decode of WAV, AIFF, M4A, MP3, MOV; the microphone |
| `AUDIOSMITH_E2E=1 go test -run TestDictationLatency -v .` | Mac + network | Dictation's speech-to-text candidates on the listen worker: cold and warm latency for ~5 s of English and Chinese |
| `AUDIOSMITH_E2E=1 go test -run TestLongRender -v -timeout 2h .` | Mac, models downloaded | 10,000-character renders (English, Chinese) on Kokoro, mixed text with `[laughter]` and a target duration on OmniVoice, `[slow]`/`[fast]`: chunks, loudness, memory |
| `AUDIOSMITH_E2E=1 go test -run TestEndToEnd -v .` | Mac | The app's state driving the real engine: start, preload, English and Chinese takes, cancel by id, unload |
| `AUDIOSMITH_E2E=1 go test -run TestRealEngineConformance ./internal/engine` | Mac | Raw protocol 2 against the built engine |
| `AUDIOSMITH_E2E=1 AUDIOSMITH_E2E_MODELS=kokoro-82m-bf16,omnivoice-bf16 go test -run TestModelLifecycle -v -timeout 40m .` | Mac + network | Download into an empty model folder, cancel and resume, accept the licence, generate with the engine offline (`sandbox-exec`), OmniVoice duration and cloning, delete |
| `AUDIOSMITH_MAC=1 go test -run TestMacKeychainAndProxy -v ./internal/hub` | Mac | Keychain token save/read/remove, the system proxy from `scutil` |

CI (`.github/workflows/ci.yml`) runs the Linux rows on every push and pull request.

![Studio](docs/screenshots/studio-light.png)
![History](docs/screenshots/history-light.png)
![Voice cloning, dark](docs/screenshots/studio-clone-dark.png)

## Next steps

M0–M6 of the plan to parity with VoiceStudio's open-source edition are in (Studio, voices, dictation, longform, dubbing, local API and MCP, workflows) and M7's first part (first run, settings, media formats). Left for M7: more UI languages, accessibility, signing, notarization and updates.

## License

[Apache-2.0](LICENSE)
