package services

import (
	"fmt"
	"path/filepath"
	"strings"
)

// dialogueDigestOf is a dialogue line's cache names (dialogueKeyDigest):
// its segment and its index in it.
func dialogueDigestOf(ds *dialogueStep) (string, int) {
	seg := ds.seg
	var b strings.Builder
	fmt.Fprintf(&b, "seg %s seed %d lang %s oneTrack %v file %s;", seg.key, seg.seed, seg.lang, seg.oneTrack, filepath.Base(seg.paths[ds.k]))
	for _, ref := range seg.refs {
		chunk := ""
		if ref.chunk != nil {
			chunk = ref.chunk.key + " " + filepath.Base(ref.chunk.path)
		}
		fmt.Fprintf(&b, " ref %q %q %s %s [%s];", ref.key, ref.text, filepath.Base(ref.audio), filepath.Base(ref.tidy), chunk)
	}
	for k := range seg.lines {
		fmt.Fprintf(&b, " turn %d %q %q;", seg.speakers[k], seg.texts[k], seg.tagged[k])
	}
	return b.String(), ds.k
}
