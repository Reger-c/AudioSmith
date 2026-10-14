package settings

import "testing"

func TestAPIDefaults(t *testing.T) {
	d := Defaults()
	if !d.API.Enabled || d.API.LoopbackPort() != DefaultAPIPort || d.API.LANPort() != DefaultAPIPort+1 || d.API.Output() != MCPOutputBoth {
		t.Fatalf("defaults %+v", d.API)
	}
	// A document from before M5 gains them.
	s, err := Migrate([]byte(`{"version":1,"general":{"language":"en"}}`))
	if err != nil || !s.API.Enabled || s.API.Port != DefaultAPIPort {
		t.Fatalf("old document: %+v %v", s.API, err)
	}
	// Turned off stays off.
	s, _ = Migrate([]byte(`{"api":{"enabled":false,"port":4000}}`))
	if s.API.Enabled || s.API.LoopbackPort() != 4000 || s.API.LANPort() != 4001 {
		t.Fatalf("off: %+v", s.API)
	}
}

func TestAPIHelpers(t *testing.T) {
	a := API{Port: 70000, SharePort: 3901, MCPOutput: "file",
		Bindings: []APIBinding{{Client: "claude-code", Voice: "v1"}, {Client: "Cursor", Voice: "v2"}}}
	if a.LoopbackPort() != DefaultAPIPort || a.LANPort() != DefaultAPIPort+1 || a.Output() != MCPOutputFile {
		t.Fatalf("%d %d %s", a.LoopbackPort(), a.LANPort(), a.Output())
	}
	if b, ok := a.Binding(" cursor "); !ok || b.Voice != "v2" {
		t.Fatalf("binding %+v %v", b, ok)
	}
	if _, ok := a.Binding(""); ok {
		t.Fatal("empty client bound")
	}
	if _, ok := a.Binding("codex"); ok {
		t.Fatal("unknown client bound")
	}
}
