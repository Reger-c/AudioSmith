# AudioSmith architecture (M0)

*Written by M0a ("skeleton + contracts"), 2026-10-13. It covers:*
- *the package layout the MVP was split into;*
- *the contracts the three parallel M0 threads implement;*
- *which files each thread may touch, so their branches merge cleanly.*

*Product plan: `plan/parity-plan.md` §3–§4. Wire format: [protocol-v2.md](protocol-v2.md).*

## 1. Package map

```
github.com/hehehai/AudioSmith
├── *.go                 package main: the UI (views + view state), MyGo wiring
├── internal/
│   ├── core/            domain types, service interfaces, event bus, sentinel errors   (leaf)
│   ├── store/           persistence contract (repositories) + schema.sql sketch
│   │   └── historyjson/ the MVP history.json, behind store.Takes; later the importer
│   ├── jobs/            job queue contract: lanes, priorities, states, DAG, Scheduler, Executor
│   ├── engine/          engine processes: v1 client (in use) + v2 Supervisor contract
│   │   └── proto/       protocol v2 wire types (leaf); fixtures in engine/Fixtures/
│   ├── catalog/         model catalogue schema + catalog.json (the 5 presets as data)  (leaf)
│   ├── hub/             HF cache location + Downloader contract
│   ├── settings/        typed, versioned settings document + Service contract          (leaf)
│   ├── i18n/            T / N / X keyed by English source; locales/*.json               (leaf)
│   └── audio/           waveform peaks, the player (later: streaming, mixer, loudness, QC)
└── engine/              the Swift helper (audiosmith-engine), Fixtures/
```

### Import rules

Arrows point to what a package may import. No cycles. `core` imports nothing internal.

```
main ──▶ every internal package
store ──▶ core, jobs              hub ──▶ catalog
engine ──▶ core, engine/proto     jobs, catalog, settings, i18n, audio, engine/proto ──▶ (nothing internal)
internal/services (A, new) ──▶ core, store, jobs, catalog, hub, settings
```

Event types live next to their publisher, and their topic starts with the publisher's name:
- `core.TakeCreated` → `take.created`
- `jobs.Updated` → `job.updated`
- `engine.StatusChanged` → `engine.status`
- `settings.Changed` → `settings.changed`
- `hub.DownloadProgress` → `model.download`

They satisfy `core.Event` (a `Topic() string` method) without importing `core`.

### Why the UI stays in package main

- **MyGo's conventions.** `mygo dev`, `mygo build` and `ui.View` build the root `main` package. Every MyGo example keeps its views there. `go tool mygo vet .` runs `go vet ./...` and the UI-lifetime checks over every package, so nothing would be lost by moving.
- **Views need the window.** They need the `*mygo.Window` (for `Update` and dialogs), `ui.Context`, and the app's view state. All of that lives on the main thread. A package boundary there would mean passing an "env" struct everywhere, for no isolation gain: the **domain** is what must be goroutine-safe and testable, and it now lives in `internal/`.
- **Parallel work without a package boundary.** Each UI area has its own files and its own state struct, embedded in `app` (Go field promotion keeps `a.text` and `a.job` working). Areas talk through a few named functions (§3.4).
- **Reusable widgets go in `internal/widgets`.** Waveform, timeline and tokenizers can move there when they grow (M1+). `internal/ui` is avoided because it would shadow `mygo/ui`.

## 2. Target data flow (end of M0)

```
          MyGo main thread                           goroutines
┌──────────────────────────────┐  calls (go …)   ┌──────────────────────────────────────┐
│ views (build pass: no I/O)   │───────────────▶ │ internal/services  (core.*Service)   │
│ app view state + snapshots   │                 │   │ store (SQLite)  │ licences, hub  │
│        ▲ Window.Update       │                 │   ▼                                  │
│        │                     │                 │ jobs.Scheduler ── Executors          │
└────────┼─────────────────────┘                 │   │ "tts.synthesize" → engine.Supervisor ──stdio v2──▶ engine --role gpu
         │                                       │   │ "model.download" → hub.Downloader
   core.Bus subscriber ◀── Publish ──────────────┤   ▼ persists jobs (store.Jobs)       │
   (main.go wiring, once)                        └──────────────────────────────────────┘
                                         Local API (M5): another Bus subscriber + service caller
```

How the Studio's Generate works once A and B are merged (integration step I1, §5):
1. **Click.** `generate` (Studio) calls `Takes.Generate` on a goroutine.
2. **Service.**
   - It checks `Licences.Check(model)` and `hub` status.
   - It submits a job: kind `tts.synthesize`, `LaneGPU`, `PriorityInteractive`, owner `take:<id>`.
   - It returns the job id.
3. **Scheduler.** It persists the job and runs the executor, which calls `Supervisor.Call(gpu, jobID, "tts.synthesize", params, notify)`.
4. **Progress.** `job.progress` and `tts.chunk` notifications become `jobs.Updated` on the bus. The UI subscriber posts them to the main thread and updates its job view.
5. **Done.** On the result, the service saves the take (`store.Takes.Put`) and publishes `TakeCreated`. The UI adds it to its snapshot and autoplays it.

## 3. Threading and state rules

1. **The build pass does no I/O.** A view reads only `app` fields, in-memory snapshots and memory-only getters (`settings.Service.Get`, `catalog.Builtin`, `i18n.T`). Store, service, hub and engine calls happen in action handlers that start a goroutine, or in background jobs.
   - **Known MVP violations, which thread A fixes:**
     - waveform peaks are decoded in `takes_view.go`;
     - the player decodes whole files;
     - `copyFile` reads whole files;
     - history writes happen on the main thread.
2. **`app` is main-thread only.** Goroutines reach it through `a.post(fn)`, which is `Window.Update`; in tests, it runs fn directly. Results that may be stale (the page changed, the take was deleted, the job was cancelled) are checked against current state inside `fn` before they are applied.
3. **Bus subscriptions are set up once, in wiring code, never in a build pass.**
   - A UI handler only calls `a.post`.
   - An API/SSE handler uses `core.NewStream`.
   - Handlers must not block: delivery is synchronous on the publisher's goroutine.
   - Publishers publish after their change is durable, and never while holding a lock.
4. **Services, the scheduler, the supervisor and the downloader are goroutine-safe.** They take a `context.Context`, return copies, and wrap `core.Err*` sentinels.
5. **MyGo lifetime rules hold** (see `.agents/skills/mygo-maintenance`):
   - keys before stateful widgets;
   - never store elements;
   - dialogs on goroutines;
   - persistent callbacks capture `c.Services()`, not the `Context`.
6. **One queue.** All long work is a `jobs` job: TTS, downloads, exports, and later ASR, separation and mux. The engine does not queue. The scheduler sends a worker no more jobs than its `hello.concurrency`.
7. **The engine is stateless.** Truth (library, queue, licences, settings) lives in Go. A worker crash interrupts its jobs; it does not lose data.

### 3.4 Seams inside package main

Areas call each other only through these functions and fields. A thread that changes one keeps its signature, or coordinates through main.

| Seam | Defined in (owner) | Used by |
|---|---|---|
| `a.showPage(name)`, `a.currentPage()` | shell.go (C) | studio.go (`reuse`), tests |
| `a.engineSummary() engineSummary` | enginelink.go (B) | shell.go (sidebar; later the status bar and ⌘E) |
| `a.startSynthesis(take, req)`, `a.preload()`, `a.cancel()`, `a.unload()`, `a.restartEngine()` | enginelink.go (B) | studio.go, studio_view.go, engine_view.go |
| `a.job` (`kind`, `status`, `fraction`, `audioSeconds`, `started`): the UI's view of the running job | enginelink.go (B) | studio_view.go (A) |
| `a.engState`, `a.loadedModel`: still read by studio_view.go (Preload/Unload) | enginelink.go (B) | studio_view.go (A) |
| `a.takeFinished(take)` | takes.go (A) | enginelink.go (B), on `done` |
| `a.preset()`, `a.generate()` | studio.go (A) | shell.go (⌘↩), enginelink.go (B) |
| `a.toast(msg)`, `a.post(fn)` | app.go (C) | everyone |

## 4. Contracts (summary)

| Contract | File | Implemented by |
|---|---|---|
| Domain types (Take, Voice, Project, LicenceAcceptance, Notification, LogEntry), `NewID` (ULID) | `internal/core/{take,domain,ids}.go` | done |
| Event bus: `Bus.Publish/Subscribe`, `core.On[T]`, `core.NewStream` (buffered, lag flag) | `internal/core/bus.go` | done (small, tested) |
| Services: `TakeService`, `VoiceService`, `ProjectService`, `LicenceService` (+ `LicenceRequiredError`), `NotificationService`, `LogService`, `DraftService`, bundled as `core.Services` | `internal/core/services.go` | A (takes, voices, projects, drafts), C (licences, notifications, logs) |
| Store: `Store` → `Takes`, `Voices`, `Projects`, `Licences`, `Documents`, `jobs.Store`; schema sketch | `internal/store/store.go`, `schema.sql` | A (SQLite); historyjson implements `Takes` today |
| Jobs: `Lane` (gpu 1, listen 1, media 2, net 3, cpu 2), `Priority`, `State`, `Spec`, `Job`, `Progress`, `Error`, `Node` (DAG), `Scheduler`, `Executor`, `Store`, event `Updated` | `internal/jobs/jobs.go` | B |
| Engine supervisor: `Supervisor.Start/Call/Status/Restart/Stop`, `Config` (roles, memory budget, restart budget), `WorkerStatus`, event `StatusChanged` | `internal/engine/supervisor.go` | B |
| Protocol v2: envelope, methods, params/results, error codes, `LegacyReady`; fixtures | `internal/engine/proto`, `engine/Fixtures`, [protocol-v2.md](protocol-v2.md) | B (Swift side + supervisor) |
| Catalogue: `Catalog{version, entries}`, `Entry{id, label, family, repo, revision, tasks, languages, voices, capabilities, sampleRate, size, files, minMemoryGB, quantization, licence}`, `Licence{id, name, url, commercial, requiresAcceptance, gated, verified}` | `internal/catalog` | C (data + validation done; C extends) |
| Downloader: `CacheDir`, `RepoDir`, `Downloader.Status/Plan/Download/Verify/Delete`, `Options` (mirror race, token), event `DownloadProgress` | `internal/hub/hub.go` | C |
| Settings: `Settings{general, appearance, studio, models, engine, privacy}`, `Defaults`, `Migrate`, `Service.Get/Update`, event `Changed` | `internal/settings` | C |
| i18n: `T`, `N`, `X`, `SetLocale`, `Locale`, `Match`, `Supported`; catalogs `locales/<tag>.json` | `internal/i18n` | C (stub done; every UI string is already wrapped) |

## 5. The three parallel threads

All three branch from this commit (`pi/c4-m0a`). **No thread opens sub-threads.** Each keeps `CGO_ENABLED=0 go test ./...`, `go tool mygo vet .` and `TestSnapshots` green on Linux, and merges back through main.

### Ownership (who may edit what)

| Path | A: services + SQLite | B: protocol v2 + supervisor + jobs | C: shell + catalogue + i18n |
|---|---|---|---|
| `internal/core/**` | **owner** | request changes | request changes |
| `internal/services/**` (new) | **owner** | — | — |
| `internal/store/**` | **owner** | — | — |
| `internal/audio/**` | **owner** | — | — |
| `internal/jobs/**` | implements `jobs.Store` only in `internal/store` | **owner** | — |
| `internal/engine/**`, `engine/**` (Swift, Package.*, Fixtures, Tests) | — | **owner** | — |
| `scripts/build-engine.sh`, `scripts/smoke-engine.sh`, `.gitignore` | — | **owner** | — |
| `docs/protocol-v2.md` | — | **owner** | — |
| `internal/catalog/**`, `internal/hub/**`, `internal/settings/**`, `internal/i18n/**` | — | — | **owner** |
| `internal/licence/`, `internal/logs/`, `internal/notify/` (new; services C implements) | — | — | **owner** |
| `studio.go`, `studio_view.go`, `takes.go`, `takes_view.go` | **owner** | — | — |
| `testutil_test.go`, `studio_test.go`, `takes_test.go` | **owner** | — | — |
| `enginelink.go`, `engine_view.go`, `enginelink_test.go`, `e2e_test.go` | — | **owner** | — |
| `main.go`, `app.go`, `shell.go`, new `shell_*.go`, `shell_test.go` (new) | — | — | **owner** |
| `snapshot_test.go`, `mygo.json`, `resources/**`, `docs/screenshots/**` | — | — | **owner** |
| `go.mod` / `go.sum` | none expected (the SQLite plugin is in the mygo module) | none expected | may make purego direct (Keychain); tell main |
| `README.md`, `docs/architecture.md` | main only, at merge time | ← | ← |

Rules:
- **Every user-visible string goes through `i18n.T`**, in any file. C owns `locales/*.json` and the key-parity test, and fills zh-Hans for the strings that exist when C finishes. Strings added later by A or B are translated by whoever merges last (main tells C).
- **Need a change in a file you do not own?** Write it under "Decisions needed" in your handoff instead of editing. Exceptions: a one-line call-site change at a §3.4 seam, plus fixing the compile errors your own contract change causes in others' files. Note both in the commit message.
- **Wiring in `main.go` (C).** A and B add their wiring as functions in their own files (`openTakeStore`, `newEngineProcess`, `connectEngine`). main.go calls them, and C adds the call lines when asked. To keep it simple, A and B may each add **one** line to `main.go`, inside a comment-marked block.

### Thread A: service layer + SQLite + migration + UI-thread I/O

- **Store.** `internal/store/sqlitestore` on `github.com/egoist/mygo/plugins/sqlite`:
  - migration 1 from `schema.sql`;
  - backup before migrating;
  - FTS triggers;
  - `jobs.Store` on the jobs tables.
  - Note: the plugin's `go test` downloads a prebuilt SQLite library (checksum-verified). Check this works offline in CI, or set `MYGO_SQLITE_LIBRARY`.
- **Import.** Bring in `history.json` on first run: read it with `historyjson.Read`, keep the file, and mark the import done in `documents` (`migration:historyjson`).
- **Services.** `internal/services`:
  - `TakeService` (Generate against a `jobs.Scheduler`, faked in tests until B lands);
  - `VoiceService` and `ProjectService` (CRUD stubs are fine in M0);
  - `DraftService` (debounced).
  - Every service publishes on `core.Bus`.
- **UI.** Studio and History go through the services:
  - snapshots are refreshed from `TakeCreated`/`TakeDeleted`, not by `reloadTakes()` on the main thread;
  - Studio drafts autosave.
- **UI-thread I/O.**
  - Peaks: computed by a background job and cached on disk (`<PathCache>/peaks/<take>.f32`); a placeholder is drawn until they are ready.
  - Player: `myaudio.OpenStream`.
  - Export: `io.Copy`.
- **Tests:**
  - the store against a temp DB;
  - the importer against an MVP `history.json` fixture;
  - services with a fake scheduler;
  - the existing view tests.

### Thread B: protocol v2 (Swift) + supervisor + job queue + multi-role engine

- **Swift.**
  - Protocol v2 per [protocol-v2.md](protocol-v2.md): `Codable` types mirroring `proto`, `engine.hello`, the job registry by id, `job.cancel`, `job.progress`, streaming `tts.chunk`, `--role`.
  - Multi-model residency (an LRU in `Synthesizer`).
  - `FixtureTests.swift` decoding `engine/Fixtures`.
  - Engine CLI smoke mode kept.
- **Go.**
  - `engine.Supervisor` implementation: spawn per role, handshake with a timeout, `LegacyReady` detection, id routing, cancel with a grace period, restart with backoff, stats polling, memory-budget eviction, `StatusChanged`.
  - A scripted v2 fake engine for tests, in Go: a test binary or `os/exec` of the test itself, replaying the fixtures.
- **Jobs.** The `jobs.Scheduler` implementation:
  - lanes, priority, DAG, cancel cascade, retry of retryable errors, interrupted-on-restart;
  - an in-memory `jobs.Store` for tests;
  - executors for `models.load` and `tts.synthesize` over the supervisor.
- **UI.** `enginelink.go` moves to supervisor + scheduler:
  - `startSynthesis` submits a job; `jobs.Updated` fills `a.job`; success calls `a.takeFinished`;
  - `engineSummary` is filled from `Supervisor.Status`;
  - the Engine page shows the workers.
  - The v1 client is deleted once nothing uses it.
- **Mac** (via `scripts/mac.sh`, when the Mac is free):
  - build, e2e and smoke tests;
  - the spike list from the plan §4 M0.5: mlx-swift 0.32.3 retest, offline load from a local snapshot path, two MLX processes' memory, Demucs/RoFormer build, OmniVoice/Sortformer/DeepFilterNet smoke.

### Thread C: app shell + catalogue/downloader + licence gate

- **Shell.**
  - `ui.Router` replaces `page`; routes `/studio`, `/history`, `/engine`, `/settings/{section...}`, `/models`, `/logs`. `showPage` maps names to paths.
  - Sidebar with ⌘1–9.
  - `audiosmith://` deep links (`mygo.json` `urlSchemes`, `App.OnOpenURL`).
  - Status bar (engine via `engineSummary`, jobs, version, logs toggle).
  - Command palette ⌘K over a command registry, and the `?` shortcut sheet.
  - Notification centre plus toasts (`core.NotificationService` in `internal/notify`).
  - Logs panel (`core.LogService` ring + rotated files in `internal/logs`).
  - ⌘E quick switch (UI over `engineSummary` plus B's unload/flush calls).
- **Settings.**
  - `settings.Service` implementation, persisted through `store.Documents` (`settings:app`). Until A merges, back it with a JSON file behind the same interface.
  - Settings pages: General (language), Appearance (theme, scale), Models (cache dir, mirrors, token), Engine (memory budget).
  - Theme scale: replace hard-coded pixel sizes with `Theme.Rem/Space`.
- **i18n.**
  - en + zh-Hans catalogs for every string, catalogue `about` texts included;
  - the key-parity test (scan `i18n.T/N/X` literals in all `*.go`);
  - language switch at run time, rebuilding the native menu;
  - pass the language to MyGo's updater later.
- **Catalogue and hub.**
  - Verify the licences of the 5 presets (two are `verified:false`).
  - Add entries planned for M1 only if their family loads; check with B's `hello.families`.
  - Implement `hub.Downloader`: HF API listing, parallel ranged + resumable downloads, sha256, refs written last, mirror race (`hf-mirror.com`), token from the Keychain (purego `SecItem*` or the `security` CLI), disk preflight. It runs as `model.download` jobs once B's scheduler lands; until then, use a direct goroutine behind the same interface.
  - Models page: list, download, progress, delete, verify.
- **Licence gate.** `core.LicenceService` (`internal/licence`) over `store.Licences` (in memory until A merges), plus the acceptance dialog. The Studio's Generate path calls `Check` (integration I1).

### Merge order and integration

1. **Merge order.** Merge each thread as soon as it is green, in any order; conflicts are limited to the §3.4 seams. Preferred: **C** first (mostly new files), then **A**, then **B**. B touches the most behaviour and benefits from A's `jobs.Store` and C's catalogue.
2. **Integration steps after the merges** (main assigns them; each is small):
   - **I1.** Studio Generate → `TakeService.Generate` → scheduler → supervisor. The licence check and hub status are part of Generate.
   - **I2.** The status bar and ⌘E read `engine.Supervisor.Status` and the job list.
   - **I3.** Settings drive the Studio defaults (model, autoplay), the engine memory budget and the hub endpoints.
   - **I4.** `model.download` jobs on `LaneNet`; the Models page shows `jobs.Updated`.
   - **I5.** Delete the v1 client and the v1 Swift protocol.
3. **M0 acceptance** (plan §4):
   - every old feature passes its regression tests;
   - concurrent queueing and cancel by id work;
   - models download, delete and resume;
   - the UI switches between en and zh-Hans.

## 6. Testing

| What | Command | Notes |
|---|---|---|
| All Go tests | `CGO_ENABLED=0 go test ./...` | Views in `ui.Tester` with a fake v1 engine. Protocol fixtures; the catalogue pins the 5 presets. |
| MyGo vet | `go tool mygo vet .` | Runs `go vet ./...` and the UI-lifetime checks. |
| Snapshots | `AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .` | M0a: byte-identical to the MVP except the Engine page's temp path. |
| Race | `go test -race ./...` | Needs cgo; run when touching shared state (bus, services, scheduler). |
| Mac e2e | `AUDIOSMITH_E2E=1 go test -run TestEndToEnd -v .` | Real engine (thread B). |
