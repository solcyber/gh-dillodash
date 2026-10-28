// Package text holds the cell-width aware string helpers and the segment
// model every view draws with: a Seg is a run of text in one named style,
// a Line is a row of segments. Widths are terminal cells, never bytes or
// runes, so CJK, emoji and Nerd Font glyphs line up.
package text

import (
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Attr is an extra attribute overlaid on a segment's style (search hits).
type Attr uint8

const (
	Bold Attr = 1 << iota
	Underline
	Reverse
)

// Seg is a run of text drawn in the named style, with optional extra
// attributes. Style names are resolved by the theme package.
type Seg struct {
	Text  string
	Style string
	Extra Attr
}

// S is a one-line constructor for a segment.
func S(text, style string) Seg { return Seg{Text: text, Style: style} }

// Line is one rendered row: its segments, and whether it sits inside a
// code block (drawn on the band background).
type Line struct {
	Segs []Seg
	Band bool
}

// Text is the line's plain text.
func (l Line) Text() string {
	var b strings.Builder
	for _, s := range l.Segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

// ASCIIOnly is set when the terminal cannot show non-ASCII: Clean then
// transliterates the common typographic characters and replaces the rest.
var ASCIIOnly = false

// Ellipsis is the glyph Ellip appends; the glyphs package sets it per set.
var Ellipsis = "…"

var asciiMap = map[rune]string{
	0xa0: " ", 0xb7: "-", 0x2014: "-", 0x2013: "-", 0x2212: "-",
	0x2026: "...", 0x2192: "->", 0x2190: "<-", 0x2018: "'",
	0x2019: "'", 0x201c: `"`, 0x201d: `"`, 0x2022: "*",
}

func isCtrl(r rune) bool {
	return (r <= 0x08) || (r >= 0x0b && r <= 0x1f) || (r >= 0x7f && r <= 0x9f)
}

// Clean makes text display-safe: tabs expanded to 8, newlines flattened
// to spaces, every other control character shown as a middle dot (or "?"
// in ASCII-only mode, where typographic characters are transliterated and
// anything else non-ASCII becomes "?").
func Clean(s string) string {
	if strings.ContainsRune(s, '\t') {
		s = expandTabs(s, 8)
	}
	if strings.ContainsRune(s, '\n') {
		s = strings.ReplaceAll(s, "\r\n", " ")
		s = strings.ReplaceAll(s, "\n", " ")
	}
	needs := false
	for _, r := range s {
		if isCtrl(r) || (ASCIIOnly && r > 0x7e) {
			needs = true
			break
		}
	}
	if !needs {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case isCtrl(r):
			if ASCIIOnly {
				b.WriteByte('?')
			} else {
				b.WriteRune('·')
			}
		case ASCIIOnly && r > 0x7e:
			if t, ok := asciiMap[r]; ok {
				b.WriteString(t)
			} else {
				b.WriteByte('?')
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func expandTabs(s string, n int) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			pad := n - col%n
			b.WriteString(strings.Repeat(" ", pad))
			col += pad
			continue
		}
		b.WriteRune(r)
		if r == '\n' {
			col = 0
		} else {
			col += RuneWidth(r)
		}
	}
	return b.String()
}

var (
	widthCache sync.Map // rune -> int
)

// RuneWidth is the cells one rune takes: 2 for wide CJK and most emoji,
// 0 for combining marks and variation selectors, 1 for everything else
// including the private-use Nerd Font glyphs.
func RuneWidth(r rune) int {
	if r < 0x7f {
		if r < 0x20 {
			return 0
		}
		return 1
	}
	if w, ok := widthCache.Load(r); ok {
		return w.(int)
	}
	w := ansi.StringWidth(string(r))
	widthCache.Store(r, w)
	return w
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// Width is the number of terminal cells s occupies.
func Width(s string) int {
	if isASCII(s) {
		return len(s)
	}
	return ansi.StringWidth(s)
}

// Clip is the longest prefix of s that fits in cols cells, cutting on
// grapheme boundaries so a wide character never straddles the edge.
func Clip(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	if isASCII(s) {
		if len(s) <= cols {
			return s
		}
		return s[:cols]
	}
	used := 0
	g := uniseg.NewGraphemes(s)
	end := 0
	for g.Next() {
		w := ansi.StringWidth(g.Str())
		if used+w > cols {
			return s[:end]
		}
		used += w
		_, end = g.Positions()
	}
	return s
}

// Ellip clips s to cols cells, ending in the ellipsis glyph when cut.
func Ellip(s string, cols int) string {
	if Width(s) <= cols {
		return s
	}
	more := Ellipsis
	if ASCIIOnly {
		more = "..."
	}
	mw := Width(more)
	if cols <= mw {
		return Clip(s, cols)
	}
	return Clip(s, cols-mw) + more
}

// EllipLeft clips s to cols cells from the left, so the END survives
// (a file's base name, a ref's last segment), with the ellipsis first.
func EllipLeft(s string, cols int) string {
	if Width(s) <= cols {
		return s
	}
	more := Ellipsis
	if ASCIIOnly {
		more = "..."
	}
	mw := Width(more)
	if cols <= mw {
		return clipLeft(s, cols)
	}
	return more + clipLeft(s, cols-mw)
}

// clipLeft keeps the last cols cells of s, whole graphemes only.
func clipLeft(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	if isASCII(s) {
		if len(s) <= cols {
			return s
		}
		return s[len(s)-cols:]
	}
	var starts []int
	var widths []int
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		a, _ := g.Positions()
		starts = append(starts, a)
		widths = append(widths, ansi.StringWidth(g.Str()))
	}
	used := 0
	for i := len(starts) - 1; i >= 0; i-- {
		if used+widths[i] > cols {
			return s[starts[i+1]:]
		}
		used += widths[i]
	}
	return s
}

// EllipSegs clips segments to width cells and, when something was cut,
// ends them in the ellipsis so the cut is visible.
func EllipSegs(segs []Seg, width int) []Seg {
	if SegsWidth(segs) <= width {
		return segs
	}
	more := Ellipsis
	if ASCIIOnly {
		more = "..."
	}
	mw := Width(more)
	if width <= mw {
		return ClipSegs(segs, width)
	}
	out := ClipSegs(segs, width-mw)
	style := "text"
	if len(out) > 0 {
		style = out[len(out)-1].Style
	}
	return append(out, Seg{Text: more, Style: style})
}

// Pad clips s to cols cells and pads it with spaces to exactly cols.
func Pad(s string, cols int) string {
	s = Clip(s, cols)
	if w := Width(s); w < cols {
		return s + strings.Repeat(" ", cols-w)
	}
	return s
}

// LPad right-aligns s in cols cells.
func LPad(s string, cols int) string {
	s = Clip(s, cols)
	if w := Width(s); w < cols {
		return strings.Repeat(" ", cols-w) + s
	}
	return s
}

// SegsWidth is the cells a run of segments occupies.
func SegsWidth(segs []Seg) int {
	n := 0
	for _, s := range segs {
		n += Width(s.Text)
	}
	return n
}

// SegsText joins the segments' text.
func SegsText(segs []Seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

// PadSegs returns the segments padded (or clipped) to exactly width cells.
func PadSegs(segs []Seg, width int) []Seg {
	out := make([]Seg, 0, len(segs)+1)
	used := 0
	for _, s := range segs {
		t := Clip(s.Text, width-used)
		if t == "" {
			continue
		}
		out = append(out, Seg{Text: t, Style: s.Style, Extra: s.Extra})
		used += Width(t)
	}
	if used < width {
		out = append(out, Seg{Text: strings.Repeat(" ", width-used), Style: "text"})
	}
	return out
}

// ClipSegs returns the segments clipped (never padded) to width cells.
func ClipSegs(segs []Seg, width int) []Seg {
	out := make([]Seg, 0, len(segs))
	used := 0
	for _, s := range segs {
		t := Clip(s.Text, width-used)
		if t == "" {
			break
		}
		out = append(out, Seg{Text: t, Style: s.Style, Extra: s.Extra})
		used += Width(t)
	}
	return out
}

// Span marks a search hit in character (rune) offsets of a line's text;
// Current is the hit the cursor is on.
type Span struct {
	Start, End int
	Current    bool
}

// OverlayHits splits segments at the span edges and marks the pieces: the
// current hit reversed+bold, the others underlined+bold. Never a colored
// background, which the theme reserves for "destroy".
func OverlayHits(segs []Seg, spans []Span) []Seg {
	if len(spans) == 0 {
		return segs
	}
	var out []Seg
	pos := 0
	for _, seg := range segs {
		runes := []rune(seg.Text)
		s0, s1 := pos, pos+len(runes)
		cuts := map[int]bool{s0: true, s1: true}
		for _, sp := range spans {
			cuts[clamp(sp.Start, s0, s1)] = true
			cuts[clamp(sp.End, s0, s1)] = true
		}
		points := make([]int, 0, len(cuts))
		for c := range cuts {
			points = append(points, c)
		}
		sortInts(points)
		for i := 0; i+1 < len(points); i++ {
			a, b := points[i], points[i+1]
			extra := seg.Extra
			for _, sp := range spans {
				if sp.Start <= a && b <= sp.End {
					if sp.Current {
						extra |= Reverse | Bold
					} else {
						extra |= Underline | Bold
					}
				}
			}
			out = append(out, Seg{Text: string(runes[a-s0 : b-s0]), Style: seg.Style, Extra: extra})
		}
		pos = s1
	}
	return out
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
