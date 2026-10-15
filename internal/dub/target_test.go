package dub

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestDefaultTargetAndRetarget(t *testing.T) {
	p := New("d", time.Now())
	if !p.TargetsAuto || !slices.Equal(p.Targets, []string{"zh-Hans"}) || p.TargetSwitched != "" {
		t.Fatalf("new %v auto %v", p.Targets, p.TargetsAuto)
	}
	// Unknown or other source: unchanged.
	for _, src := range []string{"", "en", "ja", "yue"} {
		p.Detected = src
		if p.AutoRetarget() || p.Primary() != "zh-Hans" {
			t.Errorf("%q: %v", src, p.Targets)
		}
	}
	// A Chinese video (detected or chosen, any tag) → English.
	for _, src := range []string{"zh", "zh-CN", "zh-Hans", "cmn"} {
		q := New("d", time.Now())
		q.Detected = src
		if !q.AutoRetarget() || !slices.Equal(q.Targets, []string{"en"}) || q.TargetSwitched != "zh-Hans" {
			t.Errorf("%q: %v switched %q", src, q.Targets, q.TargetSwitched)
		}
		if q.AutoRetarget() {
			t.Errorf("%q: not idempotent", src)
		}
	}
	p.SourceLang = "zh"
	if !p.AutoRetarget() || p.Primary() != "en" {
		t.Fatalf("chosen zh: %v", p.Targets)
	}
	// Stages planned in the replaced language run in the new one.
	if p.RetargetedLang("zh-Hans") != "en" || p.RetargetedLang("fr") != "fr" || p.RetargetedLang("") != "" {
		t.Error("RetargetedLang")
	}
	// The spoken language changes again: back to the default.
	p.SourceLang = "en"
	if !p.AutoRetarget() || p.Primary() != "zh-Hans" || p.TargetSwitched != "" {
		t.Errorf("back: %v %q", p.Targets, p.TargetSwitched)
	}
	if p.RetargetedLang("zh-Hans") != "zh-Hans" {
		t.Error("RetargetedLang after switching back")
	}
}

func TestChosenTargetsAreKept(t *testing.T) {
	p := New("d", time.Now())
	p.ChooseTargets([]string{"zh-Hans"})
	p.Detected = "zh"
	if p.AutoRetarget() || p.Primary() != "zh-Hans" || p.TargetsAuto {
		t.Errorf("chosen: %v auto %v", p.Targets, p.TargetsAuto)
	}
	// Several defaulted targets are not switched either.
	q := New("d", time.Now())
	q.Targets = append(q.Targets, "fr")
	q.Detected = "zh"
	if q.AutoRetarget() {
		t.Error("switched one of several targets")
	}
	// Choosing clears an earlier switch.
	r := New("d", time.Now())
	r.Detected = "zh"
	r.AutoRetarget()
	r.ChooseTargets([]string{"ja"})
	if r.TargetsAuto || r.TargetSwitched != "" || r.RetargetedLang("zh-Hans") != "zh-Hans" {
		t.Errorf("after choosing: %+v", r)
	}
}

// Documents from before the default target keep their targets.
func TestOldProjectTargetsUnchanged(t *testing.T) {
	var p Project
	if err := json.Unmarshal([]byte(`{"version":1,"id":"old","targets":["es"],"detected":"es","nextSeg":1}`), &p); err != nil {
		t.Fatal(err)
	}
	p.Normalize()
	if p.TargetsAuto || p.AutoRetarget() || !slices.Equal(p.Targets, []string{"es"}) {
		t.Errorf("old project %v auto %v", p.Targets, p.TargetsAuto)
	}
	// And the flag round-trips.
	q := New("n", time.Now())
	q.Detected = "zh"
	q.AutoRetarget()
	data, _ := json.Marshal(q)
	var back Project
	json.Unmarshal(data, &back)
	if !back.TargetsAuto || back.TargetSwitched != "zh-Hans" || back.Primary() != "en" {
		t.Errorf("round trip %+v", back)
	}
}
