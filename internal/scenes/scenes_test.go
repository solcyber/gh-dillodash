package scenes

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/solcyber/gh-dillodash/internal/amos"
	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

// clock patches fx.Now for a test and returns the setter.
func clock(t *testing.T) func(float64) {
	t.Helper()
	now := 0.0
	old := fx.Now
	fx.Now = func() float64 { return now }
	t.Cleanup(func() { fx.Now = old })
	return func(v float64) { now = v }
}

func runesAt(c *canvas.Canvas, y, x, n int) string {
	r := []rune(c.Text(y))
	if x+n > len(r) {
		n = len(r) - x
	}
	return string(r[x : x+n])
}

func blank(c *canvas.Canvas, y int) bool {
	return strings.TrimSpace(c.Text(y)) == ""
}

func TestSplashLogotypeAndMascot(t *testing.T) {
	set := clock(t)
	f := fx.New()
	f.Start("splash", 3600, nil)
	c := canvas.New(100, 30)
	info := SplashInfo{ReposText: "acme/infra"}

	art := glyphs.Logotype
	aw := artWidth(art)
	tall := len(art) + len(glyphs.Armadillo) + 6
	top, left := (30-tall)/2, (100-aw)/2

	// t=0: nothing lit yet, no tagline, still shown
	set(0)
	if !splash(c, f, 30, 100, info, theme.Mono) {
		t.Fatal("splash ended at t=0")
	}
	if !blank(c, top+len(art)+1) {
		t.Errorf("tagline shown at t=0: %q", c.Text(top+len(art)+1))
	}

	// t=2: every row of the logotype is lit, at the right rows and width
	c = canvas.New(100, 30)
	set(2.0)
	if !splash(c, f, 30, 100, info, theme.Mono) {
		t.Fatal("splash ended at t=2 while loading")
	}
	for r, line := range art {
		if got := runesAt(c, top+r, left, len([]rune(line))); got != line {
			t.Errorf("row %d: got %q want %q", top+r, got, line)
		}
		if c.StyleAt(top+r, left) == "text" && []rune(line)[0] != ' ' {
			t.Errorf("row %d: first pixel unstyled", top+r)
		}
	}
	tag := "approvals for acme/infra"
	ty := top + len(art) + 1
	if got := runesAt(c, ty, (100-len(tag))/2, len(tag)); got != tag {
		t.Errorf("tagline: %q", got)
	}
	if c.StyleAt(ty, (100-len(tag))/2+len("approvals for ")) != "repo" {
		t.Errorf("repo style: %q", c.StyleAt(ty, (100-len(tag))/2+len("approvals for ")))
	}
	// the line art trots in place under it, then the loading line + scanner
	ay := ty + 2
	ax := (100 - artWidth(glyphs.Armadillo)) / 2
	if got := runesAt(c, ay, ax, len(glyphs.Armadillo[0])); got != glyphs.Armadillo[0] {
		t.Errorf("armadillo row 0: %q", got)
	}
	ly := ay + len(glyphs.Armadillo) + 1
	if !strings.Contains(c.Text(ly), "loading acme/infra") {
		t.Errorf("loading line: %q", c.Text(ly))
	}
	if blank(c, ly+1) {
		t.Error("no scanner")
	}

	// t=0.3: the ball is rolling in, no armadillo yet
	c = canvas.New(100, 30)
	set(0.3)
	splash(c, f, 30, 100, info, theme.Mono)
	ball := glyphs.G("ball")
	if !strings.ContainsAny(c.Text(ay+3), ball) {
		t.Errorf("no ball at t=0.3: %q", c.Text(ay+3))
	}
	if strings.Contains(c.Text(ay), "__.------.__") {
		t.Error("armadillo shown during the roll-in")
	}

	// over: loaded and past the minimum, or no room
	set(fx.SplashMinSecs)
	if splash(c, f, 30, 100, SplashInfo{ReposText: "acme/infra", Loaded: true}, theme.Mono) {
		t.Error("splash did not end once loaded")
	}
	if splash(c, f, 30, 100, SplashInfo{ReposText: "acme/infra", Failed: true}, theme.Mono) {
		t.Error("splash did not end once failed")
	}
	set(0.5)
	if !splash(c, f, 30, 100, SplashInfo{ReposText: "acme/infra", Loaded: true}, theme.Mono) {
		t.Error("splash ended before the minimum")
	}
	if splash(canvas.New(100, 10), f, 10, 100, info, theme.Mono) {
		t.Error("splash shown on a short terminal")
	}
	if splash(canvas.New(40, 30), f, 30, 40, info, theme.Mono) {
		t.Error("splash shown on a narrow terminal")
	}
}

func TestSplashAmos(t *testing.T) {
	set := clock(t)
	f := fx.New()
	f.Start("splash", 3600, nil)
	c := canvas.New(100, 30)
	set(2.0)
	if !splash(c, f, 30, 100, SplashInfo{ReposText: "acme/infra"}, theme.TrueColor) {
		t.Fatal("splash ended")
	}
	tall := len(glyphs.Logotype) + amos.Height() + 6
	y := (30-tall)/2 + len(glyphs.Logotype) + 3
	half := 0
	for r := y; r < y+amos.Height(); r++ {
		half += strings.Count(c.Text(r), "▀") + strings.Count(c.Text(r), "▄")
	}
	if half == 0 {
		t.Error("no sprite cells in color")
	}
	for r := 0; r < 30; r++ {
		if strings.Contains(c.Text(r), "__.------.__") {
			t.Error("line art drawn alongside the sprite")
		}
	}
	// a terminal too short for the sprite falls back to the line art
	c = canvas.New(100, 20)
	if !splash(c, f, 20, 100, SplashInfo{ReposText: "acme/infra"}, theme.TrueColor) {
		t.Fatal("splash ended on 100x20")
	}
	found := false
	for r := 0; r < 20; r++ {
		found = found || strings.Contains(c.Text(r), "__.------.__")
	}
	if !found {
		t.Error("no line art on the short terminal")
	}
}

func TestCascade(t *testing.T) {
	set := clock(t)
	f := fx.New()
	if r, skip := CascadeRight(f, 0, 80); r != 80 || skip {
		t.Errorf("no cascade: %d %v", r, skip)
	}
	set(0)
	StartCascade(f, 10)
	if r, skip := CascadeRight(f, 0, 80); r != 0 || !skip {
		t.Errorf("t=0 row 0: %d %v", r, skip)
	}
	if _, skip := CascadeRight(f, 5, 80); !skip {
		t.Error("t=0 row 5 drawn")
	}
	set(0.28)
	if r, skip := CascadeRight(f, 0, 80); r != 80 || skip {
		t.Errorf("t=0.28 row 0: %d %v", r, skip)
	}
	if r, _ := CascadeRight(f, 5, 80); r <= 0 || r >= 80 {
		t.Errorf("t=0.28 row 5 should be mid-sweep: %d", r)
	}
	set(1.0)
	if r, skip := CascadeRight(f, 0, 80); r != 80 || skip {
		t.Errorf("over: %d %v", r, skip)
	}
}

func TestWipe(t *testing.T) {
	set := clock(t)
	shade := []rune(glyphs.G("shade"))
	for _, dir := range []int{1, -1} {
		f := fx.New()
		set(0)
		f.Start("wipe", 1.0, map[string]any{"dir": dir})
		c := canvas.New(100, 30)
		for y := 0; y < 30; y++ {
			c.PutStr(y, 0, strings.Repeat("X", 100), "text")
		}
		set(0.5)
		Wipe(c, f, 30, 100)
		edge := int(fx.EaseOut(0.5) * 103) // 90
		for y := 2; y < 28; y++ {
			row := []rune(c.Text(y))
			if dir > 0 {
				if string(row[edge:]) != strings.Repeat(" ", 100-edge) {
					t.Errorf("dir=1 row %d not blank right of %d: %q", y, edge, string(row[edge:]))
				}
				if row[edge-1] != shade[2] || row[edge-3] != shade[0] || row[edge-4] != 'X' {
					t.Errorf("dir=1 row %d edge: %q", y, string(row[edge-5:edge]))
				}
			} else {
				x := 100 - edge
				if string(row[:x]) != strings.Repeat(" ", x) {
					t.Errorf("dir=-1 row %d not blank left of %d", y, x)
				}
				if row[x] != shade[2] || row[x+2] != shade[0] || row[x+3] != 'X' {
					t.Errorf("dir=-1 row %d edge: %q", y, string(row[x:x+4]))
				}
			}
		}
		for _, y := range []int{0, 1, 28, 29} {
			if c.Text(y) != strings.Repeat("X", 100) {
				t.Errorf("dir=%d row %d touched", dir, y)
			}
		}
	}
	f := fx.New()
	c := canvas.New(100, 30)
	c.PutStr(5, 0, "keep", "text")
	Wipe(c, f, 30, 100)
	if !strings.HasPrefix(c.Text(5), "keep") {
		t.Error("wipe drew with no effect live")
	}
}

func TestConfettiStaysInTheBody(t *testing.T) {
	set := clock(t)
	f := fx.New()
	set(0)
	f.Start("confetti", 1.7, map[string]any{"seed": uint64(7), "count": 120, "big": true})
	c := canvas.New(100, 30)
	var rec []canvas.Put
	c.Record = &rec
	particles, ball := 0, 0
	for _, el := range []float64{0.1, 0.4, 0.8, 1.2, 1.6} {
		set(el)
		Confetti(c, f, 30, 100)
	}
	for _, p := range rec {
		if p.Y == 29 {
			ball++
			continue
		}
		particles++
		if p.Y < 2 || p.Y >= 29 || p.X < 0 || p.X+text.Width(p.Text) > 99 {
			t.Errorf("particle outside the body: %+v", p)
		}
	}
	if particles == 0 || ball == 0 {
		t.Errorf("particles=%d ball=%d", particles, ball)
	}
	set(2.0)
	rec = rec[:0]
	Confetti(c, f, 30, 100)
	if len(rec) != 0 {
		t.Error("confetti drawn after it ended")
	}
}

func TestLaunchLetters(t *testing.T) {
	set := clock(t)
	f := fx.New()
	set(0)
	f.Start("launch", fx.LaunchSecs, map[string]any{"seed": uint64(42)})
	c := canvas.New(100, 30)
	set(2.0)
	Launch(c, f, 30, 100)
	block := glyphs.G("block")
	cycle := map[string]bool{}
	for _, s := range fx.LaunchCycle {
		cycle[s] = true
	}
	count := func(c *canvas.Canvas) int {
		n := 0
		for y := 2; y <= 26; y++ {
			row := []rune(c.Text(y))
			for x := 1; x < 98; x++ {
				if string(row[x]) == block {
					if !cycle[c.StyleAt(y, x)] {
						t.Errorf("letter cell (%d,%d) in %q", y, x, c.StyleAt(y, x))
					}
					n++
				}
			}
		}
		return n
	}
	if n := count(c); n == 0 {
		t.Error("no letters at t=2.0")
	}
	want := 0
	for _, ch := range fx.LaunchWord {
		for _, line := range fx.LaunchFont[ch] {
			want += strings.Count(line, "#")
		}
	}
	c = canvas.New(100, 30)
	set(3.0)
	Launch(c, f, 30, 100)
	if n := count(c); n < want*8/10 {
		t.Errorf("letters at t=3.0: %d block cells, want about %d", n, want)
	}
	// the word sits in the middle rows
	ww := fx.LaunchWordWidth()
	x0 := (100 - ww) / 2
	hit := false
	for y := 9; y <= 18 && !hit; y++ {
		hit = string([]rune(c.Text(y))[x0]) == block
	}
	if !hit {
		t.Error("the R's first column is not where the word should start")
	}
	// too narrow for block letters: the plain word
	c = canvas.New(40, 20)
	Launch(c, f, 20, 40)
	found := false
	for y := 0; y < 20; y++ {
		found = found || strings.Contains(c.Text(y), fx.LaunchWord)
	}
	if !found {
		t.Error("no plain RELEASED! on a narrow screen")
	}
}

// The strobe test: every frame of the launch at 30 fps, every
// size, stays on screen and off the status line; and on 24x80, while
// only the lights play (before the burst), no border cell changes color
// more than three times in any second.
func TestLaunchNeverStrobes(t *testing.T) {
	set := clock(t)
	f := fx.New()
	type key struct{ y, x int }
	type sample struct {
		el float64
		st string
	}
	border := map[key][]sample{}
	rows := map[[2]int]bool{}
	for _, hw := range [][2]int{{24, 80}, {50, 200}, {12, 40}, {8, 20}, {5, 10}} {
		h, w := hw[0], hw[1]
		for i := 0; i < int(fx.LaunchSecs*30); i++ {
			el := float64(i) / 30
			set(0)
			f.Start("launch", fx.LaunchSecs, map[string]any{"seed": uint64(42)})
			set(el)
			c := canvas.New(w, h)
			var rec []canvas.Put
			c.Record = &rec
			Launch(c, f, h, w)
			for _, p := range rec {
				rows[[2]int{h, p.Y}] = true
				if p.Y < 0 || p.Y >= h || p.X < 0 || p.X+text.Width(p.Text) > w {
					t.Fatalf("%dx%d at %.2fs: off screen %+v", h, w, el, p)
				}
				if p.Y == h-2 {
					t.Fatalf("%dx%d at %.2fs: drew on the status line", h, w, el)
				}
			}
			if h == 24 && w == 80 && el < 1.5 {
				for _, k := range []key{{1, 10}, {12, 0}, {21, 70}} {
					border[k] = append(border[k], sample{el, c.StyleAt(k.y, k.x)})
				}
			}
		}
	}
	if !rows[[2]int{24, 1}] {
		t.Error("nothing drawn on the top border of a normal screen")
	}
	if len(border) == 0 {
		t.Fatal("no border samples")
	}
	for k, seq := range border {
		var changes []float64
		for i := 1; i < len(seq); i++ {
			if seq[i].st != seq[i-1].st {
				changes = append(changes, seq[i].el)
			}
		}
		for i := 0; i+3 < len(changes); i++ {
			if changes[i+3]-changes[i] < 1.0 {
				t.Errorf("cell %v changed color 4x between %.2fs and %.2fs", k, changes[i], changes[i+3])
			}
		}
	}
}

func TestEmptyCentersTheNote(t *testing.T) {
	set := clock(t)
	set(1.0)
	f := fx.New()
	old := fx.Ambient
	t.Cleanup(func() { fx.Ambient = old })
	note := "nothing open in acme/infra"
	x := (100 - len(note)) / 2

	fx.Ambient = false
	c := canvas.New(100, 30)
	Empty(c, f, 30, 100, note, theme.Mono)
	artTop := (30 - 4 - len(glyphs.Armadillo) - 3) / 2
	ny := artTop + len(glyphs.Armadillo) + 1
	if got := runesAt(c, ny, x, len(note)); got != note {
		t.Errorf("note row %d: %q", ny, c.Text(ny))
	}
	if c.StyleAt(ny, x) != "allclear" {
		t.Errorf("note style %q", c.StyleAt(ny, x))
	}
	ax := (100 - artWidth(glyphs.Armadillo)) / 2
	if got := runesAt(c, artTop, ax, len(glyphs.Armadillo[0])); got != glyphs.Armadillo[0] {
		t.Errorf("art row: %q", c.Text(artTop))
	}
	for y := 2; y < 28; y++ {
		if y < artTop || y > ny {
			if !blank(c, y) {
				t.Errorf("stars without ambient motion, row %d: %q", y, c.Text(y))
			}
		}
	}

	// ambient: the sky twinkles clear of the mascot's rows, which wander
	fx.Ambient = true
	c = canvas.New(100, 30)
	Empty(c, f, 30, 100, note, theme.Mono)
	if got := runesAt(c, ny, x, len(note)); got != note {
		t.Errorf("ambient note: %q", c.Text(ny))
	}
	stars := 0
	for y := 0; y < 30; y++ {
		if y >= artTop && y < artTop+len(glyphs.Armadillo)+2 {
			continue
		}
		if !blank(c, y) {
			stars++
			if y < 2 || y >= 28 {
				t.Errorf("star outside the body, row %d", y)
			}
		}
	}
	if stars == 0 {
		t.Error("no stars with ambient motion")
	}

	// Amos in color
	fx.Ambient = false
	c = canvas.New(100, 30)
	Empty(c, f, 30, 100, note, theme.Color256)
	aTop := max(3, (30-4-amos.Height()-3)/2)
	half := 0
	for y := aTop; y < aTop+amos.Height(); y++ {
		half += strings.Count(c.Text(y), "▀") + strings.Count(c.Text(y), "▄")
	}
	if half == 0 {
		t.Error("no sprite in color")
	}
	if got := runesAt(c, aTop+amos.Height()+1, x, len(note)); got != note {
		t.Errorf("note under Amos: %q", c.Text(aTop+amos.Height()+1))
	}
}

func TestStars(t *testing.T) {
	set := clock(t)
	set(0.7)
	c := canvas.New(100, 30)
	var rec []canvas.Put
	c.Record = &rec
	skip := map[int]bool{10: true, 11: true}
	Stars(c, fx.New(), 30, 100, skip)
	if len(rec) == 0 {
		t.Fatal("no stars")
	}
	for _, p := range rec {
		if p.Y < 2 || p.Y >= 28 || skip[p.Y] || p.X < 0 || p.X >= 99 {
			t.Errorf("star out of place: %+v", p)
		}
		if !strings.HasPrefix(p.Style, "fx.b") && !strings.HasPrefix(p.Style, "fx.k") {
			t.Errorf("star style %q", p.Style)
		}
	}
	// the same sky every frame
	c2 := canvas.New(100, 30)
	var rec2 []canvas.Put
	c2.Record = &rec2
	Stars(c2, fx.New(), 30, 100, skip)
	if len(rec2) != len(rec) {
		t.Error("the sky is not deterministic")
	}
}

func TestScrollbar(t *testing.T) {
	thumb, track := glyphs.G("thumb"), glyphs.G("track")
	c := canvas.New(20, 20)
	Scrollbar(c, 2, 10, 40, 0, 19)
	for i := 0; i < 10; i++ {
		got := runesAt(c, 2+i, 19, 1)
		want, st := track, "scroll.track"
		if i < 2 {
			want, st = thumb, "scroll.thumb"
		}
		if got != want || c.StyleAt(2+i, 19) != st {
			t.Errorf("first=0 row %d: %q %q", 2+i, got, c.StyleAt(2+i, 19))
		}
	}
	c = canvas.New(20, 20)
	Scrollbar(c, 2, 10, 40, 30, 19)
	for i := 0; i < 10; i++ {
		got := runesAt(c, 2+i, 19, 1)
		want := track
		if i >= 8 {
			want = thumb
		}
		if got != want {
			t.Errorf("first=30 row %d: %q", 2+i, got)
		}
	}
	c = canvas.New(20, 20)
	Scrollbar(c, 2, 10, 8, 0, 19)
	for y := 0; y < 20; y++ {
		if !blank(c, y) {
			t.Error("scrollbar drawn when everything fits")
		}
	}
}

func TestChipAndGlint(t *testing.T) {
	set := clock(t)
	segs := Chip("approved", "chip.ok")
	if len(segs) != 3 || segs[0].Text != glyphs.G("capl") || segs[2].Text != glyphs.G("capr") {
		t.Errorf("chip: %+v", segs)
	}
	if segs[0].Style != "cap:chip.ok" || segs[1].Style != "chip.ok" || segs[2].Style != "cap:chip.ok" {
		t.Errorf("chip styles: %+v", segs)
	}
	label := "awaiting review"
	want := glyphs.G("capl") + label + glyphs.G("capr")
	for _, now := range []float64{0, 0.5, 1.0, 1.7, 2.9, 3.6} {
		set(now)
		g := GlintChip(label, "chip.await", 0, true, false)
		if text.SegsText(g) != want {
			t.Errorf("t=%.1f: %q", now, text.SegsText(g))
		}
		if g[0].Style != "cap:chip.await" || g[len(g)-1].Style != "cap:chip.await" {
			t.Errorf("t=%.1f caps: %+v", now, g)
		}
	}
	set(1.0) // the glint is over the second letter
	g := GlintChip(label, "chip.await", 0, true, false)
	glint := false
	for _, s := range g {
		glint = glint || s.Style == "chip.await.glint2"
	}
	if !glint {
		t.Errorf("no glint at t=1: %+v", g)
	}
	for _, g := range [][]text.Seg{
		GlintChip(label, "chip.await", 0, false, false),
		GlintChip(label, "chip.await", 0, true, true),
		GlintChip(label, "chip.await", -1, true, false),
		GlintChip(label, "chip.ok", 0, true, false),
	} {
		if len(g) != 3 {
			t.Errorf("plain chip expected: %+v", g)
		}
	}
}

func TestGleamSegs(t *testing.T) {
	set := clock(t)
	set(1.0)
	old := fx.Ambient
	t.Cleanup(func() { fx.Ambient = old })
	fx.Ambient = true
	if GleamSegs(0, 0) != nil {
		t.Error("width 0")
	}
	segs := GleamSegs(20, 0) // mono tier: a plain rule
	if len(segs) != 1 || segs[0].Style != "rule" || text.Width(segs[0].Text) != 20 {
		t.Errorf("mono rule: %+v", segs)
	}
	if theme.Setup(colorprofile.TrueColor, false) != theme.TrueColor {
		t.Skip("no color in this environment")
	}
	t.Cleanup(func() { theme.Setup(0, true) })
	segs = GleamSegs(20, 0)
	if text.SegsWidth(segs) != 20 {
		t.Errorf("gleam width %d", text.SegsWidth(segs))
	}
	bright := false
	for _, s := range segs {
		bright = bright || s.Style == "fx.b7"
	}
	if !bright {
		t.Errorf("no gleam at t=1: %+v", segs)
	}
}

func TestNumStyleGutterBreathe(t *testing.T) {
	set := clock(t)
	f := fx.New()
	set(0)
	f.Start("new:1", 2.0, nil)
	f.Start("upd:2", 1.2, nil)
	set(0.1)
	if got := NumStyle(f, "1", "num"); got != "num.new" {
		t.Errorf("new t=0.05: %q", got)
	}
	if got := NumStyle(f, "2", "num"); got != "num.pulse" {
		t.Errorf("upd t=0.08: %q", got)
	}
	set(0.3)
	if got := NumStyle(f, "1", "num"); got != "num.pulse" {
		t.Errorf("new t=0.15: %q", got)
	}
	if got := NumStyle(f, "2", "num"); got != "num" {
		t.Errorf("upd t=0.25: %q", got)
	}
	set(1.6)
	if got := NumStyle(f, "1", "num"); got != "num" {
		t.Errorf("new t=0.8: %q", got)
	}
	if got := NumStyle(f, "9", "num"); got != "num" {
		t.Errorf("no effect: %q", got)
	}

	sel := glyphs.G("sel")
	if g := Gutter(f, "1", true, "gutter.2", 0, false); g[0].Text != sel || g[0].Style != "gutter.2" {
		t.Errorf("selected: %+v", g)
	}
	if g := Gutter(f, "1", false, "gutter", 0, false); !strings.HasPrefix(g[0].Style, "fx.g") {
		t.Errorf("new row glint: %+v", g)
	}
	if g := Gutter(f, "3", false, "gutter", 0.1, true); g[0].Text != sel || g[0].Style != "fx.b3" {
		t.Errorf("trail: %+v", g)
	}
	if g := Gutter(f, "3", false, "gutter", 0, false); g[0].Text != " " || g[0].Style != "gutter" {
		t.Errorf("plain: %+v", g)
	}

	if Breathe(false, false) != "gutter" || Breathe(true, true) != "gutter" {
		t.Error("breathe should rest")
	}
	if b := Breathe(true, false); !strings.HasPrefix(b, "gutter.") {
		t.Errorf("breathe: %q", b)
	}
}

func TestMascotNone(t *testing.T) {
	defer func() { Mascot = "sprite" }()
	Mascot = "none"
	c := canvas.New(100, 30)
	Empty(c, fx.New(), 30, 100, "all clear", theme.Color256)
	joined := strings.Join(c.Lines(), "\n")
	if !strings.Contains(joined, "all clear") {
		t.Error("the note must still be drawn")
	}
	for _, ln := range glyphs.Armadillo {
		if strings.Contains(joined, strings.TrimSpace(ln)) {
			t.Error("mascot none must not draw the line art")
		}
	}
	Mascot = "ascii"
	if useAmos(theme.TrueColor, 40, 120, 7) {
		t.Error("mascot ascii must not use the sprite")
	}
}
