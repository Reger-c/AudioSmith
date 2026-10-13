# AudioSmith

A TTS workbench for macOS on Apple silicon. Write a script, pick a model and a voice, generate takes locally on the GPU, then listen to them, compare them and export them.

- **UI**: [MyGo](https://mygo.egoist.dev/) native UI, written in Go (`*.go`).
- **Speech**: `audiosmith-engine`, a Swift helper on [mlx-audio-swift](https://github.com/Blaizzy/mlx-audio-swift) (the [hehehai fork](https://github.com/hehehai/mlx-audio-swift) Voxt uses) in `engine/`, which runs the models with MLX.

## Features

| Area | What it does |
|---|---|
| Studio | Script editor; model / voice / language / speed / sampling / duration controls that change with the model; voice cloning from a reference clip; Generate (⌘↩) and Cancel; live progress; the latest take with its waveform (click to seek), plus Play, Reveal and Export. The form autosaves and comes back at the next start. A model that is not downloaded, or whose licence is not accepted yet, is offered for download or review right there |
| Models | Kokoro 82M (54 voices, 9 languages including Chinese), Qwen3-TTS 0.6B CustomVoice / Base (clone), Marvis 250M, Soprano 80M, OmniVoice (non-commercial licence, gated), or any Hugging Face repo mlx-audio-swift loads. Downloads from huggingface.co or hf-mirror.com (the fastest wins), through the system proxy, resumable and cancellable, checked by hash; Verify, Reveal, Delete. Every model is pinned to a commit |
| History | Every take is stored with the settings that made it (model, voice, speed, seed, RTF…). Play, Reuse (loads them back into the Studio), Reveal, Export, Delete |
| Engine | One worker per role (M0: the speech worker): state, version, device, memory, loaded models, running jobs, the last stderr lines after a crash, Restart |
| Shell | Sidebar (⌘1–5), command palette (⌘K), shortcut sheet (?), status bar with the engine, the job queue (cancel any job) and downloads, ⌘E quick switch (models in memory, Unload, Clear cache, Preload), notification centre, logs page and panel (⇧⌘L), `audiosmith://` deep links |
| Settings | Language (English, 简体中文, or the system's), theme and interface size (90–125 %), reduce motion; Studio default model and autoplay; model folder, download source, proxy, Hugging Face token (Keychain), accepted licences; engine memory budget and idle unload |

All long work runs as jobs in one persistent queue: generation, preloads and downloads. A job the app quit in the middle of continues at the next start (downloads resume where they stopped). The engine never touches the network: it loads the snapshots the app downloaded.

## Data

| What | Where |
|---|---|
| Library: takes, settings, licences, jobs, drafts, notifications | `~/Library/Application Support/AudioSmith/audiosmith.db` (SQLite; the MVP's `history.json` is imported once and kept) |
| Take audio | `~/Library/Application Support/AudioSmith/clips/*.wav` |
| Waveform cache | `~/Library/Caches/AudioSmith/peaks/` |
| Logs | `~/Library/Logs/AudioSmith/audiosmith.log` (app) and `engine-gpu.log` (engine) |
| Models | the Hugging Face cache, `~/.cache/huggingface/hub`, shared with the Python tools (`HF_HUB_CACHE` / `HF_HOME`, or Settings → Models) |
| Hugging Face token | the login Keychain (or `$HF_TOKEN`, or `huggingface-cli login`'s file) |

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
│  logs, audio, text, doc, lang, dsp                       │           │ offline, writes WAV takes │
└──────────────────────────────────────────────────────────┘           └───────────────────────────┘
```

[docs/architecture.md](docs/architecture.md) maps the packages, the data flow, the threading rules, the seams between UI areas and the data on disk. The two halves share only the engine protocol, [protocol 2](docs/protocol-v2.md): JSON-RPC-shaped lines with a handshake, job ids, cancellation by id, progress and streamed audio chunks, typed errors. Golden fixtures in `engine/Fixtures/` are decoded by both sides' tests. The Swift package has three targets: `AudioSmithProtocol` (the wire types, no MLX), `AudioSmithEngine` (the executable) and `AudioSmithEngineTests` (the fixtures).

The engine exits when the app closes its stdin. Its stdout carries only the protocol: it redirects everything else to stderr, which the app logs.

Why a helper process: MyGo is pure Go without cgo, and MLX is Swift/C++/Metal. A child process keeps the Go build cgo-free, keeps the UI alive when a model crashes or runs out of memory, and gives Unload a way to free the GPU memory.

## Build and run

Needs macOS 14+ on Apple silicon, Xcode 26 (Swift 6.2+), and Go 1.27.1 (`GOTOOLCHAIN=auto` downloads it).

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

The engine uses the dependency set Voxt validated, locked in `engine/Package.resolved` (builds use it strictly):

| Dependency | Pin |
|---|---|
| [hehehai/mlx-audio-swift](https://github.com/hehehai/mlx-audio-swift) | `2a6e75d` (upstream + Voxt fixes) |
| mlx-swift | `0.31.6` |
| mlx-swift-lm | `c6446cf` |
| swift-transformers / swift-huggingface | `1.3.4` / `0.10.2` |

Keep them together. **Don't move to upstream mlx-audio-swift HEAD / mlx-swift 0.32.x without the per-model Mac smoke tests**: MLX 0.32 makes Metal command encoders thread-local, and Kokoro's `generateStream` in mlx-audio-swift yields unevaluated audio from its own `Task`, which crashes with `There is no Stream(gpu, N) in current thread` (a one-line fix, planned for M1).

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
| `AUDIOSMITH_E2E=1 go test -run TestEndToEnd -v .` | Mac | The app's state driving the real engine: start, preload, English and Chinese takes, cancel by id, unload |
| `AUDIOSMITH_E2E=1 go test -run TestRealEngineConformance ./internal/engine` | Mac | Raw protocol 2 against the built engine |
| `AUDIOSMITH_E2E=1 AUDIOSMITH_E2E_MODELS=kokoro-82m-bf16,omnivoice-bf16 go test -run TestModelLifecycle -v -timeout 40m .` | Mac + network | Download into an empty model folder, cancel and resume, accept the licence, generate with the engine offline (`sandbox-exec`), OmniVoice duration and cloning, delete |
| `AUDIOSMITH_MAC=1 go test -run TestMacKeychainAndProxy -v ./internal/hub` | Mac | Keychain token save/read/remove, the system proxy from `scutil` |

CI (`.github/workflows/ci.yml`) runs the Linux rows on every push and pull request.

![Studio](docs/screenshots/studio-light.png)
![History](docs/screenshots/history-light.png)
![Voice cloning, dark](docs/screenshots/studio-clone-dark.png)

## Next steps

The plan to full parity with VoiceStudio's open-source edition runs in milestones: M1 the complete Studio (long scripts in chunks, streaming preview, normalization, pronunciation, voice library and design), M2 transcription and global dictation, M3 stories and audiobooks, M4 video dubbing and batch, M5 the local API and MCP, M6 workflows, M7 polish and release.

## License

[Apache-2.0](LICENSE)
