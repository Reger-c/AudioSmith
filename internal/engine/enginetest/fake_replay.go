package enginetest

// Replaying a fixture transcript (option replay=PATH).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// replay answers requests from a fixture transcript: each request takes
// the next unused fixture request of the same method, and the app-bound
// lines that follow it are written with the fixture's ids replaced by the
// live ones. Lines before the first request are written after hello.
func (f *fakeEngine) replay(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	type line struct {
		To  string          `json:"to"`
		Msg json.RawMessage `json:"msg"`
	}
	var lines []line
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ln line
		if err := json.Unmarshal([]byte(l), &ln); err != nil || ln.Msg == nil {
			continue
		}
		lines = append(lines, ln)
	}
	used := make([]bool, len(lines))
	ids := map[string]string{} // fixture id → live id
	rewrite := func(raw json.RawMessage) string {
		var m proto.Message
		json.Unmarshal(raw, &m)
		if to, ok := ids[m.ID]; ok {
			m.ID = to
		}
		if len(m.Params) > 0 {
			var params map[string]any
			json.Unmarshal(m.Params, &params)
			if job, ok := params["job"].(string); ok {
				if to, ok := ids[job]; ok {
					params["job"] = to
				}
			}
			m.Params, _ = json.Marshal(params)
		}
		out, _ := json.Marshal(m)
		return string(out)
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), proto.MaxLine)
	for sc.Scan() {
		m, err := proto.Decode(sc.Bytes())
		if err != nil {
			continue
		}
		if m.Method == proto.MethodHello {
			var p proto.HelloParams
			m.DecodeParams(&p)
			if p.Role == "" {
				p.Role = proto.RoleGPU
			}
			f.result(m.ID, proto.HelloResult{Protocol: proto.Version, Version: "replay", Role: p.Role, PID: os.Getpid(), Concurrency: Concurrency(p.Role)})
			continue
		}
		if m.Method == proto.MethodStats && !strings.Contains(string(data), `"engine.stats"`) {
			f.result(m.ID, proto.StatsResult{Models: []proto.LoadedModel{}, Jobs: []string{}})
			continue
		}
		found := -1
		for i, ln := range lines {
			if used[i] || ln.To != "engine" {
				continue
			}
			var fm proto.Message
			json.Unmarshal(ln.Msg, &fm)
			if fm.Method == m.Method {
				found = i
				ids[fm.ID] = m.ID
				break
			}
		}
		if found < 0 {
			f.fail(m.ID, proto.CodeMethodNotFound, "not in the fixture: %s", m.Method)
			continue
		}
		used[found] = true
		for i := found + 1; i < len(lines) && lines[i].To == "app"; i++ {
			used[i] = true
			f.writeRaw(rewrite(lines[i].Msg))
		}
	}
}
