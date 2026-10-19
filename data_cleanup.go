package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cleanPartFiles removes the temporary files of writes that never
// finished ("."+name+".*.part": a recording or an import cut short by a
// crash or a quit) from the data folder and its folders one level down,
// those older than minAge (a write in progress is younger). They were
// never cleaned before (family audit A13).
func cleanPartFiles(dataDir string, minAge time.Duration) int {
	n := 0
	clean := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, ".") || !strings.Contains(name, ".part") {
				continue
			}
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > minAge && os.Remove(filepath.Join(dir, name)) == nil {
				n++
			}
		}
	}
	clean(dataDir)
	if entries, err := os.ReadDir(dataDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				clean(filepath.Join(dataDir, e.Name()))
			}
		}
	}
	if n > 0 {
		log.Printf("data: removed %d unfinished temporary files", n)
	}
	return n
}
