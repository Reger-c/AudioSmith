package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// engineRequest is one line written to audiosmith-engine's stdin. See
// engine/Sources/AudioSmithEngine/Protocol.swift for the protocol.
type engineRequest struct {
	ID          string   `json:"id,omitempty"`
	Cmd         string   `json:"cmd"`
	Model       string   `json:"model,omitempty"`
	Text        string   `json:"text,omitempty"`
	Voice       string   `json:"voice,omitempty"`
	Language    string   `json:"language,omitempty"`
	Speed       *float64 `json:"speed,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
	MaxTokens   int      `json:"maxTokens,omitempty"`
	RefAudio    string   `json:"refAudio,omitempty"`
	RefText     string   `json:"refText,omitempty"`
	Output      string   `json:"output,omitempty"`
}

// engineEvent is one line the engine writes to stdout.
type engineEvent struct {
	ID              string   `json:"id"`
	Event           string   `json:"event"`
	Message         string   `json:"message"`
	Model           string   `json:"model"`
	Version         string   `json:"version"`
	ProtocolVersion int      `json:"protocolVersion"`
	PID             int      `json:"pid"`
	SampleRate      int      `json:"sampleRate"`
	Seconds         float64  `json:"seconds"`
	Fraction        *float64 `json:"fraction"`
	AudioSeconds    float64  `json:"audioSeconds"`
	RTF             float64  `json:"rtf"`
	Output          string   `json:"output"`
}

// backend is what the view needs from the engine; tests replace it.
type backend interface {
	// Send writes a request, giving it an ID when it has none, and returns the ID.
	Send(req engineRequest) (string, error)
	// Restart stops the engine process, if any, and starts a new one.
	Restart() error
	Stop()
	Path() string
}

// engineProcess runs audiosmith-engine as a child process. Events arrive
// on a goroutine of its own through onEvent; onExit reports the process
// ending, with its last stderr lines.
type engineProcess struct {
	path    string
	logPath string
	onEvent func(engineEvent)
	onExit  func(err error, stderrTail string)

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	nextID int
	gen    int // bumps on every start, so a dying old process reports nothing
}

func (e *engineProcess) Path() string { return e.path }

func (e *engineProcess) Restart() error {
	e.Stop()
	return e.start()
}

func (e *engineProcess) start() error {
	if e.path == "" {
		return errors.New("audiosmith-engine was not found: build it with scripts/build-engine.sh, or set AUDIOSMITH_ENGINE")
	}
	cmd := exec.Command(e.path)
	cmd.Dir = filepath.Dir(e.path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", e.path, err)
	}

	e.mu.Lock()
	e.gen++
	gen := e.gen
	e.cmd, e.stdin = cmd, stdin
	e.mu.Unlock()

	tail := newLineTail(40)
	var logFile *os.File
	if e.logPath != "" {
		logFile, _ = os.OpenFile(e.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
	var stderrDone sync.WaitGroup
	stderrDone.Add(1)
	go func() {
		defer stderrDone.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			tail.add(line)
			if logFile != nil {
				fmt.Fprintln(logFile, line)
			}
		}
	}()

	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			var ev engineEvent
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				tail.add("stdout: " + sc.Text())
				continue
			}
			if e.current(gen) && e.onEvent != nil {
				e.onEvent(ev)
			}
		}
		stderrDone.Wait()
		err := cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}
		if e.current(gen) {
			e.mu.Lock()
			e.cmd, e.stdin = nil, nil
			e.mu.Unlock()
			if e.onExit != nil {
				e.onExit(err, tail.String())
			}
		}
	}()
	return nil
}

func (e *engineProcess) current(gen int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.gen == gen
}

func (e *engineProcess) Send(req engineRequest) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stdin == nil {
		return "", errors.New("the engine is not running")
	}
	if req.ID == "" {
		e.nextID++
		req.ID = strconv.Itoa(e.nextID)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	if _, err := e.stdin.Write(append(data, '\n')); err != nil {
		return "", fmt.Errorf("writing to the engine: %w", err)
	}
	return req.ID, nil
}

// Stop ends the engine: closing stdin makes it exit by itself; a kill
// follows for one stuck in a computation.
func (e *engineProcess) Stop() {
	e.mu.Lock()
	cmd, stdin := e.cmd, e.stdin
	e.gen++ // the old process's exit is expected: don't report it
	e.cmd, e.stdin = nil, nil
	e.mu.Unlock()
	if stdin != nil {
		stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

// findEngine looks for audiosmith-engine: $AUDIOSMITH_ENGINE, then the app's
// resources (packaged, mygo dev, or go run from the project).
func findEngine(resources string) string {
	if p := os.Getenv("AUDIOSMITH_ENGINE"); p != "" {
		return p
	}
	candidates := []string{
		filepath.Join(resources, "engine", "audiosmith-engine"),
		filepath.Join(resources, runtime.GOOS+"-"+runtime.GOARCH, "engine", "audiosmith-engine"),
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// lineTail keeps the last lines written to it.
type lineTail struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func newLineTail(max int) *lineTail { return &lineTail{max: max} }

func (t *lineTail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

func (t *lineTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}
