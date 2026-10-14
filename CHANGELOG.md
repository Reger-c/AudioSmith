# Changelog

## Unreleased

### Export audio
- History's takes have an Export button (and Export… in ⋯); the Studio's latest take, a take's details and Projects open the same Export audio dialog, which replaces the Save as… menu.
- Choose the format (WAV; M4A through the engine; MP3, FLAC and Opus with ffmpeg, greyed out with a pointer to Settings → Audio tools without it), the bit depth or bitrate, the sample rate and the channels; bring the file to −16, −18 or −23 LUFS with a true-peak ceiling; trim the silence at both ends; name the file and its title and artist tags; save the script and subtitles beside it. The dialog shows the size and length before you export, and remembers your choices for each kind of take (Studio, voice preview, converted recording, Local API).

## 0.3.0 — 2026-10-14

A new look for every page: flat, quiet and black-white-grey, in the spirit of Typeless. Nothing was removed; some actions moved into menus. Your library, voices and settings carry over unchanged.

### The new look
- One dark (ink) button per page for its main action — Generate, Create audiobook, Transcribe, Run workflow, Export — and quieter buttons for everything else.
- Colour only where it means something: a teal accent for state (selection, progress, the played part of a waveform, switches) and red-orange for recording and deleting.
- New icons everywhere (Solar), including the sidebar, the icon rail, the status bar and the dictation pill.
- Flat cards with a hairline or a light grey fill, round play buttons, grey filter chips and tags.

### Calmer pages
- Secondary actions are in "⋯" menus and drop-downs (Save ▾, Insert ▾, Add ▾, Projects ▾); help text is behind ⓘ tooltips; results show as small toasts in the page header, where they no longer cover buttons.
- Inspectors and lists open beside the page when there is room, or as drawers over it in a small window, with the same panel button on every page.
- Nothing overflows at the smallest window (820×520) or with 125 % text: toolbars drop their labels, search fields and filters move to two lines, long names end in "…".
- Notes on a page (warnings, finished steps) are one-line notices you can dismiss, instead of coloured boxes.

### Settings
- Settings opens as a dialog over the page you are on (⌘,), with its sections on the left; the page keeps its state behind it.
- The accent colour choice is gone (the accent is teal); theme, text size, icon rail and reduce motion stay.

### Pages
- Studio: the script fills the page; Voice controls and Audio quality open as popovers; the inspector can be hidden.
- Stories and Audiobook: flat line rows, an inspector with sections that fold, a ready card like the Studio's with one Save menu (whole book, one WAV per chapter, chapter times, stems). The read-along shows Chinese text without spaces between characters.
- Dub: a quiet step bar, the step's settings as a side panel, one main button at the foot for the current step.
- Voices, Gallery, Design, History, Projects, Transcribe, Batch, Workflows, Calls, Tools, Integrations, Models, Engine: the same header, search and card patterns; Workflows' zoom buttons stay visible next to the step panel.
- The first-run wizard has step breadcrumbs and a single Continue button.

## 0.2.0 — 2026-10-14

The first build meant to be installed and used: everything below runs on the Mac, offline once its models are downloaded. It follows the open-source edition of VoiceStudio feature by feature (no cloud, no account).

### Studio
- Script editor with markup: pauses, emphasis, `[voice:…]` switches, `[slow]`/`[fast]`, non-verbal tags (`[laughter]` on OmniVoice); imports TXT, Markdown, EPUB and DOCX.
- Models: Kokoro, Qwen3-TTS (CustomVoice, Base, VoiceDesign), OmniVoice, Chatterbox, Marvis, Soprano, Kitten, Pocket TTS, MOSS-TTS Nano; controls follow the model (speed, sampling, seed, target duration, steps, guidance).
- Long scripts in chunks with a streaming preview and a chunk cache (a re-render only redoes what changed).
- Mastering ("Even out volume", −18 LUFS), pronunciation dictionary, audio check (clipping, clicks, long silences).
- Save as WAV 16/24/32-bit (MP3, FLAC and Opus with an ffmpeg on the Mac), drag out, Show in Finder; History with search and stars.
- An inaudible AudioSeal watermark on generated speech (Settings → Privacy; Tools → Watermark checks a file).

### Voices
- Clone from a recording or a file: best part picked, automatic transcript, noise cleaning, consent statement.
- Design a voice from a description (Qwen3-TTS VoiceDesign, OmniVoice tags) with a seed for its identity.
- Gallery of ~1,100 archetypes with previews; portraits, tags, favourites; `.asvoice` export/import and VoiceStudio `.ovsvoice` import.

### Stories, Audiobook, Projects
- Stories: multi-voice scripts, Auto-cast of screenplay-style text, a cast per character, line auditions.
- Audiobook: chapters from the manuscript, ACX or podcast loudness per chapter, read-along with word timings, M4B with chapters and cover, stems per character, cue sheets; resumes after a quit and re-renders only edited paragraphs.
- Projects: everything made and exported, searchable.

### Dubbing and batch
- Dub a video or audio file: separation of speech and background, speaker diarization (up to 4), transcription with word timings, translation (local language model, Ollama/LM Studio, cloud LLMs, DeepL, Google, Microsoft), voice-cloned lines fitted to the timing, review checkpoints, timeline editor, A/B player, video preview.
- Exports: MP4/MOV with the original and dubbed audio tracks and soft subtitles, WAV, M4A, SRT/VTT/ASS (dual), stems, lines.
- Batch queue and a watch folder; links via yt-dlp (opt-in).

### Transcribe and dictation
- Transcripts with word times and speakers (Whisper large-v3 turbo, Qwen3-ASR, Parakeet; Qwen3 forced aligner); exports SRT, VTT, TXT, DOCX, JSON; clean-up with a language model.
- System-wide dictation (⌘⇧Space, toggle or hold) that types into the app in front, with a pill, the menu bar icon and close-to-tray.

### Local language model
- Qwen3 4B Instruct runs in the engine for offline translation and writing help (Gemma 4 E2B as the light choice); automatic routing local → Ollama → cloud only when chosen.

### Local API, MCP, integrations, tools
- OpenAI-compatible `/v1` on 127.0.0.1:3901: speech (wav, pcm, aac; mp3/opus/flac with ffmpeg; SSE), transcriptions, translations, models, voices; native `/api` with job events and WebSocket streaming; OpenAPI reference at `/docs`; optional key, LAN sharing with a PIN.
- MCP over HTTP (`/mcp`) and stdio (`AudioSmith --mcp-stdio`): generate_speech, clone_voice, design_voice, transcribe, list_voices and more.
- Setup pages for Claude Code, Cursor, Codex CLI, n8n and the OpenAI SDKs; tools: A/B voices, Convert, Rate fit, Direction, File probe, Watermark.

### Workflows
- A canvas of steps (scripts, clips, speak, transcribe, translate, convert voice, normalize, condition, export) run as resumable jobs, with six templates.

### AI phone calls (optional)
- An agent that places and answers calls over your own Twilio account (through a tunnel such as cloudflared), with a phone-quality voice, take-over, transcripts and summaries. Inert until configured.

### App
- First-run wizard: a check of the Mac, a model pack chosen by memory (Fast, Balanced, Quality, Max), privacy, dictation.
- Sidebar in sections with an icon rail, ⌘1–9, command palette (⌘K), shortcut sheet (?), status bar with engine, jobs and downloads, notifications, logs.
- Settings: appearance, models and downloads (hf-mirror.com, proxy), engine memory budget, storage (sizes, clear caches, move the data folder, erase), usage, diagnostics (self-check, bug report bundle), privacy.
- One persistent job queue: generation, renders, dubs, transcripts, workflows and downloads continue after a restart.
- Upgrades the library of earlier builds (the MVP's `history.json`, databases from migration 1 on; a backup `audiosmith.db.bak-<n>` is kept before each migration).
- UI in English and Simplified Chinese.

### Fixed in the release check
- M4B export no longer hangs on books longer than a few seconds per chapter (the encoder waited for the chapter track forever); a stalled encoder now fails the job after a minute instead.
- Qwen3-ASR and the forced aligner no longer keep a second full copy of themselves in the model folder (about 1.7 GB); copies left by earlier builds are replaced by links, and full copies of models that now load in place (Kokoro, Qwen3-TTS, Whisper) are removed.
- Kokoro no longer contacts huggingface.co for its English phonemizer at every load.

### Known limits
- **Ad-hoc signed, not notarized**: macOS blocks the first launch. Remove the quarantine (`xattr -dr com.apple.quarantine /Applications/AudioSmith.app`) or allow it in System Settings → Privacy & Security → Open Anyway. No automatic updates.
- **macOS 15 or later on Apple silicon only** (M1 or newer). 8 GB Macs: Fast pack only; 16 GB recommended for cloning and dubbing, 24 GB for the Quality pack.
- **MP3, Opus and FLAC export, WebM/MKV import and burnt-in subtitles need an ffmpeg on the Mac** (e.g. `brew install ffmpeg`, then Settings → Audio tools). WAV, M4A/AAC, M4B, MP4 and MOV work without it.
- **Diarization finds at most 4 speakers.**
- **Dictation is not streaming**: the text appears after you stop (about 0.3 s for a short sentence once the model is loaded).
- **Speech streams per sentence**, not per audio frame (the API's first audio comes after the first sentence is synthesized).
- **UI languages: English and Simplified Chinese.**
- **AI calls need your own Twilio account and a public tunnel**; call audio is not watermarked.
- **Model licences**: OmniVoice is CC-BY-NC (non-commercial; the app asks before downloading it). Marvis' card says Apache-2.0 but it was trained on non-commercial data.
- PDF import is not available yet; the watermark does not survive time-stretching beyond about ±5 %.
- Workflows are not exposed through the Local API yet; the workflow editor has no undo.

## 0.1.0

The MVP: Kokoro and Qwen3-TTS takes from a single window, history in `history.json`.
