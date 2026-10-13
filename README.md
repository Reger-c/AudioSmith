# AudioSmith

A TTS workbench for macOS on Apple silicon. Write a script, pick a model and a voice, generate takes locally on the GPU, then listen to them, compare them and export them.

- **UI**: [MyGo](https://mygo.egoist.dev/) native UI, written in Go (`*.go`).
- **Speech**: `audiosmith-engine`, a Swift helper on [mlx-audio-swift](https://github.com/Blaizzy/mlx-audio-swift) (`engine/`), which runs the models with MLX.

## MVP features

| Area | What it does |
|---|---|
| Studio | Script editor; model / voice / language / speed / sampling controls that change with the model; voice cloning from a reference clip; Generate (⌘↩) and Cancel; live progress; the latest take with its waveform (click to seek), plus Play, Reveal and Export |
| Models | Kokoro 82M (54 voices, 9 languages including Chinese), Qwen3-TTS 0.6B CustomVoice / Base (clone), Marvis 250M, Soprano 80M, or any Hugging Face repo mlx-audio-swift loads. Preload and Unload |
| History | Every take is stored with the settings that made it (model, voice, speed, RTF…). Play, Reuse (loads them back into the studio), Reveal, Export, Delete |
| Engine | Status, PID, loaded model, paths, the last stderr lines after a crash, Restart |

Data:

- Takes: `~/Library/Application Support/AudioSmith/clips/*.wav`, indexed by `history.json`
- Models: the Hugging Face cache, `~/.cache/huggingface/hub` (shared with the Python tools; `HF_HUB_CACHE` / `HF_HOME` override it, and `HF_TOKEN` is passed through)
- Engine log: `~/Library/Logs/AudioSmith/engine.log`

## Architecture

```
┌──────────────── AudioSmith.app ────────────────┐
│ Go + MyGo native UI (Metal)                     │
│  view.go   studio / history / engine pages      │
│  app.go    state and actions (main thread)      │
│  engine.go child process, JSON lines            │──stdin──▶ ┌───────────────────────────┐
│  clips.go  history, waveform peaks              │◀─stdout── │ audiosmith-engine (Swift) │
│  player.go playback (mygo/audio)                │           │ mlx-audio-swift + MLX     │
└─────────────────────────────────────────────────┘           │ writes WAV takes          │
                                                              └───────────────────────────┘
```

The two halves only share the protocol in [`engine/Sources/AudioSmithEngine/Protocol.swift`](engine/Sources/AudioSmithEngine/Protocol.swift): one JSON object per line (`load`, `synthesize`, `cancel`, `unload` → `ready`, `status`, `loaded`, `progress`, `done`, `cancelled`, `error`). The engine keeps one model loaded, runs one job at a time, and exits when the app closes its stdin. The engine's stdout carries only the protocol: it redirects everything else to stderr, which the app logs.

Why a helper process: MyGo is pure Go without cgo, and MLX is Swift/C++/Metal. A child process keeps the Go build cgo-free, keeps the UI alive when a model crashes or runs out of memory, and gives Unload a way to free the GPU memory.

## Build and run

Needs macOS 14+ on Apple silicon, Xcode 26 (Swift 6.2+), and Go 1.27.1 (`GOTOOLCHAIN=auto` downloads it).

```sh
scripts/build-engine.sh         # xcodebuild → resources/darwin-arm64/engine/  (about 4 min the first time)
go tool mygo dev                # run with live reload
go tool mygo build              # build/darwin-arm64/AudioSmith.app and a .dmg
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
CGO_ENABLED=0 go test ./...     # views run headless in ui.Tester; the engine is faked
go tool mygo vet .
```

`scripts/mac.sh` mirrors the project to a Mac over SSH (`MAC_HOST`, default `mac-air`) and runs a command there:

```sh
scripts/mac.sh scripts/build-engine.sh
scripts/mac.sh go tool mygo build
```

## Versions

- mlx-audio-swift is pinned to a revision in `engine/Package.swift` (it moves fast and has no stable tags), with mlx-swift 0.32.x and mlx-swift-lm 3.32.x as it resolves them. Update them together.
- MyGo is pinned in `go.mod`; `go tool mygo` runs the CLI at that version.

## Next steps

- Stream audio as it is generated (the engine already receives chunks) for a lower time to first sound
- Long scripts: split by paragraph into a queue of takes and join them
- A/B comparison of takes, ratings and notes
- STT round trip (MLXAudioSTT) to check pronunciation
- Model manager: downloaded models, sizes, delete
- Signing, notarization and auto-updates (`macos.signingIdentity`, `updates` in `mygo.json`)
