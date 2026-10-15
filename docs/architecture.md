# AudioSmith architecture

*Sections 1–4 and 6 are kept current (v0.2.0, 2026-10-14: M0–M7a, local LLM, watermark, AI calls); §5 records how M0 was built (2026-10-13). M0a wrote the skeleton and the contracts; threads A (services + SQLite), B (protocol 2, supervisor, jobs), C (shell, catalogue, hub, i18n), #9 (text, document and language libraries), #11 (DSP), #12 (voice design, gallery) and #13 (LLM, translation) built them; the integration thread (#10) wired them together (§5). Product plan: `plan/parity-plan.md` §3–§4. Wire format: [protocol-v2.md](protocol-v2.md).*

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
│   ├── catalog/         model catalogue: catalog.json (entries, pinned commits, licences, the removed models' alias table); presets.json (recommended models per page and language, tuned defaults: thread #41)
│   ├── hub/             model files: HF cache layout, downloader, model.download jobs, token, proxy
│   ├── licence/         the licence gate (core.LicenceService) over the store's licences
│   ├── settings/        the typed, versioned settings document + Service                (leaf)
│   ├── notify/          the notification centre (kept in the store's documents)
│   ├── logs/            log ring + rotated files
│   ├── i18n/            T / N / X keyed by English source; locales/*.json; dates     (leaf)
│   ├── audio/           waveform peaks + disk cache, streaming player (UI-facing playback only)
│   ├── dsp/             pure-Go DSP (M1+; not wired yet): Buffer, gains, fades           (leaf tree)
│   │   └── wav/, resample/, filter/, dynamics/, loudness/ (BS.1770/R128), master/, edit/, qc/, analysis/, mix/, dsptest/
│   ├── text/            script pipeline: segmentation, chunk keys (M1; not wired yet)
│   │   └── markup/, normalize/, lexicon/, autocast/
│   ├── doc/             document import: TXT/MD/EPUB/DOCX, cue sheets (M1/M3; not wired yet)
│   │   └── subtitle/, textenc/
│   ├── lang/            the language table, 652 languages                              (leaf)
│   ├── llm/             provider-agnostic chat-model layer (M2/M4; not wired yet)
│   ├── translate/       translation over LLMs and MT services, glossaries (M4; not wired yet)
│   ├── voicedesign/     Voice Design: description ⇄ attributes, recipes per model (M1; not wired yet)
│   ├── ffmpeg/          the optional ffmpeg (M7a): inspect, pinned download, probe/decode/encode/transcode/mux/burn (leaf)
│   ├── dub/             dubbing's pure parts (M4): project document, segments, timing fit, mixer, peaks, exports, yt-dlp, watch folder
│   ├── dictation/       system-wide dictation (M2): hotkey, pill, paste into the front app, clipboard restore (purego/objc)
│   ├── api/             the Local API (M5): OpenAI /v1, native /api, SSE, WebSocket, auth, OpenAPI + viewer
│   ├── mcp/             the MCP server (M5) over api, and the stdio shim
│   ├── workflow/        WorkflowStudio's graph (M6): typed ports, Validate, Plan/StepKeys, templates
│   ├── telephony/       AI phone calls (M6b): Twilio REST, signed webhooks, TwiML, gateway, Media Streams WebSocket, μ-law, endpointer, agent loop
│   ├── watermark/       AudioSeal message and weight conversion for the engine (over torchpt)
│   ├── torchpt/         a reader of PyTorch .pt/.pth checkpoints (pure Go)                (leaf)
│   └── gallery/         the Voice Gallery: ~1,100 archetypes, facets, presets, preview planner (M1; not wired yet)
│       └── persona/     the .asvoice voice file (and VoiceStudio .ovsvoice import)
└── engine/              the Swift helper (audiosmith-engine), Fixtures/, Tests/
```

### Import rules

Arrows point to what a package may import. No cycles. Leaves import nothing internal.

```
main ──▶ every internal package
services ──▶ core, store, jobs, engine/proto (the job's params), i18n (job titles), voicedesign, gallery/persona (M1 voices), text/plan, text/lexicon, dsp/{edit,loudness,master,qc,resample,wav} (M1 render)
text/plan ──▶ text, text/markup, text/normalize, text/lexicon
services ──▶ doc/subtitle, dsp, dsp/{resample,wav} (M2 transcripts)
main ──▶ dictation, llm, translate (M2)
services ──▶ doc (CueSheet), engine/proto (encode output) (M3 longform)
dub ──▶ dsp, dsp/{analysis,mix,resample,wav}, doc/subtitle, text, translate, engine/proto (M4)
dub/dubtest ──▶ dub, dsp/…, engine/proto, jobs
dsp/stretch ──▶ dsp
services ──▶ dub, dsp/{stretch,dynamics,analysis}, lang (M4)
api ──▶ core, jobs, services, catalog, settings, engine/proto, translate, lang, voicedesign, dsp, dsp/{resample,wav} (M5)
mcp ──▶ api, core, settings, voicedesign + modelcontextprotocol/go-sdk (M5)
main ──▶ api, mcp
dictation ──▶ (nothing internal; mygo/transfer, purego, purego/objc)
main ──▶ ffmpeg (M7a; ffmpeg imports nothing internal)
workflow ──▶ i18n                services ──▶ workflow (M6)
telephony ──▶ dsp, dsp/resample  services ──▶ telephony (M6b); main ──▶ telephony
watermark ──▶ torchpt            main ──▶ watermark
store ──▶ core, jobs            store/sqlitestore ──▶ core, jobs, store, store/historyjson
engine ──▶ core, engine/proto, jobs (its executors)        engine/enginetest ──▶ engine/proto
hub ──▶ catalog, jobs (the download executor)              catalog ──▶ lang
licence ──▶ catalog, core       notify, logs ──▶ core
text/* ──▶ text                 doc ──▶ text, doc/subtitle, doc/textenc      doc/subtitle ──▶ text
dsp/* ──▶ dsp siblings only (wav, edit, mix, qc… ──▶ dsp; loudness ──▶ dsp, dynamics, filter; …)
llm ──▶ core                    translate ──▶ core, lang, llm, text, text/markup
voicedesign ──▶ text            gallery ──▶ voicedesign, text, jobs, engine/proto     gallery/persona ──▶ voicedesign, lang
core, jobs, engine/proto, settings, i18n, audio, lang, text, doc/textenc, dsp, dsp/filter ──▶ (nothing internal)
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
| Render: one `take.render` job per take on the gpu lane (`services.KindRender`, `RenderParams/Result/Progress`); chunks run sequentially inside it via the engine executor, content-addressed chunk cache `<cache>/chunks/<key>.wav` (LRU 2 GB); `svc.Render.Preview/Check/ExportAs/Prune`, `svc.Pronunciation` (store.Documents key `pronunciation`) | internal/services/render*.go (M1-G) | studio, voices (Try this voice, design preview), later Stories/Audiobook |
| `a.clipCard` (output panel), `a.scriptFocused()` | studio_output.go, studio*.go (M1-G) | design page, shell (`?` shortcut) |
| `a.trSvc` (`*services.Transcripts`: Transcribe/Get/List/Update/Delete/AudioPath; `services.Export/ExportAll`), job kind `transcript.make` (owner `transcript:<id>`), event `core.TranscriptChanged` | transcribe*.go, internal/services/transcripts*.go (M2) | dictation, projects, later API `/v1/audio/transcriptions` |
| `a.llmRouter`, `a.llmAvailable()` (registry/router from `settings.LLM`; HTTP via `hub.NewHTTPClient`, keys `hub.Secret("llm.<provider>")`) | transcribe_llm.go (M2) | dictation cleanup, M4 translation |
| `a.dictToggle()`, `dictation.System`/`Inserter`, `sttEntries()`, `sttEntry(id, fallback)` | dictation*.go, internal/dictation (M2) | tray, palette, settings |
| Longform: `services.Compile`, `Longform.Start/Stop/Resume/Delete/Reconcile`, `Library.*` (search/counts over projects, renders, voices, takes, exports); jobs `longform.chapter` (gpu, one per chapter) + `longform.assemble` (cpu lane, After chapters), owner `longform:<id>`; `stt.align` owner `readalong:`; `media.encode` (M4B) via `services.Config.Encode` once the engine has it | internal/services/longform*.go, projects.go (M3) | stories, audiobook, projects pages |
| Story continuity (#39): `Production.Pacing` (`services.Pacings`, `GapsOf`, `TurnOf`/`TurnGap`), merged turns (`mergeGroups` → one chunk, `splitMerged` at pauses, `tightenTurn`, fallback per line; `ChapterResult.Turns/TurnsUnsplit`), per-character levelling (`levelGains`, `Levelled`), delivery (`StoryInstruct`, `withDelivery`, `TakesInstruct`, `ContinuityOf`); `LongformDoc.Scene`, `CastMember.Persona`, `StoryLine.Direction/Intensity` (doc version 2, additive); AI director `translate.StoryDirector` (skill `direction_parse`) | internal/services/story_pacing.go, longform_render.go, internal/translate/story_director.go, stories_director.go, stories_perf_view.go | stories, API/MCP `update_longform` |
| `a.useVoiceInStories`, `a.useVoiceInAudiobook`, `a.projectUsageLink`, `a.longformVoiceItems`, `a.recordTakeExport`, `a.recordVoiceExport` | longform*.go, projects*.go (M3) | gallery, voice editor, studio output, history |
| Dubbing: `a.dubSvc` (`*services.Dubs`: Create/Get/List/Update/Delete/Run/Resume/Cancel/Plan/LineRecipe/TrackMixer/ExportJob/Enqueue/Queue/Watch…), `a.openDub(id)`, `a.dubToStory()`; events `dub.changed`, `dub.watch`; stage jobs `dub.fetch` (net) / `ingest` (media) / `separate`,`transcribe`,`references`,`synthesize:L`,`qc:L` (gpu) / `translate:L` (net) / `mix:L` (cpu) / `dub.export`, owners `dub:<id>`, `dubexport:<id>`; missing engine methods degrade a stage to skipped | internal/dub, internal/services/dub*.go, batch.go, dub*.go, batch*.go, timeline.go (M4) | projects, stories (ToStory), batch/watch |
| Local API: `a.apiSrv`, `a.apiMCP`, `a.startAPI(dataDir)`, `a.applyAPI()`, `a.apiBaseURL()`; `apiRecipe(api.SpeakRequest, fill, path)`; `a.codeBlock`, `a.toolAwait(jobID, fn)`; server 127.0.0.1:3901 (port file `<data>/api.json`), LAN listener port+1 with PIN; reference in docs/local-api.md | api*.go, integrations*.go, tools*.go, internal/api, internal/mcp (M5) | integrations, tools, MCP stdio (`AudioSmith --mcp-stdio`) |
| Agents' modules (#35): `api.Studio` (internal/api/studio*.go: types, shared checks — input files, output folder, confirm — REST routes) implemented by `apiBackend` in api_studio*.go over the pages' services and helpers (`lfResolverOf`, `autoCastText`, `applyBook`, `newDubProject`, `galleryFilterQuery`, `archetypeVoice`, `exportEncoderOf`, `writeRenderFile`…); group jobs named by their owner (`longform:<render>`, `dub:<id>`, `workflow:<id>`) for `GET /api/jobs/{id}?wait=N`, cancel and events; MCP tools `mcp.StudioTools` (internal/mcp/studio.go); `api.SkillSource` serves the agent skill (`GET /api/agent-skill`, integrations_skill.go) | api_studio*.go, internal/api/studio*.go, internal/mcp/studio.go | MCP, REST, the setup prompt |
| Workflows: `internal/workflow` (graph, typed ports, Validate, Plan/StepKeys, templates; imports only i18n), `services.Workflows` (Save/Get/List/Delete/Validate/Run), jobs `workflow.step` (one per step with work, lane by step kind, `After` = upstream steps), owner `workflow:<id>`; table `workflows` (migration 006: separate `doc` and `run` columns); files `<data>/workflows/<id>/{items,out}` | workflow*.go, internal/workflow, internal/services/workflow*.go (M6) | projects |
| Local LLM: engine `llm.generate` (gpu; streams `llm.token`; xgrammar-constrained JSON; `context_too_long`), app provider `local` (local_llm.go) registered when `settings.LLM.LocalModel` / `catalog.DefaultLLM` is downloaded, automatic routing local → Ollama → cloud only if chosen; dub/workflow Translate on the gpu lane for `local`; the engine's ModelStore unloads idle models to fit a new model in the memory budget; short-budget skills warm it first (local_llm_warm.go: a `models.load` task `llm` when dictation with clean-up starts, clean-up is switched on, or a direction note is typed — never at app start; a cold call gets 10 s for clean-up, 15 s for the direction coach, else 4 s / 10 s). Env: `AUDIOSMITH_LLM_CONTEXT` (16384), `AUDIOSMITH_LLM_GRAMMAR=0`, `AUDIOSMITH_MEMORY_BUDGET` (default 60 % RAM) | local_llm.go, local_llm_warm.go, internal/llm/local.go, engine/ (LLM.swift) | translate, dictation cleanup, API translations, tools, workflows |
| `a.voicePicker(c, p)`, `a.applyStudioVoice(&t, p)`, `a.studioUsesSavedVoice(p)`, `a.studioVoicePrompt(p)`, `a.useVoiceInStudio`, `a.openVoiceEditor`, `a.openDesignFor`, `a.startClone` | voices*.go, clone*.go, design*.go (M1-V) | studio, gallery, palette |
| `a.probeMedia`, `a.captureDevices`, `a.startRecording(params, onLevel, then)`, `a.stopRecording`, `engineErrorText(err)`, `jobErrorText(*jobs.Error)` | enginelink.go (M1) | voices/clone (reference recorder, imports), studio |
| Engine jobs: submit `proto` job kinds (`engine.ExecutorKinds`) on `jobs.Lane(proto.RoleOf(kind))`; listen/media workers start on demand (`engine.Config.OnDemand`); default models `catalog.DefaultSTT/DefaultAlign/DefaultVAD/DefaultEnhance/DefaultDictation` | internal/engine, internal/catalog (M1) | voices, studio, later dictation/dubbing |
| Listen-role STT: app kinds `listen:stt.transcribe` / `listen:models.load` (`proto.KindFor`/`SplitKind`, `engine.KindExecutor`), `TranscribeRequest.Role` → dictation runs on `jobs.LaneListen`, never behind gpu renders; `media.encode` (WAV → M4B/M4A, AAC-LC, chapter track, iTunes tags, cover; MP3 → `unsupported {capability:"mp3"}`) | internal/engine, engine/ (M1-E) | dictation, longform |
| Sidebar (M7a): `navGroups` (sections Create, Voices, Library, Automate, Utilities, System) and `shortcutPages` (⌘1–9) in shell.go place every page; areas answer `a.longformBadge/dubBadge/wfBadge(path)` for their badge and keep their routes (`longformRoute`, `dubRoute`, `m5Route`, `wfRoute`); the palette's "Go to" and the Go menu come from the same list; icon rail, width and closed sections in `settings.Appearance` (shell_nav.go) | shell*.go | every area with a page |
| Settings pages (M7a): `settingsSections()` + `m7SettingsRoute` (settings_pages.go): audio-tools, network, privacy (holds #14's `watermarkSettings`), storage, usage, about; the wizard `a.openOnboarding()` (onboarding.go; model packs = catalogue ids) | shell_settings.go, settings_*.go, onboarding.go | palette, Settings → General |
| Settings dialog (0.3): `a.navigate("/settings/<section>")` (or "/settings": the last section) opens the dialog over the page; any other path closes it. `a.settingsOpen`, `a.setRouter` (its own router: "/general", "/models", …; section views match on it as before), `a.shownPath()` ("/settings/<section>" while open, else the page's path: tests use it); `settingRow` (help as ⓘ) / `settingRowNote` (state under the name) | shell.go, shell_settings.go | palette, deep links, notifications, every settings section |
| Design tokens (0.3): `tok(t)` (greys `G[0..9]`, `Bg`, `Sidebar`, `Line`, `Text/Text2/Text3`, `Ink/OnInk`, `Accent` (teal, state only), `AccentInk`, `Record`, semantic colours), `styleTheme` puts them into MyGo's Theme in `applyTheme`; sizes `fs*`, `sp*`, `radiusControl/radiusCard` with `px` | ui_tokens.go | every view |
| UI kit (0.3, one file): buttons by rank — `primaryButton` (ink, one per view), `primaryIconButton`, `recordButton` (red-orange: record, destructive), `secondaryIconButton`, `ghostButton`/`ghostIconButton`, `labelOrIcon` (icon only when narrow), `dropButton`/`dropText` (open a menu), `iconButton(c, icons.X, tip)`, `iconToggle`, `panelToggle` (show/hide a side panel: `panel-left`/`panel-right` on the pressed face), `moreMenu` ("⋯"), `playButton` (round ink: a take or a clip) / `playToggle` (small play icon of a list row); marks — `infoTip` (ⓘ), `truncText`, `caption`, `sectionHeading`, `tagChip`, `chip`, `statusDot`, `statusMark`, `iconMark`; layout — `pageHeader`, `card`/`fillCard`, `collapsible`, `formRow`, `fieldInfo`, `searchField`, `filterBar` (search + filters, two lines when narrow), `emptyState` (`kbd` is in shell_commands.go, `sectionTitle` in shell_settings.go); notes — `banner` (a line of state with any icon) and `notice` (a banner by kind info/ok/warn/error, with ⓘ and ×); side panels — `a.pageWidth`, `a.panelFits`, `sidePanel` (inline column or a drawer under the header), `panelHeader`, `panelState` (`layout`/`toggle`/`view`: right or left, inline or drawer). Forms and dialogs — `segmentedFit`, `segmentedSome` (segments that may be greyed out, with a tip), `numberInput`, `popoverWithFooter`, `dialogFrame` (a dialog in the Settings dialog's look: title + caption + ×, scrolling body, footer of actions under a hairline; the Export audio dialog, export_dialog.go). Toasts: `a.toast` / `c.ToastAction`, drawn by `a.toastsView` (ink, top of the page under its header). Rules and per-page guidance in research/ui-design-spec.md | ui_kit.go, shell.go (toasts) | every view |
| Icons (0.3): Solar (CC BY 4.0) as `icons.X.Linear` / `.Bold` / `.Get(selected)`; add a name to `internal/icons/names.txt`, then `go generate ./internal/icons` (downloads @iconify-json/solar 1.2.13, sha256-checked; commits only `solar_gen.go`) | internal/icons | every view |
| ffmpeg (M7a): `a.mediaTool()` (`*ffmpeg.Tool`, nil = none); the engine supervisor is wrapped (`withMediaTools`, ffmpeg_engine.go): media.probe/decode/thumbnail/transcode/mux fall back to ffmpeg on `unsupported_media`, media.encode to mp3/opus/flac/ogg runs on ffmpeg, app-level `media.burn` (`services.MethodBurn`); without ffmpeg `unsupported {capability:"ffmpeg"}`; `api.FormatEncoder` for the API's formats | ffmpeg_*.go, internal/ffmpeg | dub exports, books, Studio Save as…, API |
| Data folder (M7a): `resolveDataDir(home)` in main finishes a pending move or erase before the library opens; `location.json` in the default folder points to a moved one (`dataFolder` for the MCP stdio shim) | storage_move.go | main.go, api_stdio.go |
| AI calls (M6b): `a.callSvc` (`*services.Calls`: Place/Get/List/Delete/Hangup/Takeover/Say/PhonePreview/Reconcile), event `services.CallChanged`; `telephony.Gateway` on 127.0.0.1:3950 (only when `settings.Calls.Enabled`; Twilio webhooks + Media Streams WebSocket), `telephony.Agent` per live call; speech through the queue: `listen:stt.transcribe` (listen lane) and `take.render` jobs owned by `call:<id>/<n>` (no take saved, no watermark); LLM skill `call_agent`; table `calls` (migration 007); Keychain `twilio.token`; Projects category Calls; badge `a.callsBadge` | calls*.go, twilio*.go, internal/telephony, internal/services/calls*.go | integrations (`/integrations/twilio` → `/calls/setup`), projects, sidebar (Automate) |
| Watermark: `services.Config.Watermark` (`l.watermarkRender`) marks every finished `take.render` (24-bit in place, `watermark.embed` on the media worker with AudioSeal); dub marks the final mixed track and its speech stem (`Dubs.watermarkTracks`), not lines; `l.detectWatermark` for Tools → Watermark; per-install message id `settings.Privacy.WatermarkID`; off with `Privacy.NoWatermark` | watermark_app.go, internal/services/watermark.go, internal/watermark | studio, dub, tools, settings → privacy |
| First run (M7a): `a.openOnboarding()` — steps Mac check (`loadSysInfo`: sw_vers/sysctl, disk, network/mirror, engine, ffmpeg, permissions), model pack (`modelPacks`: fast/balanced/quality/max, each adds catalogue ids to the smaller ones; `recommendPack(memGB)`, `packSizes`), privacy, dictation; `settings.Setup` records it; the welcome line plays when Kokoro is ready (`resources/demo/welcome-<locale>.wav` may be shipped to play earlier) | onboarding.go, sysinfo.go | main (first launch), Settings → General |
| Catalogue fields (M1-E, measured on M4 by the smoke suite): `rtf`, `maxChars` (hard limit for Kokoro, recommended chunk size otherwise), `deterministic` (chunk cache ignores seed), `nonVerbal` (OmniVoice tags), `stream` (Qwen3-TTS only), `guidance` (OmniVoice) | internal/catalog/catalog.json | render, studio, dictation |
| Removed models (#43): `catalog.Removed` (catalog.json `removed`: id, label, repo, its own aux repos, `replacedBy`, `cloneReplacedBy`), `Catalog.RemovedModel(ref)` / `Replace(ref, clone)`; the app maps on load (`applyStudioDraft`, `setLfDoc` → `replaceRemovedDoc`, `dubShow`, batch settings, `wfShow` → `replaceRemovedWorkflow`, `reuse`, `migrateRemovedSettings` at start) with one notice per model (`noteRemoved`), and every render maps again (`services.Config.Replace` = `replaceRemovedTake`); `entryFor(repo)` answers the replacement; the API answers with the replacement + `replaced_by`/warning (`api.Replacement`), 400/410 `model_removed` when it can't; Models page `removedRow`/`cleanRemoved` → `hub.Leftovers`/`RemoveLeftovers` (exact repo dirs + engine copies, on request only) | models_removed.go, internal/catalog, internal/hub/local.go, internal/api/resolve.go | studio, stories, audiobook, dub, batch, workflows, calls, API/MCP |

## 4. Contracts (summary)

| Contract | File | State |
|---|---|---|
| Domain types (Take, Voice, Project, LicenceAcceptance, Notification, LogEntry), `NewID` (ULID) | `internal/core/{take,domain,ids}.go` | done; Take gained `Duration`, `Steps` (OmniVoice) |
| Event bus: `Publish/Subscribe`, `core.On[T]`, `core.NewStream` | `internal/core/bus.go` | done |
| Services, bundled as `core.Services` | `internal/core/services.go`, `internal/services` | done; `TakeRequest.Recipe.ID` may be chosen by the caller |
| Store: Takes, Voices (+Usage), Projects, Licences, Documents, Favorites, `jobs.Store` | `internal/store`, `sqlitestore/migrations/001_init.sql`, `002_voices.sql` (M1: voices search/origin, favorites) | done (SQLite) |
| Jobs: lanes, priorities, states, DAG, retry, interrupted-on-restart | `internal/jobs` | done |
| Engine supervisor: `Start/Call/Status/Restart/Stop`, `SetMemoryBudget`, `SetIdleUnload`, `SetCacheDir` | `internal/engine/supervisor.go` | done |
| Engine executors: `RegisterExecutors(sched, sup, ExecutorOptions{ModelPath, Licences})` | `internal/engine/executors.go` | done |
| Protocol 2 (+ `SynthesizeParams.modelPath`, OmniVoice `steps`/`duration`) | `internal/engine/proto`, `engine/Fixtures`, [protocol-v2.md](protocol-v2.md) | done |
| Catalogue v2: entries, aux repos, hidden, **pinned commits**, licences, language codes validated with `lang` | `internal/catalog` | done |
| Downloader: `Status/Plan/Download/Verify/Delete`, mirror race, proxy, token; `DownloadSpec`, `DownloadExecutor`, `EntryOf`, `ErrorOf`; for other network users `ProxyFunc`, `NewHTTPClient`, and Keychain items `Secret(account, label)` | `internal/hub` | done |
| Settings document + `Service` (in the store's documents, `settings:app`) | `internal/settings` | done |
| Licence gate, notification centre (persists info/success/warning), logs | `internal/licence`, `internal/notify`, `internal/logs` | done |
| i18n: `T`, `N`, `X`, `SetLocale`, `Match`, `Supported`, `Name`, `DateTime`, `Date` | `internal/i18n` | done (en, zh-Hans) |

## 5. M0: how it was built, and what changed from the sketch

### Threads and merges

M0a (`ff57a69`) wrote the package skeleton and these contracts. Three threads then worked in parallel, each owning files, with no sub-threads: **A** services + SQLite (`5d98053`), **C** shell, catalogue, hub, i18n, settings, licences (`6e88272`), **B** protocol 2 in Swift and Go, supervisor, scheduler (`8be41fb`). Thread **#9** added the pure-Go libraries `text`, `doc` and `lang` for M1 (`ae59b3d`), thread **#11** the DSP tree `dsp` (`84e716b`): mixing, loudness, mastering, QC and editing live there, not in `internal/audio`, which keeps peaks and playback; thread **#12** `voicedesign`, `gallery` and `gallery/persona` (`ecd3762`); and thread **#13** `llm` and `translate` (`fcb99f5`). The integration thread (#10) did steps I1–I5 and the leftovers:

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
| `internal/dsp/**` | thread #11's successor (M1 mastering, QC, joining chunks); the UI wires it in M1 |
| `internal/voicedesign/**`, `internal/gallery/**` | thread #12's successor (M1 voice library, design page, gallery); the UI wires them in M1 |
| `internal/llm/**`, `internal/translate/**` | thread #13's successor (M2 dictation clean-up, M4 dubbing translation). Network calls use `hub.NewHTTPClient` (the settings' proxy) and keys live in the Keychain as `hub.Secret("llm.<provider>" / "mt.<service>", …)` |
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
| Voices | `…/AudioSmith/voices/<voice id>/` (reference, locked.wav, sample, consent, portrait, prompts); unsaved clips in `…/AudioSmith/references/` |
| Transcripts | `…/AudioSmith/transcripts/<id>.wav` (16 kHz mono copy; table `transcripts`, migration 003); dictation temp `…/AudioSmith/dictation/*.wav` |
| Renders (Stories/Audiobook) | `…/AudioSmith/renders/<id>/{chapters/NNN.wav(.json), book.wav, cues.txt, report.json, stems/}`; covers `…/AudioSmith/covers/`; longform chunk cache `~/Library/Caches/AudioSmith/longform/chunks/` (60 days / 24 GB); auditions `…/Caches/AudioSmith/auditions/`; tables `renders`, `exports` (migration 004) |
| Dubs | `…/AudioSmith/dubs/<id>/` (audio.wav, speech.wav, stems, peaks, poster.jpg, preview.mp4, refs/, renders/<lang>/<seg>-<fp>.wav, tracks/<lang>.wav, download/); table `dubs` (migration 005); documents `dub:batch`, `dub:watch`; yt-dlp in `…/AudioSmith/tools/`; video served on `asmedia://localhost/<id>/video` |
| Chunk cache | `~/Library/Caches/AudioSmith/chunks/<text.Key>.wav` (float32, pruned to 2 GB LRU) |
| Gallery previews | `~/Library/Caches/AudioSmith/voice-previews/<key>.wav` (job owner `archetype:<id>`; voice jobs use owner `voicework:`) |
| Workflows | `…/AudioSmith/workflows/<id>/{items,out}`; table `workflows` (migration 006) |
| Calls | `…/AudioSmith/calls/<id>.wav` (recordings, only when on and disclosed); table `calls` (migration 007) |
| Local API | `…/AudioSmith/api.json` (port file while running), `api-tmp/` (wiped at start), `mcp/` (MCP output folder) |
| Tools | `…/AudioSmith/tools/` (yt-dlp; an ffmpeg chosen in Settings is used where it is) |
| Logs | `~/Library/Logs/AudioSmith/audiosmith.log` (app, rotated at 5 MB) and `engine-gpu.log` (engine stderr) |
| Models | the Hugging Face cache, `~/.cache/huggingface/hub` (`HF_HUB_CACHE`/`HF_HOME`, or Settings → Models → Model folder) |
| Secrets | the login Keychain, service "AudioSmith": account "huggingface-token" (HF token; or `$HF_TOKEN`, or huggingface-cli's token file), "llm.<provider>", "mt.<service>", "api.key", "twilio.token" |
| Engine model copies | `<HF cache>/mlx-audio/<owner>_<name>/`: mlx-audio-swift's own copy, made only for repos it resolves by id (auxiliary g2p repos; families without a directory loader such as OmniVoice); deleted with the model (`hub.Delete`) |

### Engine dependencies (M1-E)

The engine builds on upstream Blaizzy/mlx-audio-swift (pinned commit, see README Versions) with mlx-swift 0.32.3. AudioSmith's changes are a patch series in `engine/patches/mlx-audio-swift/` applied into `engine/.build/deps/` by `scripts/fetch-engine-deps.sh` (re-applies only after a change). Every catalogue model must pass `scripts/smoke-models.sh` (downloads with the app's hub, runs offline, reads TTS back with Whisper, F0 check for design gender, measures limits and streaming; report in `/tmp/audiosmith-smoke/`) before it is listed.

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
| Model smoke | `scripts/smoke-models.sh [ids]` | Mac + network: every catalogue model, per-model report |
| Dictation latency, long render, real media | `AUDIOSMITH_E2E=1 go test -run 'TestDictationLatency|TestLongRender|TestRealEngineMedia' -v ./...` | Mac (see README) |
| Keychain, proxy | `AUDIOSMITH_MAC=1 go test -run TestMacKeychainAndProxy -v ./internal/hub` | Mac |
