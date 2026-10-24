package main

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cleanPartFiles removes the temporary files of writes that never
// finished ("."+name+".*.part": a recording or an import cut short by a
// crash or a quit, the engine's atomic writes of a job it was stopped in)
// from the folders given and every folder below them, those older than
// minAge (a write in progress is younger). They were never cleaned
// before (family audit A13); below the first level, not until #101 L3
// (cache chunks, dub renders).
func cleanPartFiles(minAge time.Duration, dirs ...string) int {
	n := 0
	for _, root := range dirs {
		if root == "" {
			continue
		}
		filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable: skipped
			}
			name := e.Name()
			if e.IsDir() {
				if path != root && strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !e.Type().IsRegular() || !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".part") {
				return nil
			}
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > minAge && os.Remove(path) == nil {
				n++
			}
			return nil
		})
	}
	if n > 0 {
		log.Printf("data: removed %d unfinished temporary files", n)
	}
	return n
}
