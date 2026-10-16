package main

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// component is one entry of the file.
type component struct {
	name    string // e.g. "golang.org/x/text", "mlx-swift"
	version string // a version, or a revision for unversioned pins
	url     string // where its source is
	spdx    string // its licence, an SPDX expression
	notes   []string
	texts   []text
}

// text is a licence or notice text of a component, as its authors
// distribute it.
type text struct {
	name string // its file within the component, e.g. "LICENSE", "harfbuzz/LICENSE"
	body string
}

// section is a group of components.
type section struct {
	title string
	comps []component
}

// validate checks a component is complete and that the licence its main
// text reads as is in its SPDX expression (a new version that changed its
// licence fails here, not in review).
func (c component) validate() error {
	if c.name == "" || c.version == "" || c.url == "" || c.spdx == "" {
		return fmt.Errorf("%s: name, version, URL and licence are all needed (%q %q %q)", c.name, c.version, c.url, c.spdx)
	}
	if len(c.texts) == 0 {
		return fmt.Errorf("%s: no licence text", c.name)
	}
	ids := spdxIDs(c.spdx)
	for _, id := range classify(c.texts[0].body) {
		if !slices.Contains(ids, id) {
			return fmt.Errorf("%s: %s reads as %s, but its licence is given as %q: check the new version's licence and update its line in internal/tools/thirdparty/tables.go (or COMPONENT)", c.name, c.texts[0].name, id, c.spdx)
		}
	}
	return nil
}

// spdxIDs are the licence identifiers of an SPDX expression.
func spdxIDs(expr string) []string {
	var ids []string
	for _, f := range strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(expr)) {
		switch f {
		case "AND", "OR", "WITH":
			continue
		}
		ids = append(ids, f)
	}
	return ids
}

var (
	commentMarks = regexp.MustCompile(`(?m)^[ \t]*(//+|/\*+!?|\*+/|\*|#+)[ \t]?`)
	spaces       = regexp.MustCompile(`\s+`)
)

// flatten is a text as one line of words, without comment marks: for
// matching licences across their layouts.
func flatten(s string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(commentMarks.ReplaceAllString(s, ""), " "))
}

// classify names the common licences a text contains, from the sentences
// that set them apart. It is a cross-check, not a licence scanner.
func classify(body string) []string {
	s := strings.ToLower(flatten(body))
	var ids []string
	add := func(id string) {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if strings.Contains(s, "apache license") && (strings.Contains(s, "version 2.0") || strings.Contains(s, "license-2.0")) {
		add("Apache-2.0")
	}
	if strings.Contains(s, "permission is hereby granted, free of charge") {
		if strings.Contains(s, "shall be included in all copies") {
			add("MIT")
		} else {
			add("MIT-0")
		}
	}
	if strings.Contains(s, "redistribution and use in source and binary forms") {
		if strings.Contains(s, "endorse or promote products") {
			add("BSD-3-Clause")
		} else {
			add("BSD-2-Clause")
		}
	}
	if strings.Contains(s, "permission to use, copy, modify, and/or distribute this software for any purpose") {
		add("ISC")
	}
	if strings.Contains(s, "this is free and unencumbered software released into the public domain") {
		add("Unlicense")
	}
	if strings.Contains(s, "the origin of this software must not be misrepresented") {
		add("Zlib")
	}
	return ids
}

// cleanText normalises a licence text's line ends and trailing spaces,
// so the output is the same on every machine.
func cleanText(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	// Leading and trailing blank lines.
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

const (
	rule     = "================================================================================"
	thinRule = "--------------------------------------------------------------------------------"
)

// render writes the file. A text identical (but for spacing) to one
// printed before is not repeated: it names the entry that has it.
func render(sections []section) []byte {
	var b bytes.Buffer
	n := 0
	for _, s := range sections {
		n += len(s.comps)
	}
	b.WriteString(header)
	fmt.Fprintf(&b, "\nComponents (%d)\n", n)
	i := 0
	for _, s := range sections {
		fmt.Fprintf(&b, "\n  %s\n", s.title)
		for _, c := range s.comps {
			i++
			fmt.Fprintf(&b, "  %4s %s %s — %s\n", fmt.Sprintf("[%d]", i), c.name, c.version, c.spdx)
		}
	}
	seen := map[string]string{} // flattened text → where it was printed
	i = 0
	for _, s := range sections {
		fmt.Fprintf(&b, "\n\n%s\n%s\n%s\n", rule, strings.ToUpper(s.title), rule)
		for _, c := range s.comps {
			i++
			fmt.Fprintf(&b, "\n%s\n[%d] %s %s\n", rule, i, c.name, c.version)
			fmt.Fprintf(&b, "    Source:  %s\n", c.url)
			fmt.Fprintf(&b, "    Licence: %s\n", c.spdx)
			for _, note := range c.notes {
				fmt.Fprintf(&b, "    Note:    %s\n", note)
			}
			for _, t := range c.texts {
				label := t.name
				if ids := classify(t.body); len(ids) > 0 {
					label += " — " + strings.Join(ids, ", ")
				}
				fmt.Fprintf(&b, "%s\n%s\n\n", thinRule, label)
				key := flatten(t.body)
				if where, ok := seen[key]; ok {
					fmt.Fprintf(&b, "[The same text as %s above.]\n", where)
					continue
				}
				seen[key] = fmt.Sprintf("%s in [%d] %s", t.name, i, c.name)
				b.WriteString(cleanText(t.body))
			}
		}
	}
	return b.Bytes()
}

const header = `AudioSmith — third-party notices
================================

AudioSmith is free software under the GNU Affero General Public License,
version 3 (LICENSE; NOTICE says more). It includes the third-party software
and material listed below. For each: its version or pin, where its source
is, its licence as an SPDX identifier, and the full licence and notice texts
as its authors distribute them. The models AudioSmith downloads are not part
of it: each keeps its own licence, shown on the Models page.

This file is generated by scripts/third-party-notices.sh (go run
./internal/tools/thirdparty) from go.mod, engine/Package.resolved,
scripts/fetch-engine-deps.sh and third_party/licenses. Do not edit it by
hand: regenerate it.
`
