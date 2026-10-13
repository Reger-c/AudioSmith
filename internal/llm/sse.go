package llm

import (
	"bufio"
	"io"
	"strings"
)

// sseEvent is one Server-Sent Event.
type sseEvent struct {
	Event string
	Data  string
}

// readSSE calls fn for every event in r until EOF, an error, or fn
// returning false. Lines are capped at 4 MiB.
func readSSE(r io.Reader, fn func(sseEvent) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	var ev sseEvent
	var data strings.Builder
	hasData := false
	flush := func() bool {
		if !hasData && ev.Event == "" {
			return true
		}
		ev.Data = data.String()
		ok := fn(ev)
		ev, hasData = sseEvent{}, false
		data.Reset()
		return ok
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if !flush() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / keep-alive (OpenRouter sends ": OPENROUTER PROCESSING")
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			ev.Event = value
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasData = true
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	flush()
	return nil
}
