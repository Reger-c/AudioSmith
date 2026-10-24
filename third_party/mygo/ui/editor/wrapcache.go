package editor

import (
	"strings"

	"github.com/egoist/mygo/internal/text"
)

// wrapCache keeps what soft wraps shaped, so that a line is shaped to wrap
// once, not at every frame that lays it out again: per displayed line, by
// its text, the advances its characters take in it (for the style) and
// where it breaks (for the wrap width too), and the widths of the rows
// fitRow shaped, by their text. An edit makes new texts, which miss, and
// leaves those of the lines it did not touch, which hit: typing reshapes
// the line typed in. Another wrap width keeps the advances and the rows'
// widths; another style forgets all.
type wrapCache struct {
	style text.Style
	scale float32
	width float32
	lines map[string]*wrapLine
	// rows are the widths of rows shaped, in two generations, as
	// shapeCache's: cur fills up to wrapRowsMax, then becomes prev.
	rows, prevRows map[string]float32
}

// wrapLine is what wrapCache keeps of a displayed line.
type wrapLine struct {
	adv    []float32 // lineAdvances
	breaks []int     // wrapBreaks at wrapCache.width, when measured
	broken bool      // breaks is measured
	used   bool      // since the last sweep
}

// wrapRowsMax bounds a generation of row widths.
const wrapRowsMax = 8192

// check forgets what does not hold for the style, the wrap width and
// the scale of the display.
func (c *wrapCache) check(style text.Style, width, scale float32) {
	if c.style != style || c.scale != scale || c.lines == nil {
		*c = wrapCache{style: style, scale: scale, width: width, lines: map[string]*wrapLine{}}
		return
	}
	if c.width != width {
		c.width = width
		for _, l := range c.lines {
			l.breaks, l.broken = nil, false
		}
	}
}

// line returns what is kept of the line s, made empty if nothing.
func (c *wrapCache) line(s string) *wrapLine {
	l, ok := c.lines[s]
	if !ok {
		// Its own copy: s may share the memory of a larger text.
		l = &wrapLine{}
		c.lines[strings.Clone(s)] = l
	}
	l.used = true
	return l
}

// sweep forgets the lines no layout used since the last sweep once the
// cache holds many more lines than the text, as after much typing: the
// texts the lines had before.
func (c *wrapCache) sweep(lines int) {
	if len(c.lines) <= 2*lines+256 {
		return
	}
	for s, l := range c.lines {
		if !l.used {
			delete(c.lines, s)
		}
		l.used = false
	}
}

// rowWidth returns the width of row as kept, if it is.
func (c *wrapCache) rowWidth(row string) (float32, bool) {
	if w, ok := c.rows[row]; ok {
		return w, true
	}
	if w, ok := c.prevRows[row]; ok {
		c.setRowWidth(row, w)
		return w, true
	}
	return 0, false
}

func (c *wrapCache) setRowWidth(row string, w float32) {
	if c.rows == nil || len(c.rows) >= wrapRowsMax {
		c.prevRows, c.rows = c.rows, make(map[string]float32, 256)
	}
	c.rows[row] = w
}
