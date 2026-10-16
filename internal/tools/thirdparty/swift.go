package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// swiftDir holds the Swift packages' licence texts, one directory per
// package: PIN (where and at which revision they were fetched) and the
// files under files/, at their paths in the package.
const swiftDir = "third_party/licenses/swift"

// headerSuffix marks a licence comment cut from a source file (excerpt).
const headerSuffix = ".header.txt"

// swiftPin is a Swift package the engine builds against.
type swiftPin struct {
	name, url, revision, version string
	// For a git submodule: the package that has it, at which revision,
	// and its path there.
	parent, parentRevision, path string
}

// swiftPins are engine/Package.resolved's pins and the mlx-audio-swift
// commit of scripts/fetch-engine-deps.sh, sorted by name.
func swiftPins(root string) ([]swiftPin, error) {
	data, err := os.ReadFile(filepath.Join(root, "engine", "Package.resolved"))
	if err != nil {
		return nil, err
	}
	var resolved struct {
		Pins []struct {
			Identity string `json:"identity"`
			Location string `json:"location"`
			State    struct {
				Revision string `json:"revision"`
				Version  string `json:"version"`
			} `json:"state"`
		} `json:"pins"`
	}
	if err := json.Unmarshal(data, &resolved); err != nil {
		return nil, fmt.Errorf("engine/Package.resolved: %v", err)
	}
	var pins []swiftPin
	for _, p := range resolved.Pins {
		pins = append(pins, swiftPin{name: p.Identity, url: p.Location, revision: p.State.Revision, version: p.State.Version})
	}
	script, err := os.ReadFile(filepath.Join(root, "scripts", "fetch-engine-deps.sh"))
	if err != nil {
		return nil, err
	}
	up := regexp.MustCompile(`(?m)^UPSTREAM="([^"]+)"`).FindSubmatch(script)
	commit := regexp.MustCompile(`(?m)^COMMIT="([0-9a-f]{40})"`).FindSubmatch(script)
	if up == nil || commit == nil {
		return nil, fmt.Errorf("scripts/fetch-engine-deps.sh: no UPSTREAM or COMMIT line")
	}
	pins = append(pins, swiftPin{name: "mlx-audio-swift", url: string(up[1]), revision: string(commit[1])})
	slices.SortFunc(pins, func(a, b swiftPin) int { return strings.Compare(a.name, b.name) })
	return pins, nil
}

// swiftComponents reads the committed texts of every pinned package and
// of their submodules, and checks they were fetched at today's pins.
func swiftComponents(root string) ([]component, error) {
	pins, err := swiftPins(root)
	if err != nil {
		return nil, err
	}
	stored, err := readPins(filepath.Join(root, swiftDir))
	if err != nil {
		return nil, err
	}
	stale := func(what string) error {
		return fmt.Errorf("%s: the Swift licence texts in %s are not those of today's pins: run %s --fetch", what, swiftDir, regenerate)
	}
	want := map[string]swiftPin{}
	for _, p := range pins {
		want[p.name] = p
		s, ok := stored[p.name]
		if !ok || s.revision != p.revision || s.url != p.url {
			return nil, stale(p.name + " " + p.revision)
		}
	}
	var out []component
	for _, name := range sortedKeys(stored) {
		s := stored[name]
		if s.parent != "" {
			if p, ok := want[s.parent]; !ok || p.revision != s.parentRevision {
				return nil, stale(name + " (in " + s.parent + ")")
			}
		} else if _, ok := want[name]; !ok {
			return nil, stale(name + " is no longer pinned")
		}
		c, err := swiftComponent(root, s)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// swiftComponent is a package's entry, from its directory.
func swiftComponent(root string, p swiftPin) (component, error) {
	spdx, ok := swiftLicences[p.name]
	if !ok {
		return component{}, fmt.Errorf("Swift package %s has no licence in swiftLicences (internal/tools/thirdparty/tables.go): read its licence and add it", p.name)
	}
	version := p.version
	if version == "" {
		version = "revision " + p.revision[:12]
	}
	url := strings.TrimSuffix(p.url, ".git")
	if strings.HasPrefix(url, "https://github.com/") {
		url += "/tree/" + p.revision
	}
	c := component{name: p.name, version: version, url: url, spdx: spdx}
	if p.parent != "" {
		c.notes = append(c.notes, fmt.Sprintf("A git submodule of %s (%s), compiled into it.", p.parent, p.path))
	}
	c.notes = append(c.notes, swiftNotes[p.name]...)
	dir := filepath.Join(root, swiftDir, p.name, "files")
	var files, headers []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, headerSuffix) {
			headers = append(headers, rel)
		} else {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return component{}, err
	}
	// The package's own licence first, then the rest by path.
	slices.SortFunc(files, func(a, b string) int {
		ta, tb := !strings.Contains(a, "/"), !strings.Contains(b, "/")
		if ta != tb {
			if ta {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	slices.Sort(headers)
	for _, rel := range append(files, headers...) {
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return component{}, err
		}
		name := rel
		if h, ok := strings.CutSuffix(rel, headerSuffix); ok {
			name = h + ", its licence comment"
		}
		c.texts = append(c.texts, text{name: name, body: string(body)})
	}
	return c, nil
}

// readPins reads the PIN file of every package directory.
func readPins(dir string) (map[string]swiftPin, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%v: run %s --fetch", err, regenerate)
	}
	pins := map[string]swiftPin{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := readPin(filepath.Join(dir, e.Name(), "PIN"))
		if err != nil {
			return nil, err
		}
		if p.name != e.Name() {
			return nil, fmt.Errorf("%s/%s/PIN names %q", dir, e.Name(), p.name)
		}
		pins[p.name] = p
	}
	return pins, nil
}

func readPin(path string) (swiftPin, error) {
	f, err := os.Open(path)
	if err != nil {
		return swiftPin{}, err
	}
	defer f.Close()
	var p swiftPin
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), " ")
		switch k {
		case "name":
			p.name = v
		case "url":
			p.url = v
		case "revision":
			p.revision = v
		case "version":
			p.version = v
		case "parent":
			f := strings.Fields(v)
			if len(f) != 3 {
				return p, fmt.Errorf("%s: parent wants NAME REVISION PATH", path)
			}
			p.parent, p.parentRevision, p.path = f[0], f[1], f[2]
		}
	}
	if p.name == "" || p.url == "" || len(p.revision) != 40 {
		return p, fmt.Errorf("%s: name, url and a 40-hex revision are needed", path)
	}
	return p, sc.Err()
}

// pinFile is the text of a PIN file.
func (p swiftPin) pinFile() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by %s --fetch: the licence texts in files/ are those of\n# this package at this revision. Do not edit.\n", regenerate)
	fmt.Fprintf(&b, "name %s\nurl %s\nrevision %s\n", p.name, p.url, p.revision)
	if p.version != "" {
		fmt.Fprintf(&b, "version %s\n", p.version)
	}
	if p.parent != "" {
		fmt.Fprintf(&b, "parent %s %s %s\n", p.parent, p.parentRevision, p.path)
	}
	return b.String()
}
