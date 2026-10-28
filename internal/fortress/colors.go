package fortress

import (
	"fmt"
	"strings"
	"sync"

	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

// Color is one of Dwarf Fortress's 16 colors (its CGA palette): the
// xterm cube's nearest index for the 256 tier, the ANSI color (bold for
// the light ones) for the 16 tier.
type Color struct {
	X256  int
	ANSI  int
	Light bool
}

// Colors is the DF palette by name. It lives here, not in the theme's
// Styles, so the dashboard's grays-blues-golds palette check stays
// honest: the map is the one exception.
var Colors = map[string]Color{
	"black": {16, 0, false}, "blue": {19, 4, false},
	"green": {28, 2, false}, "cyan": {30, 6, false},
	"red": {124, 1, false}, "magenta": {90, 5, false},
	"brown": {136, 3, false}, "lgray": {250, 7, false},
	"dgray": {244, 0, true}, "lblue": {33, 4, true},
	"lgreen": {46, 2, true}, "lcyan": {51, 6, true},
	"lred": {196, 1, true}, "lmagenta": {201, 5, true},
	"yellow": {226, 3, true}, "white": {231, 7, true},
}

// mono: the states that want a look are bold, terrain is dim
var monoAttr = map[string]string{
	"yellow": "bold", "lred": "bold", "white": "bold",
	"lmagenta": "under", "dgray": "dim", "green": "dim",
	"blue": "dim", "brown": "dim", "black": "dim",
}

type sgrKey struct {
	name          string
	bold, reverse bool
	tier          theme.Tier
}

var sgrCache sync.Map // sgrKey -> string

// SGR is the escape prefix a DF color draws with on black at a tier (a
// mono stand-in below 16 colors): the raw style canvas.PutRaw takes.
func SGR(name string, bold, reverse bool, tier theme.Tier) string {
	key := sgrKey{name, bold, reverse, tier}
	if s, ok := sgrCache.Load(key); ok {
		return s.(string)
	}
	col, ok := Colors[name]
	if !ok {
		col = Colors["lgray"]
	}
	var parts []string
	switch tier {
	case theme.TrueColor:
		r, g, b := theme.XtermRGB(col.X256)
		parts = append(parts, fmt.Sprintf("38;2;%d;%d;%d", r, g, b), "48;2;0;0;0")
	case theme.Color256:
		parts = append(parts, fmt.Sprintf("38;5;%d", col.X256), fmt.Sprintf("48;5;%d", theme.K))
	case theme.Color16:
		parts = append(parts, fmt.Sprint(30+col.ANSI), "40")
		if col.Light {
			bold = true
		}
	default:
		switch monoAttr[name] {
		case "bold":
			bold = true
		case "dim":
			if !bold {
				parts = append(parts, "2")
			}
		case "under":
			parts = append(parts, "4")
		}
	}
	if bold {
		parts = append(parts, "1")
	}
	if reverse {
		parts = append(parts, "7")
	}
	s := "\x1b[0m"
	if len(parts) > 0 {
		s = "\x1b[" + strings.Join(parts, ";") + "m"
	}
	sgrCache.Store(key, s)
	return s
}

// StatusGlyph is the glyph that stands for an item's state when nothing
// moves (motion off, mono): the alert, the fire, the sleeper; else its own.
func StatusGlyph(it *Item) string {
	switch it.Anim {
	case AnimAlert:
		return "df.alert"
	case AnimFire:
		return "df.fire"
	case AnimSleep:
		return "df.sleep"
	}
	return it.GlyphName()
}

// Glyph is the glyph name an item shows at time t (seconds).
func Glyph(it *Item, t float64, still bool) string {
	if still {
		return StatusGlyph(it)
	}
	h := fhash(it.ID)
	phase := (int(t*2) + int(h%7)) % 2
	switch it.Anim {
	case AnimAlert, AnimFire, AnimSleep:
		if phase == 1 {
			return StatusGlyph(it)
		}
	}
	if it.Anim == AnimGlint && (int(t*3)+int(h%5))%5 == 0 {
		return "df.glint"
	}
	if it.Kind == KindBranch {
		return "df.ruin"
	}
	return it.GlyphName()
}

// runeAt is the i-th rune of a glyph set entry (the last for i < 0).
func runeAt(name string, i int) string {
	r := []rune(glyphs.G(name))
	if len(r) == 0 {
		return " "
	}
	if i < 0 || i >= len(r) {
		i = len(r) - 1
	}
	return string(r[i])
}

// Terrain is the (glyph, DF color) of a terrain cell at time t.
func Terrain(code byte, r, c int, t float64, still bool) (glyph, color string) {
	h := uint32(r)*374761393 + uint32(c)*668265263
	h = (h ^ (h >> 13)) * 1274126177
	h ^= h >> 16
	switch code {
	case 'w':
		drift := 0
		if !still {
			drift = int(t * 2)
		}
		if (c+r*3+drift)%13 == 0 {
			return runeAt("df.water", -1), "lblue"
		}
		return runeAt("df.water", 0), "blue"
	case 'o':
		color := "brown"
		if h%2 == 1 {
			color = "green"
		}
		if h%5 == 0 {
			g := []rune(glyphs.G("df.wild"))
			if len(g) > 0 {
				return string(g[int(h)%len(g)]), color
			}
		}
		return " ", color
	case 'p':
		color := "green"
		if h%3 == 0 {
			color = "lgreen"
		}
		g := []rune(glyphs.G("df.tree"))
		if len(g) == 0 {
			return " ", color
		}
		return string(g[int(h)%len(g)]), color
	case 'L':
		if h%6 == 0 {
			return glyphs.G("df.ground"), "dgray"
		}
		return " ", "dgray"
	case '-':
		return glyphs.G("df.street"), "dgray"
	case 'B':
		return glyphs.G("df.road"), "brown"
	case '=':
		return glyphs.G("df.bridge"), "brown"
	case '#':
		return glyphs.G("df.rail"), "lgray"
	}
	return " ", "black"
}
