package settings

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMigrate(t *testing.T) {
	s, err := Migrate(nil)
	if err != nil || !reflect.DeepEqual(s, Defaults()) {
		t.Fatalf("empty: %+v %v", s, err)
	}
	// An older/partial document keeps what it has and gains defaults.
	s, err = Migrate([]byte(`{"version":1,"general":{"language":"zh-Hans"},"studio":{"autoplay":false},"unknown":1}`))
	if err != nil || s.General.Language != "zh-Hans" || s.Studio.Autoplay || s.Appearance.Scale != 1 || s.Appearance.Theme != "system" {
		t.Fatalf("partial: %+v %v", s, err)
	}
	data, _ := json.Marshal(s)
	again, err := Migrate(data)
	if err != nil || !reflect.DeepEqual(again, s) {
		t.Fatalf("round trip: %+v %v", again, err)
	}
	if _, err := Migrate([]byte(`{`)); err == nil {
		t.Error("bad JSON accepted")
	}
}
