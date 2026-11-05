package scenes

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sync"

	"github.com/solcyber/gh-dillodash/internal/amos"
	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

// The psychedelic launch: the release launch from 256 colors up. Every
// color comes from a few OKLab lightness levels (lvl), each a ring of
// hues at one brightness, so the whole thing cycles the spectrum without
// a flash; see fx.LaunchSecs for the rule and psyche_test.go for the
// check. The colors bypass the theme's palette on purpose (raw SGR, like
// Amos and the fortress map): the palette rules are for status, and the
// launch carries none.

type lvl int

const (
	lvPlasmaBG lvl = iota // the tunnel's ground
	lvPlasmaFG            // the tunnel's shading
	lvRipple              // the shockwave's bands, in the ground
	lvMid                 // smoke, cooling sparks, the dim chaser
	lvBright              // the rocket, sparks, the lit chaser
	lvLetter              // RELEASED!
	lvShadow              // the plate behind it, the porthole
	lvEcho                // its drop shadow
	nLvl
)

// lvlLC is each level's OKLab lightness and chroma.
var lvlLC = [nLvl][2]float64{
	lvPlasmaBG: {0.42, 0.16},
	lvPlasmaFG: {0.42, 0.16},
	lvRipple:   {0.51, 0.19},
	lvMid:      {0.70, 0.17},
	lvBright:   {0.84, 0.17},
	lvLetter:   {0.90, 0.16},
	lvShadow:   {0.24, 0.09},
	lvEcho:     {0.29, 0.10},
}

const hueSteps = 120 // 3 degrees

var levels = func() (t [nLvl][hueSteps]fx.RGB) {
	for l := range nLvl {
		for h := range hueSteps {
			t[l][h] = fx.OKLCH(lvlLC[l][0], lvlLC[l][1], float64(h)*360/hueSteps)
		}
	}
	return t
}()

// hue is the level's color at h degrees.
func hue(l lvl, h float64) fx.RGB { return levels[l][hueAt(h)] }

// hueAt is the hue step of h degrees.
func hueAt(h float64) int {
	i := int(math.Floor(h*hueSteps/360)) % hueSteps
	if i < 0 {
		i += hueSteps
	}
	return i
}

// groundSGR is every top-over-bottom pair of tunnel hues as one SGR
// prefix, per tier, built on first use: the tunnel paints every body
// cell every frame, and a table read is all it can afford.
var groundSGR = map[theme.Tier]*struct {
	once sync.Once
	t    [hueSteps][hueSteps]string
}{theme.TrueColor: {}, theme.Color256: {}}

func groundAt(tier theme.Tier, top, bot int) string {
	g := groundSGR[tier]
	g.once.Do(func() {
		for i := range hueSteps {
			for j := range hueSteps {
				g.t[i][j] = psySGR(levels[lvPlasmaFG][i], levels[lvPlasmaBG][j], true, tier)
			}
		}
	})
	return g.t[top][bot]
}

type sgrKey struct {
	fg, bg fx.RGB
	hasBG  bool
	tier   theme.Tier
}

var (
	sgrMu    sync.Mutex
	sgrCache = map[sgrKey]string{}
	near256  = map[fx.RGB]int{}
)

// psySGR is the raw SGR prefix for fg on bg (no bg: the cell's own) at
// a tier: 24-bit, or the xterm-256 index nearest in OKLab.
func psySGR(fg fx.RGB, bg fx.RGB, hasBG bool, tier theme.Tier) string {
	k := sgrKey{fg, bg, hasBG, tier}
	sgrMu.Lock()
	defer sgrMu.Unlock()
	if s, ok := sgrCache[k]; ok {
		return s
	}
	var s string
	if tier == theme.TrueColor {
		s = fmt.Sprintf("\x1b[38;2;%d;%d;%d", fg.R, fg.G, fg.B)
		if hasBG {
			s += fmt.Sprintf(";48;2;%d;%d;%d", bg.R, bg.G, bg.B)
		}
	} else {
		s = fmt.Sprintf("\x1b[38;5;%d", nearest256(fg))
		if hasBG {
			s += fmt.Sprintf(";48;5;%d", nearest256(bg))
		}
	}
	s += "m"
	if len(sgrCache) > 1<<16 { // a bound, not a policy: the levels make ~10k
		sgrCache = map[sgrKey]string{}
	}
	sgrCache[k] = s
	return s
}

// nearest256 is the xterm cube or gray index closest to c in OKLab, the
// lightness weighted double so a hue keeps its brightness on 256 colors.
// Called with sgrMu held.
func nearest256(c fx.RGB) int {
	if i, ok := near256[c]; ok {
		return i
	}
	L, a, b := fx.OKLab(c)
	best, bestD := 16, math.Inf(1)
	for i := 16; i < 256; i++ {
		r, g, bb := theme.XtermRGB(i)
		L2, a2, b2 := fx.OKLab(fx.RGB{R: r, G: g, B: bb})
		d := 4*(L-L2)*(L-L2) + (a-a2)*(a-a2) + (b-b2)*(b-b2)
		if d < bestD {
			best, bestD = i, d
		}
	}
	near256[c] = best
	return best
}

// psyche is one frame of the launch being drawn.
type psyche struct {
	c           *canvas.Canvas
	tier        theme.Tier
	h, w        int
	top, bottom int
	// ground is the tunnel's color under each body cell, when it covers
	// it: what sparks and letters draw on.
	ground  []fx.RGB
	covered []bool
	// hole is the collapsing middle from fadeAt (rho below it shows the
	// dashboard again); everything the launch draws gives way to it, so
	// the show leaves the way it came, a ring at a time.
	hole         float64
	cx, cy       float64
	halfW, halfH float64
}

// rho is a cell's distance from the middle, 0 there to 1 at the body's
// edge (a rectangle, so the tunnel fills the screen).
func (p *psyche) rho(y, x int) float64 {
	dx := (float64(x) - p.cx) * 0.5
	dy := float64(y) - p.cy
	return math.Max(math.Abs(dx)/p.halfW, math.Abs(dy)/p.halfH)
}

func (p *psyche) idx(y, x int) int { return (y-p.top)*p.w + x }

// put draws a glyph in fg on the ground, inside the body only.
func (p *psyche) put(y, x int, glyph string, fg fx.RGB) {
	if y < p.top || y > p.bottom || x < 0 || x >= p.w-1 || p.rho(y, x) < p.hole {
		return
	}
	i := p.idx(y, x)
	p.c.PutRaw(y, x, glyph, psySGR(fg, p.ground[i], p.covered[i], p.tier))
}

// putOn draws fg on an explicit background.
func (p *psyche) putOn(y, x int, glyph string, fg, bg fx.RGB) {
	if y < p.top || y > p.bottom || x < 0 || x >= p.w-1 || p.rho(y, x) < p.hole {
		return
	}
	p.c.PutRaw(y, x, glyph, psySGR(fg, bg, true, p.tier))
}

// rocketAt is where the rocket's nose is te seconds in: it climbs,
// accelerating, on a widening wiggle.
func (p *psyche) rocketAt(te float64) (y, x int) {
	q := fx.Clamp01(te / rocketSecs)
	q *= q
	y = int(math.Round(float64(p.bottom) - float64(p.bottom-p.top-1)*q))
	amp := math.Min(float64(p.w)/10, 14) * math.Min(1, te*1.5)
	x = (p.w-1)/2 + int(math.Round(math.Sin(te*5.5)*amp))
	return y, x
}

const (
	rocketSecs = 1.6 // the climb; the rocket bursts at the top
	wordAt     = 2.0 // RELEASED! starts to drop
	meltAt     = 6.6 // ... and starts to melt
	fadeAt     = 7.2 // the tunnel collapses back into the dashboard
)

// launchPsyche draws the launch at el seconds. top..bottom is the body.
func launchPsyche(c *canvas.Canvas, f *fx.Fx, h, w int, el float64, seed uint64, tier theme.Tier) {
	top, bottom := 1, h-3
	p := &psyche{c: c, tier: tier, h: h, w: w, top: top, bottom: bottom, hole: -1}
	p.cx, p.cy = float64(w-1)/2, float64(top+bottom)/2
	p.halfW, p.halfH = math.Max(1, float64(w)/4), math.Max(1, float64(bottom-top)/2+0.5)
	if el > fadeAt {
		p.hole = 1.2 * fx.EaseInOut((el-fadeAt)/(fx.LaunchSecs-fadeAt-0.15))
	}
	rows := bottom - top + 1
	p.ground = make([]fx.RGB, rows*w)
	p.covered = make([]bool, rows*w)

	_, bx := p.rocketAt(rocketSecs)
	by := top + 1
	p.tunnel(el, by, bx)
	p.chaser(el)
	if el < rocketSecs+0.4 {
		p.rings(el)
		p.exhaust(el)
	}
	if el < rocketSecs {
		p.rocket(el)
	}
	p.galaxies(el, seed, by, bx)
	if el >= 3.4 {
		for n, x0 := range []int{w / 6, w - w/6} {
			for i, q := range fx.Particles(seed+uint64(x0), 50, el-3.4, bottom, x0, 2) {
				p.put(q.Y, q.X, q.Glyph, hue(lvBright, float64(i*37+n*90)+el*200))
			}
		}
	}
	if Mascot == "sprite" {
		p.amos(el)
	}
	p.word(el)
	p.stampede(el, seed)
}

// tunnel is the ground: a plasma folded into an eight-way kaleidoscope
// over a tunnel of rings streaming out of the middle, drawn in half
// blocks (two samples a cell, so twice the rows), every hue at one
// lightness. It grows in from the edges over the first 0.6 s and
// collapses into the middle from fadeAt. From the burst, three rainbow
// ripples roll out through it.
func (p *psyche) tunnel(el float64, by, bx int) {
	cx, cy := p.cx, p.cy
	halfW, halfH := p.halfW, p.halfH
	grow := 1.15 * fx.EaseOut(el/0.6)
	hole := p.hole
	const seg = math.Pi / 4
	spin := el * 0.6
	// the pattern scales with the screen: the same number of rings and
	// petals on a laptop as on a wall
	k := 24 / math.Max(12, math.Min(halfH*2, halfW*2))
	sample := func(dx, dy float64) float64 {
		r := math.Hypot(dx, dy)
		a := math.Mod(math.Atan2(dy, dx)+spin, 2*seg)
		if a < 0 {
			a += 2 * seg
		}
		if a > seg {
			a = 2*seg - a
		}
		kx, ky := r*fcos(a)*k, r*fsin(a)*k
		depth := 14/(r*k+2.5) + el*1.8
		v := fsin(depth*2.4+a/seg*3) + fsin(kx*0.35+el*1.3) + fsin(ky*0.5-el*1.9) + fsin((kx+ky)*0.2+el)
		return depth*130 + v*55 + el*90
	}
	ripple := el >= rocketSecs && el < rocketSecs+1.8
	upper := "▀"
	for y := p.top; y <= p.bottom; y++ {
		dy := float64(y) - cy
		for x := 0; x < p.w-1; x++ {
			dx := (float64(x) - cx) * 0.5
			rho := math.Max(math.Abs(dx)/halfW, math.Abs(dy)/halfH)
			if rho < 1-grow || rho < hole {
				continue
			}
			ti, bi := hueAt(sample(dx, dy-0.25)), hueAt(sample(dx, dy+0.25))
			sgr := ""
			bot := levels[lvPlasmaBG][bi]
			if ripple {
				d := math.Hypot(float64(x-bx)*0.5, float64(y-by))
				for n := 0; n < 3; n++ {
					rn := (el-rocketSecs)*40 - float64(n)*5
					if rn > 0 && math.Abs(d-rn) < 0.8 {
						bot = hue(lvRipple, float64(n)*120+el*300+d*6)
						sgr = psySGR(bot, bot, true, p.tier)
						break
					}
				}
			}
			i := p.idx(y, x)
			p.ground[i], p.covered[i] = bot, true
			if sgr == "" {
				sgr = groundAt(p.tier, ti, bi)
			}
			p.c.PutRaw(y, x, upper, sgr)
		}
	}
}

// chaser runs rainbow lights round the body's edge. Each cell steps
// between lit and dim three times a second, no faster.
func (p *psyche) chaser(el float64) {
	block := glyphs.G("block")
	w, top, bottom := p.w, p.top, p.bottom
	rows := bottom - top + 1
	per := 2*(w-1) + 2*rows
	step := int(el * 3)
	for i := 0; i < per; i++ {
		var y, x int
		switch {
		case i < w-1:
			y, x = top, i
		case i < w-1+rows:
			y, x = top+i-(w-1), w-2
		case i < 2*(w-1)+rows:
			y, x = bottom, w-2-(i-(w-1)-rows)
		default:
			y, x = bottom-(i-2*(w-1)-rows), 0
		}
		l := lvMid
		if (i/3+step)%2 == 0 {
			l = lvBright
		}
		p.put(y, x, block, hue(l, float64(i)*9-el*240))
	}
}

// rocket is the rocket itself: a solid hull in rainbow bands scrolling
// up through the spectrum, a porthole in the opposite hue, and a flame
// that flickers through the colors.
func (p *psyche) rocket(el float64) {
	ny, nx := p.rocketAt(el)
	art := fx.LaunchRocketBig
	x0 := nx - len([]rune(art[0]))/2
	block := glyphs.G("block")
	for i, row := range art {
		hull := hue(lvBright, el*420+float64(i)*30)
		for j, ch := range []rune(row) {
			y, x := ny+i, x0+j
			switch ch {
			case '#':
				p.put(y, x, block, hull)
			case '/':
				p.put(y, x, "◢", hull)
			case '\\':
				p.put(y, x, "◣", hull)
			case 'A':
				p.put(y, x, "▲", hue(lvLetter, el*420+180))
			case 'O':
				p.putOn(y, x, "◉", hue(lvShadow, el*420+180), hull)
			}
		}
	}
	flame := []string{"▲", "*", "'"}
	fy := ny + len(art)
	for k, wd := range []int{5, 3, 1} {
		for j := 0; j < wd; j++ {
			g := flame[(int(el*12)+j+k)%len(flame)]
			p.put(fy+k, nx-wd/2+j, g, hue(lvBright, 20+float64(k)*25+el*800))
		}
	}
}

// exhaust is a spiral of sparks unwinding from the rocket's tail.
func (p *psyche) exhaust(el float64) {
	sparks := []rune(glyphs.G("spark"))
	tail := len(fx.LaunchRocketBig) + 3
	for k := 1; k <= 26; k++ {
		te := el - float64(k)*0.028
		if te < 0 || te > rocketSecs {
			continue
		}
		y0, x0 := p.rocketAt(te)
		ang := float64(k)*0.55 + el*9
		rad := float64(k) * 0.22
		l := lvBright
		if k > 13 {
			l = lvMid
		}
		p.put(y0+tail+int(math.Round(math.Sin(ang)*rad)), x0+int(math.Round(math.Cos(ang)*rad*2)),
			string(sparks[k%len(sparks)]), hue(l, te*600+float64(k)*12))
	}
}

// rings are the smoke rings the rocket leaves: puffed out every 0.2 s,
// spreading and turning through the hues as they go.
func (p *psyche) rings(el float64) {
	tail := len(fx.LaunchRocketBig) + 2
	for i := 0; float64(i)*0.2 <= math.Min(el, rocketSecs); i++ {
		te := float64(i) * 0.2
		age := el - te
		if age > 1.2 {
			continue
		}
		y0, x0 := p.rocketAt(te)
		y0 += tail
		r := age * 8
		n := max(8, int(r*7))
		g := "o"
		if age > 0.6 {
			g = "."
		}
		for k := 0; k < n; k++ {
			a := 2 * math.Pi * float64(k) / float64(n)
			p.put(y0+int(math.Round(math.Sin(a)*r)), x0+int(math.Round(math.Cos(a)*r*2)), g,
				hue(lvMid, te*500+age*180+float64(k)*360/float64(n)))
		}
	}
}

// galaxies are the fireworks: each burst throws out spiral arms that
// wheel round as they spread, sag and cool. The first is the rocket's
// own burst at the top of its climb.
func (p *psyche) galaxies(el float64, seed uint64, by, bx int) {
	sparks := []rune(glyphs.G("spark"))
	type burst struct {
		at     float64
		y, x   int
		hue    float64
		arms   int
		spin   float64
		radius float64
	}
	rnd := rand.New(rand.NewPCG(seed, seed+7))
	R := math.Max(3, math.Min(float64(p.bottom-p.top)/2.2, float64(p.w)/9))
	bs := []burst{{rocketSecs, by, bx, rnd.Float64() * 360, 5, 1, R * 1.2}}
	for i := 0; i < 8; i++ {
		ymax := max(p.top+2, (p.top+p.bottom)/2+2)
		dir := 1.0
		if rnd.IntN(2) == 0 {
			dir = -1
		}
		bs = append(bs, burst{
			at:  1.9 + 0.45*float64(i),
			y:   p.top + 2 + rnd.IntN(max(1, ymax-p.top-1)),
			x:   4 + rnd.IntN(max(1, p.w-8)),
			hue: rnd.Float64() * 360, arms: 3 + rnd.IntN(3), spin: dir,
			radius: R * (0.7 + 0.5*rnd.Float64()),
		})
	}
	const life, points = 1.9, 22
	for _, b := range bs {
		t := el - b.at
		if t < 0 || t >= life {
			continue
		}
		l := lvBright
		if t > 1.3 {
			l = lvMid
		}
		spread := b.radius * fx.EaseOut(t/0.7)
		for arm := 0; arm < b.arms; arm++ {
			for k := 1; k <= points; k++ {
				s := float64(k) / points
				ang := float64(arm)*2*math.Pi/float64(b.arms) + b.spin*(s*4.2+t*3.2)
				y := float64(b.y) + math.Sin(ang)*spread*s + 2.5*t*t
				x := float64(b.x) + math.Cos(ang)*spread*s*2
				p.put(int(math.Round(y)), int(math.Round(x)), string(sparks[(k+arm)%len(sparks)]),
					hue(l, b.hue+s*160+t*120))
			}
		}
		if t < 0.5 {
			p.put(b.y, b.x, string(sparks[len(sparks)-1]), hue(lvLetter, b.hue+t*400))
		}
	}
}

// amos tumbles through the tunnel, head over heels, turning through
// every hue at his own lightness.
func (p *psyche) amos(el float64) {
	const from, to = 2.4, 7.8
	if el < from || el >= to {
		return
	}
	u := (el - from) / (to - from)
	o := amos.Options{Mini: true, Mirror: int(el*1.25)%2 == 1, Frame: int(el * 6)}
	g := amos.Grid(o)
	if int(el*2.5)%2 == 1 { // upside down, at 2.5 Hz at most
		for i, j := 0, len(g)-1; i < j; i, j = i+1, j-1 {
			g[i], g[j] = g[j], g[i]
		}
	}
	aw, _ := amos.Size(o)
	x0 := int(math.Round(-float64(aw) + u*float64(p.w+aw)))
	y0 := int(math.Round(float64(p.top+p.bottom)/2 + math.Sin(u*2*math.Pi*1.5)*float64(p.bottom-p.top)/3 - 3))
	pal := amos.Palette()
	shift := el * 260
	col := func(i uint8) fx.RGB {
		c := pal[i]
		return hueShifted(fx.RGB{R: c.R, G: c.G, B: c.B}, shift)
	}
	for r := 0; r+1 < len(g); r += 2 {
		y := y0 + r/2
		for x := 0; x < len(g[r]); x++ {
			t, b := g[r][x], g[r+1][x]
			yy, xx := y, x0+x
			if yy < p.top || yy > p.bottom || xx < 0 || xx >= p.w-1 {
				continue
			}
			switch {
			case t != 0 && b != 0:
				p.putOn(yy, xx, "▀", col(t), col(b))
			case t != 0:
				p.put(yy, xx, "▀", col(t))
			case b != 0:
				p.put(yy, xx, "▄", col(b))
			}
		}
	}
}

// sinLUT is a sine table: the tunnel takes a dozen sines a cell, every
// frame, and a table lookup is plenty for colors.
var sinLUT = func() (t [1025]float64) {
	for i := range t {
		t[i] = math.Sin(float64(i) * 2 * math.Pi / 1024)
	}
	return t
}()

func fsin(x float64) float64 {
	u := x * (1024 / (2 * math.Pi))
	f := math.Floor(u)
	i := int(f) & 1023
	return sinLUT[i] + (sinLUT[i+1]-sinLUT[i])*(u-f)
}

func fcos(x float64) float64 { return fsin(x + math.Pi/2) }

var (
	shiftMu    sync.Mutex
	shiftCache = map[[2]int]fx.RGB{}
)

// hueShifted is fx.HueShift at a 3-degree step, cached by color.
func hueShifted(c fx.RGB, deg float64) fx.RGB {
	step := int(math.Floor(deg/3)) % 120
	if step < 0 {
		step += 120
	}
	k := [2]int{int(c.R)<<16 | int(c.G)<<8 | int(c.B), step}
	shiftMu.Lock()
	defer shiftMu.Unlock()
	if v, ok := shiftCache[k]; ok {
		return v
	}
	v := fx.HueShift(c, float64(step*3))
	if len(shiftCache) > 1<<14 {
		shiftCache = map[[2]int]fx.RGB{}
	}
	shiftCache[k] = v
	return v
}

// word is RELEASED!: rainbow block letters dropping in one by one with a
// pop, then bobbing each on its own while the word sways, over a shadow in the
// opposite hue; from meltAt they drip down the screen. Too narrow for
// block letters, the plain word in rainbow.
func (p *psyche) word(el float64) {
	if el < wordAt {
		return
	}
	block := glyphs.G("block")
	top, bottom, w := p.top, p.bottom, p.w
	mid := top + (bottom-top)/2 - 2
	ww := fx.LaunchWordWidth()
	if ww > w-4 || bottom-top < 8 {
		word := " " + fx.LaunchWord + " "
		x := (w - len(word)) / 2
		for n, ch := range word {
			p.put(mid, x+n, string(ch), hue(lvLetter, float64(n)*40+el*160))
		}
		return
	}
	type px struct {
		y, x, drip int
		hue        float64
	}
	gap := 1
	if ww+len(fx.LaunchWord)-1 <= w-10 {
		gap = 2
		ww += len(fx.LaunchWord) - 1
	}
	x0 := (w - ww) / 2
	// the whole word sways once every letter has landed; each letter
	// bobs on its own
	landed := wordAt + 0.08*float64(len(fx.LaunchWord)) + 0.45
	sway := 0
	if el > landed {
		sway = int(math.Round(math.Sin((el-landed)*2.2) * 1.5))
	}
	// a dark plate behind the word keeps it readable over the tunnel
	if el > wordAt+0.3 && el < meltAt {
		for y := mid - 1; y <= mid+6; y++ {
			for x := x0 - 3 + sway; x <= x0+ww+2+sway; x++ {
				p.put(y, x, block, hue(lvShadow, float64(x)*4+el*90))
			}
		}
	}
	var cells []px
	x := x0
	for n, ch := range fx.LaunchWord {
		font := fx.LaunchFont[ch]
		drop := fx.EaseOutBack((el - wordAt - 0.08*float64(n)) / 0.45)
		if drop > 0 {
			dy := 0
			if drop >= 1 {
				dy = int(math.Round(math.Sin(el*3.1+float64(n)*0.9) * 0.7))
			}
			y0 := int(math.Round(float64(top-6)+float64(mid-top+6)*drop)) + dy
			for r, line := range font {
				for j, c := range line {
					if c == ' ' {
						continue
					}
					drip := 0
					if el > meltAt {
						d := el - meltAt
						drip = int(d * d * float64(2+((x+j)*7)%5))
					}
					cells = append(cells, px{y0 + r, x + j + sway, drip, float64(x+j)*6 + float64(r)*14 + el*160})
				}
			}
		}
		x += len(font[0]) + gap
	}
	for _, c := range cells { // the shadow, in the opposite hue
		p.put(c.y+1+c.drip, c.x+1, block, hue(lvEcho, c.hue+180))
	}
	for _, c := range cells {
		for k := 0; k < c.drip; k++ { // the drip's trail
			p.put(c.y+k, c.x, block, hue(lvMid, c.hue))
		}
		p.put(c.y+c.drip, c.x, block, hue(lvLetter, c.hue))
	}
}

// stampede: six armadillos roll along the key bar, every one its own
// color, trailing sparks.
func (p *psyche) stampede(el float64, seed uint64) {
	spark := RuneAt(glyphs.G("spark"), 0)
	y := p.h - 1
	rnd := rand.New(rand.NewPCG(seed, seed^0x2545f4914f6cdd1d))
	for i := 0; i < 6; i++ {
		speed := 18 + rnd.Float64()*22
		lag := rnd.Float64() * 1.8
		if el < lag {
			continue
		}
		bx := int((el-lag)*speed) - 4
		ball := RuneAt(glyphs.G("ball"), int(el*14+lag*7))
		hh := float64(i)*60 + el*200
		for j, s := range []struct {
			g string
			c fx.RGB
		}{{spark, hue(lvMid, hh-40)}, {spark, hue(lvMid, hh-20)}, {ball, hue(lvBright, hh)}} {
			if cx := bx + j; 0 <= cx && cx < p.w-1 {
				p.c.PutRaw(y, cx, s.g, psySGR(s.c, fx.RGB{}, false, p.tier))
			}
		}
	}
}
