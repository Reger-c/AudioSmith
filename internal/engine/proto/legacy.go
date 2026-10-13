package proto

import "encoding/json"

// LegacyReady recognises the first line of a protocol-1 engine (the MVP's
// {"event":"ready","protocolVersion":1,…}) and returns its version, so the
// supervisor can report "engine too old: rebuild it" instead of a timeout.
func LegacyReady(line []byte) (version int, ok bool) {
	var v struct {
		Event           string `json:"event"`
		ProtocolVersion int    `json:"protocolVersion"`
	}
	if json.Unmarshal(line, &v) != nil || v.Event != "ready" {
		return 0, false
	}
	return max(v.ProtocolVersion, 1), true
}
