package amos

import (
	"regexp"
	"strings"
	"testing"
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
	for _, o := range []Options{{}, {Mirror: true}, {Frame: 1}, {Frame: 1, Mirror: true}, {Bg: &RGB{0, 0, 40}}} {
		lines := Lines(o)
		if len(lines) != Height() {
			t.Errorf("%+v: %d lines, want %d", o, len(lines), Height())
		}
		for i, l := range lines {
			if w := visibleWidth(l); w != Width() {
				t.Errorf("%+v line %d: width %d, want %d", o, i, w, Width())
			}
			if !strings.HasSuffix(l, "\x1b[0m") {
				t.Errorf("%+v line %d does not reset", o, i)
			}
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
	if strings.Join(Plain(Options{}), "\n") == strings.Join(Plain(Options{Frame: 1}), "\n") {
		t.Error("frame 1 should differ from frame 0")
	}
}
