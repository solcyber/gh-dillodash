package markdown

import (
	"regexp"
	"sort"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/text"
)

var tableSepRE = regexp.MustCompile(`^\|?\s*:?-{3,}`)

// TableMinCol is the narrowest column worth laying out: narrower than
// this, a column wraps into confetti.
const TableMinCol = 6

func isTableRow(stripped string) bool {
	return strings.HasPrefix(stripped, "|") && strings.HasSuffix(stripped, "|") &&
		len(stripped) > 1
}

// tableCells is a GFM table row's cells, outer pipes dropped. Splits on
// every unescaped pipe, as GitHub does (inside `code` too); \| is a
// literal.
func tableCells(row string) []string {
	s := strings.TrimSpace(row)
	s = s[1 : len(s)-1]
	cells := []string{""}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '|' {
			b.WriteByte('|')
			i++
			continue
		}
		if s[i] == '|' {
			cells[len(cells)-1] = b.String()
			b.Reset()
			cells = append(cells, "")
			continue
		}
		b.WriteByte(s[i])
	}
	cells[len(cells)-1] = b.String()
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
	}
	return cells
}

// tableRaw is the fallback rendering, pipes and all: for a table with no
// header separator, or too many columns to lay out.
func tableRaw(rows []string, width int, hooks *Hooks) []text.Line {
	var out []text.Line
	for _, stripped := range rows {
		if tableSepRE.MatchString(stripped) {
			out = append(out, text.Line{Segs: []text.Seg{text.S(stripped, "md.pipe")}})
			continue
		}
		var segs []text.Seg
		for i, c := range strings.Split(stripped, "|") {
			if i > 0 {
				segs = append(segs, text.S("|", "md.pipe"))
			}
			segs = append(segs, Inline(c, "text", hooks)...)
		}
		out = append(out, Wrap(segs, width, nil, nil, true)...)
	}
	return out
}

// tableWidths picks column widths that fit room cells, the way a browser
// lays out an auto table: natural widths when they fit; else every column
// gets its longest word (minimal) and the spare room goes out in
// proportion to how much more each column wants; else (words too long for
// that) water-filling, which splits words. nil when even TableMinCol per
// column does not fit.
func tableWidths(natural, minimal []int, room int) []int {
	n := len(natural)
	if sum(natural) <= room {
		return append([]int(nil), natural...)
	}
	if room < n*TableMinCol {
		return nil
	}
	if sum(minimal) <= room {
		spare := room - sum(minimal)
		slack := make([]int, n)
		for j := range slack {
			slack[j] = natural[j] - minimal[j]
		}
		total := sum(slack)
		widths := make([]int, n)
		for j := range widths {
			widths[j] = minimal[j] + slack[j]*spare/total
		}
		order := make([]int, n)
		for j := range order {
			order[j] = j
		}
		sort.SliceStable(order, func(a, b int) bool { return slack[order[a]] > slack[order[b]] })
		for _, j := range order {
			if sum(widths) >= room {
				break
			}
			if widths[j] < natural[j] {
				widths[j]++
			}
		}
		return widths
	}
	widths := make([]int, n)
	left := room
	open := make([]int, n)
	for j := range open {
		open[j] = j
	}
	for len(open) > 0 {
		share := left / len(open)
		var small, rest []int
		for _, j := range open {
			if natural[j] <= share {
				small = append(small, j)
			} else {
				rest = append(rest, j)
			}
		}
		if len(small) == 0 {
			extra := left % len(open)
			for k, j := range open {
				widths[j] = share
				if k < extra {
					widths[j]++
				}
			}
			break
		}
		for _, j := range small {
			widths[j] = natural[j]
			left -= natural[j]
		}
		open = rest
	}
	return widths
}

func sum(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

func longestWord(segs []text.Seg) int {
	best := 0
	for _, w := range words(segs) {
		if w.space {
			continue
		}
		if ww := wordWidth(w.toks); ww > best {
			best = ww
		}
	}
	return best
}

// wrapCell wraps a table cell to exactly width cells. Wrap never wraps
// narrower than 8, so a slimmer column breaks anywhere instead: a line
// wider than its column would be clipped, and that drops words.
func wrapCell(segs []text.Seg, width int) []text.Line {
	lines := Wrap(segs, width, nil, nil, false)
	fits := true
	for _, ln := range lines {
		if text.SegsWidth(ln.Segs) > width {
			fits = false
			break
		}
	}
	if fits {
		return lines
	}
	var out []text.Line
	for _, p := range hardPieces(cleaned(segs), width, width) {
		out = append(out, text.Line{Segs: p.segs})
	}
	if len(out) == 0 {
		out = []text.Line{{}}
	}
	return out
}

// RenderTable lays out a GFM table (rows are its stripped source lines)
// as aligned columns: a bold header, a rule under it, and the vbar glyph
// between cells. Cells wider than their column word-wrap inside it, and
// then a faint rule separates the rows so wrapped lines stay with their
// row. Nothing is dropped: a table that cannot be laid out falls back to
// its source pipes.
func RenderTable(rows []string, width int) []text.Line {
	return renderTable(rows, width, nil)
}

func renderTable(rows []string, width int, hooks *Hooks) []text.Line {
	if len(rows) < 2 || !tableSepRE.MatchString(rows[1]) {
		return tableRaw(rows, width, hooks)
	}
	head := tableCells(rows[0])
	n := len(head)
	aligns := make([]byte, n)
	specs := padCells(tableCells(rows[1]), n)
	for j, spec := range specs {
		switch {
		case strings.HasSuffix(spec, ":") && !strings.HasPrefix(spec, ":"):
			aligns[j] = 'r'
		case strings.HasPrefix(spec, ":") && strings.HasSuffix(spec, ":"):
			aligns[j] = 'c'
		default:
			aligns[j] = 'l'
		}
	}
	grid := make([][][]text.Seg, 0, len(rows)-1)
	hrow := make([][]text.Seg, n)
	for j, c := range head {
		hrow[j] = Inline(c, "md.bold", hooks)
	}
	grid = append(grid, hrow)
	for _, r := range rows[2:] {
		cells := padCells(tableCells(r), n)
		row := make([][]text.Seg, n)
		for j, c := range cells {
			row[j] = Inline(c, "text", hooks)
		}
		grid = append(grid, row)
	}
	natural := make([]int, n)
	minimal := make([]int, n)
	for j := 0; j < n; j++ {
		natural[j], minimal[j] = 1, 1
		for _, row := range grid {
			if w := text.SegsWidth(row[j]); w > natural[j] {
				natural[j] = w
			}
			if w := longestWord(row[j]); w > minimal[j] {
				minimal[j] = w
			}
		}
	}
	sepW := text.Width(" " + Glyphs.VBar + " ")
	widths := tableWidths(natural, minimal, width-1-sepW*(n-1))
	if widths == nil {
		return tableRaw(rows, width, hooks)
	}
	wrapped := make([][][]text.Line, len(grid))
	tall := false
	for i, row := range grid {
		wrapped[i] = make([][]text.Line, n)
		for j, cell := range row {
			if len(cell) == 0 {
				wrapped[i][j] = []text.Line{{}}
				continue
			}
			wrapped[i][j] = wrapCell(cell, widths[j])
			if len(wrapped[i][j]) > 1 {
				tall = true
			}
		}
	}
	bar := text.S(" "+Glyphs.VBar+" ", "md.pipe")

	rule := func(style string) text.Line {
		segs := []text.Seg{text.S(" ", "text")}
		for j, cw := range widths {
			if j > 0 {
				segs = append(segs, text.S(Glyphs.Rule+Glyphs.Cross+Glyphs.Rule, style))
			}
			segs = append(segs, text.S(strings.Repeat(Glyphs.Rule, cw), style))
		}
		return text.Line{Segs: segs}
	}

	var out []text.Line
	for i, row := range wrapped {
		if i == 1 {
			out = append(out, rule("md.pipe"))
		} else if i > 1 && tall {
			out = append(out, rule("md.rule"))
		}
		height := 0
		for _, c := range row {
			if len(c) > height {
				height = len(c)
			}
		}
		for k := 0; k < height; k++ {
			segs := []text.Seg{text.S(" ", "text")}
			for j, cell := range row {
				if j > 0 {
					segs = append(segs, bar)
				}
				var piece []text.Seg
				if k < len(cell) {
					piece = cell[k].Segs
				}
				gap := widths[j] - text.SegsWidth(piece)
				if k == 0 && len(cell) == 1 && aligns[j] != 'l' && gap > 0 {
					lead := gap
					if aligns[j] == 'c' {
						lead = gap / 2
					}
					piece = append([]text.Seg{text.S(strings.Repeat(" ", lead), "text")}, piece...)
				}
				segs = append(segs, text.PadSegs(piece, widths[j])...)
			}
			out = append(out, text.Line{Segs: segs})
		}
	}
	return out
}

// padCells pads or truncates a row's cells to n.
func padCells(cells []string, n int) []string {
	out := make([]string, n)
	copy(out, cells)
	return out
}
