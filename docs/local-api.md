# AudioSmith Local API and MCP

*M5 (thread #20). Code: [`internal/api`](../internal/api) (HTTP server, OpenAI-compatible and native routes, auth, OpenAPI), [`internal/mcp`](../internal/mcp) (MCP server and stdio shim), `api*.go`, `integrations*.go`, `tools*.go` (the app's side).*

While AudioSmith runs, it serves an HTTP API on **`http://127.0.0.1:3901`** (Settings → Local API: on/off, port). It is a second client of the app's services and job queue: requests run at interactive priority next to the Studio's, the licence gate applies, takes land in the History and transcripts on the Transcribe page.

| Path | What |
|---|---|
| `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/audio/translations`, `/v1/models`, `/v1/audio/voices` | OpenAI's audio API |
| `/api/…` | AudioSmith's own: voices (list, clone from an upload, design, delete, audio), `generate` (the full recipe, any length), takes, transcripts, jobs (+ SSE), models (+ download), languages, tools (probe, direction, rate fit) |
| `/ws/tts` | streaming speech over a WebSocket |
| `/mcp` | the MCP server (Streamable HTTP) |
| `/health`, `/.well-known/audiosmith-speech` | readiness; capabilities for discovery |
| `/openapi.json`, `/docs` | the OpenAPI 3.1 description (hand-written, `internal/api/openapi.json`; `TestOpenAPIMatchesRoutes` keeps it equal to the routes) and the bundled RapiDoc viewer (also Settings → Local API → Open the reference) |

## OpenAI compatibility

Point any OpenAI SDK at `base_url="http://127.0.0.1:3901/v1"` with any `api_key` (it is ignored on this Mac unless a key is required).

**Speech.** `model`: `tts-1`, `tts-1-hd`, `gpt-4o-mini-tts` (any `gpt-*-tts`) are the Studio's default model; for a saved voice that model cannot speak (a clone on Kokoro), the voice's own model or the first downloaded one that can. A catalogue id, repo or label picks a model. `voice`: a saved voice's id or name (case-insensitive; two of a name → 409 `ambiguous_voice`), a built-in speaker of the model, `default`, or one of OpenAI's names (alloy, ash, ballad, coral, echo, fable, nova, onyx, sage, shimmer, verse, marin, cedar), which pick a built-in speaker of that voice's gender. `instructions`: the delivery on instruct models (Qwen3-TTS CustomVoice gets `"Speaker, instruction"`), the voice to design on design models without a saved voice (Qwen3-TTS VoiceDesign, OmniVoice), a style hint on clones; models that take none ignore it (`X-AudioSmith-Ignored`). `input` ≤ 4096 characters (`/api/generate` takes 100,000). `speed` 0.25–4. Extensions: `language`, `seed`, `duration`, `steps`/`num_step`, `temperature`, `top_p`, `guidance_scale`, `exaggeration`, `normalize`.

| `response_format` | Answer |
|---|---|
| `wav` (the default here: OpenAI's default, mp3, needs an encoder AudioSmith lacks) | 16-bit WAV at the model's rate; **streamed as the take is made** (a sentence or so at a time; the header says "length unknown") |
| `pcm` | 24 kHz 16-bit mono little-endian, streamed the same way (resampled when the model's rate differs) |
| `aac` / `m4a` | AAC-LC in an M4A container through the engine's `media.encode`, once the take is made |
| `mp3`, `opus`, `flac` | 400 `unsupported_format` |

`stream_format: "sse"` sends `speech.audio.delta` events (base64 of the format's bytes) and `speech.audio.done` with `usage`. `normalize: true` (Even out volume) needs the whole take, so it is sent when done. Response headers: `X-AudioSmith-Take-Id`, `-Job-Id`, `-Model`, `-Voice`, `-Ignored`, `-Seed` (not on live streams). Streamed audio equals the saved take: API takes are not mastered unless `normalize` is on.

**Transcriptions.** Multipart `file` (any audio or video AVFoundation reads, ≤ 1 GiB), `model` (`whisper-1`, `gpt-4o-transcribe` → Transcribe's Accurate model; `gpt-4o-mini-transcribe` → its Fast model; or a catalogue model), `language`, `prompt`, `response_format` (`json`, `text`, `verbose_json`, `srt`, `vtt`), `timestamp_granularities[]` (`word` runs the forced aligner when the model has no word times). `stream=true` → 400. The transcript is kept on the Transcribe page (title "<file> (API)").

**Translations** (speech → English): the transcript is translated by Settings → Writing help's language model (no catalogue model translates speech yet); without one, 501 `translation_unavailable`. English speech comes back as transcribed.

**Errors** use OpenAI's shape, `{"error":{"message","type","param","code"}}`, plus data: 403 `licence_required` (with `model`, `licence`, `licenceName`, `licenceURL`), 409 `model_not_downloaded`, 404 `model_not_found`, 400 `voice_not_found` / `unsupported_format` / `unsupported_model`, 503 `engine_unavailable` (engine down or restarting) and `queue_timeout` (waited > 120 s behind other work; Settings `api.queueSeconds`), both with `Retry-After`. A client that disconnects cancels its job.

## Native API notes

- `POST /api/generate` waits for the take and answers it (`wait: false` → 202 with the job, its SSE `events_url` and the take's `audio_url`). Even out volume is on by default, as in the Studio.
- `GET /api/jobs/{id}/events`: SSE `job` events (the job, at once and at each change), then `done`. `GET /api/jobs/events`: every job.
- `POST /api/voices` (multipart `file`, `name`, `ref_text`, `language`, `style`) clones; `POST /api/voices/design` (`description`, `name`, `language`, `seed`, `model`) designs; both save to the voice library.
- `/ws/tts`: send `{"text":…, "voice":…, …}` (the fields of `/api/generate`, plus `sample_rate`: 24000 by default, 0 for the model's); receive `{"type":"start",…}`, binary PCM16 frames, `{"type":"done","ttfa_ms","rtf",…}`; `{"type":"cancel"}` stops; one request at a time per socket.

## Security

| Check | Rule |
|---|---|
| Bind | 127.0.0.1 only. LAN sharing opens a second listener on all interfaces (port + 1 by default) |
| Host (DNS rebinding) | loopback names only (127.0.0.1, localhost, ::1); on the LAN listener also the Mac's private IPs and host name |
| Origin | absent (not a browser), the API's own (the `/docs` page), or listed in Settings → Local API; preflights answered only for those |
| CSRF | state-changing `/api` requests need `X-AudioSmith-Request: 1` or the key |
| Key | optional on loopback (a Bearer sent anyway is ignored: SDKs always send one); **Require the key on this Mac too** makes it mandatory except `/health`, discovery and the reference. The key lives in the Keychain (`AudioSmith` / `api.key`); Settings shows a new key once |
| LAN | the 6-digit PIN (`X-AudioSmith-Pin`, `?pin=`, or cookie `audiosmith_pin`; new each time sharing starts, never saved) covers speech, transcription, reading voices and models, MCP; everything else needs the key; 10 wrong PINs or keys a minute → 429 |
| Files | MCP tools read paths only for clients on this Mac (or with the key); LAN clients send base64. Tools write only to the MCP folder (Settings → Local API; default `<data>/mcp`) |

## MCP

Streamable HTTP at `/mcp` (official Go SDK, stateful sessions). Tools: `generate_speech`, `clone_voice` (needs `consent: true`), `design_voice`, `describe_voice`, `transcribe`, `list_voices`, `list_models`, `list_languages`, `check_health`. Resources: `voice://{id}`, `history://recent`. Audio comes back inline (`audio/wav` content), as a file in the MCP folder, or both (Settings, or the call's `output`). Each agent may have its own voice: Settings → Local API → Each agent's voice, keyed by `X-AudioSmith-Client` or the client's MCP name.

**stdio:** `AudioSmith.app/Contents/MacOS/AudioSmith --mcp-stdio [--client <name>]` relays a stdio client to the running app, found through `~/Library/Application Support/AudioSmith/api.json` (written while the API runs). `AUDIOSMITH_API_KEY`, `AUDIOSMITH_CLIENT`, `AUDIOSMITH_API_URL` override. If the app is not running, requests get an error that says so; if it restarts, the shim opens a new session by itself.

The Integrations page (sidebar → Utilities) generates setups for Claude Code, Cursor, Codex CLI, generic MCP, the OpenAI SDK (curl, Python, JavaScript), the OpenAI Agents SDK and n8n, for the current port. They never contain the key unless the user asks; with "Require the key" on they read `AUDIOSMITH_API_KEY`.

## Tools page

A/B voices (previews, not kept), Convert (transcribe a recording, speak it in a saved voice; "Match the recording's length" uses OmniVoice's target duration, else time-stretches the take with `dsp/stretch` — the matched take joins the History), Rate fit (`translate.Fitter`), Direction (`translate.Director`, with a spoken sample on Qwen3-TTS CustomVoice), File probe (`media.probe`).
