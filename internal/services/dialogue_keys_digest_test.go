package services

import (
	"fmt"
	"path/filepath"
	"strings"
)

// dialogueDigestOf is a dialogue line's cache names (dialogueKeyDigest):
// its segment and its index in it. (The text it writes is the one main
// wrote before the shared dialogue layer, from the fields then.)
func dialogueDigestOf(ds *dialogueStep) (string, int) {
	seg, j := ds.seg, &ds.seg.job
	var b strings.Builder
	fmt.Fprintf(&b, "seg %s seed %d lang %s oneTrack %v file %s;", j.key, j.seed, j.lang, j.oneTrack, filepath.Base(j.paths[ds.k]))
	for k, ref := range j.refs {
		chunk := ""
		if c := seg.chunks[k]; c != nil {
			chunk = c.key + " " + filepath.Base(c.path)
		}
		fmt.Fprintf(&b, " ref %q %q %s %s [%s];", ref.key, ref.text, filepath.Base(ref.audio), filepath.Base(ref.tidy), chunk)
	}
	for k := range seg.lines {
		fmt.Fprintf(&b, " turn %d %q %q;", j.speakers[k], j.texts[k], j.tagged[k])
	}
	return b.String(), ds.k
}
