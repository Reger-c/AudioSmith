package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A shell script stands in for audiosmith-engine: it speaks the protocol
// (ready, then a done or an error per request) without MLX.
const fakeEngineScript = `#!/bin/sh
echo '{"event":"ready","version":"fake","protocolVersion":1,"pid":'$$'}'
echo "engine log line" >&2
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *'"cmd":"synthesize"'*) echo '{"id":"'$id'","event":"done","output":"/tmp/x.wav","sampleRate":24000,"audioSeconds":2,"seconds":0.5}' ;;
    *'"cmd":"crash"'*) echo "fatal: boom" >&2; exit 3 ;;
    *) echo '{"id":"'$id'","event":"error","message":"unknown"}' ;;
  esac
done
`

func TestEngineProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audiosmith-engine")
	if err := os.WriteFile(path, []byte(fakeEngineScript), 0o755); err != nil {
		t.Fatal(err)
	}
	events := make(chan engineEvent, 16)
	type exit struct {
		err  error
		tail string
	}
	exits := make(chan exit, 1)
	e := &engineProcess{
		path:    path,
		logPath: filepath.Join(dir, "engine.log"),
		onEvent: func(ev engineEvent) { events <- ev },
		onExit:  func(err error, tail string) { exits <- exit{err, tail} },
	}
	next := func() engineEvent {
		t.Helper()
		select {
		case ev := <-events:
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("no event from the engine")
		}
		return engineEvent{}
	}

	if err := e.Restart(); err != nil {
		t.Fatal(err)
	}
	if ev := next(); ev.Event != "ready" || ev.Version != "fake" || ev.PID == 0 {
		t.Fatalf("first event %+v", ev)
	}

	id, err := e.Send(engineRequest{Cmd: "synthesize", Text: `He said "hi"`, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if ev := next(); ev.ID != id || ev.Event != "done" || ev.AudioSeconds != 2 || ev.SampleRate != 24000 {
		t.Fatalf("answer to %s: %+v", id, ev)
	}

	// Restarting replaces the process without reporting the old one's exit.
	if err := e.Restart(); err != nil {
		t.Fatal(err)
	}
	if ev := next(); ev.Event != "ready" {
		t.Fatalf("after restart %+v", ev)
	}

	// An unexpected exit is reported with the engine's last stderr lines.
	e.Send(engineRequest{Cmd: "crash"})
	select {
	case x := <-exits:
		if x.err == nil || !strings.Contains(x.tail, "fatal: boom") {
			t.Fatalf("exit %v, tail %q", x.err, x.tail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exit not reported")
	}
	if _, err := e.Send(engineRequest{Cmd: "ping"}); err == nil {
		t.Error("Send succeeded to a dead engine")
	}
	data, _ := os.ReadFile(e.logPath)
	if !strings.Contains(string(data), "engine log line") {
		t.Errorf("log %q", data)
	}
	select {
	case x := <-exits:
		t.Errorf("a second exit was reported: %v", x)
	default:
	}
	e.Stop()
}

func TestEngineMissing(t *testing.T) {
	e := &engineProcess{}
	if err := e.Restart(); err == nil || !strings.Contains(err.Error(), "build-engine.sh") {
		t.Fatalf("err %v", err)
	}
	if got := findEngine(t.TempDir()); got != "" && os.Getenv("AUDIOSMITH_ENGINE") == "" {
		t.Errorf("found %q in an empty directory", got)
	}
}

func TestFindEngineIsAbsolute(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "res", "engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "res", "engine", "audiosmith-engine"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("AUDIOSMITH_ENGINE", "")
	got := findEngine("res")
	if !filepath.IsAbs(got) || filepath.Base(got) != "audiosmith-engine" {
		t.Fatalf("findEngine = %q", got)
	}
}
