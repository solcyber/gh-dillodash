// Package theme maps the style names the renderers emit to terminal
// colors and attributes.
//
// Colorblind-safe by construction: grayscale, blue and yellow/gold only.
// No red, no green (and no purple or teal, which read as either for
// some). Blue vs gold is the pair the common red-green deficiencies keep
// apart, and the two also differ sharply in lightness, which covers tritan
// vision. Gold means "look here" (needs you, failing, destroys, refused,
// errors); blue means "fine / done / informational"; gray is everything
// else. Shapes back up every color (see package glyphs).
//
// The palette indices are xterm's FIXED 256-color cube and gray ramp, not
// the 16 ANSI slots terminal themes remap, so the colors (and the TRUE
// black background, 16) are the same in every terminal. Blue TEXT uses 33
// and lighter; 17-27 are too dark to read as text and are used as
// backgrounds. The truecolor tier renders the same indices as their exact
// RGB.
//
// Contrast is measured, not eyeballed: Contrast computes the WCAG 2.x
// ratio from the cube's RGB. Every style that draws text clears 4.5:1
// against every background it can land on; rules, borders, tracks and
// glyph-only icons clear 3:1 (WCAG's non-text floor). The declared colors
// clear it on black; on the selected row and in code bands, Style steps a
// foreground up its own ramp until it does (Lift), so the in-row hierarchy
// survives and no combination can slip under the floor. The tests check
// every style against every background.
package theme

import (
	"fmt"
	"image/color"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// The palette: xterm 256-color indices.
const (
	K = 16 // true black

	G1, G2, G3, G4, G5, G6, G7 = 235, 237, 241, 245, 248, 251, 254
	White                      = 231

	Navy, Navy2, Steel       = 17, 18, 24 // backgrounds only
	BlueBG, BlueBG2, BlueBG3 = 25, 26, 27 // backgrounds under white text

	Blue, Blue2, Blue3, Sky, Blue4 = 33, 75, 111, 117, 153

	GoldBG, Ochre, Gold, Gold2, GoldHi, Cream = 136, 178, 220, 221, 227, 229

	SelBG  = Navy2 // the selected row: deep navy
	BandBG = G1    // code blocks

	// None is a Spec's BG when the style draws on the row's background.
	None = -1
)

var (
	blues = []int{Blue, Blue2, Blue3, Sky, Blue4, Navy, Navy2, Steel, BlueBG, BlueBG2, BlueBG3}
	golds = []int{GoldBG, Ochre, Gold, Gold2, GoldHi, Cream}
	// Lift's ramps, darkest first
	ramps = [][]int{
		{G3, 243, G4, 246, G5, 250, G6, 252, G7, 255, White},
		{Blue, Blue2, Blue3, Sky, Blue4, White},
		{GoldBG, Ochre, Gold, Gold2, GoldHi},
	}
)

func in(c int, set []int) bool {
	for _, x := range set {
		if x == c {
			return true
		}
	}
	return false
}

// IsBlue reports whether a palette index is one of the theme's blues.
func IsBlue(c int) bool { return in(c, blues) }

// IsGold reports whether a palette index is one of the theme's golds.
func IsGold(c int) bool { return in(c, golds) }

// ansi16 is the RGB of the 16 ANSI slots as xterm ships them; a terminal's
// theme may remap these, which is why the palette never uses them.
var ansi16 = [16][3]uint8{
	{0, 0, 0}, {205, 0, 0}, {0, 205, 0}, {205, 205, 0},
	{0, 0, 238}, {205, 0, 205}, {0, 205, 205}, {229, 229, 229},
	{127, 127, 127}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
	{92, 92, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
}

// XtermRGB is the (r, g, b) of an xterm palette index: the 6x6x6 cube and
// the gray ramp are fixed from 16 up (0-15 belong to the terminal's
// theme; xterm's defaults stand in for them).
func XtermRGB(i int) (r, g, b uint8) {
	switch {
	case i < 0:
		return 0, 0, 0
	case i < 16:
		c := ansi16[i]
		return c[0], c[1], c[2]
	case i < 232:
		n := i - 16
		lv := [6]uint8{0, 95, 135, 175, 215, 255}
		return lv[n/36], lv[(n/6)%6], lv[n%6]
	case i < 256:
		v := uint8(8 + (i-232)*10)
		return v, v, v
	}
	return 255, 255, 255
}

var lumCache sync.Map // int -> float64

// Luminance is the WCAG relative luminance of a palette index.
func Luminance(i int) float64 {
	if v, ok := lumCache.Load(i); ok {
		return v.(float64)
	}
	lin := func(c uint8) float64 {
		f := float64(c) / 255.0
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	r, g, b := XtermRGB(i)
	l := 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
	lumCache.Store(i, l)
	return l
}

// Contrast is the WCAG 2.x contrast ratio of two palette indices (1.0 to
// 21.0).
func Contrast(a, b int) float64 {
	la, lb := Luminance(a), Luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// Lift is fg stepped up its own ramp (gray, blue or gold) until it clears
// ratio:1 against bg, or White when the ramp runs out.
func Lift(fg, bg int, ratio float64) int {
	if ratio == 0 || Contrast(fg, bg) >= ratio {
		return fg
	}
	for _, ramp := range ramps {
		for i, c := range ramp {
			if c != fg {
				continue
			}
			for _, up := range ramp[i+1:] {
				if Contrast(up, bg) >= ratio {
					return up
				}
			}
			return White
		}
	}
	return White
}

// Spec is a style as declared: foreground, background (None = the row's)
// and attributes. A style with a background is a chip and keeps its own
// designed pair wherever it lands.
type Spec struct {
	FG   int
	BG   int
	Attr text.Attr
}

// Chip reports whether the style carries its own background.
func (s Spec) Chip() bool { return s.BG != None }

const (
	b = text.Bold
	u = text.Underline
)

func sp(fg, bg int, a text.Attr) Spec { return Spec{FG: fg, BG: bg, Attr: a} }

// Styles is every style name the renderers may emit.
var Styles = map[string]Spec{
	// frame and chrome
	"text": sp(G6, None, 0), "bright": sp(G7, None, 0), "strong": sp(White, None, b),
	"muted": sp(G5, None, 0), "faint": sp(G4, None, 0), "rule": sp(G3, None, 0),
	"logo": sp(Blue, None, b), "app": sp(White, None, b), "repo": sp(Blue2, None, 0),
	"spin": sp(Blue3, None, b), "budget.ok": sp(Blue3, None, 0),
	"budget.low": sp(Gold, None, b), "build": sp(G4, None, 0),
	"crumb": sp(G4, None, 0), "crumb.here": sp(G7, None, b),
	"section": sp(Blue, None, b), "section.count": sp(Blue4, None, 0),
	"subsection": sp(Blue3, None, 0), "gutter": sp(Blue, None, b),
	// header segments, powerline style: [ logo app ][ owner/repo ] as me
	"hdr.app": sp(White, BlueBG, b), "hdr.app.hi": sp(K, Blue4, b),
	"hdr.logo.0": sp(White, BlueBG, b), "hdr.logo.1": sp(Blue4, BlueBG, b),
	"hdr.logo.2": sp(Sky, BlueBG, b), "hdr.repo": sp(Blue4, Navy, 0),
	"hdr.sep.app": sp(BlueBG, Navy, 0), "hdr.sep.app.k": sp(BlueBG, None, 0),
	"hdr.sep.repo": sp(Navy, None, 0),
	// chips: text on a colored background; the rounded caps draw in fg=bg
	"chip.await": sp(K, Gold, b), "chip.await.glint": sp(K, GoldHi, b),
	"chip.await.glint2": sp(K, White, b), "chip.changes": sp(Gold, G2, b),
	"chip.mine": sp(White, BlueBG, 0), "chip.approved": sp(K, Blue3, 0),
	"chip.draft": sp(G6, G2, 0), "chip.queued": sp(Blue4, Steel, 0),
	"chip.sched": sp(White, BlueBG, b), "chip.rolled": sp(K, Gold2, 0),
	"chip.label": sp(G6, G2, 0), "chip.key": sp(White, BlueBG, b),
	"chip.err": sp(K, Gold, b), "chip.state": sp(K, G5, b),
	"chip.rev.approved": sp(White, BlueBG, b), "chip.rev.changes": sp(K, Gold, b),
	"chip.rev.other": sp(G6, G2, 0), "chip.filter": sp(K, Blue3, b),
	"chip.plan.live": sp(White, BlueBG, b), "chip.plan.done": sp(Blue4, Steel, 0),
	"chip.plan.dead": sp(G5, G2, 0), "chip.plan.warn": sp(K, Gold, b),
	// list cells
	"num": sp(Blue2, None, 0), "num.issue": sp(G5, None, 0),
	"num.pulse": sp(White, None, b), "num.new": sp(GoldHi, None, b),
	"icon.pr": sp(Blue, None, 0), "icon.draft": sp(G4, None, 0),
	"icon.issue": sp(G6, None, 0), "icon.queued": sp(Blue3, None, 0),
	"author": sp(G6, None, 0), "author.me": sp(Gold2, None, 0),
	"author.bot": sp(Blue3, None, 0),
	"age.0":      sp(White, None, b), "age.1": sp(Blue4, None, 0),
	"age.2": sp(G5, None, 0), "age.3": sp(G4, None, 0),
	"title": sp(G6, None, 0), "title.attn": sp(G7, None, 0),
	"title.done": sp(G4, None, 0),
	"add":        sp(Blue2, None, 0), "del": sp(Gold2, None, 0), "zero": sp(G4, None, 0),
	"ci.pass": sp(Blue, None, 0), "ci.fail": sp(Gold, None, b),
	"ci.fail.flash": sp(K, Gold, b),
	"ci.run":        sp(Blue4, None, b), "ci.wait": sp(G5, None, 0),
	"ci.skip": sp(G4, None, 0), "ci.unknown": sp(G4, None, 0),
	"failnames": sp(Gold, None, 0), "count": sp(G5, None, 0),
	"stack": sp(Blue4, None, 0), "rev.ok": sp(Blue3, None, 0),
	"rev.attend": sp(Gold, None, b),
	"sum.attn":   sp(Gold, None, b), "sum.fail": sp(Gold2, None, 0),
	"sum.info": sp(Blue3, None, 0), "sum.muted": sp(G5, None, 0),
	// the selection gutter breathes through these
	"gutter.0": sp(Blue, None, b), "gutter.1": sp(Blue2, None, b),
	"gutter.2": sp(Blue3, None, b), "gutter.3": sp(Sky, None, b),
	"gutter.4": sp(Blue4, None, b),
	// detail: markdown
	"h1": sp(White, None, b), "h2": sp(Blue2, None, b), "h3": sp(Blue3, None, b),
	"md.bullet": sp(Blue, None, 0), "md.quote": sp(G5, None, 0),
	"md.bar": sp(G3, None, 0), "md.code": sp(G6, None, 0), "md.lang": sp(G4, None, 0),
	"md.inline": sp(Blue4, None, 0), "md.html": sp(G4, None, 0),
	"md.link": sp(Blue2, None, u), "md.url": sp(G4, None, 0),
	"md.bold": sp(G7, None, b), "md.rule": sp(G3, None, 0),
	"md.pipe": sp(G3, None, 0), "md.num": sp(G5, None, 0),
	// detail: terraform plans and approvals
	"tf.create": sp(Blue2, None, 0), "tf.destroy": sp(Gold, None, b),
	"tf.replace": sp(Gold, None, b), "tf.update": sp(White, None, 0),
	"tf.read": sp(G4, None, 0), "tf.note": sp(G5, None, 0),
	"plan.add": sp(Blue2, None, b), "plan.change": sp(White, None, b),
	"plan.destroy": sp(Gold, None, b), "plan.zero": sp(G5, None, 0),
	"approve.cmd": sp(White, BlueBG, b), "approve.cmd.hi": sp(White, BlueBG2, b),
	"approve.cmd.hi2": sp(White, BlueBG3, b),
	"approve.mention": sp(Blue2, None, b), "approve.refused": sp(Gold, None, b),
	"verdict.ok": sp(Blue3, None, 0), "verdict.disruptive": sp(Gold2, None, 0),
	"verdict.notrev": sp(Gold, None, b),
	"card":           sp(G3, None, 0), "card.title": sp(G5, None, b),
	"plan.stack": sp(Blue4, None, 0), "plan.id": sp(G5, None, 0),
	"plan.live": sp(Blue, None, b), "plan.dead": sp(G4, None, 0),
	"destroys.0": sp(Blue3, None, 0), "destroys.n": sp(Gold, None, b),
	"tl.bar": sp(G3, None, 0), "tl.dot": sp(Blue, None, 0),
	"tl.dot.bot": sp(G4, None, 0),
	"tl.when":    sp(G4, None, 0), "tl.verb": sp(G5, None, 0),
	"scroll.track": sp(G3, None, 0), "scroll.thumb": sp(Blue2, None, 0),
	"pos": sp(G4, None, 0),
	// diffs and files
	"diff.add": sp(Blue2, None, 0), "diff.del": sp(Gold2, None, 0),
	"diff.hunk": sp(G4, None, 0), "diff.func": sp(G7, None, 0),
	"diff.ctx": sp(G6, None, 0), "diff.meta": sp(G4, None, 0),
	"file.added": sp(Blue2, None, 0), "file.removed": sp(Gold2, None, 0),
	"file.modified": sp(G7, None, 0), "file.renamed": sp(Blue3, None, 0),
	"file.other": sp(G5, None, 0), "path.dir": sp(G5, None, 0),
	"path.base": sp(G7, None, 0), "bar.add": sp(Blue2, None, 0),
	"bar.del": sp(Gold2, None, 0), "bar.none": sp(G3, None, 0),
	// status line, key bar, prompts
	"toast.info": sp(G6, None, 0), "toast.ok": sp(Blue2, None, b),
	"toast.warn": sp(Gold, None, b), "toast.fade": sp(G4, None, 0),
	"keylabel": sp(G5, None, 0),
	"prompt":   sp(White, None, b), "input": sp(White, None, 0),
	"menu.title": sp(White, None, b), "menu.item": sp(G6, None, 0),
	"menu.hint": sp(G4, None, 0), "art": sp(G4, None, 0),
	"allclear": sp(Blue3, None, b),
	// the minimum-length prompt (the --outside-window reason): its hint
	// row and the meter across the bottom of the screen
	"hint.short": sp(Gold2, None, b), "hint.bad": sp(Gold, None, b),
	"hint.ok": sp(Blue2, None, b), "hint.keys": sp(G5, None, 0),
	"meter.short": sp(Gold, None, 0), "meter.ok": sp(Blue2, None, 0),
	"meter.track": sp(G3, None, 0), "meter.track.bad": sp(GoldBG, None, 0),
	"meter.shine": sp(White, None, b), "meter.label": sp(G5, None, 0),
	"meter.label.short": sp(Gold2, None, b), "meter.label.ok": sp(Blue2, None, b),
	"hazard": sp(Gold, None, b), "hazard.dim": sp(GoldBG, None, 0),
	// effects: transient decoration that never carries text, so no
	// contrast floor, but still only grays, blues and golds
	"fx.b0": sp(Navy, None, 0), "fx.b1": sp(Navy2, None, 0),
	"fx.b2": sp(BlueBG, None, 0), "fx.b3": sp(Blue, None, 0),
	"fx.b4": sp(Blue2, None, 0), "fx.b5": sp(Sky, None, 0),
	"fx.b6": sp(Blue4, None, 0), "fx.b7": sp(White, None, b),
	"fx.g0": sp(GoldBG, None, 0), "fx.g1": sp(Ochre, None, 0),
	"fx.g2": sp(Gold, None, b), "fx.g3": sp(GoldHi, None, b),
	"fx.k0": sp(236, None, 0), "fx.k1": sp(239, None, 0), "fx.k2": sp(242, None, 0),
	"fx.k3": sp(G4, None, 0), "fx.k4": sp(G6, None, 0), "fx.k5": sp(White, None, 0),
	"fx.logo.0": sp(Blue4, None, b), "fx.logo.1": sp(Sky, None, b),
	"fx.logo.2": sp(Blue3, None, b), "fx.logo.3": sp(Blue2, None, b),
	"fx.logo.4": sp(Blue, None, b), "fx.logo.5": sp(BlueBG3, None, b),
	"fx.logo.shade": sp(Steel, None, 0), "fx.logo.shine": sp(White, None, b),
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// MonoBold is the styles whose meaning is "look here": bold in mono, gold
// elsewhere.
var MonoBold = set("chip.await", "chip.changes", "chip.err", "ci.fail", "failnames",
	"tf.destroy", "tf.replace", "plan.destroy", "destroys.n",
	"approve.refused", "verdict.notrev", "rev.attend", "sum.attn",
	"budget.low", "chip.rev.changes", "chip.plan.warn", "hint.bad",
	"toast.warn", "hazard", "ci.fail.flash", "num.new")

// Contrast floors (see the package notes). Decor: rules, borders and
// tracks; Icons: glyphs whose shape carries the meaning too. Both take
// 3:1. Shape styles color a cell edge or a texture (powerline separators,
// the meter's trough), and fx.* styles are transient decoration: neither
// draws text, so neither has a floor.
var (
	Decor = set("rule", "card", "md.bar", "md.rule", "md.pipe", "tl.bar",
		"scroll.track", "bar.none", "meter.track")
	Icons = set("gutter.0", "gutter.1", "gutter.2", "gutter.3", "gutter.4",
		"hdr.logo.0", "hdr.logo.1", "hdr.logo.2", "hazard", "hazard.dim")
	Shape = set("hdr.sep.app", "hdr.sep.app.k", "hdr.sep.repo", "meter.track.bad")
)

// Floor is the contrast floor a style must clear wherever it is drawn.
func Floor(name string) float64 {
	if Shape[name] || strings.HasPrefix(name, "fx.") {
		return 0
	}
	if Decor[name] || Icons[name] {
		return 3.0
	}
	return 4.5
}

// Names lists every style name, sorted.
func Names() []string {
	out := make([]string, 0, len(Styles))
	for n := range Styles {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Variant is one (style, selected, band) combination the screen can ask
// for.
type Variant struct {
	Name      string
	Sel, Band bool
}

// Variants is every combination, base styles first: effects draw only on
// the plain background.
func Variants() []Variant {
	var out, fx []Variant
	for _, n := range Names() {
		if strings.HasPrefix(n, "fx.") {
			fx = append(fx, Variant{n, false, false})
			continue
		}
		for _, s := range []bool{false, true} {
			for _, b := range []bool{false, true} {
				out = append(out, Variant{n, s, b})
			}
		}
	}
	return append(out, fx...)
}

// spec is the declared style, or "text" for an unknown name.
func spec(name string) Spec {
	if s, ok := Styles[name]; ok {
		return s
	}
	return Styles["text"]
}

// StyleFG is the foreground a style actually draws in over bg (a palette
// index): its own, lifted to the style's contrast floor when bg would take
// it under (the selected row, a code band). Chips keep their designed
// pair.
func StyleFG(name string, bg int) int {
	s := spec(name)
	if _, ok := Styles[name]; !ok {
		name = "text"
	}
	return Lift(s.FG, bg, Floor(name))
}

// Tier is how much color the terminal gets.
type Tier int

// The tiers, least capable first.
const (
	Mono      Tier = iota // attributes only
	Color16               // the 16 ANSI colors
	Color256              // the xterm palette by index
	TrueColor             // the xterm palette as exact RGB
)

func (t Tier) String() string {
	switch t {
	case TrueColor:
		return "truecolor"
	case Color256:
		return "256"
	case Color16:
		return "16"
	}
	return "mono"
}

var (
	mu      sync.RWMutex
	current = Mono
	cache   = map[cacheKey]lipgloss.Style{}
)

type cacheKey struct {
	name      string
	sel, band bool
	tier      Tier
}

// Setup picks the tier from the terminal's color profile: truecolor and
// 256 use the fixed xterm palette, a 16-color terminal gets the ANSI
// fallbacks, and no color (NO_COLOR, --mono, a dumb terminal or a pipe)
// means attributes only. It resets the style cache.
func Setup(profile colorprofile.Profile, mono bool) Tier {
	t := Mono
	switch {
	case mono, os.Getenv("NO_COLOR") != "":
	case profile >= colorprofile.TrueColor:
		t = TrueColor
	case profile == colorprofile.ANSI256:
		t = Color256
	case profile == colorprofile.ANSI:
		t = Color16
	}
	mu.Lock()
	current = t
	cache = map[cacheKey]lipgloss.Style{}
	mu.Unlock()
	return t
}

// Current is the tier in use (Mono until Setup runs).
func Current() Tier {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// resolved is a style with its colors and attributes decided for a tier.
// fg/bg are nil for the terminal's own.
type resolved struct {
	fg, bg                        color.Color
	bold, dim, underline, reverse bool
}

func (r resolved) style() lipgloss.Style {
	s := lipgloss.NewStyle().Inline(true)
	if r.fg != nil {
		s = s.Foreground(r.fg)
	}
	if r.bg != nil {
		s = s.Background(r.bg)
	}
	if r.bold {
		s = s.Bold(true)
	}
	if r.dim {
		s = s.Faint(true)
	}
	if r.underline {
		s = s.Underline(true)
	}
	if r.reverse {
		s = s.Reverse(true)
	}
	return s
}

// paletteColor is a palette index as the tier draws it.
func paletteColor(tier Tier, i int) color.Color {
	if tier == TrueColor {
		r, g, b := XtermRGB(i)
		return lipgloss.RGBColor{R: r, G: g, B: b}
	}
	return lipgloss.ANSIColor(i)
}

// to16 is the ANSI color standing in for a palette index on the 16-color
// tier, and whether it draws bold (bright) or dim. The bright variants
// stand in for the terminal's bold-brightens-the-color convention.
func to16(i int) (c ansi.BasicColor, bold, dim bool) {
	switch {
	case i == None || i == K:
		return ansi.Black, false, false
	case IsBlue(i):
		return ansi.BrightBlue, true, false
	case IsGold(i):
		return ansi.BrightYellow, true, false
	case i >= 250 || i == White:
		return ansi.BrightWhite, true, false
	case i >= 244:
		return ansi.White, false, false
	}
	return ansi.White, false, true
}

// resolve decides a style's colors and attributes. sel = on the selected
// row (its own background, never reverse: reversing a colored chip turns
// a gold chip into a gold block); band = inside a code block.
func resolve(name string, sel, band bool, tier Tier) resolved {
	if strings.HasPrefix(name, "cap:") {
		return resolveCap(name[len("cap:"):], sel, tier)
	}
	s := spec(name)
	chip := s.Chip()
	var r resolved
	switch tier {
	case Color256, TrueColor:
		fg, bg := s.FG, s.BG
		if !chip {
			bg = K
			if sel {
				bg = SelBG
			} else if band {
				bg = BandBG
			}
			fg = StyleFG(name, bg)
		}
		r.fg, r.bg = paletteColor(tier, fg), paletteColor(tier, bg)
		r.bold = s.Attr&text.Bold != 0
		r.underline = s.Attr&text.Underline != 0
	case Color16:
		f, fb, fd := to16(s.FG)
		switch {
		case chip && IsGold(s.BG):
			// black on yellow, white on blue: and no bold on black, which
			// many terminals turn into dark gray
			r.fg, r.bg = ansi.Black, ansi.Yellow
		case chip && IsBlue(s.BG):
			r.fg, r.bg = ansi.White, ansi.Blue
			r.bold = true
		default:
			// gray chips: there is no gray background in 8 colors, so
			// plain text (the caps disappear, the label stays readable)
			r.fg, r.bg = f, ansi.Black
			r.bold = fb || s.Attr&text.Bold != 0
			r.underline = s.Attr&text.Underline != 0
			r.dim = fd
			r.reverse = sel && !chip
		}
	default: // mono: attributes only
		switch {
		case chip:
			// on the (reversed) selected row a reversed chip would vanish
			if sel {
				r.bold, r.underline = true, true
			} else {
				r.reverse = true
			}
		case s.FG == White || s.FG == G7 || MonoBold[name]:
			r.bold = true
		case s.FG < G4 && !IsBlue(s.FG) && !IsGold(s.FG):
			// dim only what is darker than faint text: rules, borders,
			// tracks; dim TEXT is unreadable on many terminals
			r.dim = true
		}
		if MonoBold[name] {
			r.bold = true
		}
		r.underline = r.underline || s.Attr&text.Underline != 0
		if sel && !chip {
			r.reverse = true
		}
	}
	if r.bold && r.dim {
		r.dim = false // conflicting SGR codes; terminals disagree
	}
	return r
}

// resolveCap is the rounded chip caps: fg = the chip's background color,
// bg = the row's.
func resolveCap(name string, sel bool, tier Tier) resolved {
	s := spec(name)
	var r resolved
	switch tier {
	case Color256, TrueColor:
		bg := K
		if sel {
			bg = SelBG
		}
		r.fg, r.bg = paletteColor(tier, s.BG), paletteColor(tier, bg)
	case Color16:
		switch {
		case IsBlue(s.BG):
			r.fg = ansi.Blue
		case IsGold(s.BG):
			r.fg = ansi.Yellow
		default:
			r.fg = ansi.Black
		}
		r.bg = ansi.Black
	}
	return r
}

// Style is the lipgloss style for a style name on the current tier,
// cached per (name, sel, band, tier). "cap:<chip>" is the chip's cap: its
// foreground is the chip's background, on the row's background. An
// unknown name draws as "text".
func Style(name string, sel, band bool) lipgloss.Style {
	mu.RLock()
	tier := current
	key := cacheKey{name, sel, band, tier}
	st, ok := cache[key]
	mu.RUnlock()
	if ok {
		return st
	}
	st = resolve(name, sel, band, tier).style()
	mu.Lock()
	cache[key] = st
	mu.Unlock()
	return st
}

// Render draws the segments in their styles, with each segment's extra
// attributes (search hits) laid on top.
func Render(segs []text.Seg, sel, band bool) string {
	var out strings.Builder
	for _, seg := range segs {
		if seg.Text == "" {
			continue
		}
		st := Style(seg.Style, sel, band)
		if seg.Extra&text.Bold != 0 {
			st = st.Bold(true)
		}
		if seg.Extra&text.Underline != 0 {
			st = st.Underline(true)
		}
		if seg.Extra&text.Reverse != 0 {
			st = st.Reverse(true)
		}
		out.WriteString(st.Render(seg.Text))
	}
	return out.String()
}

// Palette writes the --glyph-test color dump in raw xterm-256 ANSI: the
// palette swatches, a meter and two chips.
func Palette(w io.Writer) {
	sw := func(i int) string {
		return fmt.Sprintf("\x1b[48;5;%dm  \x1b[0m %-4d", i, i)
	}
	rows := []struct {
		title string
		idx   []int
	}{
		{"palette (xterm 256):", []int{K, G1, G3, G4, G5, G6, G7, White}},
		{"", []int{Navy, Navy2, Steel, BlueBG, BlueBG2, BlueBG3}},
		{"", []int{Blue, Blue2, Blue3, Sky, Blue4}},
		{"", []int{GoldBG, Ochre, Gold, Gold2, GoldHi}},
	}
	for _, r := range rows {
		cells := make([]string, len(r.idx))
		for i, c := range r.idx {
			cells[i] = sw(c)
		}
		fmt.Fprintf(w, "%-21s%s\n", r.title, strings.Join(cells, " "))
	}
	fmt.Fprint(w, "\n")
	g := glyphs.All(glyphs.Nerd)
	eighths := []rune(g["eighths"])
	fmt.Fprintf(w, "meter: \x1b[38;5;%dm%s%s\x1b[38;5;%dm%s\x1b[0m 12/20\n",
		Gold, strings.Repeat(g["block"], 7), string(eighths[3]), G3,
		strings.Repeat(g["trough"], 4))
	fmt.Fprintf(w, "chip: \x1b[38;5;%[1]dm%[3]s\x1b[48;5;%[1]d;38;5;%[2]d;1m awaiting review "+
		"\x1b[0m\x1b[38;5;%[1]dm%[4]s\x1b[0m   \x1b[38;5;%[5]dm%[3]s\x1b[48;5;%[5]d;38;5;%[6]dm"+
		" approved by me \x1b[0m\x1b[38;5;%[5]dm%[4]s\x1b[0m\n",
		Gold, K, g["capl"], g["capr"], BlueBG, White)
}
