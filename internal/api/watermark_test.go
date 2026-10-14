package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestWatermarkDetect(t *testing.T) {
	f := newFake(t)
	_, ts := f.server()
	ct, body := multipartFile(t, "clip.wav", []byte("RIFF fake"))
	req, _ := http.NewRequest("POST", ts.URL+"/api/watermark/detect", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set(HeaderCSRF, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r WatermarkReport
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || resp.StatusCode != 200 || !r.AudioSmith || r.Install != 0x3C || !r.Detected {
		t.Fatalf("%d %+v %v", resp.StatusCode, r, err)
	}
	if resp := do(t, ts, "POST", "/api/watermark/detect", map[string]any{"path": "/x.wav"}, nil); resp.StatusCode != 400 {
		t.Errorf("JSON body: %d", resp.StatusCode)
	}
}
