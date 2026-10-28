package scenes

import (
	"math"
	"math/rand/v2"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// Wipe sweeps a new view in behind a soft blue edge: from the right going
// deeper (list -> PR -> files -> diff), from the left coming back (the
// "wipe" effect's "dir" is 1 or -1). Rows 2..h-3 only: the header and
// the key bar stay.
func Wipe(c *canvas.Canvas, f *fx.Fx, h, w int) {
	t, ok := f.T("wipe")
	if !ok {
		return
	}
	d := f.Int("wipe", "dir", 1)
	edge := int(fx.EaseOut(t) * float64(w+3))
	shade := glyphs.G("shade")
	band := []text.Seg{
		text.S(runeAt(shade, 0), "fx.b2"),
		text.S(runeAt(shade, 1), "fx.b4"),
		text.S(runeAt(shade, 2), "fx.b6"),
	}
	for y := 2; y < h-2; y++ {
		var x0 int
		cells := band
		if d > 0 { // revealed up to the edge, the edge glowing ahead
			if edge < w {
				c.PutStr(y, edge, strings.Repeat(" ", w-edge), "text")
			}
			x0 = edge - 3
		} else {
			x := w - edge
			if x > 0 {
				c.PutStr(y, 0, strings.Repeat(" ", x), "text")
			}
			x0 = x
			cells = []text.Seg{band[2], band[1], band[0]}
		}
		for i, seg := range cells {
			if cx := x0 + i; 0 <= cx && cx < w-1 {
				c.PutSegs(y, cx, []text.Seg{seg}, 0, false, false)
			}
		}
	}
}

// Confetti is the celebration after a post ("confetti": seed, count,
// big): confetti over the body, and for a big one the armadillo, curled
// up, rolling across the key bar.
func Confetti(c *canvas.Canvas, f *fx.Fx, h, w int) {
	t, ok := f.T("confetti")
	if !ok {
		return
	}
	el, _ := f.Elapsed("confetti")
	seed := f.Seed("confetti")
	count := f.Int("confetti", "count", 0)
	big := f.Bool("confetti", "big")
	spread := float64(max(4, w/12))
	if big {
		spread = float64(w / 4)
	}
	for _, p := range fx.Particles(seed, count, el, h-2, w/2, spread) {
		if 2 <= p.Y && p.Y < h-1 && 0 <= p.X && p.X < w-1 {
			c.PutStr(p.Y, p.X, p.Glyph, p.Style)
		}
	}
	if big {
		bx := int(fx.EaseInOut(t)*float64(w+6)) - 3
		spark := runeAt(glyphs.G("spark"), 0)
		segs := []text.Seg{
			text.S(spark, "fx.k1"), text.S(spark, "fx.k2"), text.S(spark, "fx.k3"),
			text.S(runeAt(glyphs.G("ball"), int(el*14)), "fx.g3"),
		}
		for i, seg := range segs {
			if cx := bx - 3 + i; 0 <= cx && cx < w-1 {
				c.PutSegs(h-1, cx, []text.Seg{seg}, 0, false, false)
			}
		}
	}
}

// Launch is the /release launch, over everything but the status line (it
// says what was posted). 0-1.5 s a rocket climbs out of the key bar; at
// 1.5 s it bursts, a shockwave rolls out and seven more fireworks follow;
// from 1.8 s RELEASED! drops in letter by letter and bobs; lights march
// round the edge and armadillos stampede the key bar the whole time;
// confetti cannons fire at 3.2 s. No cell changes color more than three
// times a second: everything that cycles steps on int(el*3).
func Launch(c *canvas.Canvas, f *fx.Fx, h, w int) {
	if _, ok := f.T("launch"); !ok {
		return
	}
	el, _ := f.Elapsed("launch")
	seed := f.Seed("launch")
	top, bottom := 1, h-3 // the body: header above, status + keys below
	if bottom-top < 4 || w < 20 {
		return
	}
	at := func(y, x int, glyph, style string) {
		if top <= y && y <= bottom && 0 <= x && x < w-1 {
			c.PutStr(y, x, glyph, style)
		}
	}
	shade := glyphs.G("shade")
	block := glyphs.G("block")
	spark := runeAt(glyphs.G("spark"), 0)
	cycle := fx.LaunchCycle

	// the shockwave: a ring of shade rolling out from the burst
	if 1.5 <= el && el < 2.6 {
		r := (el - 1.5) * 70.0
		cy, cx := top, w/2
		styles := []string{"fx.b7", "fx.b5", "fx.b3"}
		for y := top; y <= bottom; y++ {
			dy := float64(y-cy) * 2.0
			for x := 0; x < w-1; x++ {
				d := r - math.Hypot(float64(x-cx), dy)
				if 0 <= d && d < 3 {
					k := min(2, int(d))
					at(y, x, runeAt(shade, k), styles[k])
				}
			}
		}
	}

	// marching lights round the edge: each cell moves on 3x a second
	step := int(el * 3)
	rows := bottom - top + 1
	per := 2*(w-1) + 2*rows
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
		k := (i/3 + step) % len(cycle)
		g := runeAt(shade, 1)
		if k%2 == 1 {
			g = block
		}
		at(y, x, g, cycle[k])
	}

	// the rocket: climbs, accelerating, on a flickering flame
	if el < 1.5 {
		p := (el / 1.5) * (el / 1.5)
		ry := int(math.Round(float64(bottom) - float64(bottom-top)*p))
		rx := w/2 - 3
		for i, row := range fx.LaunchRocket {
			for j, ch := range row {
				if ch != ' ' {
					st := "fx.g3"
					if i > 0 {
						st = "fx.b7"
					}
					at(ry+i, rx+j, string(ch), st)
				}
			}
		}
		fl := fx.LaunchFlame[int(el*3)%len(fx.LaunchFlame)]
		for j, ch := range fl {
			if ch != ' ' {
				at(ry+len(fx.LaunchRocket), rx+j, string(ch), "fx.g2")
			}
		}
		smoke := []string{"fx.k3", "fx.k2", "fx.k1"}
		for k := 1; k < 8; k++ { // the smoke it leaves
			at(ry+len(fx.LaunchRocket)+k, w/2-1+(k%3)-1, spark, smoke[min(2, k/3)])
		}
	}

	// fireworks
	for _, b := range fx.LaunchBursts(seed, w, top+2, bottom-2) {
		for _, p := range fx.BurstCells(el-b.Start, b.Y, b.X, b.Style, 22) {
			at(p.Y, p.X, p.Glyph, p.Style)
		}
	}

	// confetti cannons from both bottom corners
	if el >= 3.2 {
		for _, x0 := range []int{w / 6, w - w/6} {
			for _, p := range fx.Particles(seed+uint64(x0), 40, el-3.2, bottom, x0, 2) {
				at(p.Y, p.X, p.Glyph, p.Style)
			}
		}
	}

	// RELEASED!: each letter drops in with a pop, then bobs
	if el >= 1.8 {
		ww := fx.LaunchWordWidth()
		mid := top + (bottom-top)/2 - 2
		if ww <= w-4 && bottom-top >= 8 {
			x := (w - ww) / 2
			for n, ch := range fx.LaunchWord {
				font := fx.LaunchFont[ch]
				drop := fx.EaseOutBack((el - 1.8 - 0.09*float64(n)) / 0.45)
				if drop > 0 {
					bob := 0.0
					if drop >= 1 {
						bob = math.Sin(el*5 + float64(n)*0.7)
					}
					y0 := int(math.Round(float64(top-6) + float64(mid-top+6)*drop + bob))
					st := cycle[(n+int(el*3))%len(cycle)]
					for r, line := range font {
						for j, px := range line {
							if px != ' ' {
								at(y0+r, x+j, block, st)
							}
						}
					}
				}
				x += len(font[0]) + 1
			}
		} else { // too narrow for block letters
			word := " " + fx.LaunchWord + " "
			x := (w - len(word)) / 2
			for n, ch := range word {
				at(mid, x+n, string(ch), cycle[(n+int(el*3))%len(cycle)])
			}
		}
	}

	// the armadillo stampede on the key bar
	rnd := rand.New(rand.NewPCG(seed, seed^0x2545f4914f6cdd1d))
	for i := 0; i < 6; i++ {
		speed := 18 + rnd.Float64()*22
		lag := rnd.Float64() * 1.8
		bx := int((el-lag)*speed) - 4
		ball := runeAt(glyphs.G("ball"), int(el*14+lag*7))
		segs := []text.Seg{text.S(spark, "fx.k2"), text.S(spark, "fx.k3"), text.S(ball, "fx.g3")}
		for j, seg := range segs {
			if cx := bx + j; 0 <= cx && cx < w-1 && el >= lag {
				c.PutSegs(h-1, cx, []text.Seg{seg}, 0, false, false)
			}
		}
	}
}
