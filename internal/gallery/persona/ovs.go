package persona

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/lang"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// OVSExt is VoiceStudio's persona file extension.
const OVSExt = ".ovsvoice"

// ReadOVS imports a VoiceStudio .ovsvoice persona as a Bundle.
//
// VoiceStudio's public format notes (docs/persona-format.md) describe the
// members — manifest.json (format and schema version, identity,
// engine/design params, licence as license.spdx, tags, preview metadata),
// metadata.json (a legacy copy), preview.wav, consent.json (method,
// statement, verified flag, timestamp), ref_audio.* / locked_audio.*, and
// consent_audio.* — but not every field name. The identity fields are
// therefore looked up under the names VoiceStudio's profile API uses
// (name, ref_text, instruct, language, seed, kind, vd_states), in the
// manifest, nested objects and metadata.json, and anything not understood
// is reported in the warnings. Verify against a real exported bundle
// before relying on it.
//
// The result has a fresh-looking manifest (Format, SchemaVersion, ID from
// the source when usable); consent is carried over as an attestation only
// when the recording, the statement and consent.json are all present,
// and it is still the exporter's claim.
func ReadOVS(r io.ReaderAt, size int64, lim Limits) (*Bundle, []string, error) {
	c, err := openContainer(r, size, lim)
	if err != nil {
		return nil, nil, err
	}
	var warn []string
	docs := []map[string]any{}
	for _, name := range []string{"manifest.json", "metadata.json"} {
		if _, ok := c.files[name]; !ok {
			continue
		}
		raw, err := c.read(name, lim.MaxManifest)
		if err != nil {
			return nil, nil, err
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, name, err)
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return nil, nil, fmt.Errorf("%w: no manifest.json", ErrNotPersona)
	}
	look := func(keys ...string) any {
		for _, d := range docs {
			if v := find(d, keys, 0); v != nil {
				return v
			}
		}
		return nil
	}
	str := func(keys ...string) string {
		if s, ok := look(keys...).(string); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}

	m := Manifest{
		Format: Format, SchemaVersion: SchemaVersion,
		Name:        str("name", "display_name", "profile_name", "title"),
		Description: str("description"),
		Language:    str("language", "lang"),
		CreatedBy:   CreatedBy{App: "VoiceStudio", Version: str("app_version", "generator_version")},
	}
	m.Language = languageCode(m.Language)
	if id := str("id", "profile_id", "persona_id"); idPattern.MatchString(id) {
		m.ID = "ovs-" + id
	}
	if m.ID == "" || !idPattern.MatchString(m.ID) {
		m.ID = "ovs-" + sum([]byte(m.Name + "\x00" + fmt.Sprint(size)))[:16]
	}
	if m.Name == "" {
		m.Name = "Imported voice"
		warn = append(warn, "no name found")
	}
	if t, err := time.Parse(time.RFC3339, str("created_at", "exported_at", "createdAt")); err == nil {
		m.CreatedAt = t
	}
	if tags, ok := look("tags").([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok && s != "" && len(m.Tags) < MaxTags {
				m.Tags = append(m.Tags, s)
			}
		}
	}
	// licence: license.spdx (documented); a bare string as a fallback
	switch l := look("license", "licence").(type) {
	case map[string]any:
		if s, ok := l["spdx"].(string); ok {
			m.Licence.SPDX = s
		}
	case string:
		m.Licence.SPDX = l
	}
	if !ValidSPDX(m.Licence.SPDX) {
		if m.Licence.SPDX != "" {
			warn = append(warn, "licence "+strconv.Quote(m.Licence.SPDX)+" is not an SPDX id; using the personal licence")
		}
		m.Licence.SPDX = "LicenseRef-VoiceStudio-Personal"
	}
	if s, ok := toInt(look("seed")); ok && s >= 0 && s <= vd.MaxSeed {
		m.Engine.Seed = &s
	}
	m.Engine.Model = str("engine", "engine_id", "model")
	instruct := str("instruct")
	m.Engine.Voice = instruct

	// design attributes: vd_states (category picks; an object or a JSON
	// string), else the instruct tags
	attrs, unknown := designAttrs(look("vd_states"), instruct)
	if len(unknown) > 0 {
		warn = append(warn, "design details not understood: "+strings.Join(unknown, ", "))
	}

	b := &Bundle{}
	// members
	if _, ok := c.files["preview.wav"]; !ok {
		return nil, nil, fmt.Errorf("%w: no preview.wav", ErrCorrupt)
	}
	if b.Preview, err = c.read("preview.wav", lim.MaxPreview); err != nil {
		return nil, nil, err
	}
	if !isWAV(b.Preview) {
		return nil, nil, invalid("preview is not a WAV file")
	}
	names := make([]string, 0, len(c.files))
	for n := range c.files {
		names = append(names, n)
	}
	sort.Strings(names)
	var consentAudio []byte
	for _, prefix := range []string{"ref_audio.", "locked_audio.", "consent_audio."} {
		for _, n := range names {
			if !strings.HasPrefix(n, prefix) {
				continue
			}
			ext := strings.ToLower(strings.TrimPrefix(path.Ext(n), "."))
			if ext == "opus" {
				ext = "ogg"
			}
			max := lim.MaxReference
			if prefix == "consent_audio." {
				max = lim.MaxConsent
			}
			data, err := c.read(n, max)
			if err != nil {
				return nil, nil, err
			}
			if !sniffAudio(data, ext) {
				warn = append(warn, n+" skipped: not a "+ext+" file")
				continue
			}
			switch prefix {
			case "ref_audio.":
				b.Reference, b.ReferenceExt = data, ext
			case "locked_audio.":
				if b.Reference == nil { // a locked take stands in for a missing reference
					b.Reference, b.ReferenceExt = data, ext
				}
				m.Engine.Locked = true
			case "consent_audio.":
				if ext == "wav" {
					consentAudio = data
				} else {
					warn = append(warn, n+" skipped: the consent recording must be WAV")
				}
			}
			break
		}
	}
	if b.Reference != nil {
		m.Files.Reference = &FileRef{Text: str("ref_text", "transcript", "reference_text")}
	}

	switch k := strings.ToLower(str("kind")); {
	case k == "design" || (k == "" && b.Reference == nil && (!attrs.IsAuto() || instruct != "")):
		m.Kind = KindDesign
		seed := int64(42) // VoiceStudio renders design samples with seed 42
		if m.Engine.Seed != nil {
			seed = *m.Engine.Seed
		} else {
			warn = append(warn, "no seed; using 42")
		}
		m.Recipe = &vd.Recipe{Attrs: attrs, Seed: seed}
		if attrs.IsAuto() && instruct != "" {
			m.Recipe.Description = instruct
		}
	default:
		m.Kind = KindClone
		if b.Reference == nil {
			warn = append(warn, "no reference clip (preview-only bundle): the preview becomes the reference")
		}
	}

	// consent: all three parts, as VoiceStudio requires
	if raw, err := c.readOptional("consent.json", lim.MaxManifest); err != nil {
		return nil, nil, err
	} else if raw != nil {
		var cj map[string]any
		if json.Unmarshal(raw, &cj) == nil {
			statement := ""
			for _, k := range []string{"statement", "consent_text", "text"} {
				if s, ok := cj[k].(string); ok && strings.TrimSpace(s) != "" {
					statement = strings.TrimSpace(s)
					break
				}
			}
			method, _ := cj["method"].(string)
			if statement != "" && consentAudio != nil {
				m.Consent = &Consent{Attested: true, Method: "recording", Statement: statement, AttestedBy: method}
				if ts, ok := cj["timestamp"].(string); ok {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						m.Consent.AttestedAt = t
					}
				}
				b.ConsentAudio = consentAudio
			} else {
				warn = append(warn, "consent record incomplete; imported as unverified")
			}
		}
	}

	b.Manifest = m
	if err := m.Validate(); err != nil {
		return nil, warn, err
	}
	return b, warn, nil
}

// languageCode maps "English", "en-US" or "Auto" to a BCP-47 primary
// subtag ("" when unknown or Auto).
func languageCode(s string) string {
	if s == "" || strings.EqualFold(s, "auto") {
		return ""
	}
	if c := lang.Canonical(s); c != "" {
		return c
	}
	if hits := lang.Search(s); len(hits) > 0 && lang.Fold(hits[0].Name) == lang.Fold(s) {
		return hits[0].Code
	}
	return ""
}

func (c *container) readOptional(name string, max int64) ([]byte, error) {
	if _, ok := c.files[name]; !ok {
		return nil, nil
	}
	return c.read(name, max)
}

// find looks for the first of keys in d, then in nested objects (depth ≤ 3).
func find(d map[string]any, keys []string, depth int) any {
	for _, k := range keys {
		if v, ok := d[k]; ok && v != nil {
			return v
		}
	}
	if depth >= 3 {
		return nil
	}
	nested := make([]string, 0, len(d))
	for k, v := range d {
		if _, ok := v.(map[string]any); ok {
			nested = append(nested, k)
		}
	}
	sort.Strings(nested) // deterministic
	for _, k := range nested {
		if v := find(d[k].(map[string]any), keys, depth+1); v != nil {
			return v
		}
	}
	return nil
}

func toInt(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) {
			return int64(x), true
		}
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	}
	return 0, false
}

// designAttrs maps VoiceStudio's design picks to attributes. The picks are
// OmniVoice tags (category → tag); joining them and parsing as a tag
// string keeps us independent of the category key names.
func designAttrs(states any, instruct string) (vd.Attributes, []string) {
	var tags []string
	if s, ok := states.(string); ok && strings.HasPrefix(strings.TrimSpace(s), "{") {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			states = m
		}
	}
	if m, ok := states.(map[string]any); ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := m[k].(string); ok && s != "" && !strings.EqualFold(s, "auto") {
				tags = append(tags, s)
			}
		}
	}
	if len(tags) == 0 && instruct != "" {
		tags = []string{instruct}
	}
	return vd.ParseOmniVoiceTags(strings.Join(tags, ","))
}
