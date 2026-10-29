// Package canvas is a cell buffer the views paint into: Put draws styled
// segments at a row and column, overlays (wipes, confetti, the launch)
// paint over
// whatever is there, and Render turns the frame into one string for the
// terminal. Styles stay names until Render; the renderer is injected so
// this package knows nothing about colors.
package canvas

import (
	"strings"

	"github.com/rivo/uniseg"

	"github.com/solcyber/gh-dillodash/internal/text"
)

// Renderer styles one run of text. sel marks the selected row, band a
// code block: both change the background.
type Renderer func(s string, style string, extra text.Attr, sel, band bool) string

type cell struct {
	g     string // grapheme; "" for the continuation cell of a wide one
	width int    // 1 or 2; 0 for a continuation cell
	style string
	extra text.Attr
	sel   bool
	band  bool
	// raw, when set, is a complete SGR prefix the cell renders with
	// instead of a named style (the Amos sprite's exact colors).
	raw string
}

// PutRaw paints one cell with a literal SGR prefix (e.g. a 24-bit
// foreground and background) instead of a named style. Wide glyphs are
// not supported here: glyph must be one cell wide.
func (c *Canvas) PutRaw(y, x int, glyph, sgr string) {
	if y < 0 || y >= c.H || x < 0 || x >= c.W {
		return
	}
	if x+1 < c.W && c.cells[y][x+1].width == 0 && c.cells[y][x].width == 2 {
		c.cells[y][x+1] = cell{g: " ", width: 1, style: "text"}
	}
	if x > 0 && c.cells[y][x].width == 0 && c.cells[y][x-1].width == 2 {
		c.cells[y][x-1] = cell{g: " ", width: 1, style: "text"}
	}
	c.cells[y][x] = cell{g: glyph, width: 1, raw: sgr}
}

// Canvas is a W x H grid of cells.
type Canvas struct {
	W, H  int
	cells [][]cell
	// Record, when set, receives every Put as (y, x, text, style) so a
	// test can assert what was drawn where.
	Record *[]Put
}

// Put is one recorded draw.
type Put struct {
	Y, X  int
	Text  string
	Style string
}

// New makes a blank canvas: every cell a space in the "text" style.
func New(w, h int) *Canvas {
	c := &Canvas{W: w, H: h}
	c.cells = make([][]cell, h)
	for y := range c.cells {
		c.cells[y] = make([]cell, w)
		for x := range c.cells[y] {
			c.cells[y][x] = cell{g: " ", width: 1, style: "text"}
		}
	}
	return c
}

// Clear resets every cell.
func (c *Canvas) Clear() {
	for y := range c.cells {
		for x := range c.cells[y] {
			c.cells[y][x] = cell{g: " ", width: 1, style: "text"}
		}
	}
}

// PutSegs draws segments from column x of row y, clipped at column right
// (right <= 0 means the canvas edge). sel and band apply to every cell.
// Returns the column reached.
func (c *Canvas) PutSegs(y, x int, segs []text.Seg, right int, sel, band bool) int {
	if y < 0 || y >= c.H {
		return x
	}
	limit := c.W
	if right > 0 && right < limit {
		limit = right
	}
	for _, seg := range segs {
		if x >= limit {
			break
		}
		t := text.Clip(text.Clean(seg.Text), limit-x)
		if t == "" {
			continue
		}
		if c.Record != nil {
			*c.Record = append(*c.Record, Put{y, x, t, seg.Style})
		}
		x = c.putRun(y, x, t, seg.Style, seg.Extra, sel, band)
	}
	return x
}

// Put draws segments at (y, x) clipped at the canvas edge.
func (c *Canvas) Put(y, x int, segs ...text.Seg) int {
	return c.PutSegs(y, x, segs, 0, false, false)
}

// PutStr draws one styled string.
func (c *Canvas) PutStr(y, x int, s, style string) int {
	return c.PutSegs(y, x, []text.Seg{{Text: s, Style: style}}, 0, false, false)
}

func (c *Canvas) putRun(y, x int, s, style string, extra text.Attr, sel, band bool) int {
	if x < 0 {
		// skip the part left of the canvas
		skip := -x
		s = text.Clip(s, text.Width(s)) // normalise
		g := uniseg.NewGraphemes(s)
		cut := 0
		for g.Next() {
			w := text.Width(g.Str())
			if skip <= 0 {
				break
			}
			skip -= w
			_, cut = g.Positions()
		}
		s = s[cut:]
		x = 0
		if skip < 0 { // a wide glyph straddled the edge: blank its tail
			c.cells[y][0] = cell{g: " ", width: 1, style: style, sel: sel, band: band}
			x = 1
		}
	}
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		gs := g.Str()
		w := text.Width(gs)
		if w == 0 {
			// a combining mark on its own: attach to the previous cell
			if x > 0 && c.cells[y][x-1].width > 0 {
				c.cells[y][x-1].g += gs
			}
			continue
		}
		if x+w > c.W {
			break
		}
		// a wide glyph written over the second half of another wide one:
		// blank the orphaned first half
		if x > 0 && c.cells[y][x].width == 0 && c.cells[y][x-1].width == 2 {
			c.cells[y][x-1] = cell{g: " ", width: 1, style: c.cells[y][x-1].style, sel: c.cells[y][x-1].sel, band: c.cells[y][x-1].band}
		}
		c.cells[y][x] = cell{g: gs, width: w, style: style, extra: extra, sel: sel, band: band}
		if w == 2 {
			c.cells[y][x+1] = cell{width: 0, style: style, extra: extra, sel: sel, band: band}
		}
		x += w
		// a narrow glyph written over the first half of a wide one: blank
		// the orphaned second half
		if x < c.W && c.cells[y][x].width == 0 {
			c.cells[y][x] = cell{g: " ", width: 1, style: style, sel: sel, band: band}
		}
	}
	return x
}

// Fill paints columns [x0, x1) of row y with the style's background.
func (c *Canvas) Fill(y, x0, x1 int, style string, sel, band bool) {
	if x1 > x0 {
		c.PutSegs(y, x0, []text.Seg{{Text: strings.Repeat(" ", x1-x0), Style: style}}, x1, sel, band)
	}
}

// Text returns the plain text of row y (for tests).
func (c *Canvas) Text(y int) string {
	if y < 0 || y >= c.H {
		return ""
	}
	var b strings.Builder
	for _, ce := range c.cells[y] {
		b.WriteString(ce.g)
	}
	return b.String()
}

// Lines returns every row's plain text (for tests).
func (c *Canvas) Lines() []string {
	out := make([]string, c.H)
	for y := range out {
		out[y] = c.Text(y)
	}
	return out
}

// StyleAt returns the style name of a cell (for tests).
func (c *Canvas) StyleAt(y, x int) string {
	if y < 0 || y >= c.H || x < 0 || x >= c.W {
		return ""
	}
	return c.cells[y][x].style
}

// Render turns the frame into terminal text: rows joined by newlines,
// each row a sequence of styled runs. Runs of cells sharing a style are
// rendered together so the output stays small.
func (c *Canvas) Render(r Renderer) string {
	var b strings.Builder
	b.Grow(c.W * c.H * 2)
	for y := 0; y < c.H; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		row := c.cells[y]
		var run strings.Builder
		var cur cell
		have := false
		flush := func() {
			if run.Len() > 0 {
				b.WriteString(r(run.String(), cur.style, cur.extra, cur.sel, cur.band))
				run.Reset()
			}
		}
		for x := 0; x < c.W; x++ {
			ce := row[x]
			if ce.width == 0 {
				continue
			}
			if ce.raw != "" {
				flush()
				have = false
				b.WriteString("\x1b[0m")
				b.WriteString(ce.raw)
				b.WriteString(ce.g)
				b.WriteString("\x1b[0m")
				continue
			}
			if !have || ce.style != cur.style || ce.extra != cur.extra || ce.sel != cur.sel || ce.band != cur.band {
				flush()
				cur, have = ce, true
			}
			run.WriteString(ce.g)
		}
		flush()
	}
	return b.String()
}

// CellInfo is one cell as Walk reports it.
type CellInfo struct {
	Glyph string
	Width int // 1 or 2; continuation cells are not reported
	Style string
	Extra text.Attr
	Sel   bool
	Band  bool
	Raw   string // a literal SGR prefix instead of a named style, or ""
}

// Walk calls fn for every cell (left to right, top to bottom), skipping
// the continuation half of wide glyphs. Renderers that are not terminals
// (an image) use it.
func (c *Canvas) Walk(fn func(y, x int, cell CellInfo)) {
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			ce := c.cells[y][x]
			if ce.width == 0 {
				continue
			}
			fn(y, x, CellInfo{Glyph: ce.g, Width: ce.width, Style: ce.style, Extra: ce.extra, Sel: ce.sel, Band: ce.band, Raw: ce.raw})
		}
	}
}
