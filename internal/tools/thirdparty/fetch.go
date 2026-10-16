package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// fetchSwift replaces third_party/licenses/swift with the licence texts of
// every pinned Swift package and of its git submodules, at their pins:
// the licence and notice files (but those of tests, docs and examples) and
// the licence comments of tables.go's swiftExcerpts. It needs git and the
// network; it fetches trees, and only the blobs it reads.
func fetchSwift(root string) error {
	pins, err := swiftPins(root)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "audiosmith-thirdparty-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(root, swiftDir)
	staging := out + ".new"
	os.RemoveAll(staging)
	for len(pins) > 0 {
		p := pins[0]
		pins = pins[1:]
		fmt.Fprintf(os.Stderr, "fetching %s %s\n", p.name, p.revision[:12])
		subs, err := fetchPackage(filepath.Join(tmp, p.name), filepath.Join(staging, p.name), p)
		if err != nil {
			os.RemoveAll(staging)
			return fmt.Errorf("%s: %v", p.name, err)
		}
		pins = append(pins, subs...)
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	return os.Rename(staging, out)
}

// fetchPackage writes one package's texts and PIN into dest and returns
// its submodules.
func fetchPackage(repo, dest string, p swiftPin) ([]swiftPin, error) {
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return nil, err
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", p.url},
		{"fetch", "-q", "--depth", "1", "--filter=blob:none", "origin", p.revision},
	} {
		if _, err := git(args...); err != nil {
			return nil, err
		}
	}
	tree, err := git("ls-tree", "-r", "-z", "FETCH_HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	gitlinks := map[string]string{} // path → commit
	for _, entry := range strings.Split(strings.TrimRight(string(tree), "\x00"), "\x00") {
		meta, file, ok := strings.Cut(entry, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			continue
		}
		switch f[1] {
		case "commit":
			gitlinks[file] = f[2]
		case "blob":
			if isLicenceFile(path.Base(file)) && !skippedPath(file) {
				files = append(files, file)
			}
		}
	}
	show := func(file string) (string, error) {
		out, err := git("show", "FETCH_HEAD:"+file)
		return string(out), err
	}
	write := func(name, body string) error {
		target := filepath.Join(dest, "files", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, []byte(cleanText(body)), 0o644)
	}
	for _, file := range files {
		body, err := show(file)
		if err != nil {
			return nil, err
		}
		if err := write(file, body); err != nil {
			return nil, err
		}
	}
	for _, ex := range swiftExcerpts[p.name] {
		body, err := show(ex.path)
		if err != nil {
			return nil, err
		}
		cut, err := ex.cut(body)
		if err != nil {
			return nil, fmt.Errorf("%s: %v (update swiftExcerpts in internal/tools/thirdparty/tables.go)", ex.path, err)
		}
		if err := write(ex.path+headerSuffix, cut); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(dest, "PIN"), []byte(p.pinFile()), 0o644); err != nil {
		return nil, err
	}
	if len(gitlinks) == 0 {
		return nil, nil
	}
	modules, err := show(".gitmodules")
	if err != nil {
		return nil, err
	}
	urls := submoduleURLs(modules)
	var subs []swiftPin
	for _, sub := range sortedKeys(gitlinks) {
		url, ok := urls[sub]
		if !ok {
			return nil, fmt.Errorf("submodule %s is not in .gitmodules", sub)
		}
		subs = append(subs, swiftPin{name: path.Base(sub), url: url, revision: gitlinks[sub], parent: p.name, parentRevision: p.revision, path: sub})
	}
	return subs, nil
}

// skippedPath tells the files of tests, docs, examples and benchmarks,
// which are not built into the engine.
func skippedPath(file string) bool {
	parts := strings.Split(strings.ToLower(file), "/")
	for _, d := range parts[:len(parts)-1] {
		switch d {
		case "test", "tests", "doc", "docs", "documentation", "example", "examples", "benchmark", "benchmarks", "integrationtesting", "fixtures", ".github":
			return true
		}
	}
	return false
}

// submoduleURLs maps the paths of .gitmodules to their URLs.
func submoduleURLs(gitmodules string) map[string]string {
	urls := map[string]string{}
	var cur string
	for _, line := range strings.Split(gitmodules, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "path":
			cur = strings.TrimSpace(v)
		case "url":
			if cur != "" {
				urls[cur] = strings.TrimSpace(v)
			}
		}
	}
	return urls
}

// excerpt is a licence comment in a source file: from the line that has
// from to the first line after it that has to, both included.
type excerpt struct {
	path, from, to string
}

func (e excerpt) cut(body string) (string, error) {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if !strings.Contains(l, e.from) {
			continue
		}
		for j := i; j < len(lines); j++ {
			if strings.Contains(lines[j], e.to) {
				return strings.Join(lines[i:j+1], "\n"), nil
			}
		}
		return "", fmt.Errorf("no line with %q after %q", e.to, e.from)
	}
	return "", fmt.Errorf("no line with %q", e.from)
}
