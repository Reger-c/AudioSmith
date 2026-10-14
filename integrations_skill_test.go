package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/mcp"
	"github.com/hehehai/AudioSmith/internal/settings"
)

var promptSlugs = []string{"claude-code", "codex", "cursor", "mcp"}

func skillTestEnv(e integEnv) promptEnv {
	return promptEnv{integEnv: e, Voices: []string{"Narrator", "Ann"}, Bindings: map[string]string{"claude-code": "Narrator"}, Dir: "/Users/me/AudioSmith/mcp"}
}

// frontmatter parses a SKILL.md's frontmatter as the skill writes it:
// "key: value" lines, the description a YAML double-quoted (JSON) string.
func frontmatter(t *testing.T, s string) (map[string]string, string) {
	t.Helper()
	if !strings.HasPrefix(s, "---\n") {
		t.Fatalf("no frontmatter:\n%s", s)
	}
	end := strings.Index(s[4:], "\n---\n")
	if end < 0 {
		t.Fatalf("frontmatter not closed:\n%s", s)
	}
	fm := map[string]string{}
	for _, l := range strings.Split(s[4:4+end], "\n") {
		k, v, ok := strings.Cut(l, ": ")
		if !ok {
			t.Fatalf("frontmatter line %q", l)
		}
		if strings.HasPrefix(v, `"`) {
			if err := json.Unmarshal([]byte(v), &v); err != nil {
				t.Fatalf("%s: %v", k, err)
			}
		}
		fm[k] = v
	}
	return fm, s[4+end+5:]
}

var skillNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// TestAgentSkill: each agent's SKILL.md has valid frontmatter (name,
// description of the Agent Skills spec), the live port, client, voices
// and folder, every MCP tool with exactly its schema's arguments, and
// never the key; it is in the UI's language.
func TestAgentSkill(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	e := newAPIEnv(t)
	// The MCP server's tools and their arguments, as an agent sees them.
	c := sdk.NewClient(&sdk.Implementation{Name: "skill-test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: e.ts.URL + "/mcp", HTTPClient: http.DefaultClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	schemas := map[string]schema{}
	for _, tl := range tools.Tools {
		var s schema
		data, _ := json.Marshal(tl.InputSchema)
		json.Unmarshal(data, &s)
		schemas[tl.Name] = s
	}
	if init := cs.InitializeResult(); init == nil || init.Instructions != mcp.Instructions {
		t.Error("initialize has no instructions")
	}

	for _, lang := range []string{"en", "zh-Hans"} {
		i18n.SetLocale(lang)
		// The tools as the skill explains them = the server's.
		var names []string
		for _, tl := range skillTools() {
			names = append(names, tl.Name)
			sc, ok := schemas[tl.Name]
			if !ok {
				t.Errorf("the skill explains %s, which the server lacks", tl.Name)
				continue
			}
			var args, req []string
			for _, a := range tl.Args {
				args = append(args, a.Names...)
				if a.Required {
					req = append(req, a.Names...)
				}
			}
			var props []string
			for p := range sc.Properties {
				props = append(props, p)
			}
			slices.Sort(args)
			slices.Sort(props)
			slices.Sort(req)
			r := slices.Clone(sc.Required)
			slices.Sort(r)
			if !slices.Equal(args, props) {
				t.Errorf("%s: the skill names %v, the schema has %v", tl.Name, args, props)
			}
			if !slices.Equal(req, r) {
				t.Errorf("%s: required %v, the schema's %v", tl.Name, req, r)
			}
		}
		slices.Sort(names)
		want := slices.Clone(mcpToolNames)
		slices.Sort(want)
		if !slices.Equal(names, want) || len(schemas) != len(want) {
			t.Errorf("tools %v, server %d, want %v", names, len(schemas), want)
		}

		for _, slug := range promptSlugs {
			for mode, ie := range map[string]integEnv{
				"none":     {Base: "http://127.0.0.1:4000", Exe: testExe},
				"keychain": {Base: "http://127.0.0.1:4000", Exe: testExe, NeedKey: true},
				"included": {Base: "http://127.0.0.1:4000", Exe: testExe, NeedKey: true, Key: "as-SECRET"},
			} {
				p := skillTestEnv(ie)
				s := agentSkill(slug, p)
				fm, body := frontmatter(t, s)
				if len(fm) != 2 || fm["name"] != skillName || !skillNameRE.MatchString(fm["name"]) || len(fm["name"]) > 64 {
					t.Errorf("%s %s: frontmatter %v", lang, slug, fm)
				}
				if d := fm["description"]; d == "" || len([]rune(d)) > 1024 || !strings.Contains(d, "AudioSmith") || !strings.Contains(d, "TTS") || !strings.Contains(d, "SRT") {
					t.Errorf("%s %s: description %q", lang, slug, d)
				}
				for _, w := range []string{"http://127.0.0.1:4000/health", "http://127.0.0.1:4000/mcp", "port 4000", "Narrator, Ann", "/Users/me/AudioSmith/mcp",
					"open -a AudioSmith", "consent: true", "timestamps: true", `output: "file"`, "voice://{id}", "history://recent", "127.0.0.1", "MOSS-TTS-Nano",
					"licence_required", "needs_reference", "/v1/audio/speech", "response_format=srt"} {
					if lang == "zh-Hans" && w == "port 4000" {
						w = "端口 4000"
					}
					if !strings.Contains(body, w) {
						t.Errorf("%s %s %s: no %q in\n%s", lang, slug, mode, w, s)
					}
				}
				for _, tl := range skillTools() {
					if !strings.Contains(body, "### `"+tl.Name+"`") {
						t.Errorf("%s %s: no section for %s", lang, slug, tl.Name)
					}
					for _, a := range tl.Args {
						for _, n := range a.Names {
							if !strings.Contains(body, "`"+n+"`") {
								t.Errorf("%s %s: %s.%s missing", lang, slug, tl.Name, n)
							}
						}
					}
				}
				ag := promptAgents[slug]
				if hasClient := strings.Contains(s, "X-AudioSmith-Client: "+ag.Client+"'"); hasClient != (ag.Client != "") {
					t.Errorf("%s: client header %v", slug, hasClient)
				}
				if bound := strings.Contains(s, "Your own voice: Narrator."); lang == "en" && bound != (slug == "claude-code") {
					t.Errorf("%s: the bound voice", slug)
				}
				if strings.Contains(s, "as-SECRET") {
					t.Errorf("%s %s: the key is in the skill", slug, mode)
				}
				if strings.Contains(s, keychainRead) != (mode != "none") {
					t.Errorf("%s %s: the key from the Keychain", slug, mode)
				}
				if strings.Contains(s, skillEOF) {
					t.Errorf("%s: the skill holds its own heredoc end", slug)
				}
				if zh := strings.Contains(s, "检查"); zh != (lang == "zh-Hans") {
					t.Errorf("%s %s: language", lang, slug)
				}
				p.Base = "http://127.0.0.1:4555"
				if s2 := agentSkill(slug, p); strings.Contains(s2, "4000") || !strings.Contains(s2, "http://127.0.0.1:4555/mcp") {
					t.Errorf("%s: the port did not follow", slug)
				}
			}
		}
		// A real YAML parser agrees, when there is one.
		if py, err := exec.LookPath("python3"); err == nil && exec.Command(py, "-c", "import yaml").Run() == nil {
			s := agentSkill("claude-code", skillTestEnv(integEnv{Base: "http://127.0.0.1:3901"}))
			fm, _ := frontmatter(t, s)
			cmd := exec.Command(py, "-I", "-c", `import sys, yaml, json
t = sys.stdin.read().split("---\n")[1]
print(json.dumps(yaml.safe_load(t), ensure_ascii=False))`)
			cmd.Stdin = strings.NewReader(s)
			out, err := cmd.Output()
			var got map[string]string
			if err != nil || json.Unmarshal(out, &got) != nil || got["name"] != "audiosmith" || got["description"] != fm["description"] {
				t.Errorf("%s: YAML says %v %s", lang, err, out)
			}
		}
	}
	if agentSkill("openai-sdk", promptEnv{}) != "" {
		t.Error("a skill for a page that is not an agent")
	}
}

// TestAgentSkillScript runs the setup prompt's install script in bash and
// zsh with a temporary HOME: it writes the skill, is idempotent, backs up
// a different file; and the skill's curl example speaks.
func TestAgentSkillScript(t *testing.T) {
	e := newAPIEnv(t)
	for _, slug := range promptSlugs {
		p := promptEnv{integEnv: integEnv{Base: e.ts.URL, Exe: testExe}, Dir: "/tmp/x"}
		prompt := agentPrompt(slug, p)
		var script string
		for _, blk := range promptBlocks(prompt) {
			if strings.Contains(blk[1], "<<'"+skillEOF+"'") {
				script = blk[1]
			}
		}
		if script == "" || !strings.Contains(prompt, "````sh\n") {
			t.Fatalf("%s: no install script:\n%s", slug, prompt)
		}
		want := agentSkill(slug, p)
		for _, sh := range []string{"bash", "zsh"} {
			if _, err := exec.LookPath(sh); err != nil {
				t.Logf("no %s", sh)
				continue
			}
			home := t.TempDir()
			path := filepath.Join(home, strings.TrimPrefix(skillFile(slug), "~/"))
			run := func() string {
				cmd := exec.Command(sh, "-c", script)
				cmd.Env = append(os.Environ(), "HOME="+home)
				cmd.Dir = home
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s %s: %v\n%s", sh, slug, err, out)
				}
				return string(out)
			}
			files := func() []string {
				es, _ := os.ReadDir(filepath.Dir(path))
				var n []string
				for _, e := range es {
					n = append(n, e.Name())
				}
				return n
			}
			out := run()
			if got, err := os.ReadFile(path); err != nil || string(got) != want {
				t.Fatalf("%s %s: wrote %v\n%s", sh, slug, err, got)
			}
			if !strings.Contains(out, "name: audiosmith") {
				t.Errorf("%s %s: output %s", sh, slug, out)
			}
			run()
			if f := files(); !slices.Equal(f, []string{"SKILL.md"}) {
				t.Errorf("%s %s: again: %v", sh, slug, f)
			}
			os.WriteFile(path, []byte("old"), 0o644)
			run()
			f := files()
			if got, _ := os.ReadFile(path); string(got) != want || len(f) != 2 || !strings.HasPrefix(f[1], "SKILL.md.bak-") {
				t.Errorf("%s %s: after a different file: %v", sh, slug, f)
			} else if b, _ := os.ReadFile(filepath.Join(filepath.Dir(path), f[1])); string(b) != "old" {
				t.Errorf("%s: backup %q", sh, b)
			}
		}
	}
	// The skill's curl example (its first line) speaks.
	for _, blk := range promptBlocks(agentSkill("claude-code", promptEnv{integEnv: integEnv{Base: e.ts.URL}})) {
		if !strings.Contains(blk[1], "/v1/audio/speech") {
			continue
		}
		dir := t.TempDir()
		cmd := exec.Command("bash", "-c", strings.Split(blk[1], "\n")[0])
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "hello.wav")); err != nil || string(b[:4]) != "RIFF" {
			t.Errorf("hello.wav: %v %.80q", err, b)
		}
	}
}

// TestInstallSkill: a new file, the same (nothing), a different one
// (kept as a backup).
func TestInstallSkill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "audiosmith", "SKILL.md")
	if s := skillState(path, "x"); s != "" {
		t.Fatal(s)
	}
	if b, err := installSkill(path, "one"); err != nil || b != "" {
		t.Fatal(b, err)
	}
	if b, err := installSkill(path, "one"); err != nil || b != "" || skillState(path, "one") != "same" {
		t.Fatal(b, err)
	}
	b, err := installSkill(path, "two")
	if got, _ := os.ReadFile(b); err != nil || string(got) != "one" {
		t.Fatalf("backup %s %v %q", b, err, got)
	}
	if got, _ := os.ReadFile(path); string(got) != "two" {
		t.Fatal(string(got))
	}
	es, _ := os.ReadDir(filepath.Dir(path))
	if len(es) != 2 {
		t.Errorf("left %d files", len(es))
	}
}

// TestAgentSkillPage: the skill card installs the skill after a
// confirmation, shows whether it is up to date, saves and copies it; the
// generic MCP page saves but does not install; other pages have no card.
func TestAgentSkillPage(t *testing.T) {
	home := t.TempDir()
	skillHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { skillHome = os.UserHomeDir })
	a, _, tt := newTestApp(t)
	a.updateSettings(func(s *settings.Settings) { s.API.Port = 4123 })
	a.voices = append(a.voices, core.Voice{ID: "v1", Name: "Narrator"})
	tt.SetSize(1100, 1400)
	a.navigate("/integrations/claude-code")
	tt.Frame()
	if !tt.HasText("Agent skill") || tt.HasText("Installed: ~/.claude/skills/audiosmith/SKILL.md") {
		t.Fatal("skill card")
	}
	if err := tt.Click("Install skill…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Install the skill for Claude Code?") {
		t.Fatal("no confirmation")
	}
	if err := tt.Click("Install"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	path := filepath.Join(home, ".claude", "skills", "audiosmith", "SKILL.md")
	want := agentSkill("claude-code", a.promptEnvOf())
	if got, err := os.ReadFile(path); err != nil || string(got) != want || !strings.Contains(want, "http://127.0.0.1:4123/mcp") || !strings.Contains(want, "Narrator") {
		t.Fatalf("installed %v:\n%s", err, got)
	}
	tt.Frame()
	if !tt.HasText("Skill installed: start a new chat in Claude Code") || !tt.HasText("Installed: ~/.claude/skills/audiosmith/SKILL.md") {
		t.Error("toast or state")
	}
	// The voices change: the card says so; installing again keeps a backup.
	a.voices = append(a.voices, core.Voice{ID: "v2", Name: "Ann"})
	tt.Frame()
	if !tt.HasText("~/.claude/skills/audiosmith/SKILL.md holds another version (your voices or settings changed since): install again to update it.") {
		t.Error("no out-of-date note")
	}
	tt.Click("Install skill…")
	tt.Frame()
	if !tt.HasText("A different SKILL.md is there: it is kept beside it as a backup (SKILL.md.bak-…).") {
		t.Error("no backup note")
	}
	tt.Click("Install")
	tt.Frame()
	if es, _ := os.ReadDir(filepath.Dir(path)); len(es) != 2 {
		t.Errorf("files %v", es)
	}
	// Save skill… and Copy skill.
	integSaveDir = t.TempDir()
	t.Cleanup(func() { integSaveDir = "" })
	if err := tt.Click("Save skill…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	want = agentSkill("claude-code", a.promptEnvOf())
	if data, err := os.ReadFile(filepath.Join(integSaveDir, "SKILL.md")); err != nil || string(data) != want {
		t.Fatalf("saved %v", err)
	}
	if err := tt.Click("Copy skill"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.Clipboard() != want {
		t.Error("clipboard")
	}
	// The generic page: no Install; the other pages: no card.
	a.navigate("/integrations/mcp")
	tt.Frame()
	if !tt.HasText("Agent skill") || tt.HasText("Install skill…") || !tt.HasText("Save skill…") {
		t.Error("generic MCP page")
	}
	for _, in := range integrations {
		a.navigate("/integrations/" + in.Slug)
		tt.Frame()
		_, agent := promptAgents[in.Slug]
		if tt.HasText("Agent skill") != agent {
			t.Errorf("%s: skill card %v", in.Slug, !agent)
		}
	}
}

// TestSnapshotsAgentSkill renders the skill card and its dialog, and
// writes the review texts (skills and prompts):
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsAgentSkill .
func TestSnapshotsAgentSkill(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	home := t.TempDir()
	skillHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { skillHome = os.UserHomeDir })
	useMemKeys(t)
	for _, lang := range []string{"en", "zh-Hans"} {
		i18n.SetLocale(lang)
		p := promptEnv{integEnv: integEnv{Base: "http://127.0.0.1:3901", Exe: testExe}, Voices: []string{"Narrator", "Ann"},
			Bindings: map[string]string{"claude-code": "Narrator"}, Dir: "/Users/me/Library/Application Support/AudioSmith/mcp"}
		for _, slug := range promptSlugs {
			os.WriteFile(filepath.Join(out, fmt.Sprintf("SKILL-%s-%s.md", slug, lang)), []byte(agentSkill(slug, p)), 0o644)
			os.WriteFile(filepath.Join(out, fmt.Sprintf("prompt-%s-%s.txt", slug, lang)), []byte(agentPrompt(slug, p)), 0o644)
		}
	}
	for _, dark := range []bool{false, true} {
		for _, lang := range []string{"en", "zh-Hans"} {
			i18n.SetLocale("en")
			a, _, _ := newTestApp(t)
			a.updateSettings(func(s *settings.Settings) {
				if lang != "en" {
					s.General.Language = lang
				}
				s.API.Bindings = []settings.APIBinding{{Client: "claude-code", Voice: "v1"}}
			})
			a.voices = append(a.voices, core.Voice{ID: "v1", Name: "Narrator", Kind: core.VoiceCloned}, core.Voice{ID: "v2", Name: "Ann", Kind: core.VoiceDesigned})
			a.apiPort = 3901
			suffix := "light"
			if dark {
				suffix = "dark"
			}
			if lang != "en" {
				suffix += "-" + lang
			}
			shot := func(name string, w, h int) {
				tt := ui.NewTester(a.view, w, h)
				tt.SetScale(2)
				tt.SetDark(dark)
				tt.Frame()
				tt.Frame()
				f, err := os.Create(filepath.Join(out, name+"-"+suffix+".png"))
				if err != nil {
					t.Fatal(err)
				}
				png.Encode(f, tt.Image())
				f.Close()
			}
			a.navigate("/integrations/claude-code")
			os.RemoveAll(filepath.Join(home, ".claude"))
			shot("agent-skill-claude-code-1100x1100", 1100, 1100)
			if !dark {
				a.skillAsk = true
				shot("agent-skill-dialog", 1100, 720)
				a.skillAsk = false
				path, _ := skillTarget("claude-code")
				installSkill(path, agentSkill("claude-code", a.promptEnvOf()))
				shot("agent-skill-installed", 1100, 1100)
				a.navigate("/integrations/mcp")
				shot("agent-skill-mcp", 1100, 1100)
			}
		}
	}
}
