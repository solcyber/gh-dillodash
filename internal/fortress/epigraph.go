package fortress

import (
	"strings"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/widgets"
)

// The map's epigraph: Borges's one-paragraph story of the map the size
// of the empire, in a box laid over the map like a cartographer's
// cartouche. It goes where it covers the least of what the map is for:
// never the panels, the log, the edge counters or the look cursor, as
// little of the city as it can and none of its things if it can help it.
// View.Epigraph shows it (b in the dashboard toggles it).

// EpigraphTitle and EpigraphAuthor head and sign the box.
const (
	EpigraphTitle  = "On Exactitude in Science"
	EpigraphAuthor = "J. L. Borges"
)

const epigraphText = "…In that Empire, the Art of Cartography attained such Perfection that the map of a " +
	"single Province occupied the entirety of a City, and the map of the Empire, the entirety " +
	"of a Province. In time, those Unconscionable Maps no longer satisfied, and the " +
	"Cartographers Guilds struck a Map of the Empire whose size was that of the Empire, and " +
	"which coincided point for point with it. The following Generations, who were not so " +
	"fond of the Study of Cartography as their Forebears had been, saw that that vast Map " +
	"was Useless, and not without some Pitilessness was it, that they delivered it up to the " +
	"Inclemencies of Sun and Winters. In the Deserts of the West, still today, there are " +
	"Tattered Ruins of that Map, inhabited by Animals and Beggars; in all the Land there is " +
	"no other Relic of the Disciplines of Geography."

const epigraphSource = "—Suarez Miranda, Viajes de varones prudentes, Libro IV, Cap. XLV, Lérida, 1658"

// The box's inner widths: any from epiMinInner to epiMaxInner, so a
// short wide strip of open water takes it as well as a tall narrow one;
// among the places that cover as little, the width nearest epiReadInner
// (a comfortable measure) wins.
const (
	epiMinInner  = 30
	epiMaxInner  = 110
	epiReadInner = 56
)

// epiPlace is where the epigraph was put, and what it was put for: it
// stays until the map, the screen or the viewport changes, or the look
// cursor walks up to it.
type epiPlace struct {
	key        epiKey
	ok         bool
	top, left  int
	bh, bw     int
	lines      []string
	cost       int
	curY, curX int
}

type epiKey struct {
	state     stateKey
	h, w      int
	top, left int
	ascii     bool
}

// asciiText swaps the few non-ASCII letters for the ASCII glyph set.
func asciiText(s string) string {
	return strings.NewReplacer("…", "...", "—", "-", "é", "e").Replace(s)
}

// epigraphLines is the text wrapped to an inner width: the story, a
// blank line, and its (invented) source.
func epigraphLines(inner int, ascii bool) []string {
	body, src := epigraphText, epigraphSource
	if ascii {
		body, src = asciiText(body), asciiText(src)
	}
	lines := widgets.Wrap(body, inner)
	lines = append(lines, "")
	return append(lines, widgets.Wrap(src, inner)...)
}

const epiBlocked = 1 << 20

// epigraphCosts is what covering each screen cell costs: blocked for
// what must stay seen (the panels, the log, the edge counters, the look
// cursor and its neighbours, the rows that are not the map's), 10 for a
// thing, 3 for the city proper, 2 for a map label, 0 for the land and
// water round the city.
func (v *View) epigraphCosts(h, w, top, mh, x0, mw, vt, vl int) [][]int {
	m := v.Map
	cost := make([][]int, h)
	for y := range cost {
		cost[y] = make([]int, w)
		if y < top || y >= top+mh {
			for x := range cost[y] {
				cost[y][x] = epiBlocked
			}
		}
	}
	block := func(y0, y1, x0, x1 int) {
		for y := max(0, y0); y < min(h, y1); y++ {
			for x := max(0, x0); x < min(w, x1); x++ {
				cost[y][x] = epiBlocked
			}
		}
	}
	add := func(y, x, n int) {
		if y >= 0 && y < h && x >= 0 && x < w && cost[y][x] < epiBlocked {
			cost[y][x] += n
		}
	}
	// the street names down the left
	block(0, h, 0, x0)
	// the look and legend panels, at their tallest
	pl, pt := v.panelsBox(h, w)
	block(pt, h, pl-1, w)
	// the announcements, top left
	block(top, top+3, x0, x0+max(0, min(60, mw-LegendW-2))+1)
	// the edge counters when zoomed in
	if v.Zoom != ZoomFit {
		block(top, top+1, x0, x0+14)
		block(top+mh-1, top+mh, x0, x0+14)
	}
	// the look cursor and a little room round it
	cy, cx := top+v.Cur.R-vt, x0+v.Cur.C-vl
	block(cy-2, cy+3, cx-4, cx+5)
	// the city proper and its labels
	for vr := 0; vr < mh && vt+vr < m.Rows; vr++ {
		for vc := 0; vc < mw && vl+vc < m.Cols; vc++ {
			if m.Main[vt+vr][vl+vc] {
				add(top+vr, x0+vc, 3)
			}
		}
	}
	for _, l := range m.Labels {
		for i := range []rune(l.Text) {
			add(top+l.Row-vt, x0+l.Col-vl+i, 2)
		}
	}
	// the things (and the cell a pacing dwarf steps into)
	for cell := range v.Cells {
		y, x := top+cell.R-vt, x0+cell.C-vl
		add(y, x, 10)
		add(y, x+1, 10)
	}
	return cost
}

// placeEpigraph finds the box's place on an h x w screen whose map rows
// start at top with the viewport at (vt, vl); it keeps the last place
// while nothing it was put for has changed.
func (v *View) placeEpigraph(h, w, top, mh, x0, mw, vt, vl int) *epiPlace {
	ascii := glyphs.Active() == glyphs.ASCII
	key := epiKey{h: h, w: w, top: vt, left: vl, ascii: ascii}
	if v.key != nil {
		key.state = *v.key
	}
	cy, cx := top+v.Cur.R-vt, x0+v.Cur.C-vl
	if p := v.epi; p != nil && p.key == key {
		if !p.ok {
			return p
		}
		// the cursor may wander, as long as it keeps its distance
		near := cy >= p.top-2 && cy < p.top+p.bh+2 && cx >= p.left-4 && cx < p.left+p.bw+4
		if !near {
			return p
		}
	}
	cost := v.epigraphCosts(h, w, top, mh, x0, mw, vt, vl)
	// summed-area table: sum over [y0,y1) x [x0,x1) in O(1)
	sat := make([][]int, h+1)
	for y := range sat {
		sat[y] = make([]int, w+1)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sat[y+1][x+1] = cost[y][x] + sat[y][x+1] + sat[y+1][x] - sat[y][x]
		}
	}
	sum := func(y0, y1, x0, x1 int) int {
		return sat[y1][x1] - sat[y0][x1] - sat[y1][x0] + sat[y0][x0]
	}
	best := &epiPlace{key: key, cost: epiBlocked, curY: cy, curX: cx}
	bestFar := 0
	for inner := epiMinInner; inner <= min(epiMaxInner, w-x0-4); inner++ {
		far := abs(float64(inner - epiReadInner))
		lines := epigraphLines(inner, ascii)
		bh, bw := len(lines)+2, inner+4
		for t := top; t+bh <= top+mh; t++ {
			for l := x0; l+bw <= w; l++ {
				// the box and the blank cell DrawBox keeps either side
				c := sum(t, t+bh, l-1, min(w, l+bw+1))
				if c < best.cost || (c == best.cost && best.ok && int(far) < bestFar) {
					*best = epiPlace{key: key, ok: true, top: t, left: l, bh: bh, bw: bw, lines: lines, cost: c, curY: cy, curX: cx}
					bestFar = int(far)
				}
			}
		}
	}
	if best.cost >= epiBlocked {
		best.ok = false
	}
	v.epi = best
	return best
}

// drawEpigraph paints the box where placeEpigraph put it.
func drawEpigraph(c *canvas.Canvas, p *epiPlace, ascii bool) {
	itTop, itLeft, iw := widgets.DrawBox(c, p.top, p.left, p.bh, p.bw, EpigraphTitle, "card.title", "card", -1)
	for i, ln := range p.lines {
		c.PutSegs(itTop+i, itLeft, []text.Seg{text.S(ln, "muted")}, itLeft+iw, false, false)
	}
	sign := " — " + EpigraphAuthor + " "
	if ascii {
		sign = asciiText(sign)
	}
	c.PutSegs(p.top+p.bh-1, p.left+p.bw-2-text.Width(sign), []text.Seg{text.S(sign, "card.title")}, 0, false, false)
}

// EpigraphShown says whether the last Draw found room for the epigraph.
func (v *View) EpigraphShown() bool {
	return v.Epigraph && v.epi != nil && v.epi.ok
}
