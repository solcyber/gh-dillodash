package fonts

import (
	"fmt"
	"sort"
	"testing"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/solcyber/gh-dillodash/internal/glyphs"
)

// drawn is every non-ASCII rune of the nerd and unicode sets, sorted.
// Multi-character entries are sequences (spinner frames, box corners),
// so each rune counts; plain ASCII and spaces are the terminal's problem,
// not the font's.
func drawn(t *testing.T) []rune {
	t.Helper()
	seen := map[rune]bool{}
	for _, set := range []glyphs.Set{glyphs.Nerd, glyphs.Unicode} {
		all := glyphs.All(set)
		if len(all) == 0 {
			t.Fatalf("glyphs.All(%s) is empty", set)
		}
		for _, s := range all {
			for _, r := range s {
				if r < 0x80 || unicode.IsSpace(r) {
					continue
				}
				seen[r] = true
			}
		}
	}
	out := make([]rune, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func hexRunes(rs []rune) string {
	s := ""
	for _, r := range rs {
		s += fmt.Sprintf(" U+%04X", r)
	}
	return s
}

// Every glyph the dashboard draws exists in BOTH styles and is exactly
// one cell wide: the reason for the "Mono" variant, since a wider icon
// spills into the next column.
func TestEveryDrawnGlyphIsInBothStylesOneCellWide(t *testing.T) {
	rs := drawn(t)
	if len(rs) < 50 {
		t.Fatalf("only %d glyphs to check; the sets look wrong", len(rs))
	}
	for _, file := range Files() {
		f, err := sfnt.Parse(file.Data)
		if err != nil {
			t.Fatalf("%s: %v", file.Name, err)
		}
		var buf sfnt.Buffer
		ppem := fixed.I(int(f.UnitsPerEm())) // advances in font units
		cell := advance(t, f, &buf, ppem, 'M')
		if cell == 0 {
			t.Fatalf("%s: M has no advance", file.Name)
		}
		var missing, wide []rune
		for _, r := range rs {
			idx, err := f.GlyphIndex(&buf, r)
			if err != nil {
				t.Fatalf("%s: U+%04X: %v", file.Name, r, err)
			}
			if idx == 0 {
				missing = append(missing, r)
				continue
			}
			if adv := advance(t, f, &buf, ppem, r); adv != cell {
				wide = append(wide, r)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s lacks%s", file.Name, hexRunes(missing))
		}
		if len(wide) > 0 {
			t.Errorf("%s does not draw these one cell wide:%s", file.Name, hexRunes(wide))
		}
		// the no-break space Ico appends
		if idx, _ := f.GlyphIndex(&buf, 0xA0); idx == 0 {
			t.Errorf("%s has no U+00A0 no-break space", file.Name)
		}
		if n := f.NumGlyphs(); n < 1000 {
			t.Errorf("%s: only %d glyphs; not a patched Nerd Font", file.Name, n)
		}
	}
}

func advance(t *testing.T, f *sfnt.Font, buf *sfnt.Buffer, ppem fixed.Int26_6, r rune) fixed.Int26_6 {
	t.Helper()
	idx, err := f.GlyphIndex(buf, r)
	if err != nil {
		t.Fatalf("U+%04X: %v", r, err)
	}
	adv, err := f.GlyphAdvance(buf, idx, ppem, font.HintingNone)
	if err != nil {
		t.Fatalf("U+%04X advance: %v", r, err)
	}
	return adv
}

// Every rune of the nerd set is in the BMP private-use area
// (U+E000-U+F8FF) or is a printable BMP character: the U+F0000+ nf-md
// range is unprintable to some wcwidth tables, so it is never used.
func TestNerdGlyphsArePrivateUseOrPrintableBMP(t *testing.T) {
	var pua int
	for name, s := range glyphs.All(glyphs.Nerd) {
		for _, r := range s {
			switch {
			case r >= 0xE000 && r <= 0xF8FF:
				pua++
			case r > 0xFFFF:
				t.Errorf("%s: U+%04X is outside the BMP", name, r)
			case !unicode.IsPrint(r) && r != 0xA0:
				t.Errorf("%s: U+%04X is not printable", name, r)
			}
		}
	}
	if pua == 0 {
		t.Error("the nerd set has no private-use icons at all")
	}
}
