package amos

import (
	"regexp"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/text"
)

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

func visibleWidth(s string) int {
	return len([]rune(sgr.ReplaceAllString(s, "")))
}

func TestShape(t *testing.T) {
	if Width() != spriteW || Height() != (spriteH+1)/2 {
		t.Fatalf("Width/Height = %d/%d", Width(), Height())
	}
	if spriteW < 40 || spriteW > 80 || spriteH < 20 || spriteH > 40 {
		t.Fatalf("unexpected sprite size %dx%d: regenerate with cmd/gen-amos", spriteW, spriteH)
	}
	if w, h := Size(Options{}); w != Width() || h != Height() {
		t.Errorf("Size = %dx%d, want %dx%d", w, h, Width(), Height())
	}
	for _, o := range []Options{{}, {Mirror: true}, {Frame: 1}, {Frame: 1, Mirror: true}, {Bg: &RGB{0, 0, 40}},
		{Mini: true}, {Mini: true, Mirror: true}, {Mini: true, Frame: 1}} {
		w, h := Size(o)
		lines := Lines(o)
		if len(lines) != h {
			t.Errorf("%+v: %d lines, want %d", o, len(lines), h)
		}
		for i, l := range lines {
			if lw := visibleWidth(l); lw != w {
				t.Errorf("%+v line %d: width %d, want %d", o, i, lw, w)
			}
			if !strings.HasSuffix(l, "\x1b[0m") {
				t.Errorf("%+v line %d does not reset", o, i)
			}
		}
	}
}

// TestMini: the small Amos is a third of the sprite, every letter of its
// art is a color, and it has the sprite's yellow, white and outline.
func TestMini(t *testing.T) {
	w, h := Size(Options{Mini: true})
	if w != Width()/3 || h != 5 {
		t.Errorf("mini is %dx%d cells, want %dx5", w, h, Width()/3)
	}
	seen := map[byte]bool{}
	for r, ln := range miniArt {
		if len(ln) != mini.w {
			t.Errorf("mini row %d: %d pixels, want %d", r, len(ln), mini.w)
		}
		for i := 0; i < len(ln); i++ {
			if _, ok := miniColors[ln[i]]; !ok && ln[i] != '.' {
				t.Errorf("mini row %d: unknown pixel %q", r, ln[i])
			}
			seen[ln[i]] = true
		}
	}
	for _, k := range []byte("DYOW") {
		if !seen[k] {
			t.Errorf("mini lacks %q", k)
		}
	}
	for k, c := range miniColors {
		if spritePalette[nearest(c)] != c {
			t.Errorf("mini color %q is not in the sprite's palette", k)
		}
	}
	for i, row := range Segs(Options{Mini: true}) {
		if n := text.SegsWidth(row); n != w {
			t.Errorf("Segs row %d: width %d, want %d", i, n, w)
		}
	}
}

func TestPaletteHasAmosColors(t *testing.T) {
	var yellow, white, dark bool
	for _, c := range spritePalette[1:] {
		switch {
		case c.R > 220 && c.G > 190 && c.B < 80:
			yellow = true
		case isLight(c):
			white = true
		case isDark(c):
			dark = true
		}
	}
	if !yellow || !white || !dark {
		t.Errorf("palette lacks yellow/white/dark: %v", spritePalette)
	}
}

func TestMirrorIsReversal(t *testing.T) {
	a := Plain(Options{})
	b := Plain(Options{Mirror: true})
	for i := range a {
		r := []rune(a[i])
		for x, y := 0, len(r)-1; x < y; x, y = x+1, y-1 {
			r[x], r[y] = r[y], r[x]
		}
		if string(r) != b[i] {
			t.Errorf("row %d: mirror mismatch\n%q\n%q", i, string(r), b[i])
		}
	}
}

func TestFramesDiffer(t *testing.T) {
	for _, m := range []bool{false, true} {
		if strings.Join(Plain(Options{Mini: m}), "\n") == strings.Join(Plain(Options{Mini: m, Frame: 1}), "\n") {
			t.Errorf("mini=%v: frame 1 should differ from frame 0", m)
		}
	}
}
