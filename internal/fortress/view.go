package fortress

import (
	"fmt"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/widgets"
)

// Layout.
const (
	Margin  = 9 // street names down the left
	LegendW = 44
	// MinW and MinH: below them the view says what it needs.
	MinW, MinH = 60, 20
)

// Lane is one repo's lane and the name the legend gives it.
type Lane struct {
	Repo  model.Repo
	Label string
}

// LogEntry is one announcement.
type LogEntry struct {
	When, Msg string
}

type sigEntry struct {
	kind  Kind
	color string
	text  string
}

type stateKey struct {
	city, zoom string
	mw, mh     int
	gen        string
	hasData    bool
}

// Frame is what Draw needs from the clock and the terminal.
type Frame struct {
	Still  bool // motion off or mono: nothing moves, shapes carry state
	T      float64
	Tier   theme.Tier
	Season string
}

// View is the map's state: the city, the zoom, the look cursor, the
// viewport, the announcements log, and the raster and placed items it
// rebuilds only when the data, the city, the zoom or the size changed.
type View struct {
	City string
	Zoom string
	Cur  Cell
	// Top and Left are the viewport (only ever moving when zoomed in).
	Top, Left int
	Log       []LogEntry
	// Build makes the items and lanes from the dashboard's last refresh.
	Build func() ([]*Item, []Lane)
	// Clock stamps the announcements (clock.Now when nil).
	Clock func() time.Time

	Map    *Map
	Items  []*Item
	Lanes  []Lane
	Cells  map[Cell][]*Item
	Hidden int

	hasCur bool
	key    *stateKey
	sig    map[string]sigEntry
	curID  string
	h, w   int
	gen    string
	data   bool
}

// NewView starts a view of a city. Open logs the arrival.
func NewView(city string) *View {
	return &View{City: city, Zoom: ZoomFit}
}

func (v *View) now() time.Time {
	if v.Clock != nil {
		return v.Clock()
	}
	return clock.Now().Local()
}

// Announce logs a message (the last four are kept).
func (v *View) Announce(msg string) {
	v.Log = append(v.Log, LogEntry{v.now().Format("15:04"), msg})
	if len(v.Log) > 4 {
		v.Log = v.Log[len(v.Log)-4:]
	}
}

// Open lands in a city: the look cursor goes to the thing that most
// wants a look, the viewport to the top left; the log stays.
func (v *View) Open(city string) {
	if _, ok := Cities[city]; !ok {
		city = CityKeys[0]
	}
	v.City, v.Zoom = city, ZoomFit
	v.hasCur, v.Top, v.Left, v.key, v.curID = false, 0, 0, nil, ""
	v.Announce("Strike the earth! The fortress of " + Cities[city].Name)
}

// events announces, Dwarf Fortress style, what a refresh changed: old
// and cur are the signatures by ID, items the new items in order.
func (v *View) events(old, cur map[string]sigEntry, items []*Item) {
	if old == nil {
		return
	}
	ref := func(s sigEntry) string {
		if i := strings.IndexByte(s.text, ' '); i > 0 {
			return s.text[:i]
		}
		return s.text
	}
	for _, it := range items { // in item order, so the log reads stably
		k := it.ID
		s := cur[k]
		o, had := old[k]
		switch {
		case !had:
			switch s.kind {
			case KindPR, KindDraft:
				v.Announce("Migrants have arrived: " + ref(s))
			case KindIssue, KindEpic:
				v.Announce("A goblin is sighted: " + ref(s))
			}
		case s.color == "lred" && o.color != "lred":
			v.Announce("A dwarf has caught fire: " + ref(s) + " CI failing")
		case s.kind == KindRelease && s.color == "blue" && o.color != "blue":
			v.Announce("A caravan from " + ref(s) + " has crossed")
		}
	}
	var gone []string
	for k, o := range old {
		if _, still := cur[k]; !still {
			switch o.kind {
			case KindPR, KindDraft, KindQueued, KindRelease:
				gone = append(gone, ref(o))
			}
		}
	}
	sortStrings(gone)
	for _, g := range gone {
		v.Announce(g + " has left the fortress")
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Prepare rebuilds the raster and the placed items when the data (gen),
// the city, the zoom or the size changed; it is cheap otherwise. hasData
// says whether the branches/commits layer has landed (its first landing
// is everything at once, not news).
func (v *View) Prepare(h, w int, gen string, hasData bool) {
	v.h, v.w, v.gen, v.data = h, w, gen, hasData
	mw := max(1, w-Margin)
	mh := max(1, h-4)
	key := stateKey{v.City, v.Zoom, mw, mh, gen, hasData}
	if v.key != nil && *v.key == key && v.Map != nil {
		return
	}
	var items []*Item
	var lanes []Lane
	if v.Build != nil {
		items, lanes = v.Build()
	}
	room := mw - LegendW
	cx := 0.0
	if room > 30 {
		cx = float64(room) / 2.0
	}
	fmap := NewMap(v.City, mw, mh, v.Zoom, max(1, len(lanes)), cx)
	cells, hidden := Place(items, fmap, max(1, len(lanes)))
	sig := make(map[string]sigEntry, len(items))
	for _, it := range items {
		sig[it.ID] = sigEntry{it.Kind, it.Color, it.Text}
	}
	if v.key != nil && v.key.gen != gen && v.key.hasData == hasData {
		v.events(v.sig, sig, items)
	}
	follow := ""
	if len(cells) > 0 {
		follow = v.curID
	}
	v.key, v.Map, v.Items, v.Lanes, v.Cells, v.Hidden, v.sig = &key, fmap, items, lanes, cells, hidden, sig
	// the cursor stays on its thing when the thing moves
	if follow != "" {
		for _, it := range items {
			if it.ID == follow && it.Placed {
				v.Cur, v.hasCur = it.Cell, true
				break
			}
		}
	}
	if !v.hasCur || v.Cur.R < 0 || v.Cur.R >= fmap.Rows || v.Cur.C < 0 || v.Cur.C >= fmap.Cols {
		v.Cur, v.hasCur = v.first(), true
	}
}

func (v *View) first() Cell {
	if order := Order(v.Items); len(order) > 0 {
		return order[0].Cell
	}
	return Cell{v.Map.Rows / 2, v.Map.Cols / 2}
}

// Under is the items under the look cursor (newest state).
func (v *View) Under() []*Item {
	here := v.Cells[v.Cur]
	v.curID = ""
	if len(here) > 0 {
		v.curID = here[0].ID
	}
	return here
}

// Move steps the look cursor, clamped to the map.
func (v *View) Move(dr, dc int) {
	if v.Map == nil {
		return
	}
	v.Cur = Cell{min(v.Map.Rows-1, max(0, v.Cur.R+dr)), min(v.Map.Cols-1, max(0, v.Cur.C+dc))}
}

// Next walks the cursor to the next (dir > 0) or previous placed item.
func (v *View) Next(dir int) {
	order := Order(v.Items)
	if len(order) == 0 {
		return
	}
	i := -1
	for k, it := range order {
		if it.Cell == v.Cur {
			i = k
			break
		}
	}
	step := 1
	if dir < 0 {
		step = -1
	}
	i = mod(i+step, len(order))
	v.Cur = order[i].Cell
}

// SetZoom switches the zoom, keeping the cursor on what it looked at
// (the same item, else the same spot).
func (v *View) SetZoom(zoom string) {
	if zoom != ZoomFit {
		zoom = ZoomStreet
	}
	if zoom == v.Zoom || v.Map == nil {
		return
	}
	id := ""
	if under := v.Under(); len(under) > 0 {
		id = under[0].ID
	}
	y := v.Map.RowY(v.Cur.R)
	x := v.Map.ColX(v.Cur.C)
	v.Zoom, v.key = zoom, nil
	v.Prepare(v.h, v.w, v.gen, v.data)
	found := false
	for _, it := range v.Items {
		if id != "" && it.ID == id && it.Placed {
			v.Cur, found = it.Cell, true
			break
		}
	}
	if !found {
		r, c := v.Map.CellOf(x, y)
		v.Cur = Cell{r, c}
	}
	v.Cur = Cell{min(v.Map.Rows-1, max(0, v.Cur.R)), min(v.Map.Cols-1, max(0, v.Cur.C))}
}

// Counts are the title row's tallies.
func (v *View) Counts() (dwarves, goblins, ruins, statues, merchants int) {
	for _, it := range v.Items {
		switch it.Kind {
		case KindPR, KindDraft, KindQueued:
			dwarves++
		case KindIssue, KindEpic:
			goblins++
		case KindBranch:
			ruins++
		case KindCommit:
			statues++
		case KindRelease:
			merchants++
		}
	}
	return
}

// Draw paints the map, the look cursor, the announcements and the
// panels onto rows 1..h-3 (the header above, the status and key bar
// below). Prepare must have run for this (h, w).
func (v *View) Draw(c *canvas.Canvas, h, w int, f Frame) {
	if h < MinH || w < MinW {
		c.PutSegs(2, 1, []text.Seg{text.S(fmt.Sprintf("the fortress needs at least %dx%d; this terminal is %dx%d", MinW, MinH, w, h), "muted")}, 0, false, false)
		return
	}
	if v.Map == nil {
		v.Prepare(h, w, v.gen, v.data)
	}
	m := v.Map
	city := m.City
	top, x0 := 2, Margin
	mh, mw := h-4, w-x0
	// the viewport follows the cursor (it only moves when zoomed in)
	r, cc := v.Cur.R, v.Cur.C
	v.Top = min(max(0, m.Rows-mh), max(0, min(v.Top, r-3), r-mh+4))
	v.Left = min(max(0, m.Cols-mw), max(0, min(v.Left, cc-4), cc-mw+5))
	vt, vl := v.Top, v.Left
	// title row: Dwarf Fortress's status line
	dwarves, goblins, ruins, statues, merchants := v.Counts()
	sep := glyphs.G("sep")
	title := []text.Seg{
		text.S(" Fortress of "+city.Name, "strong"),
		text.S("  "+f.Season+"  ", "muted"),
		text.S(fmt.Sprintf("%d dwarves %s %d goblins %s %d ruins %s %d statues %s %d merchants",
			dwarves, sep, goblins, sep, ruins, sep, statues, sep, merchants), "faint"),
	}
	c.PutSegs(1, 0, title, w, false, false)
	// terrain
	for vr := 0; vr < mh; vr++ {
		mr := vt + vr
		if mr >= m.Rows {
			break
		}
		y := top + vr
		for vc := 0; vc < mw; vc++ {
			mc := vl + vc
			if mc >= m.Cols {
				break
			}
			g, color := Terrain(m.T[mr][mc], mr, mc, f.T, f.Still)
			if g == " " || g == "" {
				continue
			}
			c.PutRaw(y, x0+vc, g, SGR(color, false, false, f.Tier))
		}
		if label, ok := m.StreetRows[mr]; ok {
			c.PutSegs(y, 0, []text.Seg{text.S(text.Pad(label, x0-2)+glyphs.G("rule"), "faint")}, 0, false, false)
		}
	}
	for _, l := range m.Labels {
		if l.Row < vt || l.Row >= vt+mh {
			continue
		}
		for i, ch := range l.Text {
			x := x0 + l.Col - vl + i
			if x >= x0 && x < x0+mw {
				c.PutRaw(top+l.Row-vt, x, string(ch), SGR("lgray", false, false, f.Tier))
			}
		}
	}
	// the things themselves
	for cell, here := range v.Cells {
		mr, mc := cell.R, cell.C
		if mr < vt || mr >= vt+mh || mc < vl || mc >= vl+mw {
			continue
		}
		it := here[0]
		var g string
		if it.Kind == KindBranch {
			n := len(here)
			i := 2
			if n == 1 {
				i = 0
			} else if n <= 3 {
				i = 1
			}
			g = runeAt("df.ruin", i)
		} else {
			g = runeAt(Glyph(it, f.T, f.Still), 0)
		}
		dc := 0
		if it.Anim == AnimPace && !f.Still && (int(f.T*2)+mc)%4 == 1 && m.Placeable(mr, mc+1) {
			if _, taken := v.Cells[Cell{mr, mc + 1}]; !taken {
				dc = 1
			}
		}
		c.PutRaw(top+mr-vt, x0+mc-vl+dc, g, SGR(it.Color, it.Urgency <= 2, false, f.Tier))
	}
	// edge counters when zoomed in
	if v.Zoom != ZoomFit {
		above, below := 0, 0
		for cell := range v.Cells {
			if cell.R < vt {
				above++
			} else if cell.R >= vt+mh {
				below++
			}
		}
		if above > 0 {
			c.PutSegs(top, x0, []text.Seg{text.S(fmt.Sprintf(" %s %d more ", glyphs.G("df.up"), above), "chip.key")}, 0, false, false)
		}
		if below > 0 {
			c.PutSegs(top+mh-1, x0, []text.Seg{text.S(fmt.Sprintf(" %s %d more ", glyphs.G("df.down"), below), "chip.key")}, 0, false, false)
		}
	}
	// the look cursor
	if r >= vt && r < vt+mh && cc >= vl && cc < vl+mw {
		if f.Still || int(f.T*3)%3 != 0 {
			c.PutRaw(top+r-vt, x0+cc-vl, runeAt("df.cursor", 0), SGR("yellow", true, f.Still, f.Tier))
		}
	}
	// announcements, top left
	logs := v.Log
	if len(logs) > 3 {
		logs = logs[len(logs)-3:]
	}
	for i, e := range logs {
		c.PutSegs(top+i, x0, []text.Seg{text.S(" "+e.When+" ", "faint"), text.S(e.Msg+" ", "text")},
			x0+min(60, mw-LegendW-2), false, false)
	}
	v.drawPanels(c, h, w, f)
}

// drawPanels is the look panel and, under it, the legend: lower right.
func (v *View) drawPanels(c *canvas.Canvas, h, w int, f Frame) {
	m := v.Map
	city := m.City
	bw := min(LegendW, w-Margin-2)
	left := w - bw - 1
	var laneNames []string
	for i, l := range v.Lanes {
		name := l.Label
		if name == "" {
			name = l.Repo.Name
		}
		laneNames = append(laneNames, fmt.Sprintf("%d %s", i+1, name))
	}
	scale := fmt.Sprintf("1 row = %.0f days, not to scale (+ zooms)", m.UnitsPerRow())
	if v.Zoom != ZoomFit {
		scale = "1 row = 1 day (- fits the city)"
	}
	notes := []string{
		fmt.Sprintf("street = days open; %s = today", city.Zero),
		fmt.Sprintf("older: %s; past %dth St: %s", city.Older, city.Limit, city.Beyond),
		scale,
	}
	notes = append(notes, widgets.Wrap("lanes W to E: "+strings.Join(laneNames, " "), max(10, bw-4))...)
	notes = append(notes,
		fmt.Sprintf("barracks %s, depot %s", city.Barracks, city.Depot),
		"PRs the list did not carry: no CI read")
	if v.Hidden > 0 {
		notes = append(notes, fmt.Sprintf("%d not drawn for room: zoom in (+)", v.Hidden))
	}
	// look panel
	under := v.Under()
	r, cc := v.Cur.R, v.Cur.C
	var look []string
	if len(under) > 0 {
		it := under[0]
		look = []string{it.Text}
		if stacks(it.Kind) && len(under) > 1 {
			noun := "commits"
			if it.Kind == KindBranch {
				noun = "branches with no PR"
			}
			look = []string{fmt.Sprintf("%d %s here, e.g.", len(under), noun), it.Text}
		} else if len(under) > 1 {
			look = append(look, fmt.Sprintf("+%d more here", len(under)-1))
		}
		if it.Title != "" {
			look = append(look, it.Title)
		}
		if it.Zone == "" {
			look = append(look, fmt.Sprintf("%.0f days old", it.Age))
		}
	} else {
		what := m.What(r, cc)
		if what == "" {
			what = "?"
		}
		look = []string{what}
		if m.IsMain(r, cc) && m.RowY(r)*float64(city.Dir) >= 0 {
			age := int(abs(m.RowY(r)))
			look = append(look, fmt.Sprintf("%d St · %d days old here", age, age))
		}
	}
	lh := len(look) + 2
	legendH := len(Legend) + len(notes) + 3
	avail := h - 4
	if legendH+lh > avail {
		keep := max(0, avail-lh-len(Legend)-3)
		if keep < len(notes) {
			notes = notes[:keep]
		}
		legendH = len(Legend) + len(notes) + 3
	}
	ltop := max(2, h-2-legendH)
	if legendH <= avail {
		itTop, itLeft, iw := widgets.DrawBox(c, ltop, left, legendH, bw, "legend", "card.title", "card", -1)
		for i, row := range Legend {
			c.PutRaw(itTop+i, itLeft, runeAt(row.Glyph, 0), SGR(row.Color, true, false, f.Tier))
			c.PutSegs(itTop+i, itLeft+2, []text.Seg{text.S(row.Text, "muted")}, itLeft+iw, false, false)
		}
		for i, n := range notes {
			c.PutSegs(itTop+len(Legend)+1+i, itLeft, []text.Seg{text.S(n, "faint")}, itLeft+iw, false, false)
		}
	}
	ptop := ltop - lh
	if ptop >= 2 {
		itTop, itLeft, iw := widgets.DrawBox(c, ptop, left, lh, bw, "look", "card.title", "card", -1)
		for i, s := range look {
			st := "strong"
			if i > 0 {
				st = "text"
			}
			c.PutSegs(itTop+i, itLeft, []text.Seg{text.S(text.Ellip(s, iw), st)}, itLeft+iw, false, false)
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
