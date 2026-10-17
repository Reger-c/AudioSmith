package family

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	ass "github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/actnowkit/conformance"
	"github.com/hehehai/actnowkit/models"
	famset "github.com/hehehai/actnowkit/settings"
)

// sharedIn is AudioSmith started in the fixture with own settings (fn
// changes the defaults first) and its Shared, not started yet. hasModels
// says whether AudioSmith has models in a folder.
type sharedApp struct {
	*testFamily
	own *ass.Store
	sh  *Shared
	ctx context.Context
}

func newShared(t *testing.T, fx *conformance.Fixture, hasModels func(string) bool, fn func(*ass.Settings)) *sharedApp {
	t.Helper()
	f := newFamily(t, fx, "")
	ctx := context.Background()
	if err := f.Start(ctx, inputs(fx.Hub), wantedOf()); err != nil {
		t.Fatal(err)
	}
	own := ass.NewMemory()
	if fn != nil {
		if err := own.Update(ctx, fn); err != nil {
			t.Fatal(err)
		}
	}
	sh := NewShared(f.Family, SharedOptions{Own: own, DefaultHub: func() string { return fx.Hub }, HasModels: hasModels})
	return &sharedApp{f, own, sh, ctx}
}

// set is the user changing AudioSmith's settings: the app hands the
// change to Shared (settings.Changed).
func (a *sharedApp) set(t *testing.T, fn func(*ass.Settings)) {
	t.Helper()
	old := a.own.Get()
	if err := a.own.Update(a.ctx, fn); err != nil {
		t.Fatal(err)
	}
	if err := a.sh.Changed(a.ctx, old, a.own.Get()); err != nil {
		t.Fatal(err)
	}
}

func (a *sharedApp) adopt(t *testing.T) ass.Settings {
	t.Helper()
	old := a.own.Get()
	if err := a.sh.Adopt(a.ctx); err != nil {
		t.Fatal(err)
	}
	// What the app does with the change Adopt made.
	if err := a.sh.Changed(a.ctx, old, a.own.Get()); err != nil {
		t.Fatal(err)
	}
	return a.own.Get()
}

func famValue(t *testing.T, fx *conformance.Fixture, key string) (string, bool) {
	t.Helper()
	s, _, err := famset.Read(fx.Family)
	if err != nil {
		t.Fatal(err)
	}
	return s.Get(key)
}

func peerSet(t *testing.T, fx *conformance.Fixture, key, value string) {
	t.Helper()
	if _, err := famset.Set(context.Background(), fx.Family, auralinkID, key, value); err != nil {
		t.Fatal(err)
	}
}

// The migration's branches (spec §9.4): the family has no value (a
// default stays unset, the user's value becomes the family's), the same
// value, another value with AudioSmith at its default (adopted), another
// value with the user's own (kept, follow off, the row says so once).
func TestSharedMigrate(t *testing.T) {
	fx := conformance.New(t)
	peerSet(t, fx, famset.KeyProxy, "http://127.0.0.1:7897") // same as AudioSmith's
	peerSet(t, fx, famset.KeyHFEndpoints, famset.EndpointsMirror)
	peerSet(t, fx, famset.KeyLanguage, "zh-Hans")
	a := newShared(t, fx, nil, func(s *ass.Settings) {
		s.Models.Proxy = "http://127.0.0.1:7897"
		s.General.Language = "en" // the user's own
		// the endpoints at the default: the family's mirror is adopted
	})
	if err := a.sh.Start(a.ctx); err != nil {
		t.Fatal(err)
	}
	s := a.own.Get()
	if !s.Family.SharedMigrated {
		t.Error("not marked migrated")
	}
	for key, follow := range map[string]bool{famset.KeyProxy: true, famset.KeyHFEndpoints: true, famset.KeyLanguage: false, famset.KeyModelsRoot: true} {
		if s.Family.Follows(key) != follow {
			t.Errorf("%s: follows %v, want %v", key, s.Family.Follows(key), follow)
		}
	}
	if !slices.Equal(s.Models.Endpoints, []string{famset.MirrorEndpoint}) {
		t.Errorf("endpoints %v: the family's mirror not adopted", s.Models.Endpoints)
	}
	if s.General.Language != "en" || !s.Family.OtherValue[famset.KeyLanguage] || len(s.Family.OtherValue) != 1 {
		t.Errorf("language %q, other values %v", s.General.Language, s.Family.OtherValue)
	}
	if v, ok := a.sh.View().Conflict(s, famset.KeyLanguage); !ok || v != "zh-Hans" {
		t.Errorf("conflict %q %v", v, ok)
	}
	if _, ok := famValue(t, fx, famset.KeyModelsRoot); ok {
		t.Error("AudioSmith's default model folder was written to the family")
	}
	if v, _ := famValue(t, fx, famset.KeyLanguage); v != "zh-Hans" {
		t.Errorf("the family's language became %q", v)
	}
	// Once: a second start changes nothing.
	before := a.own.Get()
	if err := a.sh.Start(a.ctx); err != nil {
		t.Fatal(err)
	}
	if after := a.own.Get(); after.General.Language != before.General.Language || after.Family.Follows(famset.KeyLanguage) {
		t.Errorf("second start: %+v", after.Family)
	}

	// The family has no value: the user's proxy goes there.
	fx2 := conformance.New(t)
	b := newShared(t, fx2, nil, func(s *ass.Settings) { s.Models.Proxy = "socks5://127.0.0.1:1080" })
	if err := b.sh.Start(b.ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := famValue(t, fx2, famset.KeyProxy); v != "socks5://127.0.0.1:1080" {
		t.Errorf("family proxy %q", v)
	}
	if _, ok := famValue(t, fx2, famset.KeyLanguage); ok {
		t.Error("a default language was written")
	}
	if s := b.own.Get(); len(s.Family.Follow) != 0 || len(s.Family.OtherValue) != 0 {
		t.Errorf("follow %v, other %v", s.Family.Follow, s.Family.OtherValue)
	}
}

// models.root (spec §9.4 "in use", §15.2 step 5): AudioSmith on the
// default folder with its models there keeps it when the family names
// another (follow off, the Models page offers the move); without models
// there it follows the family's.
func TestSharedMigrateModelsRoot(t *testing.T) {
	for _, has := range []bool{true, false} {
		fx := conformance.New(t)
		other := filepath.Join(t.TempDir(), "models")
		peerSet(t, fx, famset.KeyModelsRoot, other)
		a := newShared(t, fx, func(hub string) bool { return has && hub == fx.Hub }, nil)
		if err := a.sh.Start(a.ctx); err != nil {
			t.Fatal(err)
		}
		s := a.own.Get()
		switch {
		case has && (s.Family.Follows(famset.KeyModelsRoot) || !s.Family.OtherValue[famset.KeyModelsRoot] || s.Models.CacheDir != ""):
			t.Errorf("models in the default folder: %+v, cache %q", s.Family, s.Models.CacheDir)
		case !has && (!s.Family.Follows(famset.KeyModelsRoot) || s.Models.CacheDir != other):
			t.Errorf("no models: %+v, cache %q", s.Family, s.Models.CacheDir)
		}
	}
	// A custom folder of AudioSmith's, the family has none: it becomes the family's.
	fx := conformance.New(t)
	mine := filepath.Join(t.TempDir(), "mine")
	a := newShared(t, fx, nil, func(s *ass.Settings) { s.Models.CacheDir = mine + "/" })
	if err := a.sh.Start(a.ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := famValue(t, fx, famset.KeyModelsRoot); v != mine {
		t.Errorf("family root %q, want %q", v, mine)
	}
}

// Another app's changes reach AudioSmith's settings while they follow
// (Adopt), and are not written back; AudioSmith's own changes reach the
// family; "Use a different setting for this app" stops both ways; follow
// again takes the family's value.
func TestSharedFollow(t *testing.T) {
	fx := conformance.New(t)
	a := newShared(t, fx, nil, nil)
	if err := a.sh.Start(a.ctx); err != nil {
		t.Fatal(err)
	}
	peerSet(t, fx, famset.KeyLanguage, "zh-Hans")
	peerSet(t, fx, famset.KeyHFEndpoints, "https://a.example/ https://b.example")
	rev := readRev(t, fx)
	s := a.adopt(t)
	if s.General.Language != "zh-Hans" || !slices.Equal(s.Models.Endpoints, []string{"https://a.example", "https://b.example"}) {
		t.Errorf("adopted %q %v", s.General.Language, s.Models.Endpoints)
	}
	if readRev(t, fx) != rev {
		t.Error("adopted values were written back to the family")
	}
	// The user picks the official hub in AudioSmith.
	a.set(t, func(s *ass.Settings) { s.Models.Endpoints = []string{"https://huggingface.co/"} })
	if v, _ := famValue(t, fx, famset.KeyHFEndpoints); v != famset.EndpointsOfficial {
		t.Errorf("family endpoints %q", v)
	}
	// Stop following the proxy.
	a.set(t, func(s *ass.Settings) { s.Family.SetFollows(famset.KeyProxy, false) })
	a.set(t, func(s *ass.Settings) { s.Models.Proxy = "direct" })
	if _, ok := famValue(t, fx, famset.KeyProxy); ok {
		t.Error("AudioSmith's own proxy reached the family")
	}
	peerSet(t, fx, famset.KeyProxy, "http://127.0.0.1:7897")
	if s := a.adopt(t); s.Models.Proxy != "direct" {
		t.Errorf("proxy %q: followed while not following", s.Models.Proxy)
	}
	if v, ok := a.sh.View().Conflict(a.own.Get(), famset.KeyProxy); !ok || v != "http://127.0.0.1:7897" {
		t.Errorf("conflict %q %v", v, ok)
	}
	// Follow again: the family's value.
	a.set(t, func(s *ass.Settings) { s.Family.SetFollows(famset.KeyProxy, true) })
	if s := a.own.Get(); s.Models.Proxy != "http://127.0.0.1:7897" {
		t.Errorf("follow again: proxy %q", s.Models.Proxy)
	}
	if a.sh.Effective(famset.KeyProxy) != "http://127.0.0.1:7897" {
		t.Error("Effective")
	}
}

func readRev(t *testing.T, fx *conformance.Fixture) int {
	t.Helper()
	s, _, err := famset.Read(fx.Family)
	if err != nil {
		t.Fatal(err)
	}
	return s.Rev
}

// A family folder AudioSmith follows changes while its models are in the
// current one: it stays and offers the move; without models it follows.
func TestSharedAdoptModelsRoot(t *testing.T) {
	fx := conformance.New(t)
	has := true
	a := newShared(t, fx, func(string) bool { return has }, nil)
	if err := a.sh.Start(a.ctx); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.Join(t.TempDir(), "f1")
	peerSet(t, fx, famset.KeyModelsRoot, f1)
	s := a.adopt(t)
	if s.Models.CacheDir != "" || s.Family.Follows(famset.KeyModelsRoot) || !s.Family.OtherValue[famset.KeyModelsRoot] {
		t.Errorf("with models: cache %q, %+v", s.Models.CacheDir, s.Family)
	}
	// "Keep separate" forgets the prompt; following again with no models
	// takes the family's folder.
	a.set(t, func(s *ass.Settings) { delete(s.Family.OtherValue, famset.KeyModelsRoot) })
	has = false
	a.set(t, func(s *ass.Settings) { s.Family.SetFollows(famset.KeyModelsRoot, true) })
	if s := a.own.Get(); s.Models.CacheDir != f1 || !s.Family.Follows(famset.KeyModelsRoot) {
		t.Errorf("follow again: cache %q, %+v", s.Models.CacheDir, s.Family)
	}
}

func TestEndpointsMapping(t *testing.T) {
	for _, c := range []struct {
		eps  []string
		want string
	}{
		{nil, "auto"},
		{[]string{}, "auto"},
		{[]string{"https://huggingface.co"}, "official"},
		{[]string{"https://huggingface.co/"}, "official"},
		{[]string{"https://hf-mirror.com"}, "mirror"},
		{[]string{"https://huggingface.co", "https://hf-mirror.com"}, "https://huggingface.co https://hf-mirror.com"},
		{[]string{"https://m.example/", " https://n.example"}, "https://m.example https://n.example"},
	} {
		got := EndpointsValue(c.eps)
		if got != c.want {
			t.Errorf("EndpointsValue(%q) = %q, want %q", c.eps, got, c.want)
		}
		back := EndpointsList(got)
		var norm []string
		for _, e := range c.eps {
			norm = append(norm, trimURL(e))
		}
		if !slices.Equal(back, norm) && !(len(back) == 0 && len(norm) == 0) {
			t.Errorf("EndpointsList(%q) = %q, want %q", got, back, norm)
		}
	}
	if got := EndpointsList(""); got != nil {
		t.Errorf("EndpointsList(\"\") = %q", got)
	}
	if !sameValue(famset.KeyHFEndpoints, "auto", "https://huggingface.co https://hf-mirror.com") {
		t.Error("auto is the race of both")
	}
}

func trimURL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '/' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	return s
}

// A settings.json of a newer major: nothing is read into AudioSmith's
// settings, nothing written.
func TestSharedReadOnly(t *testing.T) {
	fx := conformance.New(t)
	v2, err := conformance.File("settings-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx.Family.SettingsFile(), v2, 0o600); err != nil {
		t.Fatal(err)
	}
	a := newShared(t, fx, nil, func(s *ass.Settings) { s.Models.Proxy = "direct" })
	if err := a.sh.Start(a.ctx); err != ErrReadOnly {
		t.Errorf("Start: %v", err)
	}
	a.set(t, func(s *ass.Settings) { s.General.Language = "en" })
	if data, _ := os.ReadFile(fx.Family.SettingsFile()); string(data) != string(v2) {
		t.Errorf("rewritten:\n%s", data)
	}
	if a.own.Get().Family.SharedMigrated {
		t.Error("migrated over a newer format")
	}
}

// The name of the app that last wrote settings.json, when a release
// build of it is installed.
func TestSharedViewNamesTheWriter(t *testing.T) {
	fx := conformance.New(t)
	a := newShared(t, fx, nil, nil)
	fx.Register(auralinkID)
	a.Rediscover()
	if err := a.sh.Start(a.ctx); err != nil {
		t.Fatal(err)
	}
	peerSet(t, fx, famset.KeyProxy, "direct")
	a.adopt(t)
	if v := a.sh.View(); v.By[famset.KeyProxy] != "Auralink" || v.Values[famset.KeyProxy] != "direct" {
		t.Errorf("view %+v", v)
	}
	// AudioSmith's own value is nobody's to name.
	a.set(t, func(s *ass.Settings) { s.Models.Proxy = "http://127.0.0.1:1" })
	if v := a.sh.View(); v.By[famset.KeyProxy] != "" {
		t.Errorf("by %q after AudioSmith set it", v.By[famset.KeyProxy])
	}
}

var _ = models.Ref{}
