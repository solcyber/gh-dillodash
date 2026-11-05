package scenes

import (
	"fmt"
	"image/color"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

// psyTier switches the theme to a tier for one test.
func psyTier(t testing.TB, p colorprofile.Profile) {
	t.Helper()
	theme.Setup(p, false)
	clear(lumaCache)
	t.Cleanup(func() { theme.Setup(0, true); clear(lumaCache) })
}

// dashboard is a stand-in for what the launch draws over: a header, rows
// of text, a status line and a key bar.
func dashboard(h, w int) *canvas.Canvas {
	c := canvas.New(w, h)
	c.PutStr(0, 0, strings.Repeat("dillodash acme/infra ", w/21+1)[:w], "header")
	for y := 1; y < h-2; y++ {
		c.PutStr(y, 2, fmt.Sprintf("#%d fix the referral flow when the signup form is long", 40+y), "text")
	}
	c.PutStr(h-2, 0, "posted /release-now on web-portal#91", "text")
	c.PutStr(h-1, 0, " enter open  / filter  : go to #  ? help", "keys")
	return c
}

var (
	sgrFG = regexp.MustCompile(`38;(?:2;(\d+);(\d+);(\d+)|5;(\d+))`)
	sgrBG = regexp.MustCompile(`48;(?:2;(\d+);(\d+);(\d+)|5;(\d+))`)
)

func sgrColor(m []string) fx.RGB {
	if m[4] != "" {
		i, _ := strconv.Atoi(m[4])
		r, g, b := theme.XtermRGB(i)
		return fx.RGB{R: r, G: g, B: b}
	}
	r, _ := strconv.Atoi(m[1])
	g, _ := strconv.Atoi(m[2])
	b, _ := strconv.Atoi(m[3])
	return fx.RGB{R: uint8(r), G: uint8(g), B: uint8(b)}
}

func toRGB(c color.Color) fx.RGB {
	r, g, b, _ := c.RGBA()
	return fx.RGB{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)}
}

// coverage is how much of a cell a glyph inks, roughly.
func coverage(g string) float64 {
	switch g {
	case " ", "":
		return 0
	case "█":
		return 1
	case "▀", "▄", "▒":
		return 0.5
	case "░":
		return 0.25
	case "▓":
		return 0.75
	}
	return 0.3
}

// lumas is every cell's relative luminance as it shows on screen: the
// background and the glyph's ink mixed by how much of the cell it inks.
// Each SGR is parsed once.
func lumas(c *canvas.Canvas) []float64 {
	out := make([]float64, c.W*c.H)
	c.Walk(func(y, x int, ce canvas.CellInfo) {
		k := lumaKey{ce.Raw, ce.Style, ce.Sel, ce.Band, ce.Glyph}
		if v, ok := lumaCache[k]; ok {
			out[y*c.W+x] = v
			return
		}
		var fg, bg fx.RGB
		if ce.Raw != "" {
			fg = fx.RGB{R: 204, G: 204, B: 204}
			if m := sgrFG.FindStringSubmatch(ce.Raw); m != nil {
				fg = sgrColor(m)
			}
			if m := sgrBG.FindStringSubmatch(ce.Raw); m != nil {
				bg = sgrColor(m)
			}
		} else {
			st := theme.Style(ce.Style, ce.Sel, ce.Band)
			fg = fx.RGB{R: 204, G: 204, B: 204}
			if col := st.GetForeground(); col != nil {
				if _, _, _, a := col.RGBA(); a > 0 {
					fg = toRGB(col)
				}
			}
			if col := st.GetBackground(); col != nil {
				if _, _, _, a := col.RGBA(); a > 0 {
					bg = toRGB(col)
				}
			}
		}
		cov := coverage(ce.Glyph)
		v := fx.Luminance(bg)*(1-cov) + fx.Luminance(fg)*cov
		lumaCache[k] = v
		out[y*c.W+x] = v
	})
	return out
}

type lumaKey struct {
	raw, style string
	sel, band  bool
	glyph      string
}

// lumaCache is per test binary; the theme's tier is part of the raw SGR
// (24-bit or 256), and named styles are only drawn under one tier per
// test, which clears it.
var lumaCache = map[lumaKey]float64{}

// swings are the frames where an area's mean luminance turns round by
// 10% or more of the scale (WCAG's flash: a pair of opposing changes of
// at least 10%).
func swings(series []float64) []int {
	const thr = 0.1
	var out []int
	dir, ext := 0, series[0]
	for i, v := range series {
		switch dir {
		case 0:
			if v-ext >= thr {
				dir, ext = 1, v
				out = append(out, i)
			} else if ext-v >= thr {
				dir, ext = -1, v
				out = append(out, i)
			}
		case 1:
			if v > ext {
				ext = v
			} else if ext-v >= thr {
				dir, ext = -1, v
				out = append(out, i)
			}
		case -1:
			if v < ext {
				ext = v
			} else if v-ext >= thr {
				dir, ext = 1, v
				out = append(out, i)
			}
		}
	}
	return out
}

// The flash guard: every frame of the launch at 30 fps, from just
// before it starts to just after it ends, at several sizes, both color
// tiers and two seeds. No quarter of the screen, nor the whole of it,
// turns round in mean luminance by 10% more than three times in any
// second; and no frame changes the luminance of more than a quarter of
// the cells by 10% at once.
func TestLaunchNeverFlashes(t *testing.T) {
	set := clock(t)
	worstFrac, worstSwings := 0.0, 0
	defer func() {
		t.Logf("worst frame: %.1f%% of cells changed by 10%%; most swings in any second in any area: %d", worstFrac*100, worstSwings)
	}()
	for _, prof := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256} {
		psyTier(t, prof)
		sizes, seeds := [][2]int{{24, 80}, {30, 100}, {60, 200}}, []uint64{42, 7}
		if prof == colorprofile.ANSI256 { // the same show, rounded to the cube
			sizes, seeds = [][2]int{{24, 80}, {60, 200}}, []uint64{42}
		}
		for _, hw := range sizes {
			for _, seed := range seeds {
				h, w := hw[0], hw[1]
				name := fmt.Sprintf("%v %dx%d seed %d", theme.Current(), h, w, seed)
				type region struct {
					name           string
					y0, y1, x0, x1 int
				}
				regions := []region{
					{"screen", 0, h, 0, w},
					{"top-left", 0, h / 2, 0, w / 2}, {"top-right", 0, h / 2, w / 2, w},
					{"bottom-left", h / 2, h, 0, w / 2}, {"bottom-right", h / 2, h, w / 2, w},
					{"middle", h / 4, h * 3 / 4, w / 4, w * 3 / 4},
				}
				series := make([][]float64, len(regions))
				var prev []float64
				n := int((fx.LaunchSecs + 0.4) * 30)
				for i := -3; i < n; i++ {
					el := float64(i) / 30
					f := fx.New()
					set(0)
					f.Start("launch", fx.LaunchSecs, map[string]any{"seed": seed})
					set(el)
					c := dashboard(h, w)
					if el >= 0 {
						Launch(c, f, h, w)
					}
					lum := lumas(c)
					for ri, r := range regions {
						sum := 0.0
						for y := r.y0; y < r.y1; y++ {
							for x := r.x0; x < r.x1; x++ {
								sum += lum[y*w+x]
							}
						}
						series[ri] = append(series[ri], sum/float64((r.y1-r.y0)*(r.x1-r.x0)))
					}
					if prev != nil {
						changed := 0
						for k := range lum {
							if d := lum[k] - prev[k]; d > 0.1 || d < -0.1 {
								changed++
							}
						}
						frac := float64(changed) / float64(len(lum))
						worstFrac = max(worstFrac, frac)
						if frac > 0.25 {
							t.Errorf("%s at %.2fs: %.0f%% of the cells changed by 10%% in one frame", name, el, frac*100)
						}
					}
					prev = lum
				}
				for ri, r := range regions {
					sw := swings(series[ri])
					for k := range sw {
						n := 1
						for k+n < len(sw) && sw[k+n]-sw[k] < 30 {
							n++
						}
						worstSwings = max(worstSwings, n)
					}
					for k := 0; k+3 < len(sw); k++ {
						if sw[k+3]-sw[k] < 30 {
							t.Errorf("%s, %s: 4 luminance swings within 1 s (frames %v)", name, r.name, sw[k:k+4])
						}
					}
				}
			}
		}
	}
}

// The launch keeps off the header and the status line, at every frame
// and size, the tiny ones included.
func TestLaunchPsycheStaysInItsRows(t *testing.T) {
	set := clock(t)
	psyTier(t, colorprofile.TrueColor)
	for _, hw := range [][2]int{{24, 80}, {50, 200}, {12, 40}, {8, 20}, {5, 10}} {
		h, w := hw[0], hw[1]
		base := dashboard(h, w)
		for i := 0; i < int(fx.LaunchSecs*30); i += 3 {
			el := float64(i) / 30
			f := fx.New()
			set(0)
			f.Start("launch", fx.LaunchSecs, map[string]any{"seed": uint64(42)})
			set(el)
			c := dashboard(h, w)
			Launch(c, f, h, w)
			for _, y := range []int{0, h - 2} {
				if y >= 0 && c.Text(y) != base.Text(y) {
					t.Fatalf("%dx%d at %.2fs drew on row %d: %q", h, w, el, y, c.Text(y))
				}
			}
		}
	}
}

// RELEASED! reads: once every letter has landed and before the melt,
// the letter cells are all there, in letter colors, on the plate.
func TestLaunchPsycheWordReads(t *testing.T) {
	set := clock(t)
	psyTier(t, colorprofile.TrueColor)
	letter := map[string]bool{}
	for _, c := range levels[lvLetter] {
		letter[fmt.Sprintf("38;2;%d;%d;%d", c.R, c.G, c.B)] = true
	}
	want := 0
	for _, ch := range fx.LaunchWord {
		for _, line := range fx.LaunchFont[ch] {
			want += strings.Count(line, "#")
		}
	}
	for _, el := range []float64{3.2, 4.5, 6.0} {
		f := fx.New()
		set(0)
		f.Start("launch", fx.LaunchSecs, map[string]any{"seed": uint64(42)})
		set(el)
		c := dashboard(30, 120)
		Launch(c, f, 30, 120)
		got := 0
		c.Walk(func(y, x int, ce canvas.CellInfo) {
			if ce.Glyph == glyphs.G("block") && ce.Raw != "" && letter[strings.TrimPrefix(strings.SplitN(ce.Raw, ";48", 2)[0], "\x1b[")] {
				got++
			}
		})
		if got < want*95/100 {
			t.Errorf("at %.1fs: %d letter cells, want %d", el, got, want)
		}
	}
}

// Below 256 colors, or with ASCII glyphs, the classic launch plays: no
// raw colors at all.
func TestLaunchFallsBackBelow256(t *testing.T) {
	set := clock(t)
	check := func(label string) {
		f := fx.New()
		set(0)
		f.Start("launch", fx.LaunchSecs, map[string]any{"seed": uint64(42)})
		set(2.5)
		c := dashboard(30, 100)
		Launch(c, f, 30, 100)
		raw := 0
		c.Walk(func(_, _ int, ce canvas.CellInfo) {
			if ce.Raw != "" {
				raw++
			}
		})
		if raw != 0 {
			t.Errorf("%s: %d raw cells, want the classic launch", label, raw)
		}
	}
	psyTier(t, colorprofile.ANSI)
	check("16 colors")
	psyTier(t, colorprofile.TrueColor)
	glyphs.Use(glyphs.ASCII)
	t.Cleanup(func() { glyphs.Use(glyphs.Nerd) })
	check("ascii")
}

// One frame on a big terminal, mid-show (everything on screen at once).
func BenchmarkLaunchPsyche(b *testing.B) {
	psyTier(b, colorprofile.TrueColor)
	old := fx.Now
	b.Cleanup(func() { fx.Now = old })
	now := 0.0
	fx.Now = func() float64 { return now }
	f := fx.New()
	f.Start("launch", fx.LaunchSecs, map[string]any{"seed": uint64(42)})
	c := canvas.New(200, 60)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = 2.6 + float64(i%30)/30
		c.Clear()
		Launch(c, f, 60, 200)
	}
}
