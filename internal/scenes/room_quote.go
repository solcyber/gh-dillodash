package scenes

import (
	"strings"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// A page on the wall: Miss Bartlett's complaint on arriving in
// Florence, from the first chapter of A Room with a View (1908), framed
// and hung where the room has a stretch of bare wall for it. The novel
// is in the public domain.
const (
	forsterQuote = "“The Signora had no business to do it,” said Miss Bartlett, “no business at all. She promised us south rooms with a view close together, instead of which here are north rooms, looking into a courtyard, and a long way apart. Oh, Lucy!”"
	forsterBy    = "— E. M. Forster, A Room with a View"
)

// The framed page's colors: an ebony frame round ivory paper, the
// quote in ink and the attribution a little softer.
var (
	quotePaper   = rgb{232, 222, 198}
	quoteFrame   = rgb{48, 36, 32}
	quoteFrameHi = rgb{86, 68, 58}
	quoteFrameLo = rgb{26, 18, 16}
	quoteInkSoft = rgb{112, 86, 68}
)

// quotePage is where the page hangs and what it says: r is the frame in
// cells, the text starts inset columns in from its left edge and one row
// down; wire says it hangs from a picture wire.
type quotePage struct {
	r     cellRect
	inset int
	lines []string // the quote, wrapped
	by    []string // the attribution, wrapped, set right
	gap   bool     // a blank line between the quote and its author
	wire  bool
}

// quoteZone is a stretch of wall the page may hang on: columns [x0, x1)
// and rows [y0, y1), a frame minW to maxW columns wide. The page is
// centred across the zone; down it, centred too (mid) or as high as it
// fits.
type quoteZone struct {
	x0, x1, y0, y1 int
	minW, maxW     int
	mid, wire      bool
}

// quoteASCII is s in plain ASCII: straight quotes and a hyphen.
func quoteASCII(s string) string {
	return strings.NewReplacer("“", `"`, "”", `"`, "—", "-", "’", "'").Replace(s)
}

// quoteWrap word-wraps s to width columns; a word wider than a line is
// cut.
func quoteWrap(s string, width int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		for text.Width(word) > width {
			if cur != "" {
				lines = append(lines, cur)
				cur = ""
			}
			head := text.Clip(word, width)
			lines = append(lines, head)
			word = word[len(head):]
		}
		switch {
		case cur == "":
			cur = word
		case text.Width(cur)+1+text.Width(word) <= width:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// quoteSize is the page in a frame fw columns wide: the wrapped quote,
// the wrapped attribution and the frame's height (a row of frame top
// and bottom and, with gap, a blank line between the quote and its
// author).
func quoteSize(fw, inset int, ascii, gap bool) (lines, by []string, h int) {
	q, b := forsterQuote, forsterBy
	if ascii {
		q, b = quoteASCII(q), quoteASCII(b)
	}
	tw := fw - 2*inset
	if tw < 8 {
		return nil, nil, 1 << 20
	}
	lines = quoteWrap(q, tw)
	// the author on one line, else broken after his name
	if text.Width(b) <= tw {
		by = []string{b}
	} else {
		name, title, _ := strings.Cut(b, ", ")
		by = append(quoteWrap(name+",", tw), quoteWrap(title, tw)...)
	}
	h = 2 + len(lines) + len(by)
	if gap {
		h++
	}
	return lines, by, h
}

// quoteFit hangs the page in the first zone that holds it, as wide as
// that zone allows (with the blank line before the author if it fits,
// else without); ok vets a frame's rect against what is already on the
// wall. False when no zone holds it: the page is left off rather than
// cut short.
func quoteFit(zones []quoteZone, inset int, ascii bool, ok func(z quoteZone, r cellRect) bool) (quotePage, bool) {
	for _, z := range zones {
		for fw := min(z.maxW, z.x1-z.x0); fw >= z.minW; fw-- {
			for _, gap := range []bool{true, false} {
				lines, by, h := quoteSize(fw, inset, ascii, gap)
				if h > z.y1-z.y0 {
					continue
				}
				// the frame hugs the longest line
				used := 0
				for _, ln := range append(append([]string(nil), lines...), by...) {
					used = max(used, text.Width(ln))
				}
				fw := used + 2*inset
				x := z.x0 + (z.x1-z.x0-fw)/2
				ys := []int{z.y0 + (z.y1-z.y0-h)/2}
				if !z.mid {
					ys = ys[:0]
					for y := z.y0; y+h <= z.y1; y++ {
						ys = append(ys, y)
					}
				}
				for _, y := range ys {
					r := cellRect{y: y, x: x, h: h, w: fw}
					if ok(z, r) {
						return quotePage{r: r, inset: inset, lines: lines, by: by, gap: gap, wire: z.wire}, true
					}
				}
			}
		}
	}
	return quotePage{}, false
}

// quoteText writes the page's words: the quote from the top, then
// (after a blank line, when there is one) the attribution, set right.
func quoteText(q quotePage, put func(y, x int, s string, soft bool)) {
	tw := q.r.w - 2*q.inset
	y := q.r.y + 1
	for _, ln := range q.lines {
		put(y, q.r.x+q.inset, ln, false)
		y++
	}
	if q.gap {
		y++
	}
	for _, ln := range q.by {
		put(y, q.r.x+q.inset+tw-text.Width(ln), ln, true)
		y++
	}
}

// quoteHang draws the framed page's pixels (and its wire).
func quoteHang(p *pix, q quotePage) {
	frame(p, q.r, quoteFrame, quoteFrameHi, quoteFrameLo, quotePaper)
	if q.wire {
		picWire(p, q.r)
	}
}

// quoteInk writes the page's words onto the room's cells.
func quoteInk(c *canvas.Canvas, q quotePage) {
	quoteText(q, func(y, x int, s string, soft bool) {
		fg := ink
		if soft {
			fg = quoteInkSoft
		}
		sgr := sgrFg(fg, quotePaper)
		for _, ch := range s {
			c.PutRaw(y, x, string(ch), sgr)
			x += text.RuneWidth(ch)
		}
	})
}

// quoteLine draws the page in the line-art room: a ruled box with the
// words in it.
func quoteLine(c *canvas.Canvas, q quotePage, ascii bool) {
	hz, vt, tl, tr, bl, br := "─", "│", "┌", "┐", "└", "┘"
	if ascii {
		hz, vt, tl, tr, bl, br = "-", "|", "+", "+", "+", "+"
	}
	r := q.r
	for x := r.x + 1; x < r.x+r.w-1; x++ {
		c.PutStr(r.y, x, hz, "art")
		c.PutStr(r.y+r.h-1, x, hz, "art")
	}
	for y := r.y + 1; y < r.y+r.h-1; y++ {
		c.PutStr(y, r.x, vt, "art")
		c.PutStr(y, r.x+r.w-1, vt, "art")
	}
	c.PutStr(r.y, r.x, tl, "art")
	c.PutStr(r.y, r.x+r.w-1, tr, "art")
	c.PutStr(r.y+r.h-1, r.x, bl, "art")
	c.PutStr(r.y+r.h-1, r.x+r.w-1, br, "art")
	quoteText(q, func(y, x int, s string, soft bool) {
		style := "text"
		if soft {
			style = "muted"
		}
		c.PutStr(y, x, s, style)
	})
}

// quoteOverlaps says whether two rects share a cell.
func quoteOverlaps(a, b cellRect) bool {
	return a.x < b.x+b.w && b.x < a.x+a.w && a.y < b.y+b.h && b.y < a.y+a.h
}

// quoteLinePage is where the page hangs in the line-art room: right of
// the window, under it, or over the plant (plantH rows tall).
func quoteLinePage(g geometry, plantH int, ascii bool) (quotePage, bool) {
	l := g.l
	return quoteFit([]quoteZone{
		{x0: g.rightIn + 3, x1: g.w - 1, y0: 1, y1: g.floorY, minW: 22, maxW: 40},
		{x0: g.leftOut, x1: g.rightIn + 1, y0: g.sillY + 4, y1: g.floorY, minW: 30, maxW: 72, mid: true},
		{x0: 1, x1: g.leftOut - 1, y0: 1, y1: g.floorY - plantH, minW: 22, maxW: 40},
	}, 2, ascii, func(z quoteZone, r cellRect) bool {
		// clear of the window's frame and the plaque under it
		return !quoteOverlaps(r, cellRect{y: l.Y - 1, x: l.X - 2, h: l.H + 2, w: l.W + 4})
	})
}
