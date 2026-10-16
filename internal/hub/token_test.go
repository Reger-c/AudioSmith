package hub

// The Hugging Face token and the other Keychain secrets.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestToken(t *testing.T) {
	saved := security
	defer func() { security = saved }()
	var stdin string
	var args []string
	security = func(in string, a ...string) ([]byte, error) {
		stdin, args = in, a
		if a[0] == "find-generic-password" {
			return []byte("hf_fromkeychain\n"), nil
		}
		return nil, nil
	}
	k := Keychain{Service: "S", Account: "A"}
	if err := k.Set("hf_abc"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args, []string{"-i"}) || !strings.Contains(stdin, `-w "hf_abc"`) || strings.Contains(strings.Join(args, " "), "hf_abc") {
		t.Errorf("set: args %q stdin %q", args, stdin)
	}
	if err := k.Set(`bad"token`); err == nil {
		t.Error("accepted a quote")
	}
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HF_TOKEN_PATH", filepath.Join(t.TempDir(), "token"))
	if tok, src := Token(k); tok != "hf_fromkeychain" || src != TokenKeychain {
		t.Errorf("keychain: %q %q", tok, src)
	}
	t.Setenv("HF_TOKEN", "hf_env")
	if tok, src := Token(k); tok != "hf_env" || src != TokenEnv {
		t.Errorf("env: %q %q", tok, src)
	}
	t.Setenv("HF_TOKEN", "")
	security = func(string, ...string) ([]byte, error) { return nil, ErrNoKeychain }
	os.WriteFile(os.Getenv("HF_TOKEN_PATH"), []byte("hf_file\n"), 0o600)
	if tok, src := Token(k); tok != "hf_file" || src != TokenCLI {
		t.Errorf("file: %q %q", tok, src)
	}
	if MaskToken("hf_abcdefghijkl") != "hf_…ijkl" || MaskToken("short") != "•••••" {
		t.Error(MaskToken("hf_abcdefghijkl"))
	}
}

// Every secret is an AudioSmith item with its own account and label; the
// HF token keeps the account it always had.
func TestSecrets(t *testing.T) {
	saved := security
	defer func() { security = saved }()
	var stdin string
	security = func(in string, a ...string) ([]byte, error) { stdin = in; return nil, nil }
	if DefaultKeychain.Service != "AudioSmith" || DefaultKeychain.Account != "huggingface-token" {
		t.Errorf("HF token item moved: %+v", DefaultKeychain)
	}
	if err := Secret("llm.openai", "AudioSmith OpenAI API key").Set("sk-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdin, `-s "AudioSmith" -a "llm.openai" -l "AudioSmith OpenAI API key"`) {
		t.Errorf("stdin %q", stdin)
	}
	Secret("mt.deepl", "").Set("k")
	if !strings.Contains(stdin, `-l "AudioSmith mt.deepl"`) {
		t.Errorf("default label: %q", stdin)
	}
}
