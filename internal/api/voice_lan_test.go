package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
)

// TestLANVoiceAudio: over the LAN with the PIN, a voice's audio is its
// sample only, never the reference recording (security review #100, L7);
// on this Mac a voice without a sample still plays its reference.
func TestLANVoiceAudio(t *testing.T) {
	f := newFake(t)
	both := core.Voice{ID: "both", Name: "Both", Kind: core.VoiceCloned, RefAudio: "reference.wav", Sample: "sample.wav"}
	ref := core.Voice{ID: "ref", Name: "Ref only", Kind: core.VoiceCloned, RefAudio: "reference.wav"}
	f.voices = append(f.voices, both, ref)
	for _, v := range []core.Voice{both, ref} {
		os.MkdirAll(filepath.Join(f.dir, "voices", v.ID), 0o755)
		os.WriteFile(f.VoiceFile(v, "reference.wav"), []byte("REFERENCE"), 0o644)
		if v.Sample != "" {
			os.WriteFile(f.VoiceFile(v, v.Sample), []byte("SAMPLE"), 0o644)
		}
	}
	s, ts := f.server()
	share, err := s.StartLAN(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopLAN()
	lan := httptest.NewServer(s.LANHandler(share.Port))
	defer lan.Close()
	get := func(base, id string, pin bool) (int, string) {
		req, _ := http.NewRequest("GET", base+"/api/voices/"+id+"/audio", nil)
		if pin {
			req.Header.Set(HeaderPIN, share.PIN)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, b := get(lan.URL, "both", true); c != 200 || b != "SAMPLE" {
		t.Fatalf("LAN with sample: %d %q", c, b)
	}
	if c, b := get(lan.URL, "ref", true); c != 404 || b == "REFERENCE" {
		t.Fatalf("LAN reference only: %d %q", c, b)
	}
	if c, b := get(ts.URL, "ref", false); c != 200 || b != "REFERENCE" {
		t.Fatalf("loopback reference: %d %q", c, b)
	}
	s.SetKey("as-k")
	req, _ := http.NewRequest("GET", lan.URL+"/api/voices/ref/audio", nil)
	req.Header.Set("Authorization", "Bearer as-k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "REFERENCE" {
		t.Fatalf("LAN with the key: %d %q", resp.StatusCode, b)
	}
}
