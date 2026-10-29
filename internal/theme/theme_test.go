package theme

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/solcyber/gh-dillodash/internal/text"
)

// withTier runs f on a tier and restores the default afterwards.
func withTier(t *testing.T, tier Tier, f func()) {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	var p colorprofile.Profile
	switch tier {
	case TrueColor:
		p = colorprofile.TrueColor
	case Color256:
		p = colorprofile.ANSI256
	case Color16:
		p = colorprofile.ANSI
	default:
		p = colorprofile.ASCII
	}
	if got := Setup(p, false); got != tier {
		t.Fatalf("Setup(%v) = %v, want %v", p, got, tier)
	}
	defer Setup(colorprofile.ASCII, true)
	f()
}

func TestContrastBlackOnWhiteIs21(t *testing.T) {
	if got := math.Round(Contrast(K, White)*10) / 10; got != 21.0 {
		t.Errorf("Contrast(K, White) = %v, want 21", got)
	}
	if Contrast(33, 33) != 1.0 {
		t.Errorf("Contrast(33, 33) = %v, want 1", Contrast(33, 33))
	}
	if Contrast(K, White) != Contrast(White, K) {
		t.Error("Contrast is not symmetric")
	}
}

func TestXtermRGB(t *testing.T) {
	cases := map[int][3]uint8{
		16: {0, 0, 0}, 231: {255, 255, 255}, 33: {0, 135, 255}, 220: {255, 215, 0},
		232: {8, 8, 8}, 255: {238, 238, 238}, 0: {0, 0, 0}, 15: {255, 255, 255},
	}
	for i, want := range cases {
		r, g, b := XtermRGB(i)
		if [3]uint8{r, g, b} != want {
			t.Errorf("XtermRGB(%d) = %v, want %v", i, [3]uint8{r, g, b}, want)
		}
	}
}

func TestEveryStyleClearsItsContrastFloorEverywhere(t *testing.T) {
	var low []string
	for _, name := range Names() {
		s := Styles[name]
		fl := Floor(name)
		if fl == 0 {
			continue
		}
		if s.Chip() { // a chip: its own designed pair
			if c := Contrast(s.FG, s.BG); c < fl {
				low = append(low, fmt.Sprintf("%s on its chip %.2f", name, c))
			}
			continue
		}
		if c := Contrast(s.FG, K); c < fl { // the table itself is honest on black
			low = append(low, fmt.Sprintf("%s declared, on black %.2f", name, c))
		}
		for where, bg := range map[string]int{"black": K, "the selected row": SelBG, "a code band": BandBG} {
			f := StyleFG(name, bg)
			if c := Contrast(f, bg); c < fl {
				low = append(low, fmt.Sprintf("%s on %s %.2f", name, where, c))
			}
		}
	}
	if len(low) > 0 {
		t.Errorf("under the floor: %v", low)
	}
}

func TestFloors(t *testing.T) {
	// text floors are 4.5:1, rules and icons 3:1, effects and shapes none
	for name, want := range map[string]float64{"title": 4.5, "rule": 3, "gutter.2": 3, "fx.b3": 0, "hdr.sep.app": 0, "chip.await": 4.5} {
		if got := Floor(name); got != want {
			t.Errorf("Floor(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestSelectedRowLiftsADimForegroundRatherThanDrownIt(t *testing.T) {
	f := StyleFG("faint", SelBG)
	if f != Styles["faint"].FG && Contrast(f, SelBG) < 4.5 {
		t.Errorf("faint on the selected row = %d (%.2f:1)", f, Contrast(f, SelBG))
	}
	if f == Styles["faint"].FG {
		t.Logf("faint already clears the selected row")
	}
}

func TestLiftClimbsTheStylesOwnRamp(t *testing.T) {
	// gray stays gray
	l := Lift(241, SelBG, 4.5)
	r, g, b := XtermRGB(l)
	if r != g || g != b {
		t.Errorf("Lift(241) = %d, rgb %d %d %d: left the gray ramp", l, r, g, b)
	}
	if l <= 241 {
		t.Errorf("Lift(241) = %d, did not climb", l)
	}
	// blue stays blue, gold stays gold
	if l := Lift(Blue, SelBG, 7); !IsBlue(l) && l != White {
		t.Errorf("Lift(Blue) = %d", l)
	}
	if l := Lift(GoldBG, K, 4.5); !IsGold(l) {
		t.Errorf("Lift(GoldBG) = %d", l)
	}
	// already clear: unchanged; off every ramp: white; no ratio: unchanged
	if Lift(White, K, 4.5) != White || Lift(Navy, K, 4.5) != White || Lift(Navy, K, 0) != Navy {
		t.Error("Lift edge cases")
	}
	// the ramp running out ends at white
	if Lift(GoldHi, GoldHi, 4.5) != White {
		t.Errorf("Lift(GoldHi on GoldHi) = %d", Lift(GoldHi, GoldHi, 4.5))
	}
}

func TestNoChipSharesTheSelectionBarsColor(t *testing.T) {
	// it would lose its shape
	for name, s := range Styles {
		if strings.HasPrefix(name, "chip.") && s.BG == SelBG {
			t.Errorf("%s uses the selection background", name)
		}
	}
}

func TestPaletteIsOnlyGraysBluesAndYellows(t *testing.T) {
	for name, s := range Styles {
		for _, c := range []int{s.FG, s.BG} {
			if c == None {
				continue
			}
			r, g, b := XtermRGB(c)
			gray := r == g && g == b
			blue := b > r && b >= g && r <= g
			yellow := r >= 175 && g >= 135 && b <= 95 && int(math.Abs(float64(r)-float64(g))) <= 40
			if !(gray || blue || yellow) {
				t.Errorf("%s uses %d = %d,%d,%d", name, c, r, g, b)
			}
		}
	}
}

func TestEveryNameTheAppUsesIsDefined(t *testing.T) {
	for _, n := range []string{
		"text", "dim", "title", "num", "author", "author.me", "author.bot",
		"age.0", "age.1", "age.2", "age.3", "chip.await", "chip.mine",
		"chip.approved", "chip.changes", "chip.draft", "chip.state",
		"chip.rolled", "chip.err", "ci.pass", "ci.fail", "ci.wait", "ci.run",
		"ci.skip", "ci.unknown", "add", "del", "zero", "stack", "hdr.app",
		"sel", "band", "rule", "card", "md.code", "tl.bar", "tf.destroy",
		"verdict.ok", "rev.ok", "plan.add", "fx.b0", "meter.short",
		"approve.cmd", "prompt", "gutter.0",
	} {
		if _, ok := Styles[n]; !ok {
			// "dim", "sel" and "band" are not styles in the source table:
			// dim is "muted"/"faint", sel and band are Style arguments
			if n == "dim" || n == "sel" || n == "band" {
				continue
			}
			t.Errorf("style %q missing", n)
		}
	}
	for _, n := range Names() {
		if strings.HasPrefix(n, "df") {
			t.Errorf("fortress colors must stay out of Styles: %s", n)
		}
	}
	if len(Styles) < 190 {
		t.Errorf("only %d styles", len(Styles))
	}
}

func TestVariantsCoverEveryStyle(t *testing.T) {
	v := Variants()
	base, fx := 0, 0
	for _, n := range Names() {
		if strings.HasPrefix(n, "fx.") {
			fx++
		} else {
			base++
		}
	}
	if len(v) != base*4+fx {
		t.Errorf("%d variants for %d base + %d fx styles", len(v), base, fx)
	}
	if strings.HasPrefix(v[0].Name, "fx.") || !strings.HasPrefix(v[len(v)-1].Name, "fx.") {
		t.Error("effects must come last")
	}
}

func TestMonoTier(t *testing.T) {
	withTier(t, Mono, func() {
		for _, name := range Names() {
			r := resolve(name, false, false, Mono)
			if r.bold && r.dim {
				t.Errorf("mono: %s is bold+dim", name)
			}
			if r.fg != nil || r.bg != nil {
				t.Errorf("mono: %s has a color", name)
			}
		}
		if !resolve("chip.await", false, false, Mono).reverse {
			t.Error("mono: chips are reversed")
		}
		if r := resolve("chip.await", true, false, Mono); r.reverse || !r.bold || !r.underline {
			t.Errorf("mono: a chip on the selected row is bold+underline, not reversed: %+v", r)
		}
		for name := range MonoBold {
			if !resolve(name, false, false, Mono).bold {
				t.Errorf("mono: attention style %s is not bold", name)
			}
		}
		if !Style("ci.fail", false, false).GetBold() {
			t.Error("mono: Style(ci.fail) lost bold")
		}
		if resolve("faint", false, false, Mono).dim {
			t.Error("mono: faint TEXT is never dimmed (unreadable on many terminals)")
		}
		if !resolve("rule", false, false, Mono).dim {
			t.Error("mono: rules are dim")
		}
		if !resolve("strong", false, false, Mono).bold || !resolve("bright", false, false, Mono).bold {
			t.Error("mono: white and G7 text are bold")
		}
		if !resolve("md.link", false, false, Mono).underline {
			t.Error("mono: links keep underline")
		}
		if !resolve("text", true, false, Mono).reverse {
			t.Error("mono: the selected row is reversed")
		}
		if r := resolve("cap:chip.await", true, false, Mono); r.bold || r.reverse || r.fg != nil {
			t.Errorf("mono: caps draw nothing: %+v", r)
		}
	})
}

func TestColor16Tier(t *testing.T) {
	withTier(t, Color16, func() {
		if r := resolve("faint", false, false, Color16); r.dim || r.fg != ansi.White {
			t.Errorf("16-color: faint text is plain white, not dim: %+v", r)
		}
		if r := resolve("rule", false, false, Color16); !r.dim {
			t.Error("16-color: rules are dim")
		}
		if r := resolve("ci.pass", false, false, Color16); r.fg != ansi.BrightBlue || !r.bold {
			t.Errorf("16-color: blues map to bright blue: %+v", r)
		}
		if r := resolve("ci.fail", false, false, Color16); r.fg != ansi.BrightYellow || !r.bold || r.dim {
			t.Errorf("16-color: golds map to bright yellow: %+v", r)
		}
		if r := resolve("strong", false, false, Color16); r.fg != ansi.BrightWhite || !r.bold {
			t.Errorf("16-color: white is bright white: %+v", r)
		}
		if r := resolve("chip.await", false, false, Color16); r.fg != ansi.Black || r.bg != ansi.Yellow || r.bold {
			t.Errorf("16-color: gold chips are black on yellow, no bold: %+v", r)
		}
		if r := resolve("chip.mine", false, false, Color16); r.fg != ansi.White || r.bg != ansi.Blue || !r.bold {
			t.Errorf("16-color: blue chips are bold white on blue: %+v", r)
		}
		if r := resolve("chip.draft", false, false, Color16); r.bg != ansi.Black || r.reverse {
			t.Errorf("16-color: gray chips are plain text: %+v", r)
		}
		if r := resolve("text", true, false, Color16); !r.reverse {
			t.Error("16-color: the selected row is reversed")
		}
		if r := resolve("chip.await", true, false, Color16); r.reverse {
			t.Error("16-color: a chip on the selected row is not reversed")
		}
		if r := resolve("cap:chip.await", false, false, Color16); r.fg != ansi.Yellow || r.bg != ansi.Black {
			t.Errorf("16-color: a gold chip's cap is yellow on black: %+v", r)
		}
		if r := resolve("cap:chip.mine", false, false, Color16); r.fg != ansi.Blue {
			t.Errorf("16-color: a blue chip's cap is blue: %+v", r)
		}
		if r := resolve("cap:chip.draft", false, false, Color16); r.fg != ansi.Black {
			t.Errorf("16-color: a gray chip's cap vanishes: %+v", r)
		}
	})
}

func TestColor256Tier(t *testing.T) {
	withTier(t, Color256, func() {
		if r := resolve("text", false, false, Color256); r.fg != lipgloss.ANSIColor(G6) || r.bg != lipgloss.ANSIColor(K) {
			t.Errorf("256: text is G6 on black: %+v", r)
		}
		if r := resolve("text", true, false, Color256); r.bg != lipgloss.ANSIColor(SelBG) || r.reverse {
			t.Errorf("256: the selected row has its own background, never reverse: %+v", r)
		}
		if r := resolve("md.code", false, true, Color256); r.bg != lipgloss.ANSIColor(BandBG) {
			t.Errorf("256: code bands: %+v", r)
		}
		if r := resolve("md.code", true, true, Color256); r.bg != lipgloss.ANSIColor(SelBG) {
			t.Errorf("256: the selected row wins over the band: %+v", r)
		}
		// lifted: a rule (G3) on the selected row climbs the gray ramp
		// (faint, G4, already clears it there, so stays as declared)
		plain := resolve("rule", false, false, Color256)
		sel := resolve("rule", true, false, Color256)
		if plain.fg != lipgloss.ANSIColor(G3) || sel.fg == plain.fg || sel.fg != lipgloss.ANSIColor(StyleFG("rule", SelBG)) {
			t.Errorf("256: rule is not lifted on the selected row: %+v / %+v", plain, sel)
		}
		if r := resolve("faint", true, false, Color256); r.fg != lipgloss.ANSIColor(G4) {
			t.Errorf("256: faint already clears the selected row and must stay G4: %+v", r)
		}
		if r := resolve("chip.await", true, true, Color256); r.fg != lipgloss.ANSIColor(K) || r.bg != lipgloss.ANSIColor(Gold) || !r.bold {
			t.Errorf("256: chips keep their pair everywhere: %+v", r)
		}
		if r := resolve("md.link", false, false, Color256); !r.underline {
			t.Error("256: links underline")
		}
		if r := resolve("no.such.style", false, false, Color256); r != resolve("text", false, false, Color256) {
			t.Error("256: an unknown style draws as text")
		}
	})
}

func TestChipCaps(t *testing.T) {
	withTier(t, Color256, func() {
		for _, name := range Names() {
			s := Styles[name]
			if !s.Chip() {
				continue
			}
			for _, sel := range []bool{false, true} {
				r := resolve("cap:"+name, sel, false, Color256)
				wantBG := K
				if sel {
					wantBG = SelBG
				}
				if r.fg != lipgloss.ANSIColor(s.BG) || r.bg != lipgloss.ANSIColor(wantBG) {
					t.Errorf("cap:%s sel=%v = %+v, want fg %d bg %d", name, sel, r, s.BG, wantBG)
				}
				if r.bold || r.underline || r.reverse || r.dim {
					t.Errorf("cap:%s carries attributes: %+v", name, r)
				}
			}
		}
	})
	withTier(t, TrueColor, func() {
		r := resolve("cap:chip.await", false, false, TrueColor)
		if r.fg != (lipgloss.RGBColor{R: 255, G: 215, B: 0}) {
			t.Errorf("truecolor cap fg = %+v", r.fg)
		}
	})
}

func TestTrueColorTierUsesExactRGB(t *testing.T) {
	withTier(t, TrueColor, func() {
		r := resolve("text", false, false, TrueColor)
		cr, cg, cb := XtermRGB(G6)
		if r.fg != (lipgloss.RGBColor{R: cr, G: cg, B: cb}) || r.bg != (lipgloss.RGBColor{}) {
			t.Errorf("truecolor text = %+v", r)
		}
		out := Render([]text.Seg{text.S("x", "ci.pass")}, false, false)
		if !strings.Contains(out, "38;2;0;135;255") {
			t.Errorf("truecolor render = %q", out)
		}
	})
}

func TestSetupPicksTheTier(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	cases := []struct {
		p    colorprofile.Profile
		mono bool
		want Tier
	}{
		{colorprofile.TrueColor, false, TrueColor},
		{colorprofile.ANSI256, false, Color256},
		{colorprofile.ANSI, false, Color16},
		{colorprofile.ASCII, false, Mono},
		{colorprofile.NoTTY, false, Mono},
		{colorprofile.Unknown, false, Mono},
		{colorprofile.TrueColor, true, Mono},
	}
	for _, c := range cases {
		if got := Setup(c.p, c.mono); got != c.want || Current() != c.want {
			t.Errorf("Setup(%v, %v) = %v, want %v", c.p, c.mono, got, c.want)
		}
	}
	t.Setenv("NO_COLOR", "1")
	if got := Setup(colorprofile.TrueColor, false); got != Mono {
		t.Errorf("NO_COLOR: %v", got)
	}
	Setup(colorprofile.ASCII, true)
	for tier, s := range map[Tier]string{TrueColor: "truecolor", Color256: "256", Color16: "16", Mono: "mono"} {
		if tier.String() != s {
			t.Errorf("%d.String() = %q", tier, tier.String())
		}
	}
}

func TestStyleIsCachedPerTier(t *testing.T) {
	withTier(t, Color256, func() {
		a := Style("text", false, false)
		if a.GetForeground() != lipgloss.ANSIColor(G6) {
			t.Errorf("256 fg = %v", a.GetForeground())
		}
	})
	withTier(t, Mono, func() {
		if Style("text", false, false).GetForeground() != (lipgloss.NoColor{}) {
			t.Error("mono style kept the 256 cache entry")
		}
	})
}

func TestRenderAppliesExtraAttributes(t *testing.T) {
	withTier(t, Color256, func() {
		plain := Render([]text.Seg{text.S("hit", "text")}, false, false)
		bold := Render([]text.Seg{{Text: "hit", Style: "text", Extra: text.Bold}}, false, false)
		rev := Render([]text.Seg{{Text: "hit", Style: "text", Extra: text.Reverse | text.Bold}}, false, false)
		ul := Render([]text.Seg{{Text: "hit", Style: "text", Extra: text.Underline}}, false, false)
		if ansi.Strip(plain) != "hit" || ansi.Strip(bold) != "hit" || ansi.Strip(rev) != "hit" {
			t.Error("Render changed the text")
		}
		if !strings.Contains(plain, "38;5;251") || !strings.Contains(plain, "48;5;16") {
			t.Errorf("plain = %q", plain)
		}
		if plain == bold || !strings.Contains(bold, "\x1b[1;") && !strings.Contains(bold, ";1;") && !strings.Contains(bold, ";1m") {
			t.Errorf("bold = %q", bold)
		}
		if !strings.Contains(rev, "7") || rev == bold {
			t.Errorf("reverse = %q", rev)
		}
		if !strings.Contains(ul, "4") || ul == plain {
			t.Errorf("underline = %q", ul)
		}
		if Render([]text.Seg{text.S("", "text")}, false, false) != "" {
			t.Error("empty segment should render nothing")
		}
	})
	withTier(t, Mono, func() {
		out := Render([]text.Seg{text.S("a", "text"), text.S("b", "ci.fail")}, false, false)
		if ansi.Strip(out) != "ab" || strings.Contains(out, "38;") {
			t.Errorf("mono render = %q", out)
		}
	})
}

func TestRenderPaddedSegsCarryBackground(t *testing.T) {
	withTier(t, Color256, func() {
		segs := []text.Seg{text.S("abc", "title")}
		out := Render(text.PadSegs(segs, 10), false, false)
		if ansi.StringWidth(out) != 10 || ansi.Strip(out) != "abc       " {
			t.Errorf("Render = %q", ansi.Strip(out))
		}
		// the padding carries the band / selected-row background
		band := Render(text.PadSegs(segs, 10), false, true)
		if !strings.Contains(band, fmt.Sprintf("48;5;%d", BandBG)) {
			t.Errorf("band padding = %q", band)
		}
		sel := Render(text.PadSegs(segs, 10), true, false)
		if !strings.Contains(sel, fmt.Sprintf("48;5;%d", SelBG)) || strings.Contains(sel, fmt.Sprintf("48;5;%d", K)) {
			t.Errorf("selected padding = %q", sel)
		}
		if w := ansi.StringWidth(Render(text.PadSegs([]text.Seg{text.S("日本語です", "text")}, 5), false, false)); w != 5 {
			t.Errorf("clipped width = %d", w)
		}
	})
}

func TestPaletteDump(t *testing.T) {
	var buf bytes.Buffer
	Palette(&buf)
	out := buf.String()
	for _, want := range []string{"palette (xterm 256):", "48;5;16m", "48;5;220m", "48;5;17m", "meter:", "12/20", "awaiting review", "approved by me", "\ue0b6", "\ue0b4"} {
		if !strings.Contains(out, want) {
			t.Errorf("palette dump lacks %q:\n%s", want, out)
		}
	}
}
