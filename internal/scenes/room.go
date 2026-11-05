package scenes

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/amos"
	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

// The room: while the release window is open the dashboard is the view
// out of a window. The glass is a small opening in the middle of the
// screen, crossed by a pair of mullions, and around it is the room you
// are sitting in at night: a lamp-lit wall, curtains tied back, plants,
// pictures, a wall clock, a mug of something hot on the sill and a brass
// plaque with the key that steps back out of it.
//
// Everything but the text is drawn in half-block pixels (two to a cell,
// so a pixel is square), in the room's own colors: like Amos and the
// fortress, decoration that carries no meaning keeps out of the theme's
// grays-blues-golds. The renderer downgrades the 24-bit colors on a
// 256- or 16-color terminal; with no color, or the ASCII glyph set, the
// room is line art.

// RoomMinW and RoomMinH are the smallest terminal the room draws in:
// below it the view through the glass would be too small to work in.
const (
	RoomMinW = 100
	RoomMinH = 30
)

// RoomLayout is where the glass is: the opening the dashboard is drawn
// in, in terminal cells.
type RoomLayout struct{ Y, X, H, W int }

// RoomFit lays the room out on an h x w screen; false when the screen is
// too small for a room around a usable window.
func RoomFit(h, w int) (RoomLayout, bool) {
	if w < RoomMinW || h < RoomMinH {
		return RoomLayout{}, false
	}
	iw := max(60, int(math.Round(float64(w)*0.55)))
	ih := max(16, int(math.Round(float64(h)*0.55)))
	side := (w - iw) / 2
	top := max(4, int(float64(h-ih)*0.36))
	if side < 14 || h-top-ih < 8 {
		return RoomLayout{}, false
	}
	return RoomLayout{Y: top, X: side, H: ih, W: iw}, true
}

// RoomInfo is what changes from frame to frame.
type RoomInfo struct {
	Tier   theme.Tier
	Clock  time.Time // the wall clock's time, already in its zone
	Moving bool      // ambient motion: the plants sway, the steam rises, the second hand sweeps
	T      float64   // the frame clock (seconds)
	Sign   string    // the plaque, e.g. "W · back to work"
}

// RoomAnimated says whether the room has anything that moves at a tier
// (the line-art room is still).
func RoomAnimated(tier theme.Tier) bool { return pixelRoom(tier) }

func pixelRoom(tier theme.Tier) bool {
	return tier >= theme.Color16 && glyphs.Active() != glyphs.ASCII
}

// Room paints the room behind the window: call it before the
// dashboard's frame is blitted into the opening, then RoomFront.
func Room(c *canvas.Canvas, l RoomLayout, in RoomInfo) {
	if !pixelRoom(in.Tier) {
		lineRoom(c, l, in)
		return
	}
	st := staticRoom(c.H, c.W, l, in)
	c.Blit(st.cells, 0, 0)
	// the moving parts, drawn over a copy of the still pixels in their
	// own boxes and turned into cells there only
	p := st.px.clone()
	t := 0.0
	if in.Moving {
		t = in.T
	}
	for _, b := range st.boxes {
		b.draw(p, t, in)
		p.cells(c, b.r, nil)
	}
	steam(c, p, st.mug, in)
}

// RoomFront paints what is in front of the glass, over the dashboard:
// the mullions, a faint glare, and the night sky where the view is blank.
func RoomFront(c *canvas.Canvas, l RoomLayout, in RoomInfo) {
	my, mx := l.Y+l.H/2, l.X+l.W/2
	if !pixelRoom(in.Tier) {
		v, hz, x := "|", "-", "+"
		if glyphs.Active() != glyphs.ASCII {
			v, hz, x = "│", "─", "┼"
		}
		for y := l.Y; y < l.Y+l.H; y++ {
			c.PutStr(y, mx, v, "art")
		}
		for x := l.X; x < l.X+l.W; x++ {
			c.PutStr(my, x, hz, "art")
		}
		c.PutStr(my, mx, x, "art")
		return
	}
	night := rgb{0, 0, 0}
	// the sky first, so nothing of it lands on the bars
	sky(c, l, in)
	// a faint glare: two short streaks in the first pane, from the
	// bottom left round, with blank glass for them (never over a letter)
	glare := sgrFg(rgb{42, 46, 56}, night)
	streaks := func(py, px int, put bool) bool {
		for s, n := range []int{3, 2} {
			x0 := px - 4 - 3*s
			for i := 0; i < n; i++ {
				y, x := py+1+i, x0-2*i
				if !c.Blank(y, x) {
					return false
				}
				if put {
					c.PutRaw(y, x, "╱", glare)
				}
			}
		}
		return true
	}
	for _, at := range [][2]int{{my + 1, mx - 2}, {my + 1, l.X + l.W - 2}, {l.Y, mx - 2}, {l.Y, l.X + l.W - 2}} {
		if streaks(at[0], at[1], false) {
			streaks(at[0], at[1], true)
			break
		}
	}
	bar := sgrFg(cream, night)
	shade := sgrFg(creamShade, night)
	for y := l.Y; y < l.Y+l.H; y++ {
		c.PutRaw(y, mx, "┃", bar)
	}
	for x := l.X; x < l.X+l.W; x++ {
		c.PutRaw(my, x, "━", shade)
	}
	c.PutRaw(my, mx, "╋", bar)
}

// sky puts a few stars and, where the glass is blank enough, a crescent
// moon in the top panes: the night outside, behind the dashboard.
func sky(c *canvas.Canvas, l RoomLayout, in RoomInfo) {
	seed := uint64(l.W*7919 + l.H)
	rnd := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	n := max(4, l.W*l.H/90)
	for i := 0; i < n; i++ {
		y := l.Y + rnd.IntN(l.H)
		x := l.X + rnd.IntN(l.W)
		period, off := 3+rnd.Float64()*4, rnd.Float64()
		b := 0.55
		if in.Moving {
			b = 0.35 + 0.4*(0.5+0.5*math.Sin(2*math.Pi*(in.T/period+off)))
		}
		if !c.Blank(y, x) {
			continue
		}
		g := "·"
		if rnd.Float64() < 0.2 {
			g = "•"
		}
		c.PutRaw(y, x, g, sgrFg(mix(rgb{0, 0, 0}, rgb{150, 160, 196}, b), rgb{0, 0, 0}))
	}
	// the moon, high in a pane, only on glass blank all round it
	blank := func(my, mx int) bool {
		for y := my - 1; y <= my+3; y++ {
			for x := mx - 2; x <= mx+7; x++ {
				if !c.Blank(y, x) {
					return false
				}
			}
		}
		return true
	}
	my, mx := -1, -1
	for _, at := range [][2]int{{l.Y + 2, l.X + l.W - 12}, {l.Y + 2, l.X + l.W/2 - 12}, {l.Y + l.H/2 + 2, l.X + l.W - 12}, {l.Y + l.H/2 + 2, l.X + l.W/2 - 12}} {
		if blank(at[0], at[1]) {
			my, mx = at[0], at[1]
			break
		}
	}
	if my < 0 {
		return
	}
	p := newPix(6, 6)
	moon, dark := rgb{236, 230, 200}, rgb{0, 0, 0}
	for py := 0; py < 6; py++ {
		for px := 0; px < 6; px++ {
			dx, dy := float64(px)-2.5, float64(py)-2.5
			if math.Hypot(dx, dy) <= 2.9 && math.Hypot(dx+1.6, dy-0.6) > 2.5 {
				p.set(px, py, moon)
			} else {
				p.set(px, py, dark)
			}
		}
	}
	for r := 0; r < 3; r++ {
		for x := 0; x < 6; x++ {
			top, bot := p.at(x, 2*r), p.at(x, 2*r+1)
			if top == dark && bot == dark {
				continue
			}
			g, s := cellOf(top, bot)
			c.PutRaw(my+r, mx+x, g, s)
		}
	}
}

// ---- colors ----

type rgb struct{ r, g, b uint8 }

func mix(a, b rgb, t float64) rgb {
	t = math.Max(0, math.Min(1, t))
	f := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return rgb{f(a.r, b.r), f(a.g, b.g), f(a.b, b.b)}
}

func (c rgb) add(d int) rgb {
	f := func(x uint8) uint8 { return uint8(max(0, min(255, int(x)+d))) }
	return rgb{f(c.r), f(c.g), f(c.b)}
}

// The room's colors: a warm wall in lamplight, cream-painted woodwork,
// teal velvet curtains, a walnut floor.
var (
	wallBase    = rgb{70, 50, 50}
	wallStripe  = rgb{64, 46, 46}
	wallMotif   = rgb{96, 70, 66}
	lampWarm    = rgb{150, 108, 76}
	cream       = rgb{226, 214, 190}
	creamHi     = rgb{244, 236, 218}
	creamShade  = rgb{168, 154, 132}
	curtainLo   = rgb{16, 50, 58}
	curtainHi   = rgb{52, 108, 114}
	brass       = rgb{190, 152, 74}
	brassHi     = rgb{226, 192, 112}
	brassLo     = rgb{130, 100, 46}
	walnut      = rgb{92, 62, 42}
	walnutLo    = rgb{60, 40, 28}
	terracotta  = rgb{178, 92, 62}
	terraHi     = rgb{204, 118, 82}
	terraLo     = rgb{132, 66, 44}
	soil        = rgb{52, 36, 28}
	leaf        = rgb{46, 116, 64}
	leafHi      = rgb{90, 158, 90}
	leafLo      = rgb{28, 82, 48}
	stem        = rgb{70, 112, 60}
	pothos      = rgb{96, 152, 70}
	pothosHi    = rgb{170, 196, 96}
	ink         = rgb{40, 30, 26}
	matte       = rgb{28, 34, 54}
	faceCream   = rgb{240, 232, 212}
	secondHand  = rgb{196, 60, 52}
	shadeLit    = rgb{246, 220, 160}
	shadeEdge   = rgb{255, 238, 196}
	bronze      = rgb{70, 58, 46}
	mugBlue     = rgb{70, 112, 168}
	mugBlueLo   = rgb{48, 80, 128}
	coffee      = rgb{58, 36, 24}
	rugField    = rgb{118, 40, 52}
	rugBorder   = rgb{196, 152, 64}
	rugMotif    = rgb{226, 206, 168}
	cactus      = rgb{62, 132, 82}
	cactusHi    = rgb{120, 176, 116}
	shadowTint  = 0.35
	floorShadow = rgb{36, 24, 18}
)

var sgrCache sync.Map // [2]rgb + kind -> string

type sgrKey struct {
	fg, bg rgb
	kind   int // 0 fg+bg, 1 bg only
}

// sgrFg is a cell's SGR with a foreground and a background.
func sgrFg(fg, bg rgb) string {
	k := sgrKey{fg, bg, 0}
	if s, ok := sgrCache.Load(k); ok {
		return s.(string)
	}
	s := fmt.Sprintf("\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm", fg.r, fg.g, fg.b, bg.r, bg.g, bg.b)
	sgrCache.Store(k, s)
	return s
}

func sgrBg(bg rgb) string {
	k := sgrKey{bg: bg, kind: 1}
	if s, ok := sgrCache.Load(k); ok {
		return s.(string)
	}
	s := fmt.Sprintf("\x1b[48;2;%d;%d;%dm", bg.r, bg.g, bg.b)
	sgrCache.Store(k, s)
	return s
}

// cellOf is the glyph and SGR of a cell whose top and bottom pixels are
// top and bot.
func cellOf(top, bot rgb) (string, string) {
	if top == bot {
		return " ", sgrBg(top)
	}
	return "▀", sgrFg(top, bot)
}

// ---- the pixel buffer ----

// pix is a w x h grid of square pixels, two to a terminal cell (h is
// twice the rows).
type pix struct {
	w, h int
	p    []rgb
}

func newPix(w, h int) *pix { return &pix{w: w, h: h, p: make([]rgb, w*h)} }

func (p *pix) clone() *pix {
	q := &pix{w: p.w, h: p.h, p: make([]rgb, len(p.p))}
	copy(q.p, p.p)
	return q
}

func (p *pix) in(x, y int) bool { return x >= 0 && x < p.w && y >= 0 && y < p.h }

func (p *pix) set(x, y int, c rgb) {
	if p.in(x, y) {
		p.p[y*p.w+x] = c
	}
}

func (p *pix) at(x, y int) rgb {
	if !p.in(x, y) {
		return rgb{}
	}
	return p.p[y*p.w+x]
}

func (p *pix) rect(x0, y0, x1, y1 int, c rgb) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			p.set(x, y, c)
		}
	}
}

// line draws a one-pixel line.
func (p *pix) line(x0, y0, x1, y1 float64, c rgb) {
	n := int(math.Ceil(math.Max(math.Abs(x1-x0), math.Abs(y1-y0)))) + 1
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		p.set(int(math.Round(x0+(x1-x0)*t)), int(math.Round(y0+(y1-y0)*t)), c)
	}
}

// cellRect is a box of cells.
type cellRect struct{ y, x, h, w int }

// cells turns the pixels of r into cells on c; skip, when set, leaves a
// cell alone.
func (p *pix) cells(c *canvas.Canvas, r cellRect, skip func(y, x int) bool) {
	for y := r.y; y < r.y+r.h; y++ {
		for x := r.x; x < r.x+r.w; x++ {
			if x < 0 || x >= p.w || 2*y+1 >= p.h || y < 0 {
				continue
			}
			if skip != nil && skip(y, x) {
				continue
			}
			g, s := cellOf(p.at(x, 2*y), p.at(x, 2*y+1))
			c.PutRaw(y, x, g, s)
		}
	}
}

// ---- the still room, cached ----

// box is a moving part: its cells, and how it draws itself at time t.
type box struct {
	r    cellRect
	draw func(p *pix, t float64, in RoomInfo)
}

type roomKey struct {
	h, w int
	l    RoomLayout
	tier theme.Tier
	set  glyphs.Set
	sign string
}

type still struct {
	key   roomKey
	px    *pix
	cells *canvas.Canvas
	boxes []box
	mug   cellRect // the mug's cells; the steam rises from it
}

var (
	roomMu    sync.Mutex
	roomCache *still
)

// staticRoom is the room with nothing moving, built once per size.
func staticRoom(h, w int, l RoomLayout, in RoomInfo) *still {
	key := roomKey{h, w, l, in.Tier, glyphs.Active(), in.Sign}
	roomMu.Lock()
	defer roomMu.Unlock()
	if roomCache != nil && roomCache.key == key {
		return roomCache
	}
	s := buildRoom(h, w, l, in)
	s.key = key
	roomCache = s
	return s
}

// geometry is the room's layout in cells, worked out once.
type geometry struct {
	h, w             int
	l                RoomLayout
	floorY           int // first floor row
	sillY            int // the sill's row
	rodY             int // the curtain rod's row
	curtW            int // a curtain's width at the rod
	leftOut, rightIn int // the left curtain's outer column; the right one's outer column
	lampX, lampTop   int // the lamp's centre column and its shade's top pixel row
	glowX, glowY     float64
	glowR            float64
}

func layout(h, w int, l RoomLayout) geometry {
	g := geometry{h: h, w: w, l: l}
	floorRows := max(3, h/9)
	g.floorY = h - floorRows
	g.sillY = l.Y + l.H + 1
	g.rodY = l.Y - 2
	side := l.X
	g.curtW = max(5, min(9, side/4))
	g.leftOut = l.X - 2 - g.curtW + 1
	g.rightIn = l.X + l.W + 2 + g.curtW - 1
	// the lamp stands in the right-hand corner
	g.lampX = g.rightIn + max(6, (w-g.rightIn)*2/3)
	if g.lampX > w-6 {
		g.lampX = w - 6
	}
	shadeH := 7
	g.lampTop = 2*g.floorY - max(18, (g.floorY-l.Y)*2*9/20) - shadeH
	g.glowX, g.glowY = float64(g.lampX), float64(g.lampTop+shadeH)
	g.glowR = float64(w) * 0.26
	return g
}

// wallAt is the wall's color at pixel (x, y): striped paper, warmed by
// the lamp, a little darker toward the ceiling.
func (g geometry) wallAt(x, y int) rgb {
	c := wallBase
	if (x/3)%2 == 1 {
		c = wallStripe
	}
	dx, dy := float64(x)-g.glowX, float64(y)-g.glowY
	d2 := dx*dx + dy*dy
	glow := math.Exp(-d2 / (2 * g.glowR * g.glowR))
	c = mix(c, lampWarm, glow*0.8)
	top := float64(y) / float64(2*g.h)
	if top < 0.25 {
		c = mix(c, rgb{0, 0, 0}, (0.25-top)*0.6)
	}
	return c
}

func buildRoom(h, w int, l RoomLayout, in RoomInfo) *still {
	g := layout(h, w, l)
	p := newPix(w, 2*h)
	s := &still{px: p}

	// the wall
	for y := 0; y < 2*g.floorY; y++ {
		for x := 0; x < w; x++ {
			p.set(x, y, g.wallAt(x, y))
		}
	}
	// crown molding
	for x := 0; x < w; x++ {
		p.set(x, 0, creamShade)
		p.set(x, 1, mix(creamShade, rgb{0, 0, 0}, 0.4))
	}
	floor(p, g)
	// the skirting board
	for x := 0; x < w; x++ {
		p.set(x, 2*g.floorY-3, creamHi)
		p.set(x, 2*g.floorY-2, cream)
		p.set(x, 2*g.floorY-1, creamShade)
	}
	window(p, g)
	curtains(p, g)
	lamp(p, g)

	// the things on the walls and the floor, each where it fits
	var placed []cellRect
	fits := func(r cellRect) bool {
		if r.x < 1 || r.y < 1 || r.x+r.w > w-1 || r.y+r.h > g.floorY+2 {
			return false
		}
		for _, q := range placed {
			if r.x < q.x+q.w && q.x < r.x+r.w && r.y < q.y+q.h && q.y < r.y+r.h {
				return false
			}
		}
		return true
	}
	leftZone := cellRect{y: 1, x: 0, h: g.floorY - 1, w: g.leftOut}
	rightZone := cellRect{y: 1, x: g.rightIn + 1, h: g.floorY - 1, w: w - g.rightIn - 1}
	placed = append(placed,
		cellRect{y: g.lampTop/2 - 1, x: g.lampX - 6, h: g.floorY - g.lampTop/2 + 1, w: 13},
		cellRect{y: g.rodY - 1, x: g.leftOut - 5, h: 3, w: g.rightIn - g.leftOut + 11}, // the rod and its finials
	)

	// the floor plant in the left corner, its leaves moving
	plantW := min(22, leftZone.w-2)
	plantH := min(18, g.floorY-l.Y-1)
	var plantRect cellRect
	if plantW >= 10 && plantH >= 8 {
		px := leftZone.x + (leftZone.w-plantW)/2
		plantRect = cellRect{y: g.floorY + 1 - plantH, x: px, h: plantH, w: plantW}
		pr := plantRect
		potW := max(6, plantW/2)
		potH := 5
		potX := pr.x + (pr.w-potW)/2
		potTop := 2*(g.floorY+1) - potH - 1
		pot(p, potX, potTop, potW, potH)
		s.boxes = append(s.boxes, box{r: cellRect{y: pr.y, x: pr.x, h: pr.h - 2, w: pr.w}, draw: func(q *pix, t float64, _ RoomInfo) {
			monstera(q, pr, potX+potW/2, potTop, t)
		}})
		placed = append(placed, pr)
	}

	// above the plant, top down: a hanging pothos, then Amos's portrait
	// (if the room has the colors for him), each where there is room
	above := g.floorY
	if plantRect.w > 0 {
		above = plantRect.y
	}
	aw, ah := amos.Size(amos.Options{Mini: true})
	portrait := cellRect{x: leftZone.x + max(1, (leftZone.w-(aw+6))/2), h: ah + 2, w: aw + 6}
	hang := cellRect{y: 0, x: leftZone.x + (leftZone.w-9)/2, w: 9}
	wantAmos := in.Tier >= theme.Color256
	switch {
	case wantAmos && above >= 9+portrait.h+5:
		hang.h = min(16, above-portrait.h-5)
		portrait.y = hang.h + 2
	case wantAmos:
		portrait.y = max(2, (above-portrait.h-1)/2)
	case above >= 11:
		hang.h = min(16, above-2)
	}
	if wantAmos && !fits(portrait) && hang.h == 0 {
		portrait.y = g.rodY + 3 // below the rod's end
	}
	showAmos := wantAmos && fits(portrait) && portrait.y+portrait.h+1 < above
	if showAmos {
		frame(p, portrait, brass, brassHi, brassLo, matte)
		picWire(p, portrait)
		placed = append(placed, cellRect{y: portrait.y - 2, x: portrait.x, h: portrait.h + 3, w: portrait.w})
	}

	// the clock, top right
	cd := 15
	if rightZone.w < 19 {
		cd = 11
	}
	clockR := cellRect{y: 2, x: rightZone.x + (rightZone.w-cd)/2, h: (cd + 1) / 2, w: cd}
	if !fits(clockR) {
		clockR.y = g.rodY + 2 // below the rod's end
	}
	if fits(clockR) {
		cr := clockR
		s.boxes = append(s.boxes, box{r: cr, draw: func(q *pix, t float64, in RoomInfo) { wallClock(q, cr, in) }})
		placed = append(placed, cr)
	}
	// a landscape under it
	lw := min(24, rightZone.w-3)
	land := cellRect{y: clockR.y + clockR.h + 2, x: rightZone.x + (rightZone.w-lw)/2, h: 8, w: lw}
	if lw >= 12 && fits(land) {
		landscape(p, land)
		picWire(p, land)
		placed = append(placed, cellRect{y: land.y - 2, x: land.x, h: land.h + 2, w: land.w})
	}

	if hang.h >= 9 && hang.x >= 1 {
		hp := hang
		hangingPot(p, hp)
		s.boxes = append(s.boxes, box{r: cellRect{y: hp.y + 4, x: hp.x - 1, h: hp.h - 4, w: hp.w + 2}, draw: func(q *pix, t float64, _ RoomInfo) {
			vines(q, hp, t)
		}})
	}

	// on the sill: a cactus on the left, the mug on the right
	cx := l.X - 2 - g.curtW + 2
	if cx >= 1 {
		cactusPot(p, cx, 2*g.sillY)
	}
	mx := l.X + l.W + 2 + g.curtW - 6
	mug(p, mx, 2*g.sillY)
	s.mug = cellRect{y: g.sillY - 2, x: mx, h: 2, w: 5}

	// the rug, under the window
	rug(p, g)

	// to cells, then the text and the sprite on top
	s.cells = canvas.New(w, h)
	p.cells(s.cells, cellRect{0, 0, h, w}, func(y, x int) bool {
		return y >= l.Y && y < l.Y+l.H && x >= l.X && x < l.X+l.W
	})
	// a motif in the paper, here and there
	for y := 2; y < g.floorY-1; y += 3 {
		for x := (y / 3 % 2) * 6; x < w; x += 12 {
			if y >= l.Y-3 && y <= g.sillY+1 && x >= g.leftOut-1 && x <= g.rightIn+1 {
				continue
			}
			top, bot := p.at(x, 2*y), p.at(x, 2*y+1)
			if top != bot || !isWall(g, x, 2*y, top) {
				continue
			}
			s.cells.PutRaw(y, x, "·", sgrFg(mix(top, wallMotif, 0.8), top))
		}
	}
	if showAmos {
		bg := amos.RGB{R: matte.r, G: matte.g, B: matte.b}
		for r, row := range amos.Cells(amos.Options{Mini: true, Bg: &bg}) {
			for i, ce := range row {
				s.cells.PutRaw(portrait.y+1+r, portrait.x+3+i, ce.Glyph, ce.SGR)
			}
		}
		// his name, on a little brass plate
		ny, nx := portrait.y+portrait.h, portrait.x+(portrait.w-6)/2
		for i, ch := range " amos " {
			s.cells.PutRaw(ny, nx+i, string(ch), sgrFg(ink, brass))
		}
	}
	plaque(s.cells, p, g, in.Sign)
	return s
}

// isWall: the pixel is the bare wall (nothing hung or standing on it).
func isWall(g geometry, x, y int, c rgb) bool { return c == g.wallAt(x, y) }

// floor: walnut boards, staggered, darker away from the lamp.
func floor(p *pix, g geometry) {
	y0 := 2 * g.floorY
	for y := y0; y < p.h; y++ {
		board := (y - y0) / 3
		for x := 0; x < p.w; x++ {
			c := walnut.add(int(hash3(board, 0, 7)%13) - 6)
			if (y-y0)%3 == 2 {
				c = walnutLo
			}
			if (x+board*17)%29 == 0 {
				c = walnutLo
			}
			if hash3(x, y, 3)%11 == 0 {
				c = c.add(-6)
			}
			// lamplight pools on the boards
			dx, dy := float64(x)-g.glowX, float64(y-y0)*2.2
			c = mix(c, lampWarm.add(10), math.Exp(-(dx*dx+dy*dy)/(2*g.glowR*g.glowR*0.5))*0.35)
			// nearer is lighter
			c = mix(c, rgb{0, 0, 0}, 0.25*(1-float64(y-y0)/float64(p.h-y0)))
			p.set(x, y, c)
		}
	}
}

// window: the cream frame round the glass and the sill under it.
func window(p *pix, g geometry) {
	l := g.l
	x0, x1 := l.X-2, l.X+l.W+2
	y0, y1 := 2*(l.Y-1), 2*(l.Y+l.H+1)
	p.rect(x0, y0, x1, y1, cream)
	for x := x0; x < x1; x++ {
		p.set(x, y0, creamHi)
		p.set(x, 2*l.Y-1, creamShade) // the inner edge, in shadow
		p.set(x, 2*(l.Y+l.H), creamShade)
	}
	for y := y0; y < y1; y++ {
		p.set(x0, y, creamHi)
		p.set(l.X-1, y, creamShade)
		p.set(l.X+l.W, y, creamShade)
		p.set(x1-1, y, mix(cream, creamShade, 0.5))
	}
	// the sill: a deeper ledge, wider than the frame, and its shadow
	sx0, sx1 := x0-g.curtW-1, x1+g.curtW+1
	sy := 2 * g.sillY
	for x := sx0; x < sx1; x++ {
		p.set(x, sy, creamHi)
		p.set(x, sy+1, cream)
		p.set(x, sy+2, creamShade)
		p.set(x, sy+3, mix(p.at(x, sy+3), rgb{0, 0, 0}, shadowTint))
	}
}

// curtains: teal velvet hanging from a brass rod, tied back, pleated.
func curtains(p *pix, g geometry) {
	l := g.l
	rodPy := 2*g.rodY + 1
	rx0, rx1 := g.leftOut-3, g.rightIn+4
	for x := rx0; x < rx1; x++ {
		p.set(x, rodPy, brass)
	}
	for _, fx := range []int{rx0 - 1, rx1} { // finials
		p.rect(fx-1, rodPy-1, fx+1, rodPy+2, brassHi)
		p.set(fx, rodPy, brassLo)
	}
	top := rodPy + 1
	tie := 2*(l.Y+l.H*55/100) + 1
	end := 2*g.sillY + 3
	tieW := max(2, g.curtW*2/5)
	widthAt := func(y int) float64 {
		if y <= tie {
			f := float64(y-top) / float64(max(1, tie-top))
			return float64(g.curtW) - float64(g.curtW-tieW)*math.Pow(f, 1.7)
		}
		f := float64(y-tie) / float64(max(1, end-tie))
		return float64(tieW) + (float64(g.curtW)*0.75-float64(tieW))*math.Sqrt(f)
	}
	pleats := math.Max(2, float64(g.curtW)/2.6)
	for y := top; y <= end; y++ {
		wd := widthAt(y)
		n := int(math.Round(wd))
		for i := 0; i < n; i++ {
			u := (float64(i) + 0.5) / wd
			s := 0.5 + 0.5*math.Cos(2*math.Pi*u*pleats)
			c := mix(curtainLo, curtainHi, s)
			if i == n-1 {
				c = mix(c, curtainHi, 0.5) // the leading edge catches the light
			}
			if y == end {
				c = mix(c, rgb{0, 0, 0}, 0.3)
			}
			if y == top && i%2 == 0 {
				c = brassLo // the rings
			}
			p.set(g.leftOut+i, y, c)
			p.set(g.rightIn-i, y, c)
		}
	}
	// the tie-backs, and a tassel
	for _, side := range []int{1, -1} {
		ox := g.leftOut
		if side < 0 {
			ox = g.rightIn
		}
		for i := -1; i <= tieW; i++ {
			x := ox + side*i
			p.set(x, tie-1, brassHi)
			p.set(x, tie, brass)
		}
		tx := ox + side*tieW
		p.set(tx, tie+1, brass)
		p.set(tx, tie+2, brassLo)
		p.set(tx+side, tie+2, brassLo)
		p.set(tx, tie+3, brassLo)
	}
}

// lamp: a floor lamp, its shade lit from inside.
func lamp(p *pix, g geometry) {
	x, top := g.lampX, g.lampTop
	shadeH := 7
	for i := 0; i < shadeH; i++ {
		half := 3 + i*3/shadeH*1 + i/3
		for dx := -half; dx <= half; dx++ {
			c := mix(shadeLit, lampWarm, math.Abs(float64(dx))/float64(half+2)*0.6)
			if i == shadeH-1 {
				c = shadeEdge
			}
			if i == 0 {
				c = mix(shadeLit, brassLo, 0.4)
			}
			p.set(x+dx, top+i, c)
		}
	}
	for y := top + shadeH; y < 2*g.floorY+3; y++ {
		p.set(x, y, bronze)
	}
	base := 2*g.floorY + 3
	for dx := -3; dx <= 3; dx++ {
		p.set(x+dx, base, bronze)
		p.set(x+dx, base+1, mix(bronze, rgb{0, 0, 0}, 0.4))
	}
}

// pot: a terracotta pot, rim and all, its top at pixel row top.
func pot(p *pix, x, top, w, h int) {
	for i := 0; i < h; i++ {
		in := 0
		if i >= 2 {
			in = (i - 1) / 2
		}
		for dx := in; dx < w-in; dx++ {
			c := terracotta
			if i < 2 {
				c = terraHi
				if i == 1 {
					c = terracotta.add(8)
				}
			}
			if dx >= w-in-2 {
				c = mix(c, terraLo, 0.6)
			}
			if dx == in && i >= 2 {
				c = terraHi
			}
			p.set(x+dx, top+i, c)
		}
	}
	for dx := 1; dx < w-1; dx++ {
		p.set(x+dx, top-1, soil)
	}
	// its shadow on the floor
	for dx := -1; dx <= w; dx++ {
		p.set(x+dx, top+h, mix(p.at(x+dx, top+h), floorShadow, 0.6))
	}
}

// monstera: a split-leaf plant on stems from the pot at (cx, top),
// swaying with t.
func monstera(p *pix, r cellRect, cx, top int, t float64) {
	H := float64(top - 2*r.y)
	type lf struct{ ang, reach, size, phase float64 }
	leaves := []lf{
		{-1.2, 0.55, 0.17, 0.0}, {1.15, 0.5, 0.16, 2.8}, {-0.7, 0.86, 0.19, 1.3}, {0.7, 0.8, 0.19, 1.9},
		{-0.22, 0.98, 0.2, 2.1}, {0.25, 0.62, 0.17, 0.7}, {-0.35, 0.42, 0.15, 2.9},
	}
	for _, f := range leaves {
		sway := math.Sin(2*math.Pi*(t/5.5)+f.phase) * 1.2 * f.reach
		bx, by := float64(cx), float64(top)
		lx := bx + math.Sin(f.ang)*H*f.reach*0.7 + sway
		ly := by - math.Cos(f.ang)*H*f.reach*0.86
		// the stem, a gentle curve
		mx, my := bx+math.Sin(f.ang)*H*f.reach*0.2, ly+(by-ly)*0.45
		for i := 0; i <= 24; i++ {
			s := float64(i) / 24
			x := (1-s)*(1-s)*bx + 2*(1-s)*s*mx + s*s*lx
			y := (1-s)*(1-s)*by + 2*(1-s)*s*my + s*s*ly
			p.set(int(math.Round(x)), int(math.Round(y)), stem)
		}
		leafAt(p, lx, ly, f.ang*0.9+sway*0.04, H*f.size, float64(int(f.phase*10)))
	}
}

// leafAt draws one monstera leaf centred on (x, y), pointing at angle a
// (from up), rx long, with its splits.
func leafAt(p *pix, x, y, a, rx, seed float64) {
	ry := rx * 0.78
	ca, sa := math.Cos(a), math.Sin(a)
	n := int(rx + 2)
	for dy := -n; dy <= n; dy++ {
		for dx := -n; dx <= n; dx++ {
			// leaf space: u along the midrib (up = +), v across
			u := -float64(dy)*ca + float64(dx)*sa
			v := float64(dx)*ca + float64(dy)*sa
			e := (u*u)/(rx*rx) + (v*v)/(ry*ry)
			if e > 1 {
				continue
			}
			// the notch where the stem meets the leaf
			if u < -rx*0.55 && math.Abs(v) < rx*0.22 {
				continue
			}
			// the splits, from the edge toward the midrib
			w := math.Abs(v) / ry
			if w > 0.42 {
				k := (u + rx) / (rx * 0.42)
				if f := k - math.Floor(k); f < 0.2 {
					continue
				}
			}
			base := leaf.add(int(seed)%5*4 - 8)
			c := base
			switch {
			case e > 0.72:
				c = leafLo // the edge, so leaves read apart
			case math.Abs(v) < 0.6:
				c = leafHi // the midrib
			case v > 0:
				c = mix(base, leafLo, w*0.7)
			default:
				c = mix(base, leafHi, (1-w)*0.4)
			}
			p.set(int(math.Round(x))+dx, int(math.Round(y))+dy, c)
		}
	}
}

// hangingPot: a cream pot on a macrame hanger from the ceiling.
func hangingPot(p *pix, r cellRect) {
	cx := r.x + r.w/2
	potTop := 2*r.y + 7
	cord := rgb{150, 128, 98}
	p.line(float64(cx), 2, float64(r.x+1), float64(potTop-1), cord)
	p.line(float64(cx), 2, float64(r.x+r.w-2), float64(potTop-1), cord)
	p.set(cx, 2, brassHi)
	p.set(cx, 3, brassLo)
	for i := 0; i < 4; i++ {
		in := i / 2
		for dx := r.x + in; dx < r.x+r.w-in; dx++ {
			c := cream
			if dx >= r.x+r.w-in-2 {
				c = creamShade
			}
			if i == 0 {
				c = creamHi
			}
			p.set(dx, potTop+i, c)
		}
	}
}

// vines: the trailing pothos, its strands swinging with t.
func vines(p *pix, r cellRect, t float64) {
	potBot := 2*r.y + 11
	long := float64(2*(r.y+r.h) - potBot - 1)
	strands := []struct{ x, n, ph float64 }{
		{float64(r.x + 1), long * 0.6, 0.4}, {float64(r.x + 3), long, 1.7},
		{float64(r.x + r.w - 3), long * 0.82, 2.6}, {float64(r.x + r.w - 1), long * 0.5, 0.9},
	}
	for _, s := range strands {
		for i := 0.0; i < s.n; i++ {
			f := i / s.n
			x := s.x + math.Sin(i/5+s.ph)*0.8 + math.Sin(2*math.Pi*(t/6.5)+s.ph)*1.6*f*f
			y := float64(potBot) + i
			xi, yi := int(math.Round(x)), int(math.Round(y))
			p.set(xi, yi, stem)
			if int(i)%3 == 1 {
				side := 1
				if int(i/3)%2 == 0 {
					side = -1
				}
				c := pothos
				if int(i+s.ph*3)%5 == 0 {
					c = pothosHi
				}
				p.set(xi+side, yi, c)
				p.set(xi+2*side, yi, c)
				p.set(xi+side, yi+1, mix(c, leafLo, 0.4))
			}
		}
	}
}

// cactusPot: a small cactus in a pot standing on the sill at pixel row y.
func cactusPot(p *pix, x, y int) {
	pot(p, x, y-4, 5, 4)
	for i := 0; i < 7; i++ {
		c := cactus
		p.set(x+2, y-6-i, c)
		p.set(x+1, y-6-i, mix(c, cactusHi, 0.4))
		p.set(x+3, y-6-i, mix(c, leafLo, 0.4))
	}
	p.set(x+2, y-13, cactusHi)
	p.set(x, y-9, cactus)
	p.set(x, y-10, cactus)
	p.set(x, y-11, cactusHi)
	p.set(x+4, y-8, cactus)
	p.set(x+4, y-9, cactusHi)
	p.set(x+1, y-12, rgb{236, 120, 150}) // in flower
}

// mug: a blue mug of coffee on the sill at pixel row y.
func mug(p *pix, x, y int) {
	for i := 0; i < 4; i++ {
		for dx := 0; dx < 4; dx++ {
			c := mugBlue
			if dx == 3 {
				c = mugBlueLo
			}
			if dx == 0 {
				c = mugBlue.add(24)
			}
			if i == 0 {
				c = coffee
				if dx == 0 || dx == 3 {
					c = mugBlue.add(30)
				}
			}
			p.set(x+dx, y-4+i, c)
		}
	}
	p.set(x+4, y-3, mugBlueLo)
	p.set(x+4, y-2, mugBlueLo)
}

// steam rises from the mug: wisps drifting up and fading.
func steam(c *canvas.Canvas, p *pix, m cellRect, in RoomInfo) {
	wisps := []string{"(", ")", "~"}
	if glyphs.Active() == glyphs.ASCII {
		return
	}
	n := 3
	for k := 0; k < n; k++ {
		ph := float64(k) / float64(n)
		if in.Moving {
			ph = math.Mod(in.T/2.6+ph, 1)
		}
		y := m.y - 1 - int(ph*3.5)
		x := m.x + 1 + k%2 + int(math.Round(math.Sin(ph*5+float64(k))*0.8))
		bg := mix(p.at(x, 2*y), p.at(x, 2*y+1), 0.5)
		fg := mix(bg, rgb{236, 232, 226}, 0.85*(1-ph*0.8))
		c.PutRaw(y, x, wisps[(k+int(ph*4))%len(wisps)], sgrFg(fg, bg))
	}
}

// frame: a picture frame round r with a plain inside.
func frame(p *pix, r cellRect, c, hi, lo, inside rgb) {
	x0, y0 := r.x, 2*r.y
	x1, y1 := r.x+r.w, 2*(r.y+r.h)
	p.rect(x0, y0, x1, y1, c)
	for x := x0; x < x1; x++ {
		p.set(x, y0, hi)
		p.set(x, y1-1, lo)
	}
	for y := y0; y < y1; y++ {
		p.set(x0, y, hi)
		p.set(x1-1, y, lo)
	}
	p.rect(x0+1, y0+1, x1-1, y1-1, mix(c, lo, 0.5))
	p.rect(x0+2, y0+2, x1-2, y1-2, inside)
	// its shadow on the wall
	for y := y0 + 1; y <= y1; y++ {
		p.set(x1, y, mix(p.at(x1, y), rgb{0, 0, 0}, shadowTint))
	}
	for x := x0 + 1; x <= x1; x++ {
		p.set(x, y1, mix(p.at(x, y1), rgb{0, 0, 0}, shadowTint))
	}
}

// picWire: the wire and nail a picture hangs from.
func picWire(p *pix, r cellRect) {
	cx, ny := r.x+r.w/2, 2*r.y-3
	p.line(float64(cx), float64(ny), float64(r.x+2), float64(2*r.y), rgb{54, 44, 40})
	p.line(float64(cx), float64(ny), float64(r.x+r.w-3), float64(2*r.y), rgb{54, 44, 40})
	p.set(cx, ny, brassHi)
}

// landscape: a little painting of mountains at sunset over a lake, in a
// walnut frame.
func landscape(p *pix, r cellRect) {
	frame(p, r, walnut, walnut.add(30), walnutLo, rgb{})
	x0, y0 := r.x+2, 2*r.y+2
	w, h := r.w-4, 2*r.h-4
	horizon := y0 + h*3/5
	sunX, sunY := float64(x0)+float64(w)*0.66, float64(horizon)-1.5
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			var c rgb
			if y < horizon {
				f := float64(y-y0) / float64(horizon-y0)
				c = mix(rgb{36, 46, 104}, rgb{120, 70, 120}, f*1.2)
				if f > 0.55 {
					c = mix(c, rgb{246, 150, 86}, (f-0.55)/0.45)
				}
				if math.Hypot(float64(x)-sunX, float64(y)-sunY) < 2.6 {
					c = rgb{255, 214, 120}
				}
				// the far range, then the near one
				fx := float64(x - x0)
				far := float64(horizon) - 3.2 - 2.4*math.Abs(math.Sin(fx/3.1)) - 1.4*math.Sin(fx/1.7+1)
				near := float64(horizon) - 1.2 - 3.4*math.Max(0, math.Sin(fx/4.3+2.2)) - 0.6*math.Sin(fx/1.3)
				if float64(y) > far {
					c = mix(rgb{126, 92, 132}, rgb{246, 150, 86}, 0.15)
				}
				if float64(y) > near {
					c = rgb{58, 52, 86}
				}
			} else {
				f := float64(y-horizon) / float64(y0+h-horizon)
				c = mix(rgb{170, 100, 110}, rgb{30, 40, 86}, f)
				if math.Abs(float64(x)-sunX) < 1.6-f && (y-horizon)%2 == 0 {
					c = rgb{246, 196, 120} // the sun on the water
				}
			}
			p.set(x, y, c)
		}
	}
}

// wallClock: a round clock with the time in in.Clock; the second hand
// only while things move.
func wallClock(p *pix, r cellRect, in RoomInfo) {
	d := float64(r.w)
	R := d / 2
	cx, cy := float64(r.x)+R-0.5, float64(2*r.y)+R-0.5
	for y := 2 * r.y; y < 2*(r.y+r.h); y++ {
		for x := r.x; x < r.x+r.w; x++ {
			dist := math.Hypot(float64(x)-cx, float64(y)-cy)
			switch {
			case dist <= R-1.3:
				p.set(x, y, faceCream)
			case dist <= R:
				c := walnut
				if float64(y) < cy {
					c = walnut.add(26)
				}
				p.set(x, y, c)
			}
		}
	}
	for i := 0; i < 12; i++ {
		a := float64(i) * math.Pi / 6
		c := mix(faceCream, ink, 0.35)
		if i%3 == 0 {
			c = ink
		}
		p.set(int(math.Round(cx+math.Sin(a)*(R-2.2))), int(math.Round(cy-math.Cos(a)*(R-2.2))), c)
	}
	t := in.Clock
	mins := float64(t.Minute()) + float64(t.Second())/60
	hrs := float64(t.Hour()%12) + mins/60
	hand := func(frac, length float64, c rgb) {
		a := frac * 2 * math.Pi
		p.line(cx, cy, cx+math.Sin(a)*length, cy-math.Cos(a)*length, c)
	}
	hand(hrs/12, R*0.5, ink)
	hand(mins/60, R*0.78, ink)
	if in.Moving {
		hand(float64(t.Second())/60, R*0.8, secondHand)
	}
	p.set(int(math.Round(cx)), int(math.Round(cy)), brassLo)
}

// rug: a patterned rug on the floor under the window.
func rug(p *pix, g geometry) {
	l := g.l
	y0, y1 := 2*g.floorY+3, p.h-2
	if y1-y0 < 4 {
		return
	}
	x0, x1 := l.X+4, l.X+l.W-4
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			c := rugField
			bx := min(x-x0, x1-1-x)
			by := min(y-y0, y1-1-y)
			switch {
			case bx < 2 || by < 1:
				c = rugBorder
			case bx < 3 || by < 2:
				c = rugMotif
			default:
				// a row of diamonds down the middle
				cy := float64(y0+y1-1) / 2
				dd := math.Abs(float64((x-x0)%12)-6) + math.Abs(float64(y)-cy)*1.6
				if dd < 2.5 {
					c = rugBorder
				} else if dd < 3.6 {
					c = rugMotif
				}
			}
			p.set(x, y, c)
		}
	}
	for y := y0; y < y1; y++ { // the fringe
		if y%2 == 0 {
			p.set(x0-1, y, rugMotif)
			p.set(x1, y, rugMotif)
		}
	}
}

// plaque: the brass plaque under the window with the key that leaves
// the room.
func plaque(c *canvas.Canvas, p *pix, g geometry, sign string) {
	if sign == "" {
		return
	}
	l := g.l
	label := "  " + sign + "  "
	w := text.Width(label)
	y := g.sillY + 2
	if y >= g.floorY-1 {
		return
	}
	x := l.X + (l.W-w)/2
	for i, ch := range []rune(label) {
		fg := ink
		if i == 0 || i == w-1 {
			fg = brassLo
		}
		g := string(ch)
		if i == 0 || i == w-1 {
			g = "•"
		}
		c.PutRaw(y, x+i, g, sgrFg(fg, brass))
	}
	// its edges and shadow, in pixels
	for i := -1; i <= w; i++ {
		top := p.at(x+i, 2*y-1)
		c.PutRaw(y-1, x+i, "▄", sgrFg(brassHi, top))
		bot := p.at(x+i, 2*y+2)
		c.PutRaw(y+1, x+i, "▀", sgrFg(brassLo, mix(bot, rgb{0, 0, 0}, shadowTint)))
	}
	for _, i := range []int{-1, w} {
		c.PutRaw(y, x+i, " ", sgrBg(brassLo))
	}
}

// hash3 is a small integer hash for texture.
func hash3(a, b, c int) uint32 {
	h := uint32(a)*374761393 + uint32(b)*668265263 + uint32(c)*2246822519
	h = (h ^ (h >> 13)) * 1274126177
	return h ^ (h >> 16)
}

// ---- the line-art room (no color, or ASCII) ----

func lineRoom(c *canvas.Canvas, l RoomLayout, in RoomInfo) {
	ascii := glyphs.Active() == glyphs.ASCII
	h, w := c.H, c.W
	g := layout(h, w, l)
	hz, vt, tl, tr, bl, br := "═", "║", "╔", "╗", "╚", "╝"
	curtain, floorG, sill := "▒", "─", "▀"
	if ascii {
		hz, vt, tl, tr, bl, br = "=", "|", "+", "+", "+", "+"
		curtain, floorG, sill = ":", "_", "="
	}
	for x := l.X - 2; x < l.X+l.W+2; x++ {
		c.PutStr(l.Y-1, x, hz, "art")
		c.PutStr(l.Y+l.H, x, hz, "art")
	}
	for y := l.Y - 1; y <= l.Y+l.H; y++ {
		c.PutStr(y, l.X-2, vt, "art")
		c.PutStr(y, l.X+l.W+1, vt, "art")
	}
	c.PutStr(l.Y-1, l.X-2, tl, "art")
	c.PutStr(l.Y-1, l.X+l.W+1, tr, "art")
	c.PutStr(l.Y+l.H, l.X-2, bl, "art")
	c.PutStr(l.Y+l.H, l.X+l.W+1, br, "art")
	for x := l.X - 2 - g.curtW; x < l.X+l.W+2+g.curtW; x++ {
		c.PutStr(g.sillY, x, sill, "art")
		c.PutStr(g.rodY, x, hz, "art")
	}
	tie := l.Y + l.H*55/100
	for y := g.rodY + 1; y <= g.sillY; y++ {
		n := g.curtW
		if y >= tie-1 && y <= tie+1 {
			n = max(2, g.curtW/2)
		}
		for i := 0; i < n; i++ {
			c.PutStr(y, g.leftOut+i, curtain, "art")
			c.PutStr(y, g.rightIn-i, curtain, "art")
		}
	}
	for x := 0; x < w; x++ {
		c.PutStr(g.floorY, x, floorG, "art")
	}
	plant := []string{`  \ | /  `, ` --(@)-- `, `  / | \  `, `   \|/   `, `   [_]   `}
	if ascii {
		plant = []string{`  \ | /  `, ` --(@)-- `, `  / | \  `, `   \|/   `, `   [_]   `}
	}
	px := (g.leftOut - 9) / 2
	if px >= 1 {
		for i, ln := range plant {
			c.PutStr(g.floorY-len(plant)+i, px, ln, "art")
		}
	}
	if in.Sign != "" {
		label := "[ " + in.Sign + " ]"
		c.PutStr(g.sillY+2, l.X+(l.W-text.Width(label))/2, label, "hdr.app")
	}
}
