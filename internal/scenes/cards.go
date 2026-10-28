package scenes

import (
	"strings"

	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/markdown"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// BoxGlyph is one character of the box-drawing set (index into G("box")).
func BoxGlyph(i int) string {
	r := []rune(glyphs.G("box"))
	if i < len(r) {
		return string(r[i])
	}
	return "|"
}

// CardLines wraps segments inside a card's vertical rules: `width` is the
// card's full width, `inner` the wrap width of its text.
func CardLines(segs []text.Seg, width, inner int) []text.Line {
	vt := BoxGlyph(5)
	var out []text.Line
	for _, ln := range markdown.Wrap(segs, inner, []text.Seg{text.S(vt+" ", "card")}, []text.Seg{text.S(vt+"   ", "card")}, false) {
		gap := width - 1 - text.SegsWidth(ln.Segs)
		out = append(out, text.Line{Segs: append(ln.Segs, text.S(strings.Repeat(" ", max(0, gap))+vt, "card"))})
	}
	return out
}

// CardTop and CardBottom draw a rounded card's top and bottom rules with
// an optional title on the top one.
func CardTop(title string, width int) text.Line {
	tl, tr, hz := BoxGlyph(0), BoxGlyph(1), BoxGlyph(4)
	segs := []text.Seg{text.S(tl+hz, "card")}
	if title != "" {
		segs = append(segs, text.S(" "+title+" ", "card.title"))
	}
	used := text.SegsWidth(segs)
	segs = append(segs, text.S(strings.Repeat(hz, max(0, width-used-1))+tr, "card"))
	return text.Line{Segs: segs}
}

// CardBottom is the card's bottom rule.
func CardBottom(width int) text.Line {
	bl, br, hz := BoxGlyph(2), BoxGlyph(3), BoxGlyph(4)
	return text.Line{Segs: []text.Seg{text.S(bl+strings.Repeat(hz, max(0, width-2))+br, "card")}}
}
