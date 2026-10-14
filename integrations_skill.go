package main

// The agent skill (thread #34, a map of the app since #35): a SKILL.md
// (the Agent Skills format that Claude Code, Codex and Cursor read) that
// tells an agent in every new chat what AudioSmith is, what each of its
// pages does and which tools do it (or that it stays in the app), how to
// check that it runs, the long-job pattern, every tool, recipes and
// manners; every tool's arguments are in reference/tools.md beside it. Without it a new
// chat sees only bare tool names. The setup prompt (integrations_prompt.go)
// installs it with a shell script; the agent pages can install, save or
// copy it (integrations_view.go). It is made from the live settings (port,
// client, voices, the agents' folder) in the UI's language; commands,
// tool and argument names stay literal; it never carries the API key.
//
// Where each agent reads user-level skills (checked 2026-10-14):
//   - Claude Code: ~/.claude/skills/<name>/SKILL.md (code.claude.com/docs/en/skills)
//   - Codex: ~/.agents/skills/<name>/SKILL.md (developers.openai.com/codex/skills)
//   - Cursor: ~/.cursor/skills/<name>/SKILL.md, also ~/.agents/skills and
//     ~/.claude/skills (cursor.com/docs/skills)
//
// TestAgentSkill keeps the tools and arguments equal to the MCP server's.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// skillName is the skill's name and folder.
const skillName = "audiosmith"

// skillEOF ends the skill's heredoc in the setup prompt's script.
const skillEOF = "AUDIOSMITH_SKILL"

// skillDir is the user-level folder of an agent's skill, from the home
// folder ("~/.claude/skills/audiosmith").
func skillDir(slug string) string { return promptAgents[slug].Skills + "/" + skillName }

// skillFile is the user-level path of an agent's SKILL.md
// ("~/.claude/skills/audiosmith/SKILL.md").
func skillFile(slug string) string { return skillDir(slug) + "/SKILL.md" }

// skillArg is one or more arguments of a tool and what to know of them.
type skillArg struct {
	Names    []string
	Text     string
	Required bool
}

// skillTool is a tool in the skill: what it is for, its arguments, and
// what it returns.
type skillTool struct {
	Name, Lead string
	Args       []skillArg
	After      string
}

// skillTools are the MCP tools as the skill explains them; their
// arguments are all of the schema's (TestAgentSkill).
func skillTools() []skillTool {
	arg := func(text string, names ...string) skillArg { return skillArg{Names: names, Text: text} }
	must := func(text, name string) skillArg { return skillArg{Names: []string{name}, Text: text, Required: true} }
	return []skillTool{
		{Name: "generate_speech", Lead: i18n.T("text to speech, as a WAV."), Args: []skillArg{
			must(i18n.T("What to say. Long text is fine (up to 100,000 characters): AudioSmith splits it into sentences and joins the take; for a whole book, one call per chapter or section keeps each call short. %s inserts a pause.", "`[pause 500ms]`"), "text"),
			arg(i18n.T("A saved voice's name or id (list_voices), a built-in speaker of the model (list_models → voices) or an OpenAI name (alloy, nova…). Empty: your own voice, else the model's default."), "voice"),
			arg(i18n.T("A model id from list_models. Empty: AudioSmith's default, or a model that can speak the voice. Models marked needs_reference (MOSS-TTS-Nano) speak cloned voices only."), "model"),
			arg(i18n.T("BCP-47 code (en, zh, ja…). Empty: the voice's or the model's."), "language"),
			arg(i18n.T("How to say it, on models that take it (\"calm and slow, with a smile\"); on a voice-design model without a voice, the voice to design."), "instructions"),
			arg(i18n.T("Speaking rate, 0.5–2 (1 = normal)."), "speed"),
			arg(i18n.T("The same seed gives the same take again."), "seed"),
			arg(i18n.T("true evens out the volume."), "normalize"),
			arg(i18n.T("OmniVoice only: target length in seconds; diffusion steps (8 draft, 32 quality)."), "duration", "steps"),
			arg(i18n.T("%s writes a WAV to the agents' folder and returns its path in %s; %s returns the audio in the result; %s does both. Empty: AudioSmith's setting. Pass %s: inline audio comes into your context as base64.", "`file`", "`file`", "`inline`", "`both`", "`output: \"file\"`"), "output"),
			arg(i18n.T("The WAV's name in file mode (no folders; .wav is added). A file of that name is replaced."), "filename"),
		}, After: i18n.T("Returns take_id, model, voice, audio_seconds and file. Every take is also in AudioSmith's History.")},
		{Name: "transcribe", Lead: i18n.T("speech in an audio or video file to text."), Args: []skillArg{
			arg(i18n.T("Absolute path of an audio or video file on this Mac (~/ works; wav, mp3, m4a, mp4, mov…)."), "path"),
			arg(i18n.T("Or the audio itself as base64, with its file name for the format."), "audio_base64", "filename"),
			arg(i18n.T("BCP-47 code; empty: detected."), "language"),
			arg(i18n.T("Names and terms to expect, for their spelling."), "prompt"),
			arg(i18n.T("A speech-to-text model (list_models with task stt); empty: AudioSmith's default."), "model"),
			arg(i18n.T("true returns segments with start and end in seconds (and words): use them for subtitles."), "timestamps"),
		}, After: i18n.T("Returns text, language, duration and transcript_id. The transcript is also kept on AudioSmith's Transcribe page.")},
		{Name: "clone_voice", Lead: i18n.T("a new saved voice from a recording."), Args: []skillArg{
			must(i18n.T("The new voice's name."), "name"),
			arg(i18n.T("Absolute path of the recording: 5–15 s of clean speech by one person, without music or noise."), "path"),
			arg(i18n.T("Or the recording as base64, with its file name."), "audio_base64", "filename"),
			arg(i18n.T("What the recording says; empty: the engine listens."), "ref_text"),
			arg(i18n.T("The speaker's language (BCP-47); a delivery hint kept with the voice (calm, whisper…)."), "language", "style"),
			must(i18n.T("true only after the user has confirmed that the speaker agreed to being cloned; without it the tool refuses."), "consent"),
		}, After: i18n.T("Then speak with it: generate_speech with its name as voice.")},
		{Name: "design_voice", Lead: i18n.T("a new saved voice from a description."), Args: []skillArg{
			must(i18n.T("The voice in plain words: gender, age, pitch, accent or dialect, style (\"a warm older British man\", \"女，青年，四川话\")."), "description"),
			arg(i18n.T("Empty: made from the description."), "name"),
			arg(i18n.T("The language it will speak."), "language"),
			arg(i18n.T("Its identity: another seed gives another voice from the same description."), "seed"),
			arg(i18n.T("A model that designs voices (list_models); empty: the first."), "model"),
		}, After: i18n.T("Returns the voice and the words it could not map (unmatched).")},
		{Name: "describe_voice", Lead: i18n.T("how AudioSmith reads a voice description, without saving anything."), Args: []skillArg{
			must(i18n.T("Use it to refine a description before design_voice: rephrase the words it cannot map."), "description"),
		}},
		{Name: "list_voices", Lead: i18n.T("the saved voices (id, name, kind, language, style), your_voice and the OpenAI names."), Args: []skillArg{
			arg(i18n.T("Filter by name, style, description or tags."), "search"),
		}},
		{Name: "list_models", Lead: i18n.T("the models: id, task, capabilities, built-in voices, downloaded, licence, licence_to_accept, needs_reference."), Args: []skillArg{
			arg(i18n.T("tts or stt; empty: both."), "task"),
		}},
		{Name: "list_languages", Lead: i18n.T("the languages a model speaks."), Args: []skillArg{
			arg(i18n.T("A model id; empty: every speech model."), "model"),
		}},
		{Name: "check_health", Lead: i18n.T("whether AudioSmith's engine is ready, starting or unavailable. No arguments.")},
		// Thread #35: the app's modules.
		{Name: "search_voices", Lead: i18n.T("search Discover voices: about 1,100 ready-made designed voices, with the page's filters."), Args: []skillArg{
			arg(i18n.T("Words that must all occur in a voice's name, description or tags (English or Chinese)."), "query"),
			arg(i18n.T("The page's filters; the answer's facets list each filter's values with how many voices they give. Categories: narration, conversational, characters, social, advertising, education, news, games, wellness."), "category", "gender", "age", "pitch", "accent", "language", "style"),
			arg(i18n.T("One voice by its id (the other filters are ignored)."), "id"),
			arg(i18n.T("Paging: at most limit voices (20 by default, up to 100) after offset."), "limit", "offset"),
		}, After: i18n.T("Returns voices (id, name, description, languages, tags, sample_text, saved_voice_id when it was used already) and facets.")},
		{Name: "use_gallery_voice", Lead: i18n.T("save a voice of Discover voices to My voices, as its Use voice button does (once)."), Args: []skillArg{
			must(i18n.T("The voice's id from search_voices."), "id"),
		}, After: i18n.T("Returns the saved voice, the model it was designed for, the models that can speak it, and whether that model is downloaded. Then use its name as voice anywhere.")},
		{Name: "export_transcript", Lead: i18n.T("save a transcript as a file."), Args: []skillArg{
			must(i18n.T("The transcript's id (transcribe returns it)."), "transcript_id"),
			arg(i18n.T("txt (default), srt, vtt or json."), "format"),
			arg(i18n.T("An absolute folder to write to (made if missing); empty: the agents' folder. The file's name, without folders; empty: from the transcript's title."), "directory", "filename"),
		}, After: i18n.T("Returns files: the path written.")},
		{Name: "create_audiobook", Lead: i18n.T("an Audiobook project from a book or a manuscript."), Args: []skillArg{
			arg(i18n.T("Absolute path of the book: EPUB, TXT, Markdown or DOCX; its headings become chapters, its title, author and language are kept."), "path"),
			arg(i18n.T("Or the manuscript itself: %s lines start chapters, %s switches to a character's voice until the next switch or chapter.", "`# Title`", "`[voice:NAME]`"), "text"),
			arg(i18n.T("A speech model id (list_models); empty: AudioSmith's default."), "model"),
			arg(i18n.T("The narrator: a saved voice's name or id, or a built-in speaker of the model; empty: the first voice that fits the model."), "voice"),
			arg(i18n.T("The voices of the [voice:NAME] names: an object NAME → voice."), "cast"),
			arg(i18n.T("The title; empty: the book's. The text's language (BCP-47); empty: the book's, else automatic."), "title", "language"),
			arg(i18n.T("wav (default), or m4b to make an M4B audiobook with chapters and cover as well."), "format"),
		}, After: i18n.T("Returns the project: project_id, chapters, cast, problems (blocking ones stop a render) and the estimated length. It is saved in Projects and opens on the Audiobook page.")},
		{Name: "create_story", Lead: i18n.T("a Stories project (several voices) from a script, a file or lines."), Args: []skillArg{
			arg(i18n.T("The script: lines like %s go to that character, as Auto-cast does; %s lines start chapters.", "`Alice: Good morning.`", "`# Title`"), "text"),
			arg(i18n.T("Or a file to import (TXT, Markdown, EPUB, DOCX, SRT, VTT)."), "path"),
			arg(i18n.T("Or the lines as data: objects with text, speaker, voice, speed, pause_after_ms, chapter."), "lines"),
			arg(i18n.T("How text becomes lines: speakers (default), paragraphs, sentences or chapters."), "split"),
			arg(i18n.T("Character name → voice; characters left out get voices in turn, the ones nobody plays first."), "cast"),
			arg(i18n.T("The narrator (lines without a character), the model, the title, the language and the format, as for create_audiobook."), "voice", "model", "title", "language", "format"),
		}, After: i18n.T("Returns the project with its lines (ids), cast and problems.")},
		{Name: "get_longform", Lead: i18n.T("a story or audiobook project: chapters, cast and voices, problems, the last render and its files."), Args: []skillArg{
			must(i18n.T("The project (create_audiobook, create_story, or list_projects with category story or audiobook)."), "project_id"),
		}},
		{Name: "update_longform", Lead: i18n.T("set a story's or audiobook's voices and settings; what you leave out stays."), Args: []skillArg{
			must(i18n.T("The project."), "project_id"),
			arg(i18n.T("The narrator (default voice): a saved voice's name or id, or a built-in speaker."), "voice"),
			arg(i18n.T("Character → voice; %s gives the character the narrator's voice again.", "`default`"), "cast"),
			arg(i18n.T("Stories: line id → a voice for that line only (%s: back to its character's).", "`default`"), "line_voices"),
			arg(i18n.T("Another speech model; voices that do not fit it show as problems."), "model"),
			arg(i18n.T("wav or m4b (M4B: chapters, cover and tags; needs the engine's encoder)."), "format"),
			arg(i18n.T("The book's loudness target: acx (Audible, −19 LUFS), podcast (−16 LUFS) or off."), "loudness"),
			arg(i18n.T("true: renders also write one WAV per character (stems)."), "stems"),
			arg(i18n.T("Title, language (BCP-47, or auto) and speaking rate (0.5–2)."), "title", "language", "speed"),
			arg(i18n.T("Book details for the M4B's tags; cover is the absolute path of a PNG or JPEG (≤ 8 MB)."), "author", "narrator", "year", "genre", "description", "cover"),
		}},
		{Name: "render_longform", Lead: i18n.T("render a story or audiobook in the background."), Args: []skillArg{
			must(i18n.T("The project; it must have no blocking problems (get_longform)."), "project_id"),
			arg(i18n.T("Chapter numbers (1-based) to render, for a book of those only; empty: everything."), "chapters"),
		}, After: i18n.T("Returns a job_id (longform:<render id>) at once. Only one book or story renders at a time (busy). Follow it with get_job; when it succeeds its files are the M4B (if format m4b), the book's WAV and the chapters.")},
		{Name: "export_longform", Lead: i18n.T("save a finished render where the user wants it."), Args: []skillArg{
			must(i18n.T("The project; its last render is exported."), "project_id"),
			arg(i18n.T("Or one of its renders (render_longform's job without longform:)."), "render_id"),
			arg(i18n.T("m4b (rendered with format m4b), wav (default), mp3 or opus (with chapters; need ffmpeg), chapters (one WAV per chapter), cues (chapter times) or stems."), "format"),
			arg(i18n.T("WAV bit depth: 16, 24 or 32; 0: as rendered (32-bit float)."), "bits"),
			arg(i18n.T("An absolute folder (made if missing); empty: the agents' folder. The file's name; for chapters and stems the name of a folder made inside it."), "directory", "filename"),
		}, After: i18n.T("Returns files: the paths written. It is listed in Projects → Exports.")},
		{Name: "create_dub", Lead: i18n.T("dub a video or audio file into other languages: transcribe, translate, speak each line in its speaker's voice, mix."), Args: []skillArg{
			must(i18n.T("Absolute path of the video or audio (mp4, mov, mkv, webm, mp3, wav, m4a…)."), "path"),
			arg(i18n.T("BCP-47 codes to dub into, the main one first; empty: AudioSmith's Dubbing settings."), "target_languages"),
			arg(i18n.T("The language spoken; empty: detected."), "source_language"),
			arg(i18n.T("How far to run now: transcript (stop to review the lines), translation, dub (default: everything) or none."), "through"),
			arg(i18n.T("One voice for every speaker (a saved voice's name or id); empty: each speaker's own voice, cloned from the source."), "voice"),
			arg(i18n.T("The speech model; empty: the dubbing default. How many people speak (0: detected). Keep music and effects (default true). A title (default: the file's name)."), "model", "speakers", "keep_background", "title"),
		}, After: i18n.T("Returns the dub (dub_id) and its job_id (dub:<dub_id>). Translation uses the translator of AudioSmith's settings (local or cloud, as the user chose): if none is set up, the error is dub.no_translator.")},
		{Name: "get_dub", Lead: i18n.T("a dub: stages, speakers and their voices, tracks (files), and its lines with their translations."), Args: []skillArg{
			must(i18n.T("The dub."), "dub_id"),
			arg(i18n.T("The target language whose translations to show; empty: the main one."), "language"),
			arg(i18n.T("false leaves the lines out."), "segments"),
		}},
		{Name: "edit_dub", Lead: i18n.T("change a dub's lines and voices (not while it runs)."), Args: []skillArg{
			must(i18n.T("The dub."), "dub_id"),
			arg(i18n.T("Line edits: objects with id and any of text (the source; its translations are made again), translation with language, speaker, voice."), "segments"),
			arg(i18n.T("Speaker label (S1, S2…) or * → voice (a saved voice's name or id; default: cloned from the source); speaker label → display name."), "cast", "names"),
		}, After: i18n.T("Then run_dub makes only the changed lines again.")},
		{Name: "run_dub", Lead: i18n.T("run a dub's pipeline (again)."), Args: []skillArg{
			must(i18n.T("The dub."), "dub_id"),
			arg(i18n.T("transcript, translation, dub (default) or verify (listen again and flag lines that came out wrong)."), "through"),
			arg(i18n.T("Only these target languages; empty: all."), "languages"),
			arg(i18n.T("Translate every line again (edits too); transcribe the source again (drops the lines and their edits)."), "retranslate", "retranscribe"),
			arg(i18n.T("Only re-render these line ids."), "segments"),
		}, After: i18n.T("Returns the job (dub:<dub_id>); busy while a run goes on.")},
		{Name: "export_dub", Lead: i18n.T("save a dub."), Args: []skillArg{
			must(i18n.T("The dub."), "dub_id"),
			must(i18n.T("mp4 (the video with dubbed audio tracks), wav, m4a or mp3 (a dubbed track; mp3 needs ffmpeg), srt, vtt or ass (subtitles), stems or segments (ZIP), burn (subtitles drawn into the video; needs ffmpeg)."), "format"),
			arg(i18n.T("The language of a track or of subtitles (orig: the source's); empty: the main target."), "language"),
			arg(i18n.T("mp4: its audio tracks (languages and orig; default: the main target and orig), the one players pick, and soft subtitle tracks."), "tracks", "default_track", "subtitles"),
			arg(i18n.T("Subtitles with the translation and the original; WAV bit depth (default 24)."), "dual", "bits"),
			arg(i18n.T("An absolute folder (made if missing); empty: the agents' folder. The file's name; empty: from the title and language."), "directory", "filename"),
		}, After: i18n.T("Returns a job_id; when it succeeds its files hold the path written.")},
		{Name: "batch_dub", Lead: i18n.T("dub several files with the Batch queue: one dub each, at background priority, without review stops."), Args: []skillArg{
			arg(i18n.T("Absolute paths of videos or audio files, or a folder (its files, not its subfolders)."), "paths", "folder"),
			arg(i18n.T("Override the Batch page's settings for these files only (nothing is saved): target languages, source language, one voice, model, keep music."), "target_languages", "source_language", "voice", "model", "keep_background"),
		}, After: i18n.T("Returns added (each with its dub_id and job_id) and errors (files it could not take).")},
		{Name: "list_batch", Lead: i18n.T("the Batch queue, its settings and its watch folder (read only: the user sets up watch folders in the app). No arguments.")},
		{Name: "list_projects", Lead: i18n.T("AudioSmith's project library, as on its Projects page, newest first."), Args: []skillArg{
			arg(i18n.T("story, audiobook, dub, workflow, voice, transcript, take, export or call; empty: all."), "category"),
			arg(i18n.T("Words in titles, scripts, names or paths."), "search"),
			arg(i18n.T("Paging: at most limit (50 by default, up to 500) after offset."), "limit", "offset"),
		}, After: i18n.T("Each item says which tool opens it (tool).")},
		{Name: "list_takes", Lead: i18n.T("the History: generated takes with their text, voice, length and WAV file, newest first."), Args: []skillArg{
			arg(i18n.T("Words in the text; only a saved voice's takes; only a model's; only starred ones."), "search", "voice_id", "model", "starred"),
			arg(i18n.T("One take by id."), "take_id"),
			arg(i18n.T("Paging: at most limit (20 by default, up to 200) after offset."), "limit", "offset"),
		}},
		{Name: "export_audio", Lead: i18n.T("export a take as the Export audio dialog does."), Args: []skillArg{
			must(i18n.T("The take (generate_speech's take_id, or list_takes)."), "take_id"),
			arg(i18n.T("wav (default), m4a, mp3, flac or opus; mp3, flac and opus need ffmpeg, m4a the engine."), "format"),
			arg(i18n.T("wav: 16, 24 or 32 (float); flac: 16 or 24. Lossy formats: bitrate_kbps (m4a 128/192/256, mp3 128/192/320, opus 64/96/128)."), "bits", "bitrate_kbps"),
			arg(i18n.T("44100 or 48000 (0: the take's; opus is always 48000); 1 or 2 channels (0: the take's)."), "sample_rate", "channels"),
			arg(i18n.T("Loudness target: podcast (−16 LUFS), speech (−18), broadcast (−23) or off; trim: true cuts leading and trailing silence."), "loudness", "trim"),
			arg(i18n.T("Title and artist tags (not wav)."), "title", "artist"),
			arg(i18n.T("Also save the text (.txt) or subtitles timed to the audio (.srt) beside it."), "script", "subtitles"),
			arg(i18n.T("An absolute folder (made if missing); empty: the agents' folder. The file's name; empty: from the text."), "directory", "filename"),
		}, After: i18n.T("Returns files, seconds, bytes and the loudness reached.")},
		{Name: "delete_item", Lead: i18n.T("delete a take, voice, transcript, story or audiobook project, or dub."), Args: []skillArg{
			must(i18n.T("take, voice, transcript, project (a story or audiobook) or dub."), "kind"),
			must(i18n.T("Its id."), "id"),
			arg(i18n.T("true only after the user said yes to deleting it; without it nothing is deleted (confirm_required)."), "confirm"),
		}},
		{Name: "list_workflows", Lead: i18n.T("the saved workflows (WorkflowStudio): steps, inputs (scripts or files), export folders, last run. No arguments.")},
		{Name: "run_workflow", Lead: i18n.T("run a saved workflow. Its steps cannot be changed here: the user edits them on the Workflows page."), Args: []skillArg{
			must(i18n.T("The workflow (list_workflows)."), "workflow_id"),
			arg(i18n.T("New inputs, saved as the workflow's: scripts (each one an item) for workflows that start with scripts, or absolute paths of audio files for those that start with audio."), "scripts", "files"),
			arg(i18n.T("Make every result anew (default: keep the results whose inputs did not change)."), "again"),
		}, After: i18n.T("Returns a job_id (workflow:<id>); its result lists each item's steps and files, and files the final results.")},
		{Name: "get_job", Lead: i18n.T("follow a long job: state, stage, progress (0–1), error code and, when it ends, the absolute paths of the files it made."), Args: []skillArg{
			must(i18n.T("A job_id a tool returned."), "job_id"),
			arg(i18n.T("Wait up to this many seconds (≤ 60) for it to end before answering; it answers as soon as it ends."), "wait_seconds"),
		}},
		{Name: "cancel_job", Lead: i18n.T("stop a job; what was made so far is kept, and a run goes on later from there."), Args: []skillArg{
			must(i18n.T("The job_id."), "job_id"),
		}},
	}
}

// Areas of the tools, in the skill's order.
const (
	areaSpeech   = "speech"
	areaText     = "transcribe"
	areaLongform = "longform"
	areaDub      = "dub"
	areaLibrary  = "library"
	areaWorkflow = "workflow"
	areaJobs     = "jobs"
)

var skillAreaOrder = []string{areaSpeech, areaText, areaLongform, areaDub, areaLibrary, areaWorkflow, areaJobs}

// skillAreas puts every tool in an area.
var skillAreas = map[string]string{
	"generate_speech": areaSpeech, "list_voices": areaSpeech, "list_models": areaSpeech, "list_languages": areaSpeech,
	"design_voice": areaSpeech, "describe_voice": areaSpeech, "clone_voice": areaSpeech, "search_voices": areaSpeech, "use_gallery_voice": areaSpeech,
	"transcribe": areaText, "export_transcript": areaText,
	"create_audiobook": areaLongform, "create_story": areaLongform, "get_longform": areaLongform, "update_longform": areaLongform,
	"render_longform": areaLongform, "export_longform": areaLongform,
	"create_dub": areaDub, "get_dub": areaDub, "edit_dub": areaDub, "run_dub": areaDub, "export_dub": areaDub, "batch_dub": areaDub, "list_batch": areaDub,
	"list_projects": areaLibrary, "list_takes": areaLibrary, "export_audio": areaLibrary, "delete_item": areaLibrary,
	"list_workflows": areaWorkflow, "run_workflow": areaWorkflow,
	"get_job": areaJobs, "cancel_job": areaJobs, "check_health": areaJobs,
}

// areaTitle names an area.
func areaTitle(area string) string {
	switch area {
	case areaSpeech:
		return i18n.T("Speech and voices")
	case areaText:
		return i18n.T("Transcription")
	case areaLongform:
		return i18n.T("Stories and audiobooks")
	case areaDub:
		return i18n.T("Dubbing and batch")
	case areaLibrary:
		return i18n.T("Library, History and exports")
	case areaWorkflow:
		return i18n.T("Workflows")
	}
	return i18n.T("Jobs and health")
}

// skillToolsByArea are the tools in the skill's order.
func skillToolsByArea() []skillTool {
	tools := skillTools()
	var out []skillTool
	for _, area := range skillAreaOrder {
		for _, tl := range tools {
			if skillAreas[tl.Name] == area {
				out = append(out, tl)
			}
		}
	}
	return out
}

// skillModule is a page of AudioSmith in the skill's map of the app.
type skillModule struct {
	Path  string   // its route ("/studio"); "" for Settings and Dictation
	Name  string   // as the sidebar names it (translated)
	What  string   // what it is for
	Tools []string // the tools that do it; none: only in the app
	App   string   // what stays in the app, and where (with Tools), or why
}

// skillModules are every page of the sidebar (navGroups), then Settings
// and Dictation (TestAgentSkill checks that none is missing).
func skillModules() map[string]skillModule {
	m := func(path, what, app string, tools ...string) skillModule {
		return skillModule{Path: path, Name: pageTitle(path), What: what, Tools: tools, App: app}
	}
	l := []skillModule{
		m("/studio", i18n.T("text to speech with any model and voice: script markup (pauses, tones), takes, preview."), "",
			"generate_speech", "export_audio"),
		m("/stories", i18n.T("stories and dialogues with several voices: lines, characters (the cast), chapters, one render."), "",
			"create_story", "get_longform", "update_longform", "render_longform", "export_longform"),
		m("/audiobook", i18n.T("a whole book narrated chapter by chapter, with a narrator and character voices, as WAV or M4B with chapters and cover."), "",
			"create_audiobook", "get_longform", "update_longform", "render_longform", "export_longform"),
		m("/dub", i18n.T("dubs a video or audio into other languages in the speakers' own voices, with subtitles."),
			i18n.T("Timeline edits (timing, splitting lines) only in the app."),
			"create_dub", "get_dub", "edit_dub", "run_dub", "export_dub"),
		m("/gallery", i18n.T("about 1,100 ready-made voices to search and filter."), "", "search_voices", "use_gallery_voice"),
		m("/voices", i18n.T("the saved voices: designed, cloned and gallery voices."),
			i18n.T("Editing a voice (name, tags, locking its sample) only in the app."), "list_voices", "delete_item"),
		m("/design", i18n.T("a new voice from a description (gender, age, accent, style)."), "", "describe_voice", "design_voice"),
		m("/clone", i18n.T("a new voice from a recording of someone (with their consent)."),
			i18n.T("Recording from the microphone only in the app."), "clone_voice"),
		m("/projects", i18n.T("everything made: stories, audiobooks, dubs, workflows, voices, transcripts, takes, exports."), "",
			"list_projects", "delete_item"),
		m("/history", i18n.T("every take generated, by anyone (you too), with its recipe."), "", "list_takes", "export_audio", "delete_item"),
		m("/transcripts", i18n.T("transcripts of recordings and videos, with times and speakers."),
			i18n.T("Editing a transcript and its clean-up only in the app."), "transcribe", "export_transcript"),
		m("/workflows", i18n.T("saved pipelines (scripts or recordings → speak, transcribe, translate, convert, normalize → export), run over many items."),
			i18n.T("Building or changing a workflow only in the app."), "list_workflows", "run_workflow"),
		m("/batch", i18n.T("many files dubbed one after another, and a watch folder that adds new ones."),
			i18n.T("Watch folders only in the app."), "batch_dub", "list_batch"),
		m("/calls", i18n.T("AI phone calls through the user's Twilio number."),
			i18n.T("Only in the app (sidebar → %s → %s): you never place calls.", groupTitle("Automate"), pageTitle("/calls"))),
		m("/tools", i18n.T("A/B voices, Convert (a recording in another voice), Rate fit, Direction, File probe, Watermark check."),
			i18n.T("In the app; probe, rate fit, direction and the watermark check also through the Local API (/api/tools/…, /api/watermark/detect).")),
		m("/integrations", i18n.T("setups for agents and SDKs (this skill comes from there)."), i18n.T("Only in the app.")),
		m("/models", i18n.T("the speech models: downloads, licences, sizes."),
			i18n.T("Downloading models and accepting licences only in the app (sidebar → %s → %s): ask the user.", groupTitle("System"), pageTitle("/models")), "list_models", "list_languages"),
		m("/engine", i18n.T("the speech engine: its state, memory, restart."), i18n.T("Restarting it only in the app."), "check_health"),
		m("/logs", i18n.T("the app's and the engine's logs."), i18n.T("Only in the app: when something fails without a clear error, ask the user to look there.")),
	}
	out := map[string]skillModule{}
	for _, x := range l {
		out[x.Path] = x
	}
	out["settings"] = skillModule{Name: pageTitle("/settings"), What: i18n.T("everything the user chose: default models, translation, dubbing, privacy, the Local API."),
		App: i18n.T("Only in the app (⌘,). Never change it yourself: tell the user what to change.")}
	out["dictation"] = skillModule{Name: i18n.T("Dictation"), What: i18n.T("speak into any app with a global hotkey."),
		App: i18n.T("Only in the app (%s → %s): not for agents.", pageTitle("/settings"), i18n.T("Dictation"))}
	return out
}

// skillDescription is the frontmatter's description: what the skill is
// for and when to use it, trigger words first (agents shorten long lists).
func skillDescription() string {
	return i18n.T("Text to speech, audiobooks, multi-voice stories, video dubbing, voices and transcription on this Mac with AudioSmith (local and private; MCP server audiosmith). Use it when the user wants speech or TTS (read text aloud, narration, a voice-over, a WAV), to turn a book or EPUB into an audiobook (M4B), a story with several voices, to dub a video into another language with subtitles, to find, design or clone a voice, to transcribe audio or video into text or subtitles (SRT, VTT), to export audio for a podcast, or to run AudioSmith's workflows.")
}

// yamlString is s as a YAML double-quoted scalar (a JSON string is one).
func yamlString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// skillDoc is a file of the skill: its path in the skill's folder and
// its text.
type skillDoc struct{ Path, Text string }

// skillRefPath is the tool reference, beside SKILL.md (agents read it
// when they need the arguments).
const skillRefPath = "reference/tools.md"

// agentSkillFiles are the skill's files of the agent of a page: SKILL.md
// and the tool reference; nil when the page has none. They never contain
// the API key.
func agentSkillFiles(slug string, p promptEnv) []skillDoc {
	if _, ok := promptAgents[slug]; !ok {
		return nil
	}
	return []skillDoc{{"SKILL.md", agentSkill(slug, p)}, {skillRefPath, skillReference()}}
}

// agentSkillText is the skill as one text (Copy skill): SKILL.md, then
// the reference.
func agentSkillText(slug string, p promptEnv) string {
	var parts []string
	for _, f := range agentSkillFiles(slug, p) {
		parts = append(parts, f.Text)
	}
	return strings.Join(parts, "\n---\n\n")
}

// agentSkillZip is the skill as a ZIP of its folder (Save skill…: the
// form claude.ai and Claude Desktop take).
func agentSkillZip(slug string, p promptEnv) ([]byte, error) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, f := range agentSkillFiles(slug, p) {
		fw, err := w.Create(skillName + "/" + f.Path)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(fw, f.Text); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// agentSkill is the SKILL.md of the agent of a page; "" when the page has
// none. It never contains the API key.
func agentSkill(slug string, p promptEnv) string {
	ag, ok := promptAgents[slug]
	if !ok {
		return ""
	}
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	item := func(s string) { line("- " + s) }
	head := func(s string) { line("\n## " + s) }
	q := func(s string) string { return "`" + s + "`" }
	tools := func(ns []string) string {
		out := make([]string, len(ns))
		for i, n := range ns {
			out[i] = q(n)
		}
		return strings.Join(out, ", ")
	}

	line("---")
	line("name: " + skillName)
	line("description: " + yamlString(skillDescription()))
	line("---")
	line("")
	line("# AudioSmith")
	line("")
	line(i18n.T("AudioSmith is a voice studio on this Mac: it speaks text (TTS), makes audiobooks and multi-voice stories, dubs videos into other languages, finds, designs and clones voices, transcribes audio and video, exports audio and runs workflows. Everything runs locally with MLX models: nothing leaves this Mac and nothing is paid per call. Use it through the tools of the MCP server audiosmith (below); without them, through its Local API (the last section)."))

	head(i18n.T("Check first"))
	item(i18n.T("AudioSmith must be open with its Local API on. %s (or %s) answers %s when it is ready; %s: wait a few seconds and check again; %s: its engine is down, ask the user to look at AudioSmith.",
		q("check_health"), q("curl -sS "+p.Base+"/health"), q(`"status":"ok"`), q(`"starting"`), q(`"unavailable"`)))
	item(i18n.T("If the connection is refused, or the audiosmith tools cannot connect, AudioSmith is not running: ask the user to open it and turn on Settings → Local API → Run the Local API (port %s). Run %s yourself only if the user agrees, then wait for /health.", p.port(), q("open -a AudioSmith")))
	item(i18n.T("If this chat has no audiosmith tools although /health answers, the MCP server is not set up in this agent: tell the user (AudioSmith → Integrations has a setup prompt) and use the Local API meanwhile."))
	item(i18n.T("Errors start with a code that says what to do: %s or %s: ask the user to accept the licence or to download the model in AudioSmith → Models; %s or %s: wait and try again; %s: that model speaks cloned voices only, use a cloned voice or another model; %s: check list_voices.",
		q("licence_required"), q("model_not_downloaded"), q("engine_unavailable"), q("queue_timeout"), q("needs_reference"), q("voice_not_found")))
	item(i18n.T("More codes: %s (another render or run goes on: wait for it or ask the user), %s (a step must come first, the message says which), %s (ask the user before deleting), %s (fix the argument), %s (the format needs ffmpeg or the engine).",
		q("busy"), q("not_ready"), q("confirm_required"), q("invalid_request"), q("unsupported_format")))

	head(i18n.T("On this Mac"))
	item(i18n.T("Local API: %s; MCP server: %s.", p.Base, p.mcpURL()))
	if ag.Client != "" {
		item(i18n.T("AudioSmith knows you as %s (the header %s).", q(ag.Client), q(api.HeaderClient)))
	}
	if n := len(p.Voices); n == 0 {
		item(i18n.T("No saved voices when this skill was written: list_voices has the current list, and list_models the models' built-in speakers."))
	} else {
		s := i18n.T("Saved voices when this skill was written: %s. list_voices has the current list.", strings.Join(p.Voices[:min(n, promptVoices)], ", "))
		if n > promptVoices {
			s += " " + i18n.T("And %d more: see list_voices.", n-promptVoices)
		}
		item(s)
	}
	if v := p.Bindings[ag.Client]; v != "" && ag.Client != "" {
		item(i18n.T("Your own voice: %s. generate_speech uses it when you give no voice.", v))
	}
	if filepath.IsAbs(p.Dir) {
		item(i18n.T("The agents' folder, where your files go unless you give a directory: %s.", p.Dir))
	}
	if p.NeedKey {
		item(i18n.T("AudioSmith requires its API key. Your MCP configuration already sends it; for curl, read it from the Keychain without printing it: %s.", q(keychainRead)))
	}

	head(i18n.T("What AudioSmith does"))
	line(i18n.T("The pages of AudioSmith's sidebar, by the names the user sees, what each is for, and the tools that do it (or where it stays in the app)."))
	mods := skillModules()
	sp := " " // between sentences; Chinese sets none
	if strings.HasPrefix(i18n.Locale(), "zh") {
		sp = ""
	}
	for _, g := range navGroups {
		line("")
		line("### " + groupTitle(g.id))
		for _, pg := range g.pages {
			md := mods[pg.path]
			s := "**" + md.Name + "**: " + md.What
			if len(md.Tools) > 0 {
				s += sp + i18n.T("Tools: %s.", tools(md.Tools))
			}
			if md.App != "" {
				s += sp + md.App
			}
			item(s)
		}
	}
	line("")
	line("### " + i18n.T("Not in the sidebar"))
	for _, k := range []string{"settings", "dictation"} {
		md := mods[k]
		item("**" + md.Name + "**: " + md.What + sp + md.App)
	}

	head(i18n.T("Long jobs"))
	item(i18n.T("Renders, dubs, batch dubs, dub exports and workflow runs take minutes: their tools return a %s at once (a queue job, or %s, %s, %s). They also show in AudioSmith's job list.", q("job_id"), q("longform:…"), q("dub:…"), q("workflow:…")))
	item(i18n.T("Follow one with %s and %s: it answers when the job ends or the time is up, with state, stage, progress (0–1) and, at the end, %s (absolute paths) or an error code. Tell the user the job_id and the progress; don't call it in a tight loop.", q("get_job"), q("wait_seconds: 30"), q("files")))
	item(i18n.T("%s stops a job when the user asks; what was made is kept, and running it again goes on from there.", q("cancel_job")))

	head(i18n.T("Tools"))
	line(i18n.T("Every argument of every tool is in %s, next to this file: read it before you call a tool you have not used yet.", q(skillRefPath)))
	byName := map[string]skillTool{}
	for _, tl := range skillTools() {
		byName[tl.Name] = tl
	}
	for _, area := range skillAreaOrder {
		line("")
		line("### " + areaTitle(area))
		for _, tl := range skillToolsByArea() {
			if skillAreas[tl.Name] == area {
				item(q(tl.Name) + ": " + tl.Lead)
			}
		}
	}
	line("")
	line(i18n.T("Resources: %s (a saved voice), %s (the 20 latest takes).", q("voice://{id}"), q("history://recent")))

	head(i18n.T("Recipes"))
	item("**" + i18n.T("Narrate a README section to a file.") + "** " + i18n.T("Read the section and make it speakable: leave out code blocks, URLs and markup, and turn tables and lists into sentences. Call generate_speech with %s and a %s such as %s, then report the file's path and length. For a long document, make one file per section.", q(`output: "file"`), q("filename"), q("readme-intro.wav")))
	item("**" + i18n.T("A voice-over for a video.") + "** " + i18n.T("If the timing matters, transcribe the video with %s to see when each part is spoken. Write the lines, then call generate_speech for each with %s and numbered names (%s, %s…). Report the files and their lengths. To put them on the video, suggest AudioSmith's Dub page, or use ffmpeg if the user has it.", q("timestamps: true"), q(`output: "file"`), q("vo-01.wav"), q("vo-02.wav")))
	item("**" + i18n.T("A meeting to SRT subtitles.") + "** " + i18n.T("Call transcribe with the recording's %s and %s, then export_transcript with the transcript_id it returns, %s and a %s next to the recording (or write the segments as SRT yourself). Report the file's path.", q("path"), q("timestamps: true"), q(`format: "srt"`), q("directory")))
	item("**" + i18n.T("Find a voice.") + "** " + i18n.T("Read the user's wish with describe_voice and rephrase the words it cannot map; then design_voice; then generate_speech a short sample in the new voice to a file and let the user listen. Another seed gives another voice from the same description."))
	item("**" + i18n.T("An EPUB to an M4B audiobook.") + "** " + i18n.T("create_audiobook with the book's %s; read its chapters and problems. Ask the user for the narrator (search_voices or list_voices) and set it with update_longform with %s, %s and the author. render_longform, then get_job with %s until it is done (a book takes a while: report the progress). Then export_longform with %s and report the file.", q("path"), q(`format: "m4b"`), q(`loudness: "acx"`), q("wait_seconds"), q(`format: "m4b"`)))
	item("**" + i18n.T("A two-speaker story from a script.") + "** " + i18n.T("Write the script as %s lines (one speaker per line, %s for chapters) and call create_story with it as %s and a %s for each speaker; check its cast. render_longform, get_job until done, export_longform as wav (or mp3 with ffmpeg).", q("Name: what they say"), q("# Title"), q("text"), q("cast")))
	item("**" + i18n.T("Dub a video into English with subtitles.") + "** " + i18n.T("create_dub with the video's %s and %s; follow its job_id with get_job. get_dub shows the lines and translations: fix any with edit_dub, then run_dub. Export with export_dub: %s with %s (the video with the English track and soft subtitles), and %s for a subtitle file. Report the files.", q("path"), q(`target_languages: ["en"]`), q(`format: "mp4"`), q("subtitles: true"), q(`format: "srt"`)))
	item("**" + i18n.T("Batch-dub a folder.") + "** " + i18n.T("batch_dub with the %s and %s; it answers a dub_id and job_id per file. Follow the jobs with get_job (or list_batch), then export_dub each dub as mp4. Report what failed with its error.", q("folder"), q("target_languages")))
	item("**" + i18n.T("Pick a voice from Discover voices and narrate.") + "** " + i18n.T("search_voices with the wish as filters (%s, %s, %s…); show the user two or three with their descriptions; use_gallery_voice with the chosen id; then generate_speech with its name as %s, on one of the models it lists, with %s.", q("category"), q("gender"), q("language"), q("voice"), q(`output: "file"`)))
	item("**" + i18n.T("Export for podcast loudness.") + "** " + i18n.T("generate_speech (or list_takes for an older take), then export_audio with its %s, %s, %s (or m4a), %s and the user's folder as %s. Report the file and the loudness reached.", q("take_id"), q(`loudness: "podcast"`), q(`format: "mp3"`), q("trim: true"), q("directory")))
	item("**" + i18n.T("Run a saved workflow.") + "** " + i18n.T("list_workflows; pick the one the user means and check its input (scripts or files). run_workflow with new %s or %s if the user gave some, follow its job, then report each item's files from the job's result.", q("scripts"), q("files")))
	item("**" + i18n.T("Check a job and cancel it.") + "** " + i18n.T("get_job with the job_id and %s; report its stage and progress. If the user wants it stopped, cancel_job and confirm with get_job that it ended.", q("wait_seconds: 30")))

	head(i18n.T("Manners"))
	item(i18n.T("Ask before cloning anyone's voice, and pass %s only after the user has confirmed that the speaker agreed.", q("consent: true")))
	item(i18n.T("Ask before deleting anything, and pass %s to delete_item only after the user said yes to that item.", q("confirm: true")))
	item(i18n.T("Long jobs: tell the user the job_id, follow it with wait_seconds rather than polling fast, and report its progress and files."))
	item(i18n.T("AudioSmith stays on this Mac: use only 127.0.0.1 and never expose its API (no 0.0.0.0, tunnels or port forwarding)."))
	item(i18n.T("Keep the audio in the agents' folder unless the user names another place (then pass it as directory), and always report the paths of the files you made."))
	item(i18n.T("Don't change AudioSmith's settings, accept licences or download models yourself: ask the user."))
	item(i18n.T("Phone calls and dictation are the user's: never try to use them."))
	if p.NeedKey {
		item(i18n.T("Never show the API key or write it into a file."))
	}

	head(i18n.T("Without the MCP tools"))
	line(i18n.T("Use the Local API with curl. These OpenAI-style routes take up to 4,096 characters; %s lists every route.", p.Base+"/docs"))
	hdr := ""
	if ag.Client != "" {
		hdr = " -H '" + api.HeaderClient + ": " + ag.Client + "'"
	}
	if p.NeedKey {
		hdr += ` -H "Authorization: Bearer $(` + keychainRead + `)"`
	}
	voice := "alloy"
	if v := p.Bindings[ag.Client]; v != "" && ag.Client != "" {
		voice = v
	} else if len(p.Voices) > 0 {
		voice = p.Voices[0]
	}
	body, _ := json.Marshal(map[string]string{"model": "tts-1", "voice": voice, "input": "Hello from AudioSmith."})
	if strings.Contains(string(body), "'") {
		body = []byte(`{"input":"Hello from AudioSmith.","model":"tts-1","voice":"alloy"}`)
	}
	line("```sh")
	line("curl -sS " + p.Base + "/v1/audio/speech -H 'Content-Type: application/json'" + hdr + " -d '" + string(body) + "' -o hello.wav")
	line("curl -sS " + p.Base + "/v1/audio/transcriptions" + hdr + " -F file=@meeting.m4a -F response_format=srt -o meeting.srt")
	line("```")
	line(i18n.T("The other modules have REST routes too (POST %s, %s, %s; GET %s…); calls that change something need the header %s.", "/api/audiobooks", "/api/stories", "/api/dubs", "/api/jobs/{id}?wait=30", q(api.HeaderCSRF+": 1")))
	return b.String()
}

// skillReference is reference/tools.md: every tool with all its
// arguments (TestAgentSkill compares them with the server's schemas).
func skillReference() string {
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	q := func(s string) string { return "`" + s + "`" }
	line("# " + i18n.T("AudioSmith's tools"))
	line("")
	line(i18n.T("Every tool of the MCP server audiosmith with all its arguments; (required) marks the ones it needs. Paths are absolute paths on this Mac (~/ works). Errors start with a code (SKILL.md, Check first)."))
	for _, area := range skillAreaOrder {
		line("")
		line("## " + areaTitle(area))
		for _, tl := range skillToolsByArea() {
			if skillAreas[tl.Name] != area {
				continue
			}
			line("")
			line("### " + q(tl.Name) + ": " + tl.Lead)
			for _, a := range tl.Args {
				names := make([]string, len(a.Names))
				for i, n := range a.Names {
					names[i] = q(n)
				}
				n := strings.Join(names, ", ")
				if a.Required {
					n += " (" + i18n.T("required") + ")"
				}
				line("- " + n + ": " + a.Text)
			}
			if tl.After != "" {
				line(tl.After)
			}
		}
	}
	return b.String()
}

// skillScript is the setup prompt's shell script that installs the skill
// at the agent's user level: it writes each file through a heredoc,
// backs up a different file that is there, and changes nothing when run
// again. It runs in bash and zsh.
func skillScript(slug string, p promptEnv) string {
	var b strings.Builder
	fmt.Fprintf(&b, "D=\"$HOME/%s\"\n", strings.TrimPrefix(skillDir(slug), "~/"))
	b.WriteString("mkdir -p \"$D/" + filepath.Dir(skillRefPath) + "\"\n")
	for _, f := range agentSkillFiles(slug, p) {
		b.WriteString("F=\"$D/" + f.Path + "\"\n")
		b.WriteString("cat > \"$F.new\" <<'" + skillEOF + "'\n")
		b.WriteString(f.Text)
		b.WriteString(skillEOF + "\n")
		b.WriteString("if [ -f \"$F\" ] && ! cmp -s \"$F\" \"$F.new\"; then cp \"$F\" \"$F.bak-$(date +%Y%m%d-%H%M%S)\"; fi\n")
		b.WriteString("mv \"$F.new\" \"$F\"\n")
	}
	b.WriteString("ls \"$D\" \"$D/" + filepath.Dir(skillRefPath) + "\"; head -n 3 \"$D/SKILL.md\"\n")
	return b.String()
}

// promptSkillStep is the setup prompt's step 4: install the skill at
// the user level, verify it, and tell the user to start a new chat. The
// script is fenced with four backticks: the skill has fences of three.
func promptSkillStep(b *strings.Builder, slug string, p promptEnv) {
	w := func(s string) { b.WriteString(s + "\n") }
	if promptAgents[slug].Client == "" {
		w("4. " + i18n.T("Install AudioSmith's skill, so that you know how to use AudioSmith in every new chat (what it does, its tools, voices and recipes): a folder %s with SKILL.md and %s. If you read Agent Skills (folders with a SKILL.md), set D to that folder in your user-level skills folder (many agents read %s) and run this script as it is: it backs up a different file that is there, and running it again changes nothing. If you don't, put the texts between the %s lines, without SKILL.md's frontmatter (the lines from --- to ---), into your user-level instructions file in place of an earlier AudioSmith section, and tell me which file.", "`"+skillName+"`", "`"+skillRefPath+"`", "`~/.agents/skills`", skillEOF))
	} else {
		w("4. " + i18n.T("Install AudioSmith's skill, so that you know how to use AudioSmith in every new chat (what it does, its tools, voices and recipes): the folder %s, with SKILL.md and %s (my user level, all projects); use %s in this project only if I ask. Run this script as it is: it backs up a different file that is there, and running it again changes nothing.", "`"+skillDir(slug)+"/`", "`"+skillRefPath+"`", "`"+strings.TrimPrefix(skillDir(slug), "~/")+"/`"))
	}
	w("````sh\n" + skillScript(slug, p) + "````")
	w("   " + i18n.T("Check that its output shows %s, then tell me that the skill is installed and that it works in new chats.", "`name: "+skillName+"`"))
}

// skillHome is the home folder skills are installed under (tests set it).
var skillHome = os.UserHomeDir

// skillTarget is the absolute path of an agent's user-level skill folder.
func skillTarget(slug string) (string, error) {
	home, err := skillHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(skillDir(slug), "~/")), nil
}

// skillState is what installing files into dir would do: "" new, "same"
// nothing, "differs" replace (with backups).
func skillState(dir string, files []skillDoc) string {
	state := "same"
	for _, f := range files {
		old, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
		switch {
		case err != nil && f.Path == "SKILL.md":
			return ""
		case err != nil || string(old) != f.Text:
			state = "differs"
		}
	}
	return state
}

// installSkill writes files into dir like the setup prompt's script: a
// different file there is kept as <name>.bak-<time>, the same is left
// alone. It returns the backups made.
func installSkill(dir string, files []skillDoc) (backups []string, err error) {
	stamp := time.Now().Format("20060102-150405")
	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return backups, err
		}
		old, rerr := os.ReadFile(path)
		if rerr == nil && string(old) == f.Text {
			continue
		}
		if rerr == nil {
			backup := path + ".bak-" + stamp
			if err := os.WriteFile(backup, old, 0o644); err != nil {
				return backups, err
			}
			backups = append(backups, backup)
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".skill-*")
		if err != nil {
			return backups, err
		}
		_, err = tmp.WriteString(f.Text)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Chmod(tmp.Name(), 0o644)
		}
		if err == nil {
			err = os.Rename(tmp.Name(), path)
		}
		if err != nil {
			os.Remove(tmp.Name())
			return backups, err
		}
	}
	return backups, nil
}
