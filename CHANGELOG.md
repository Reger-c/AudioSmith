# Changelog

## 0.4.0-beta.6

AudioSmith joins the ActNow family: it works together with the other ActNow apps on this Mac (Dicta, Auralink, Pik, VoxtNew). The apps share one copy of each model, some settings, and, if you agree, API keys, and they don't get in each other's way. AudioSmith names another app only when a released version of it is installed; with no other ActNow app, nothing on screen changes.

### Models shared with other ActNow apps
- On first run, if another app (say Dicta) has already downloaded some of the models AudioSmith needs, the setup says so ("3 models are already on this Mac · Installed by Dicta · no download needed") and uses them. Only the rest is downloaded. Choose models… lets you pick them yourself on the Models page.
- The Models page shows which models AudioSmith shares with other apps ("Shared with Dicta"), which ones another app has loaded right now ("Also loaded in Dicta"), and which ones another app installed ("Installed · from Dicta", with Use).
- Deleting a model that another app also uses asks what to do. Remove from AudioSmith keeps the files on this Mac for that app. Delete for all apps deletes them; the other app then has to download the model again. Clean up and "Erase everything" keep what other apps use, and say which app uses it. A model another app deleted for all apps shows "Deleted in Dicta · Download again".
- Settings → Storage shows how much space AudioSmith shares with other apps. It also offers to clean up models that only uninstalled apps used.
- If you accepted a model's licence in another app, AudioSmith still shows the licence, says when you accepted it, and asks you to continue.
- Kokoro's, Chatterbox's and Whisper's helper files now always load from the copy AudioSmith downloaded, at that version. Before, the engine could download them again on first use, or pick up another app's version.

### Settings shared with other ActNow apps
- The interface language, proxy, download source and model folder are shared with the other ActNow apps. With another app installed, each of these settings says whether it applies to all ActNow apps, and you can keep a different one for AudioSmith only.
- When another app keeps its models in a different folder, the Models page offers to move AudioSmith's models there. The move can be cancelled, and models the folder already has are not copied again.

### API keys shared with other ActNow apps
- API keys and the Hugging Face token can be shared with the other ActNow apps. With another app installed, a checkbox under the key ("Let other ActNow apps on this Mac use this key") chooses whether to share it.
- AudioSmith offers a key saved in another app ("Use the DeepSeek key from Auralink"). It reads that key only after you click Use this key.
- Removing a key that another app also uses asks whether to stop using it here or to delete it for all apps. If another app deletes a shared key, AudioSmith asks you for a key again. "Erase everything" never deletes another app's key.
- The Hugging Face token is looked up in this order: `$HF_TOKEN`, the shared token, AudioSmith's own, the huggingface-cli token file.

### Shortcuts and memory
- If another ActNow app uses the same keys as the dictation shortcut, Settings → Dictation says so. The app that took the keys last shows the notice once, with Change shortcut, Open Auralink settings and Ignore. While you record a new shortcut, "Also used by Auralink (Transcribe)" warns you, but you can still save it.
- The ActNow apps share one memory budget. Before AudioSmith loads a model, it unloads its own idle models first. If that is not enough, it asks the other apps to unload models that have been idle for a minute. It does the same when they ask, and never unloads a model a job is using.
- Dictation's paste no longer overwrites the clipboard while another ActNow app is pasting.

### Logs
- Click a line in the logs (the page or the panel) to copy it. Shift-click, ⌘-click or drag to select several lines, as in Finder; ⌘A selects every line shown, Esc deselects, ⌘C copies. While lines are selected the header says how many, with Copy and Deselect. Right-click a line to copy it, the selected lines or every line shown.

### Dubbing
- The online translators (DeepL, Google, Microsoft) have an API key field in the Translate step's Advanced options. Before, the key had to be added to the Keychain by hand.

### Fixes
- The Dub page's button read "Generate dub%!(EXTRA int=1)" with one target language. It reads "Generate dub" again, and the Translate button got the same fix.
- `HF_ENDPOINT` now overrides the download source chosen in Settings. Before, the setting won.

## 0.4.0-beta.5

### Downloads and updates move to actnow.dev
- AudioSmith now has its home on our website: [actnow.dev/audiosmith](https://actnow.dev/audiosmith). Download it from [actnow.dev/download/audiosmith](https://actnow.dev/download/audiosmith) (free; the download needs a free account), and read every version's notes at [actnow.dev/audiosmith/changelog](https://actnow.dev/audiosmith/changelog). The source code stays on GitHub under the AGPL-3.0, and you can still build AudioSmith yourself.
- Updates come from actnow.dev: the daily check reads the version file there, and with Receive beta updates on also the betas' one, and offers the newer version. Copies of 0.4.0-beta.4 and earlier update to this version from GitHub, one last time.
- Settings → Diagnostics and about links to the website, the download page and the release notes.

### Models shared with other ActNow apps
- Clean up, Delete and Settings → Storage → "Erase everything" no longer delete the models Dicta or VoxtNew use while those apps are installed: Clean up lists them as "Kept: used by Dicta", and Delete says the files stay on this Mac.
- Downloads coordinate with the other apps: a file another app is downloading is not fetched twice ("Downloading in Dicta…"), and a model is not deleted while another app downloads or uses it.
- The engine no longer guesses which version of a model to load when several are on disk.

### New icon
- AudioSmith has its own icon: a white anvil with a lime spark on a green-to-teal gradient, in the same style as the other ActNow apps (macOS 26 adds its Liquid Glass look). The menu bar icon is the anvil too; while dictation listens, a dot takes the spark's place.

## 0.4.0-beta.4

### Updates and downloads behind a proxy
- Updating failed behind a proxy ("unexpected EOF" part way through the download): the updater did not use the system proxy. It now goes through the proxy of Settings → Models (automatic: the environment, then the macOS system proxy), like model downloads, and a download whose connection drops resumes where it stopped.
- Importing a video from a link also uses that proxy: yt-dlp, started from the app, did not get it.

## 0.4.0-beta.3

### Dubbing
- Importing a video from a link works again for YouTube videos offered only as separate video and audio streams (most of them now): AudioSmith gives yt-dlp the ffmpeg it uses (or one found on this Mac) to join them. Downloads stop at 1080p. Without any ffmpeg the error says to set it up in Settings → Audio tools, instead of "Requested format is not available".
- When a step fails, the dub shows why that step failed, not "A job it depends on failed".

### Diagnostics
- Failed jobs (a dub step, a render, a transcription…) are written to the log, so Settings → Logs shows why.

### Licence
- The app ships THIRD-PARTY-NOTICES.txt with the full licence texts of the 45 components it includes; Settings → Diagnostics and about → Third-party licences opens it.

## 0.4.0-beta.2

### Voices
- Hear a voice before you pick it: every voice list (Studio, Stories, Audiobook, Dub) has a ▶ on each voice, ■ to stop. Built-in speakers play a short sample that ships with the app, so they play before their model is downloaded; your own voices play the first seconds of their sample. One preview at a time; closing the list stops it. Keyboard: P plays the highlighted voice.
- Stories, Audiobook and Dub use the same voice picker as the Studio.
- Kokoro's Japanese voices are hidden, like its Chinese ones: our engine does not speak them intelligibly yet.

### Licence
- AudioSmith is now licensed under the GNU Affero General Public License, version 3 (AGPL-3.0); earlier releases, up to and including 0.4.0-beta.1, remain under the Apache License 2.0. Settings → Diagnostics and about shows the licence and links to the source code; the Local API's `/`, `/health` and capabilities document carry `source_url` and `license`. The app ships LICENSE and NOTICE.
- Contributions need the Contributor License Agreement (CLA.md): a bot asks you to sign it on your first pull request, by a comment. See CONTRIBUTING.md.

## 0.4.0-beta.1

The first signed release is a beta (a GitHub prerelease): copies that receive beta updates get the next beta, and 0.4.0 once it is out.

### Updates
- AudioSmith updates itself: once a day it reads the version file of its latest release on GitHub and, when a new version is out, shows its notes (this file's section of the version) and offers to install it, skip it or remind you later. Updates are signed; AudioSmith checks the signature before it installs one, and usually downloads only what changed. AudioSmith → Check for Updates…, the command palette and Settings → Diagnostics and about check at once; that page also turns the daily check and installing without asking on or off. Copies built on your own Mac don't update themselves.
- Releases are signed with a Developer ID and notarized by Apple, so they open without Gatekeeper's warning (docs/release.md).
- Beta updates: Settings → Diagnostics and about → Receive beta updates. Betas get new features sooner but may be less stable; turned off, AudioSmith only updates to releases. It is on in a beta copy (such as this one) and off in a release until you choose. Turning it off never goes back to an older version: a beta stays until a release newer than it is out. About shows a Beta badge next to a beta's version.
- MyGo 0.5.5: MyGo's own texts (text fields' menus, date pickers, dialogs) follow the app's language, and Chinese text is drawn with the Chinese forms of its characters.

### Transcription with speakers
- New model "Transcription with speakers" (区分说话人转写; MOSS-Transcribe-Diarize 0.9B, 8-bit, 1.3 GB, Apache-2.0): it transcribes and tells who says what in one pass. Download it from the Models page, Transcribe or the Dub page; nothing downloads by itself.
- Transcribe: a Speakers option in the toolbar. Transcripts list their speakers (S1, S2…) with their lines; click one to name it. Copy and TXT export put one paragraph per turn after the speaker's name, SRT cues start with it, VTT marks voices, JSON has each segment's speaker. Dictation and plain transcription stay on Qwen3-ASR (MOSS is about 4× slower: a 10-minute recording takes about 4 minutes on a MacBook Air M4).
- Dubbing: with the model downloaded, a source with more than one speaker is transcribed by it and every line gets its speaker from it (in our tests 1.00 of the lines right on three of four 2–4 speaker dialogues and 0.93 on a 13-minute one with five people, where Qwen3-ASR + Sortformer managed 0.50–0.69 and never found more than 2 speakers; no limit of 4 speakers); the aligner still times the words, and the glossary's terms are its vocabulary. Without it, the dub works as before (Qwen3-ASR + Sortformer) and suggests the download, then Transcribe again.
- Long recordings go in 10-minute windows that overlap by a minute, linked by who speaks in the overlap; stretches without speech are skipped, and the model's chat answers to silence ("I'm sorry, I can't assist…") are dropped.
- Local API and MCP: `speakers=true`, `model=gpt-4o-transcribe-diarize` or `response_format=diarized_json`; segments carry `speaker`. MCP `transcribe` takes `speakers: true`.

### Fewer transcription and writing models; Qwen3-ASR transcribes everything
- Speech recognition is Qwen3-ASR 0.6B ("Chinese & English transcription") everywhere: dictation, Transcribe's Fast and Accurate modes, dubbing and its read-back check, workflows, Convert, clone references and calls. It made 0.5–4 % errors on Chinese where Whisper made 8–33 %, punctuates, and is 2–4× faster (thread #44's measurements). Whisper large-v3 turbo ("Multilingual transcription", tagged Fallback) is used on its own for the languages Qwen3-ASR does not know (it knows 30) and for translating speech into English. Fast and Accurate now differ only in the word timings.
- Long files: the vocabulary is only sent with recordings up to 30 s (Qwen3-ASR copied it into the end of longer transcripts); the " The." (or "嗯。") Qwen3-ASR sometimes invents after the last sentence of a long file is dropped; files longer than about 4½ minutes are timed word by word in windows of at most 4 minutes, cut between sentences (the aligner's times broke down after about 4½ minutes).
- Removed: Parakeet TDT 0.6B v3 (no Chinese) → Qwen3-ASR, or Whisper for a language Qwen3-ASR does not know; Mel-RoFormer (its output ended in 2–3.5 s of silence, and it was 5–9× slower) → HTDemucs; Gemma 4 E4B, Gemma 4 E2B and Qwen3 1.7B (slower or broken JSON, weaker translations and clean-up) → Qwen3 4B Instruct 2507, now the only language model, named in Settings → Writing help instead of a choice. Settings that named a removed model go back to the default once, with a notice; a model you chose that is still there (Whisper) stays. API and MCP: a transcription that names Parakeet is answered by its replacement with `replaced_by` and a warning (`X-AudioSmith-Replaced-By` / `X-AudioSmith-Warning`); `GET /api/models/{id}` describes the removed models, and they are not listed. Their files stay until Models → Clean up, which now also lists them (and shows on their tab).
- Onboarding: Balanced no longer downloads Whisper; Max adds it instead of Mel-RoFormer.

### Models named for what they do
- Every model has a plain name that says what it is for — Standard voices, High-fidelity clone, Fast clone, Voice design, Fast English voices, English clone, Multilingual all-rounder; Chinese & English transcription, Writing assistant · Standard… (标准音色、高保真克隆、快速克隆…) — a one-line description and a few tags (Chinese, Cloning, Built-in voices, Low memory, Non-commercial…). Pickers, the Models page, ⌘E, Settings, the bar beside Generate, History, onboarding, notifications and messages use them.
- The family, version, size, quantisation, repository and licence are still there: in a model card's Details on the Models page, in the pickers' tooltip and ⓘ, and in History's details (Technical name). Ids, repos and saved projects don't change.
- The model picker and the Models page have a search field that finds a model by its name, its technical name, id, repo or a tag.
- Local API and MCP: model lists add `display_name` (English), `ui_name` (as the app shows it), `description` and `tags`; a friendly name picks a model too.

### Portable voices, and simpler Design, Clone and Export
- Make a portable copy of a model's built-in speaker or of a designed voice (My voices → ⋯, the Studio's voice list, or Make portable where a voice can't be used): its model says a ten-second line, evened out, and the take is saved as a cloned voice "<name> (approximate)" that every model that clones can speak with. It sounds close to the original, not the same, and says so.
- When a voice can't be used with a model, the message says why and what to do (make a portable copy, or pick a model that designs).
- Design shows only what the model can't express; what it will hear is behind ⓘ, and the seed moved into Details. Clone: style and language are under Advanced. Export audio: format, loudness, file name and extras in view; bit depth or bitrate, sample rate, channels and trimming under Advanced.

### Simpler Stories, Audiobook and Dub
- Stories and Audiobook: the inspector keeps Setup, Cast, Performance (Stories) or Chapters and Cover & details (Audiobook), and one Advanced section for the rest — language, merging and evening out voices, refinement and guidance, gaps, trimming, seed, a stem per character, a story's M4B with its cover and details, an audiobook's own pronunciations — with N changes and Restore recommended (your pacing stays).
- The Project section moved to a Project ▾ menu in the header: Save (under the title — the title is the project's name), Save a copy, New, recent projects, All projects…
- A story shows only what its model hears: personas, the scene, the AI director and line directions disappear on models that say lines as written, and a line's speed on models without one.
- Dub: each step keeps its main choices; the rest folds into Advanced at the foot of the step (speakers and review, more languages and the translator's options, voice match, the voice model's parameters, a WAV's bit depth). The buttons the editor's ⋯ menu already had left the panels; Transcribe again… and Translate everything again joined that menu.

### A simpler Studio
- The inspector shows what most takes need: the model, one Voice list (the model's speakers, your saved voices, a clip to clone, Manage voices…) and the speed. Everything else — style, reference transcript, length, refinement, guidance, sampling, seed, reading numbers, evening out the volume — is under Advanced, filled with the model's recommended settings; its header says "Recommended settings" or how many you changed, with Restore recommended inside. Advanced stays open or closed as you left it.
- The Voice controls and Audio quality buttons are gone, and so are the WAV precision (takes are kept at 24-bit; choose the file's bit depth in Export audio), Preload / Unload (in the ⌘E engine switcher, now with Unload), the autoplay switch (Settings → Studio) and the steps slider (three tiers remain).
- A clip chosen to clone is transcribed for you when it is a WAV.
- The bar beside Generate says which model and voice will speak, and how many settings you changed; a click shows the inspector.

### Recommended models and settings
- Every speech model was measured on a MacBook Air (intelligibility with two recognisers, naturalness, voice stability, speed, memory). Each page now recommends two or three models for its job and your language, with a one-line "best for" and a speed and quality hint; the others are one click away under "Show all models", and the ones that can't do the job (no cloning for a dub) are greyed out with the reason.
- Tuned defaults: Qwen3-TTS uses temperature 0.7 and top-p 0.8 unless you (or an API request) set them — the models' own 0.9 / 1.0 sounded worse and less stable; OmniVoice uses guidance 1.5 with 32 steps (16 for dubbing). The Local API and MCP tools follow the same defaults. Rendered chunks from before are made again once with the new settings.
- Removed: six speech models that misread, paused or covered too little in the tests — KittenTTS mini, MOSS-TTS-Nano, Chatterbox (multilingual), Marvis, Soprano and Pocket TTS. Seven remain: Qwen3-TTS 0.6B CustomVoice, 0.6B Base, 1.7B Base and 1.7B VoiceDesign, Kokoro, Chatterbox Turbo and OmniVoice (under Show all models). Drafts, projects, dubs, workflows, settings, history and API or MCP calls that name a removed model use its replacement — Kokoro, Qwen3-TTS 0.6B Base for MOSS-TTS-Nano, Chatterbox Turbo for Chatterbox and for cloning with Marvis, Soprano or Pocket TTS — and the app says so once; the API answers with `replaced_by` and a warning. A removed model's own speakers saved in My voices can't be used any more.
- Models → "Removed models still take X GB": Clean up deletes their files (after listing them); nothing is deleted on its own.
- Chatterbox's expressiveness control is gone with the multilingual model (Turbo does not take it).
- New dubs fit lines with "Smart fit" instead of "Lip sync": lip sync sped most Chinese lines up audibly (1.2–1.4×). Dubs you already made keep their timing.
- Kokoro's six Chinese voices are hidden (unintelligible in the tests); its English voices are the best English default.

### Stories: dialogue that flows
- Turn-aware pacing: the silence between lines follows the conversation — shorter when a character goes on or answers a question, a little longer after a statement, shortest when a line is cut off (… —), longer around narration. Three presets (Dialogue, Reading, Relaxed) in the new Performance section; new stories use Dialogue, stories made before keep their fixed gap until you pick a preset. A line's own "pause after" still wins.
- Consecutive lines of one character are said in one go, so the voice flows from one sentence into the next, then split back into lines at their pauses (player, stems and the sidecar see lines as before; a turn that cannot be split is made line by line). The pause inside a turn is kept close to the preset's.
- Each character's lines are brought toward one loudness (measured 4–7 LU apart on Qwen3-TTS before, about 1 after).
- A scene for the story, a persona for each character and a direction (with an intensity) for each line. Qwen3-TTS CustomVoice and VoiceDesign hear them as an instruction with every line; other models say lines as written, and the page says so ("Line by line"). Never spoken.
- AI director: the writing-help language model (Qwen3 4B on this Mac by default) reads the story and fills in the scene, personas and directions; edit any of them, Undo, or direct again.
- Imported scripts keep stage directions out of the spoken text: `妈妈（温柔地）：…`, `MOM (softly): …`, `[Mia] (pleading) …` become the line's direction.
- Agents: `update_longform` sets a story's scene, personas, directions, intensities and pacing, and `direct: fill | all` runs the AI director; `get_longform` shows them with the model's continuity level.
- The catalogue marks Qwen3-TTS CustomVoice and VoiceDesign as taking instructions, as the engine reports them; dubs and stories ask one predicate.

### Export audio
- History's takes have an Export button (and Export… in ⋯); the Studio's latest take, a take's details and Projects open the same Export audio dialog, which replaces the Save as… menu.
- Choose the format (WAV; M4A through the engine; MP3, FLAC and Opus with ffmpeg, greyed out with a pointer to Settings → Audio tools without it), the bit depth or bitrate, the sample rate and the channels; bring the file to −16, −18 or −23 LUFS with a true-peak ceiling; trim the silence at both ends; name the file and its title and artist tags; save the script and subtitles beside it. The dialog shows the size and length before you export, and remembers your choices for each kind of take (Studio, voice preview, converted recording, Local API).

### Writing help no longer fails during a render
- The language model on this Mac now waits for its turn when a render, dub or batch is using the engine, instead of failing with "the engine is busy": writing help, the AI director, dub translation, transcript clean-up, the direction coach, phone calls and API/MCP requests go next, ahead of queued batch work (a running job is never interrupted). These short requests don't appear in the jobs list. Their time limits count from when the model starts.
- Dictation never waits for a render: while one runs, it types the polished text at once without the model's clean-up. The Tools direction coach shows 「等待当前任务完成…」 (Waiting for the current job to finish…) and, if the job is still running after 30 s, reads the note by keywords.

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
