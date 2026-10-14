package main

// The agent skill (thread #34): a SKILL.md (the Agent Skills format that
// Claude Code, Codex and Cursor read) that tells an agent in every new
// chat what AudioSmith is, when to use it, how to check that it runs,
// each MCP tool with its arguments, recipes and manners. Without it a new
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
	"bytes"
	"encoding/json"
	"fmt"
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

// skillFile is the user-level path of an agent's skill, from the home
// folder ("~/.claude/skills/audiosmith/SKILL.md").
func skillFile(slug string) string {
	return promptAgents[slug].Skills + "/" + skillName + "/SKILL.md"
}

// skillProjectFile is its path in a project, for when the user asks.
func skillProjectFile(slug string) string {
	return strings.TrimPrefix(promptAgents[slug].Skills, "~/") + "/" + skillName + "/SKILL.md"
}

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
	}
}

// skillDescription is the frontmatter's description: what the skill is
// for and when to use it, trigger words first (agents shorten long lists).
func skillDescription() string {
	return i18n.T("Text to speech, voice cloning and design, and transcription on this Mac with AudioSmith (local and private; MCP server audiosmith). Use it when the user wants speech or TTS: to read text aloud, narration, a voice-over, an audiobook chapter, a spoken preview or a WAV of some text; to clone a voice from a recording or design one from a description; or to transcribe audio or video (meetings, interviews, podcasts) into text, timestamps or subtitles (SRT, VTT).")
}

// yamlString is s as a YAML double-quoted scalar (a JSON string is one).
func yamlString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
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

	line("---")
	line("name: " + skillName)
	line("description: " + yamlString(skillDescription()))
	line("---")
	line("")
	line("# AudioSmith")
	line("")
	line(i18n.T("AudioSmith is a speech app on this Mac: it speaks text (TTS), clones and designs voices, and transcribes audio and video. Everything runs locally with MLX models: nothing leaves this Mac and nothing is paid per call. Use it through the tools of the MCP server audiosmith (below); without them, through its Local API (the last section)."))

	head(i18n.T("Check first"))
	item(i18n.T("AudioSmith must be open with its Local API on. %s (or %s) answers %s when it is ready; %s: wait a few seconds and check again; %s: its engine is down, ask the user to look at AudioSmith.",
		q("check_health"), q("curl -sS "+p.Base+"/health"), q(`"status":"ok"`), q(`"starting"`), q(`"unavailable"`)))
	item(i18n.T("If the connection is refused, or the audiosmith tools cannot connect, AudioSmith is not running: ask the user to open it and turn on Settings → Local API → Run the Local API (port %s). Run %s yourself only if the user agrees, then wait for /health.", p.port(), q("open -a AudioSmith")))
	item(i18n.T("If this chat has no audiosmith tools although /health answers, the MCP server is not set up in this agent: tell the user (AudioSmith → Integrations has a setup prompt) and use the Local API meanwhile."))
	item(i18n.T("Errors start with a code that says what to do: %s or %s: ask the user to accept the licence or to download the model in AudioSmith → Models; %s or %s: wait and try again; %s: that model speaks cloned voices only, use a cloned voice or another model; %s: check list_voices.",
		q("licence_required"), q("model_not_downloaded"), q("engine_unavailable"), q("queue_timeout"), q("needs_reference"), q("voice_not_found")))

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
		item(i18n.T("generate_speech writes its files to %s (the agents' folder).", p.Dir))
	}
	if p.NeedKey {
		item(i18n.T("AudioSmith requires its API key. Your MCP configuration already sends it; for curl, read it from the Keychain without printing it: %s.", q(keychainRead)))
	}

	head(i18n.T("Tools"))
	for _, tl := range skillTools() {
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
			item(n + ": " + a.Text)
		}
		if tl.After != "" {
			line(tl.After)
		}
	}
	line("")
	line(i18n.T("Resources: %s (a saved voice), %s (the 20 latest takes).", q("voice://{id}"), q("history://recent")))

	head(i18n.T("Recipes"))
	item("**" + i18n.T("Narrate a README section to a file.") + "** " + i18n.T("Read the section and make it speakable: leave out code blocks, URLs and markup, and turn tables and lists into sentences. Call generate_speech with %s and a %s such as %s, then report the file's path and length. For a long document, make one file per section.", q(`output: "file"`), q("filename"), q("readme-intro.wav")))
	item("**" + i18n.T("A voice-over for a video.") + "** " + i18n.T("If the timing matters, transcribe the video with %s to see when each part is spoken. Write the lines, then call generate_speech for each with %s and numbered names (%s, %s…). Report the files and their lengths. To put them on the video, suggest AudioSmith's Dub page, or use ffmpeg if the user has it.", q("timestamps: true"), q(`output: "file"`), q("vo-01.wav"), q("vo-02.wav")))
	item("**" + i18n.T("A meeting to SRT subtitles.") + "** " + i18n.T("Call transcribe with the recording's %s and %s. Write the segments as SRT: a number, %s, the text, a blank line. Save it next to the recording with the same name and .srt unless the user says otherwise, and report its path.", q("path"), q("timestamps: true"), q("00:01:02,500 --> 00:01:05,000")))
	item("**" + i18n.T("Find a voice.") + "** " + i18n.T("Read the user's wish with describe_voice and rephrase the words it cannot map; then design_voice; then generate_speech a short sample in the new voice to a file and let the user listen. Another seed gives another voice from the same description."))

	head(i18n.T("Manners"))
	item(i18n.T("Ask before cloning anyone's voice, and pass %s only after the user has confirmed that the speaker agreed.", q("consent: true")))
	item(i18n.T("AudioSmith stays on this Mac: use only 127.0.0.1 and never expose its API (no 0.0.0.0, tunnels or port forwarding)."))
	item(i18n.T("Keep the audio in the agents' folder unless the user names another place (then copy it there), and always report the paths of the files you made."))
	item(i18n.T("Don't change AudioSmith's settings, accept licences or download models yourself: ask the user."))
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
	return b.String()
}

// skillScript is the setup prompt's shell script that installs the skill
// at the agent's user level: it writes the file through a heredoc, backs
// up a different one that is there, and changes nothing when run again.
// It runs in bash and zsh.
func skillScript(slug string, p promptEnv) string {
	var b strings.Builder
	fmt.Fprintf(&b, "D=\"$HOME/%s\"; F=\"$D/SKILL.md\"\n", strings.TrimPrefix(filepath.Dir(skillFile(slug)), "~/"))
	b.WriteString("mkdir -p \"$D\"\n")
	b.WriteString("cat > \"$F.new\" <<'" + skillEOF + "'\n")
	b.WriteString(agentSkill(slug, p))
	b.WriteString(skillEOF + "\n")
	b.WriteString("if [ -f \"$F\" ] && ! cmp -s \"$F\" \"$F.new\"; then cp \"$F\" \"$F.bak-$(date +%Y%m%d-%H%M%S)\"; fi\n")
	b.WriteString("mv \"$F.new\" \"$F\"\n")
	b.WriteString("ls \"$D\"; head -n 3 \"$F\"\n")
	return b.String()
}

// promptSkillStep is the setup prompt's step 4: install the skill at
// the user level, verify it, and tell the user to start a new chat. The
// script is fenced with four backticks: the skill has fences of three.
func promptSkillStep(b *strings.Builder, slug string, p promptEnv) {
	w := func(s string) { b.WriteString(s + "\n") }
	if promptAgents[slug].Client == "" {
		w("4. " + i18n.T("Install AudioSmith's skill, so that you know how to use AudioSmith in every new chat (when to use it, its tools, voices and recipes). If you read Agent Skills (folders with a SKILL.md), set D to your user-level skills folder (many agents read %s) and run this script as it is: it backs up a different SKILL.md that is there, and running it again changes nothing. If you don't, put the text between the %s lines, without its frontmatter (the lines from --- to ---), into your user-level instructions file in place of an earlier AudioSmith section, and tell me which file.", "`~/.agents/skills`", skillEOF))
	} else {
		w("4. " + i18n.T("Install AudioSmith's skill, so that you know how to use AudioSmith in every new chat (when to use it, its tools, voices and recipes). It goes to %s (my user level, all projects); use %s in this project only if I ask. Run this script as it is: it backs up a different SKILL.md that is there, and running it again changes nothing.", "`"+skillFile(slug)+"`", "`"+skillProjectFile(slug)+"`"))
	}
	w("````sh\n" + skillScript(slug, p) + "````")
	w("   " + i18n.T("Check that its output shows %s, then tell me that the skill is installed and that it works in new chats.", "`name: "+skillName+"`"))
}

// skillHome is the home folder skills are installed under (tests set it).
var skillHome = os.UserHomeDir

// skillTarget is the absolute path of an agent's user-level skill.
func skillTarget(slug string) (string, error) {
	home, err := skillHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(skillFile(slug), "~/")), nil
}

// skillState is what installing would do: "" new, "same" nothing,
// "differs" replace (with a backup).
func skillState(path, text string) string {
	old, err := os.ReadFile(path)
	switch {
	case err != nil:
		return ""
	case string(old) == text:
		return "same"
	}
	return "differs"
}

// installSkill writes text to path like the setup prompt's script: a
// different file there is kept as SKILL.md.bak-<time>, the same is left
// alone. It returns the backup's path, if any.
func installSkill(path, text string) (backup string, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	switch skillState(path, text) {
	case "same":
		return "", nil
	case "differs":
		old, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		backup = path + ".bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, old, 0o644); err != nil {
			return "", err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".SKILL-*.md")
	if err != nil {
		return "", err
	}
	_, err = tmp.WriteString(text)
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
		return "", err
	}
	return backup, nil
}
