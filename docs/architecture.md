# AudioSmith architecture

*State at the end of M0 (2026-10-13). M0a wrote the skeleton and the contracts; threads A (services + SQLite), B (protocol 2, supervisor, jobs), C (shell, catalogue, hub, i18n) and #9 (text, document and language libraries) built them; the integration thread (#10) wired them together (§5). Product plan: `plan/parity-plan.md` §3–§4. Wire format: [protocol-v2.md](protocol-v2.md).*

## 1. Package map

```
github.com/hehehai/AudioSmith
├── *.go                 package main: the UI (views + view state) and the wiring
├── internal/
│   ├── core/            domain types, service interfaces, event bus, sentinel errors   (leaf)
│   ├── services/        TakeService, VoiceService, ProjectService, DraftService
│   ├── store/           persistence contract (repositories)
│   │   ├── sqlitestore/ SQLite store: migrations, FTS5, jobs tables, history.json import
│   │   └── historyjson/ the MVP history.json format (read by the importer)
│   ├── jobs/            the job queue: lanes, priorities, states, DAG, Scheduler, MemStore (leaf)
│   ├── engine/          Supervisor (one engine process per role), job executors
│   │   ├── proto/       protocol-2 wire types (leaf); fixtures in engine/Fixtures/
│   │   └── enginetest/  a scripted protocol-2 engine for tests (the test binary re-runs itself)
│   ├── catalog/         model catalogue: catalog.json (entries, pinned commits, licences)
│   ├── hub/             model files: HF cache layout, downloader, model.download jobs, token, proxy
│   ├── licence/         the licence gate (core.LicenceService) over the store's licences
│   ├── settings/        the typed, versioned settings document + Service                (leaf)
│   ├── notify/          the notification centre (kept in the store's documents)
│   ├── logs/            log ring + rotated files
│   ├── i18n/            T / N / X keyed by English source; locales/*.json; dates     (leaf)
│   ├── audio/           waveform peaks + disk cache, streaming player
│   ├── text/            script pipeline: segmentation, chunk keys (M1; not wired yet)
│   │   └── markup/, normalize/, lexicon/, autocast/
│   ├── doc/             document import: TXT/MD/EPUB/DOCX, cue sheets (M1/M3; not wired yet)
│   │   └── subtitle/, textenc/
│   └── lang/            the language table, 652 languages                              (leaf)
└── engine/              the Swift helper (audiosmith-engine), Fixtures/, Tests/
```

### Import rules

Arrows point to what a package may import. No cycles. Leaves import nothing internal.

```
main ──▶ every internal package
services ──▶ core, store, jobs, engine/proto (the job's params), i18n (job titles)
store ──▶ core, jobs            store/sqlitestore ──▶ core, jobs, store, store/historyjson
engine ──▶ core, engine/proto, jobs (its executors)        engine/enginetest ──▶ engine/proto
hub ──▶ catalog, jobs (the download executor)              catalog ──▶ lang
licence ──▶ catalog, core       notify, logs ──▶ core
text/* ──▶ text                 doc ──▶ text, doc/subtitle, doc/textenc      doc/subtitle ──▶ text
core, jobs, engine/proto, settings, i18n, audio, lang, text, doc/textenc ──▶ (nothing internal)
```

`jobs` keeps its own `ErrNotFound`/`ErrInvalid`/`ErrClosed` (it cannot import `core`); callers map them to `core` sentinels where needed.

Event types live next to their publisher, and their topic starts with the publisher's name. They satisfy `core.Event` (a `Topic() string` method) without importing `core`:

| Event | Topic | Publisher |
|---|---|---|
| `core.TakeCreated/Updated/Deleted`, `VoiceChanged`, `ProjectChanged`, `DraftSaved`, `LicenceChanged`, `NotificationPosted`, `LogAppended` | `take.*`, `voice.changed`, … | services, licence, notify, logs |
| `jobs.Updated` | `job.updated` | the scheduler (after the change is saved) |
| `engine.StatusChanged` | `engine.status` | the supervisor |
| `settings.Changed` | `settings.changed` | the settings store |
| `hub.DownloadProgress` | `model.download` | the download executor (≤4×/s) |

### Why the UI stays in package main

- **MyGo's conventions.** `mygo dev`, `mygo build` and `ui.View` build the root `main` package. `go tool mygo vet .` runs `go vet ./...` and the UI-lifetime checks over every package, so nothing would be lost by moving.
- **Views need the window** (`*mygo.Window` for `Update` and dialogs, `ui.Context`, the view state), all on the main thread. The **domain** is what must be goroutine-safe and testable, and it lives in `internal/`.
- **Areas** have their own files and state structs, embedded in `app`; they talk through the seams of §3.4.
- **Reusable widgets go in `internal/widgets`** when they grow (M1+). `internal/ui` is avoided because it would shadow `mygo/ui`.

## 2. Data flow

```
          MyGo main thread                           goroutines
┌──────────────────────────────┐  calls (go …)   ┌─────────────────────────────────────────────┐
│ views (build pass: no I/O)   │───────────────▶ │ library: store (SQLite) · bus · settings ·  │
│ app view state + snapshots   │                 │   licence gate · hub client · services      │
│        ▲ Window.Update       │                 │   │                                          │
│        │                     │                 │ jobs.Scheduler (persists to the jobs table)  │
└────────┼─────────────────────┘                 │   ├ "tts.synthesize", "models.load" ──▶ engine executors ──▶ Supervisor ──stdio v2──▶ engine --role gpu (offline)
         │                                       │   └ "model.download" (LaneNet) ──▶ hub.Client (HF API, mirrors, resume)
   core.Bus subscribers ◀── Publish ─────────────┤                                              │
   (wiring code, once)                           └─────────────────────────────────────────────┘
```

`openLibrary` (takes.go) builds the domain in this order: SQLite store (migrated; history.json imported once) → bus → settings → licence gate → scheduler over the store's jobs table, with the `model.download` executor → services (`services.New` gets the scheduler, the gate and `Ready`, which asks the hub whether a model's pinned snapshot is on disk). `connectEngine` makes the supervisor and registers the engine executors; `startEngine` (when the window opens) starts the gpu worker, then the scheduler, so jobs left from the last run (downloads, idempotent takes) continue.

How the Studio's Generate works:
1. **Click.** `generate` (studio.go) builds the take's recipe with a new ULID and calls `startTake`, which runs `Takes.Generate` on a goroutine and follows the job by its owner, `take:<id>`, even before Generate returns.
2. **Service.** `Generate` checks the licence (`*core.LicenceRequiredError` → the UI shows the licence dialog, then generates) and that the model is downloaded (`core.ErrNotDownloaded` → the UI offers Download next to the error). It submits `tts.synthesize` on `LaneGPU`, `PriorityInteractive`, idempotent, two attempts, and returns the job id.
3. **Scheduler.** It persists the job and runs the executor. The executor checks the licence again, fills `modelPath` with the local snapshot (`hub.Status` over the catalogue's pinned commit; a missing model fails with `model_not_downloaded`), and calls `Supervisor.Call(gpu, jobID, "tts.synthesize", …)`.
4. **Progress.** `job.progress` and `tts.chunk` become `jobs.Updated` on the bus; the UI's subscriber posts them to the main thread (the Studio's job view, the status bar's job list).
5. **Done.** TakeService saves the take (`store.Takes.Put`) and publishes `TakeCreated`; the UI adds it to its snapshot and, because it is the take the Studio waits for (`awaitTake`), makes it the latest take and autoplays it. After a restart, a re-run job's take is rebuilt from the job's params.

Downloads (Models page, or Download next to the Studio's error) are `model.download` jobs owned by `model:<repo>`: three at a time on `LaneNet`, cancellable by id, resumed after a restart (idempotent; the hub resumes partial files). The licence dialog comes before the download of a model whose licence needs accepting.

## 3. Threading and state rules

1. **The build pass does no I/O.** A view reads only `app` fields, in-memory snapshots and memory-only getters (`settings.Service.Get`, `catalog.Builtin`, `i18n.T`). Store, service, hub and engine calls happen in action handlers that start a goroutine, or in jobs. (The MVP's violations — peaks decoded in the view, whole-file player, `copyFile`, history writes on the main thread — are gone since thread A.)
2. **`app` is main-thread only.** Goroutines reach it through `a.post(fn)`, which is `Window.Update`. In tests it runs fn at once, or queues it (`postQueue`) in tests that run real goroutines. Results that may be stale are checked against current state inside `fn`.
3. **Bus subscriptions are set up once, in wiring code** (`openLibrary`, `openShell`, `openTakes`, `connectEngine`), never in a build pass. Handlers must not block: delivery is synchronous on the publisher's goroutine. Publishers publish after their change is durable, never while holding a lock.
4. **Services, the scheduler, the supervisor, the hub client, the settings store and the notification centre are goroutine-safe.** They take a `context.Context`, return copies, and wrap `core.Err*` sentinels.
5. **MyGo lifetime rules hold** (see `.agents/skills/mygo-maintenance`): keys before stateful widgets; never store elements; dialogs on goroutines; persistent callbacks capture `c.Services()`, not the `Context`.
6. **One queue.** All long work is a `jobs` job: TTS, preloads, downloads; later ASR, separation, exports, mux. The engine does not queue. The scheduler sends a worker no more jobs than its lane allows (gpu 1).
7. **The engine is stateless and offline.** Truth (library, queue, licences, settings, model files) lives in Go. A worker crash interrupts its jobs; it does not lose data. Engines start with `hello.offline: true` and load the snapshot paths the executors give them.
8. **Quit order.** `OnWillQuit` stops the supervisor and closes the scheduler (running jobs are recorded as interrupted); then `OnQuit` stops playback, waits for notification saves, closes the logs, and closes the library (drafts flushed, takes being saved waited for, the database closed).

### 3.4 Seams inside package main

| Seam | Defined in | Used by |
|---|---|---|
| `a.lib` (`*library`: store, bus, settings, licences, sched, svc, hub(), modelSnapshot) | takes.go | every area |
| `a.showPage(name)`, `a.navigate(path)`, `a.currentPage()` | shell.go | studio.go (`reuse`), notifications, tests |
| `a.engineSummary() engineSummary` | enginelink.go | shell (sidebar, status bar, ⌘E), Models page |
| `a.startTake(recipe)`, `a.preload()`, `a.cancel()`, `a.unload()`, `a.flushEngine()`, `a.restartEngine()`, `a.startEngine()` | enginelink.go | studio, ⌘E, Engine page, main |
| `a.job` (the Studio's job: `kind`, `status`, `fraction`, `audioSeconds`, `started`), `a.jobError`, `a.needDownload` | enginelink.go | studio_view.go |
| `a.takeCreated(take)`, `a.awaitTake` | takes.go | the bus subscriber; enginelink.go clears `awaitTake` on failure |
| `a.preset()`, `a.generate()`, `a.selectModel(entry)` | studio.go | shell (⌘↩, ⌘E, palette), tests |
| `a.download(entry)`, `a.cancelDownload(entry)`, `a.model(entry)`, `a.downloadUpdated(job)` | shell_models.go | Models page, Studio (Download), status bar |
| `a.jobsChanged(job)`, `a.activeJobs()`, `a.cancelJobByID(job)` | shell_status.go | enginelink.go's subscriber, status bar |
| `a.updateSettings(fn)`, `a.prefs` | shell_settings.go | every area that changes a setting |
| `a.askLicence(entry, then)` | shell_models.go | Models page, Studio (Generate, Preload) |
| `a.toast(msg)`, `a.notify(…)`, `a.post(fn)`, `a.async(fn)` | app.go, shell_status.go | everyone |

## 4. Contracts (summary)

| Contract | File | State |
|---|---|---|
| Domain types (Take, Voice, Project, LicenceAcceptance, Notification, LogEntry), `NewID` (ULID) | `internal/core/{take,domain,ids}.go` | done; Take gained `Duration`, `Steps` (OmniVoice) |
| Event bus: `Publish/Subscribe`, `core.On[T]`, `core.NewStream` | `internal/core/bus.go` | done |
| Services, bundled as `core.Services` | `internal/core/services.go`, `internal/services` | done; `TakeRequest.Recipe.ID` may be chosen by the caller |
| Store: Takes, Voices, Projects, Licences, Documents, `jobs.Store` | `internal/store`, `sqlitestore/migrations/001_init.sql` | done (SQLite) |
| Jobs: lanes, priorities, states, DAG, retry, interrupted-on-restart | `internal/jobs` | done |
| Engine supervisor: `Start/Call/Status/Restart/Stop`, `SetMemoryBudget`, `SetIdleUnload`, `SetCacheDir` | `internal/engine/supervisor.go` | done |
| Engine executors: `RegisterExecutors(sched, sup, ExecutorOptions{ModelPath, Licences})` | `internal/engine/executors.go` | done |
| Protocol 2 (+ `SynthesizeParams.modelPath`, OmniVoice `steps`/`duration`) | `internal/engine/proto`, `engine/Fixtures`, [protocol-v2.md](protocol-v2.md) | done |
| Catalogue v2: entries, aux repos, hidden, **pinned commits**, licences, language codes validated with `lang` | `internal/catalog` | done |
| Downloader: `Status/Plan/Download/Verify/Delete`, mirror race, proxy, token; `DownloadSpec`, `DownloadExecutor`, `EntryOf`, `ErrorOf` | `internal/hub` | done |
| Settings document + `Service` (in the store's documents, `settings:app`) | `internal/settings` | done |
| Licence gate, notification centre (persists info/success/warning), logs | `internal/licence`, `internal/notify`, `internal/logs` | done |
| i18n: `T`, `N`, `X`, `SetLocale`, `Match`, `Supported`, `Name`, `DateTime`, `Date` | `internal/i18n` | done (en, zh-Hans) |

## 5. M0: how it was built, and what changed from the sketch

### Threads and merges

M0a (`ff57a69`) wrote the package skeleton and these contracts. Three threads then worked in parallel, each owning files, with no sub-threads: **A** services + SQLite (`5d98053`), **C** shell, catalogue, hub, i18n, settings, licences (`6e88272`), **B** protocol 2 in Swift and Go, supervisor, scheduler (`8be41fb`). Thread **#9** added the pure-Go libraries `text`, `doc` and `lang` for M1 (`ae59b3d`). The integration thread (#10) did steps I1–I5 and the leftovers:

- **I1.** `openLibrary` builds store + bus → settings, licence gate, scheduler → services. The Studio's Generate goes through `TakeService.Generate` (licence and download checks; `take:<id>` jobs); the Studio-owned job path and the double save of takes are gone. `selectModel(entry)` replaced the shell's `chooseModel`.
- **I2.** The status bar lists every unfinished job (any owner), each cancellable by id; ⌘E shows the gpu worker's memory and Clear cache; the workers' status comes from `Supervisor.Status` and `StatusChanged`.
- **I3.** Settings drive the Studio's defaults (Settings → Studio: default model, autoplay), and, at run time, the memory budget, the idle unload limit and the model folder (the engine restarts into a new folder when idle). The hub client follows endpoints, proxy and folder.
- **I4.** `model.download` jobs on `LaneNet` (hub.DownloadExecutor); the Models page follows `jobs.Updated`; downloads survive a restart.
- **I5.** Engines start offline and load local snapshots by path; catalogue revisions are pinned to commits.
- **Leftovers.** `px()` scaling in every view; localized dates; a dialog instead of `log.Fatalf` when the database cannot open; zh-Hans says 生成结果 for "take"; notifications persist.

### Deviations from the M0a sketch (all accepted by main)

- **Schema** (thread A): `internal/store/sqlitestore/migrations/001_init.sql` (embedded; `go:embed` cannot reach a parent directory). `takes`, `voices` and `projects` have `seq INTEGER PRIMARY KEY` plus `id TEXT UNIQUE` (FTS external content needs a stable rowid); FTS uses `tokenize='trigram'` (substring search, CJK; queries under 3 characters use `LIKE`); `takes.project_id` is `ON DELETE SET NULL` (deleting a project never orphans audio silently; revisit in M3); `job_deps.after_id` has no FK (graph nodes save in any order); `job_events` is filled by triggers.
- **Services** (A): `core.TakeService` gained `Add` (takes made elsewhere: imports) and `AudioDir`; new event `core.DraftSaved`; `services` imports `engine/proto` and `i18n` (both leaves).
- **Library** (A, then I1): `openTakeStore` returns the `*library`, which owns the store, bus, settings, gate, scheduler and services. If the database cannot open, the app shows why and quits; there is no fallback that would put takes somewhere temporary.
- **Behaviour** (A): takes whose WAV was deleted by hand stay listed (Play explains the error; the waveform is flat). The history.json import still skips them.
- **Engine** (B): `internal/engine` imports `internal/jobs` for its executors. Cancel grace stays 5 s; Kokoro gets sentence chunking in M1 (a long Kokoro take can outlast the grace on a slow Mac, which restarts the worker without losing data). The supervisor stops on `OnWillQuit`.
- **Shell** (C): search inputs are `ui.TextInput` (MyGo's `SearchField` hard-codes English labels); the mirror-race winner is kept 24 h in memory only; licence acceptances carry no terms fingerprint yet.
- **Hub** (I5): for a pinned commit the hub also writes `refs/main` (unless it names another snapshot that is there), so swift-huggingface and the Python tools resolve the repo by id offline.

### Ownership from M1 on

Threads now own areas, not M0 contracts. Each keeps `CGO_ENABLED=0 go test ./...`, `go tool mygo vet .`, `TestSnapshots` and the i18n parity test green, and merges through main.

| Path | Owner |
|---|---|
| `internal/text/**`, `internal/doc/**`, `internal/lang/**` | thread #9's successor (M1 text pipeline); the UI wires them in M1 |
| `internal/engine/**`, `engine/**`, `docs/protocol-v2.md`, `scripts/*engine*.sh` | the engine thread of the milestone |
| everything else | assigned per milestone by main; `README.md` and this file: main |

Rules that stay:
- **Every user-visible string goes through `i18n.T`**, and whoever adds strings fills zh-Hans (the parity test fails otherwise).
- **A change in a file another running thread owns** goes under "Decisions needed" in the handoff, except a one-line call-site change at a §3.4 seam or a compile fix caused by your own contract change (say so in the commit message).

## 6. Data on disk (macOS)

| What | Where |
|---|---|
| Library database (takes, voices, projects, licences, jobs, settings `settings:app`, drafts `draft:*`, notifications) | `~/Library/Application Support/AudioSmith/audiosmith.db` (WAL; `audiosmith.db.bak-<v>` before each migration) |
| Take audio | `~/Library/Application Support/AudioSmith/clips/<take id>.wav` |
| MVP history | `…/AudioSmith/history.json`, imported once and kept |
| Waveform cache | `~/Library/Caches/AudioSmith/peaks/<take id>.f32` |
| Logs | `~/Library/Logs/AudioSmith/audiosmith.log` (app, rotated at 5 MB) and `engine-gpu.log` (engine stderr) |
| Models | the Hugging Face cache, `~/.cache/huggingface/hub` (`HF_HUB_CACHE`/`HF_HOME`, or Settings → Models → Model folder) |
| HF token | the login Keychain, service "AudioSmith", account "huggingface-token" (or `$HF_TOKEN`, or huggingface-cli's token file) |

## 7. Testing

| What | Command | Notes |
|---|---|---|
| All Go tests | `CGO_ENABLED=0 go test ./...` | Views in `ui.Tester` with fake engines; the real scheduler and supervisor against a scripted engine; the hub against a fake HF; protocol fixtures; i18n key parity. The SQLite library is downloaded and cached on first run (`MYGO_SQLITE_LIBRARY` overrides it; see `.github/workflows/ci.yml`). |
| MyGo vet | `go tool mygo vet .` | `go vet ./...` and the UI-lifetime checks |
| Race | `go test -race ./...` | needs cgo |
| Snapshots | `AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .` | every page, light/dark, en/zh-Hans |
| Swift fixtures | `scripts/test-engine.sh` | Mac: the Swift wire types against `engine/Fixtures` |
| Engine smoke | `scripts/smoke-engine.sh` | Mac: the engine binary alone |
| Real engine | `AUDIOSMITH_E2E=1 go test -run 'TestEndToEnd' -v .` and `… -run TestRealEngineConformance ./internal/engine` | Mac, built engine, Kokoro in the HF cache |
| Model lifecycle | `AUDIOSMITH_E2E=1 AUDIOSMITH_E2E_MODELS=kokoro-82m-bf16,omnivoice-bf16 go test -run TestModelLifecycle -v -timeout 40m .` | Mac + network: download, cancel, resume, licence, generate offline under `sandbox-exec`, delete |
| Keychain, proxy | `AUDIOSMITH_MAC=1 go test -run TestMacKeychainAndProxy -v ./internal/hub` | Mac |
