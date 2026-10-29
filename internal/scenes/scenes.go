// Package scenes holds the animated set pieces and the small ambient
// decorations the views draw with: the startup splash, the view wipe, the
// confetti after a post, the /release launch, the empty-list armadillo
// under its stars, and the row-level bits (the breathing selection bar, a
// chip's glint, a rule's gleam, a pulsing row number, the scrollbar).
//
// Every function here is pure drawing: it takes the canvas, the effect set
// and the few values it needs, reads the clock through package fx, and
// keeps no state of its own. Nothing here knows about the app model or
// the terminal.
package scenes

import (
	"math"
	"math/rand/v2"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/amos"
	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

// Chip is a rounded status chip: cap, text, cap. The caps draw in
// "cap:<style>", which the theme resolves to the chip's background as a
// foreground.
func Chip(label, style string) []text.Seg {
	return []text.Seg{
		text.S(glyphs.G("capl"), "cap:"+style),
		text.S(label, style),
		text.S(glyphs.G("capr"), "cap:"+style),
	}
}

// run appends ch in style st, merging with the last segment when the
// style is the same.
func run(segs []text.Seg, ch, st string) []text.Seg {
	if n := len(segs); n > 0 && segs[n-1].Style == st {
		segs[n-1].Text += ch
		return segs
	}
	return append(segs, text.S(ch, st))
}

// RuneAt is the i'th rune of s (cycling), or "?" when s is empty.
func RuneAt(s string, i int) string {
	r := []rune(s)
	if len(r) == 0 {
		return "?"
	}
	if i < 0 {
		i = -i
	}
	return string(r[i%len(r)])
}

// artWidth is the widest row of a piece of ASCII art, in cells.
func artWidth(art []string) int {
	w := 0
	for _, ln := range art {
		if n := text.Width(ln); n > w {
			w = n
		}
	}
	return w
}

// useAmos says whether the mascot draws as the Amos sprite: a 256-color
// or truecolor terminal, a glyph set with the half blocks, and room for
// the sprite (rows is what the scene needs around it).
// Mascot is how Amos is drawn, from [display] mascot: "sprite" (the
// pixel art, where the terminal can show it), "ascii" (the line art
// everywhere) or "none" (no mascot at all).
var Mascot = "sprite"

func useAmos(tier theme.Tier, h, w, rows int) bool {
	return Mascot == "sprite" && tier >= theme.Color256 && glyphs.Active() != glyphs.ASCII &&
		w >= amos.Width()+4 && h >= amos.Height()+rows
}

// MiniAmos says whether the small Amos (amos.Options.Mini) can be drawn:
// useAmos's terminal and glyph checks, without the room (the caller
// measures that).
func MiniAmos(tier theme.Tier) bool {
	return Mascot == "sprite" && tier >= theme.Color256 && glyphs.Active() != glyphs.ASCII
}

// paintAmos paints the sprite with its top-left at (y, x), leaving the
// transparent cells alone.
func paintAmos(c *canvas.Canvas, y, x int, o amos.Options) {
	for r, row := range amos.Cells(o) {
		for i, cell := range row {
			if cell.SGR == "" {
				continue
			}
			c.PutRaw(y+r, x+i, cell.Glyph, cell.SGR)
		}
	}
}

// GleamSegs is a section's rule width cells wide; with ambient motion a
// short gleam of light runs along it now and then, each section (pos) in
// its own time.
func GleamSegs(width, pos int) []text.Seg {
	if width <= 0 {
		return nil
	}
	p := fx.Wave(7.0, -float64(pos)*0.13)
	if !fx.Ambient || theme.Current() == theme.Mono || p > 0.5 {
		return []text.Seg{text.S(strings.Repeat(glyphs.G("rule"), width), "rule")}
	}
	g := int(p/0.5*float64(width+8)) - 4
	rule, gleam := glyphs.G("rule"), glyphs.G("gleam")
	var segs []text.Seg
	for i := 0; i < width; i++ {
		d := i - g
		if d < 0 {
			d = -d
		}
		st := "rule"
		switch {
		case d == 0:
			st = "fx.b7"
		case d <= 1:
			st = "fx.b5"
		case d <= 3:
			st = "fx.b3"
		}
		ch := gleam
		if st == "rule" {
			ch = rule
		}
		segs = run(segs, ch, st)
	}
	return segs
}

// GlintChip is a chip; an "awaiting review" one catches a glint of light
// that ripples down the list row by row (pos; negative means no row), so
// the eye goes to exactly the rows that need you. ambient and mono are
// the current motion and tier flags.
func GlintChip(label, style string, pos int, ambient, mono bool) []text.Seg {
	segs := Chip(label, style)
	if style != "chip.await" || pos < 0 || !ambient || mono {
		return segs
	}
	rs := []rune(label)
	g := int(fx.Wave(4.0, -float64(pos)*0.07)*float64(len(rs)+14)) - 6
	if g < -1 || g > len(rs) {
		return segs
	}
	out := []text.Seg{segs[0]}
	for i, ch := range rs {
		st := style
		switch d := i - g; {
		case d == 0:
			st = "chip.await.glint2"
		case d == 1 || d == -1:
			st = "chip.await.glint"
		}
		if len(out) > 1 && out[len(out)-1].Style == st {
			out[len(out)-1].Text += string(ch)
		} else {
			out = append(out, text.S(string(ch), st))
		}
	}
	return append(out, segs[len(segs)-1])
}

// NumStyle is a row's #number style: it pulses gold/white while the row
// is new since the last refresh ("new:"+ident), white once if it was
// updated ("upd:"+ident), and base otherwise.
func NumStyle(f *fx.Fx, ident, base string) string {
	if t, ok := f.T("new:" + ident); ok && t < 0.75 {
		return []string{"num.new", "num.pulse"}[int(t*10)%2]
	}
	if t, ok := f.T("upd:" + ident); ok && int(t*6)%2 == 0 {
		return "num.pulse"
	}
	return base
}

// Gutter is a row's first cell: the selection bar (in the breathe style),
// a new row's gold glint, or the comet tail the cursor leaves on the rows
// it just left (trailAge seconds old, when hasTrail).
func Gutter(f *fx.Fx, ident string, sel bool, breathe string, trailAge float64, hasTrail bool) []text.Seg {
	if sel {
		return []text.Seg{text.S(glyphs.G("sel"), breathe)}
	}
	if t, ok := f.T("new:" + ident); ok {
		sp := glyphs.G("spark")
		return []text.Seg{text.S(RuneAt(sp, int(t*16)), fx.Ramp("fx.g", 4, fx.Tri(t*3)))}
	}
	if hasTrail {
		i := int(trailAge / 0.3 * 4)
		if i > 3 {
			i = 3
		}
		if i < 0 {
			i = 0
		}
		return []text.Seg{text.S(glyphs.G("sel"), []string{"fx.b4", "fx.b3", "fx.b2", "fx.b1"}[i])}
	}
	return []text.Seg{text.S(" ", "gutter")}
}

// Breathe is the selection bar's style: with ambient motion in color it
// breathes through gutter.0..4 in step with the shared clock.
func Breathe(ambient, mono bool) string {
	if ambient && !mono {
		return fx.Ramp("gutter.", 5, fx.Tri(fx.Wave(1.4, 0)))
	}
	return "gutter"
}

// Scrollbar draws a thin scrollbar in column x, visible rows from top: a
// track plus a thumb sized to the visible share. Nothing when it all fits.
func Scrollbar(c *canvas.Canvas, top, visible, total, first, x int) {
	if total <= visible || visible <= 0 {
		return
	}
	size := max(1, visible*visible/total)
	pos := (visible - size) * first / max(1, total-visible)
	thumb, track := glyphs.G("thumb"), glyphs.G("track")
	for i := 0; i < visible; i++ {
		if pos <= i && i < pos+size {
			c.PutStr(top+i, x, thumb, "scroll.thumb")
		} else {
			c.PutStr(top+i, x, track, "scroll.track")
		}
	}
}

// Stars is a sky of stars, each twinkling in its own time, laid out from
// the screen size so it is the same sky every frame. Rows in skipRows
// stay clear (the mascot's).
func Stars(c *canvas.Canvas, _ *fx.Fx, h, w int, skipRows map[int]bool) {
	seed := uint64(h*1009 + w)
	rnd := rand.New(rand.NewPCG(seed, seed^0x5851f42d4c957f2d))
	sparks := []rune(glyphs.G("spark"))
	if len(sparks) > 4 {
		sparks = sparks[:4]
	}
	if len(sparks) == 0 {
		return
	}
	n := max(4, w*h/70)
	for i := 0; i < n; i++ {
		yhi := max(3, h-2)
		y := 2 + rnd.IntN(yhi-2)
		x := rnd.IntN(max(1, w-1))
		period := 2.0 + rnd.Float64()*3.5
		off := rnd.Float64()
		blue := rnd.Float64() < 0.4
		b := fx.Tri(fx.Wave(period, off))
		if skipRows[y] || b < 0.3 {
			continue
		}
		g := min(len(sparks)-1, int(b*float64(len(sparks))))
		st := fx.Ramp("fx.k", 6, b)
		if blue {
			st = fx.Ramp("fx.b", 8, b)
		}
		c.PutStr(y, x, string(sparks[g]), st)
	}
}

// Empty is the nothing-to-show scene: the armadillo (Amos in color, the
// line art otherwise) trundling to and fro under a twinkling sky with
// ambient motion, and the note on why the list is empty centered beneath.
func Empty(c *canvas.Canvas, f *fx.Fx, h, w int, note string, tier theme.Tier) {
	if Mascot == "none" {
		c.PutStr(max(3, (h-4)/2), max(0, (w-text.Width(note))/2), note, "allclear")
		return
	}
	sprite := useAmos(tier, h, w, 7)
	art := glyphs.Armadillo
	aw, ah := artWidth(art), len(art)
	if sprite {
		aw, ah = amos.Width(), amos.Height()
	}
	artTop := max(3, (h-4-ah-3)/2)
	x := max(0, (w-aw)/2)
	frame, mirror := 0, false
	if fx.Ambient {
		skip := map[int]bool{}
		for y := artTop; y < artTop+ah+2; y++ {
			skip[y] = true
		}
		Stars(c, f, h, w, skip)
		span := max(0, w-aw-4)
		p := fx.Wave(math.Max(6.0, float64(span)/5.0), 0)
		x = 2 + int(fx.Tri(p)*float64(span))
		frame = int(fx.Now()*4) % 2
		mirror = p >= 0.5 // on its way back
	}
	if sprite {
		paintAmos(c, artTop, x, amos.Options{Mirror: mirror, Frame: frame})
	} else {
		if frame == 1 {
			art = glyphs.ArmadilloStep
		}
		if mirror {
			art = glyphs.Mirror(art)
		}
		for i, ln := range art {
			c.PutStr(artTop+i, x, ln, "art")
		}
	}
	c.PutStr(artTop+ah+1, max(0, (w-text.Width(note))/2), note, "allclear")
}
