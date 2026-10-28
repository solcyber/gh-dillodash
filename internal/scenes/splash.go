package scenes

import (
	"math"

	"github.com/solcyber/gh-dillodash/internal/amos"
	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

// SplashInfo is what the splash says: the repos it is loading and
// whether the first refresh has landed (or failed).
type SplashInfo struct {
	ReposText string // "acme/infra" or "acme/infra, web-portal"
	Loaded    bool
	Failed    bool
}

// Splash draws the startup splash while the first refresh is in flight:
// the logotype lights up row by row under a sweeping shine, the armadillo
// rolls in as a ball and uncurls (Amos in color), a scanner runs. Returns
// false once the data is in and fx.SplashMinSecs have passed, or the
// terminal is too small for it; the caller then stops the "splash" effect
// and starts the cascade.
func Splash(c *canvas.Canvas, f *fx.Fx, h, w int, info SplashInfo) bool {
	return splash(c, f, h, w, info, theme.Current())
}

func splash(c *canvas.Canvas, f *fx.Fx, h, w int, info SplashInfo, tier theme.Tier) bool {
	el, _ := f.Elapsed("splash")
	art := glyphs.Logotype
	ascii := glyphs.Active() == glyphs.ASCII
	if ascii {
		art = glyphs.LogotypeASCII
	}
	aw := artWidth(art)
	sprite := useAmos(tier, h, w, len(art)+7)
	mw, mh := artWidth(glyphs.Armadillo), len(glyphs.Armadillo)
	if sprite {
		mw, mh = amos.Width(), amos.Height()
	}
	tall := len(art) + mh + 6
	if ((info.Loaded || info.Failed) && el >= fx.SplashMinSecs) || h < tall+1 || w < aw+4 {
		return false
	}
	top := max(0, (h-tall)/2)
	left := (w - aw) / 2
	shine := math.Mod(el*75, float64(aw+70)) - 12
	block := []rune(glyphs.G("block"))
	for r, line := range art {
		rs := []rune(line)
		n := int(math.Round(fx.EaseOut((el-float64(r)*0.07)/0.4) * float64(len(rs))))
		if n > len(rs) {
			n = len(rs)
		}
		var segs []text.Seg
		for i, ch := range rs[:n] {
			var st string
			switch {
			case ch == ' ':
				st = "text"
			case ascii || (len(block) > 0 && ch == block[0]):
				if math.Abs(float64(i-2*r)-shine) < 2.5 {
					st = "fx.logo.shine"
				} else {
					st = "fx.logo." + itoa(min(5, r))
				}
			default:
				st = "fx.logo.shade"
			}
			segs = run(segs, string(ch), st)
		}
		c.PutSegs(top+r, left, segs, 0, false, false)
	}
	tag := "approvals for " + info.ReposText
	y := top + len(art) + 1
	if el > 0.35 {
		c.PutSegs(y, (w-text.Width(tag))/2, []text.Seg{
			text.S("approvals for ", "muted"), text.S(info.ReposText, "repo"),
		}, 0, false, false)
	}
	// the armadillo: a ball rolling in, then itself, trotting in place
	y += 2
	ax := (w - mw) / 2
	if Mascot == "none" {
		// no mascot: the logotype and the loading line only
	} else if el < 0.6 {
		p := fx.EaseOut(el / 0.6)
		bx := int(p * float64(ax+mw/2))
		ball := runeAt(glyphs.G("ball"), int(el*16))
		trail := ""
		for i := 0; i < min(4, bx); i++ {
			trail += runeAt(glyphs.G("spark"), 0)
		}
		c.PutSegs(y+mh-2, max(0, bx-4), []text.Seg{text.S(trail, "fx.k2"), text.S(ball, "fx.b6")}, 0, false, false)
	} else {
		frame := int(el*4) % 2
		if sprite {
			paintAmos(c, y, ax, amos.Options{Frame: frame})
		} else {
			art := glyphs.Armadillo
			if frame == 1 {
				art = glyphs.ArmadilloStep
			}
			for i, ln := range art {
				c.PutStr(y+i, ax, ln, "muted")
			}
		}
	}
	y += mh + 1
	if !info.Loaded {
		msg := glyphs.SpinnerFrame() + " loading " + info.ReposText
		c.PutStr(y, (w-text.Width(msg))/2, msg, "spin")
		bw := min(40, w-8)
		c.PutSegs(y+1, (w-bw)/2, fx.ScannerSegs(bw, fx.Wave(1.3, 0)), 0, false, false)
	}
	return true
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// StartCascade starts the list's cascade: the rows sweep in one after
// another behind a bright edge, a little longer for a longer list.
func StartCascade(f *fx.Fx, rows int) {
	f.Start("cascade", 0.3+0.018*float64(min(rows, 50)), nil)
}

// CascadeRight is the column row i of the list is clipped at while the
// cascade plays (w once it is over); skip says the row is not drawn yet.
func CascadeRight(f *fx.Fx, i, w int) (right int, skip bool) {
	if _, ok := f.T("cascade"); !ok {
		return w, false
	}
	casc, _ := f.Elapsed("cascade")
	right = int(fx.EaseOut((casc-float64(i)*0.018)/0.28) * float64(w))
	return right, right <= 0
}
