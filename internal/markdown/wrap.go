// Package markdown renders PR, issue and comment bodies as styled lines:
// light GitHub-flavored markdown (headings, lists, quotes, fences, tables,
// inline code/bold/links) with cell-width aware wrapping. Workflows extend
// it through Hooks; the terraform plan coloring ships here as one.
package markdown

import (
	"strings"
	"unicode/utf8"

	"github.com/solcyber/gh-dillodash/internal/text"
)

// GlyphSet holds the decorative characters the renderer draws with. The
// app swaps Glyphs for ASCIIGlyphs on a terminal that cannot show them.
type GlyphSet struct {
	Cont, More, Rule, VBar, Cross   string
	H1, H2, H3, Fold, Quote, Bullet string
	Shield, Window, Warn            string
	// IcoSep follows an icon so wrapping never strands it from its text:
	// a no-break space, or a plain space in the ASCII set.
	IcoSep string
}

// NerdGlyphs is the default set (Nerd Font icons, box-drawing rules).
var NerdGlyphs = GlyphSet{
	Cont: "↪", More: "…", Rule: "─", VBar: "│", Cross: "┼",
	H1: "█", H2: "▸", H3: "▹", Fold: "▾", Quote: "│", Bullet: "•",
	Shield: "", Window: "", Warn: "",
	IcoSep: " ",
}

// ASCIIGlyphs leaves decorative icons out rather than invent letters.
var ASCIIGlyphs = GlyphSet{
	Cont: ">", More: "..", Rule: "-", VBar: "|", Cross: "+",
	H1: "#", H2: "#", H3: ">", Fold: "v", Quote: "|", Bullet: "*",
	Shield: "", Window: "", Warn: "!",
	IcoSep: " ",
}

// Glyphs is the set in use.
var Glyphs = NerdGlyphs

// ico is a glyph plus the icon separator, or "" when the set has none.
func ico(g string) string {
	if g == "" {
		return ""
	}
	return g + Glyphs.IcoSep
}

// word is a wrapping unit: a run of spaces, or a run of non-space tokens
// that moves to the next line whole.
type word struct {
	space bool
	toks  []text.Seg
}

func isChip(style string) bool {
	return strings.HasPrefix(style, "chip.") || strings.HasPrefix(style, "cap:")
}

// splitSpaces splits s into alternating runs of non-space and space
// characters (ASCII space only), keeping both, like re.split(r"( +)").
func splitSpaces(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); {
		if s[i] == ' ' {
			if i > start {
				parts = append(parts, s[start:i])
			}
			j := i
			for j < len(s) && s[j] == ' ' {
				j++
			}
			parts = append(parts, s[i:j])
			start, i = j, j
			continue
		}
		i++
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

// words groups segments into wrapping units: a run of non-space pieces is
// one unit even across styles (a CI cluster, a chip's caps and text,
// "**bold**text"), and chip text never splits at its own spaces.
func words(segs []text.Seg) []word {
	var out []word
	for _, seg := range segs {
		t := text.Clean(seg.Text)
		var parts []string
		if isChip(seg.Style) {
			parts = []string{t}
		} else {
			parts = splitSpaces(t)
		}
		for _, p := range parts {
			if p == "" {
				continue
			}
			tok := text.Seg{Text: p, Style: seg.Style, Extra: seg.Extra}
			switch {
			case strings.TrimSpace(p) == "" && !isChip(seg.Style):
				out = append(out, word{space: true, toks: []text.Seg{tok}})
			case len(out) > 0 && !out[len(out)-1].space:
				last := &out[len(out)-1]
				last.toks = append(last.toks, tok) // glued to the word before it
			default:
				out = append(out, word{toks: []text.Seg{tok}})
			}
		}
	}
	return out
}

func wordWidth(toks []text.Seg) int {
	n := 0
	for _, t := range toks {
		n += text.Width(t.Text)
	}
	return n
}

// piece is a run of tokens that fills one line; ends says the line is
// full and the next piece starts a new one.
type piece struct {
	segs []text.Seg
	ends bool
}

// hardPieces splits tokens at cell boundaries: the first piece fits room,
// the rest width each.
func hardPieces(toks []text.Seg, room, width int) []piece {
	var out []piece
	var line []text.Seg
	used := 0
	for _, tok := range toks {
		t := tok.Text
		for t != "" {
			avail := room - used
			p := text.Clip(t, avail)
			if p == "" {
				if used == 0 {
					_, n := utf8.DecodeRuneInString(t)
					p = t[:n] // not even one cell fits: take it anyway
				} else {
					out = append(out, piece{line, true})
					line, used, room = nil, 0, width
					continue
				}
			}
			line = append(line, text.Seg{Text: p, Style: tok.Style, Extra: tok.Extra})
			used += text.Width(p)
			t = t[len(p):]
			if t != "" {
				out = append(out, piece{line, true})
				line, used, room = nil, 0, width
			}
		}
	}
	if len(line) > 0 {
		out = append(out, piece{line, false})
	}
	return out
}

func cleaned(segs []text.Seg) []text.Seg {
	out := make([]text.Seg, len(segs))
	for i, s := range segs {
		out[i] = text.Seg{Text: text.Clean(s.Text), Style: s.Style, Extra: s.Extra}
	}
	return out
}

func firstRuneWidth(s string) int {
	r, _ := utf8.DecodeRuneInString(s)
	return text.RuneWidth(r)
}

// Wrap wraps styled segments to width cells (never narrower than 8).
// prefix starts the first line and rest every later one (nil rest means
// prefix again). Word-wraps by default: a word moves whole to the next
// line, and is split only when it is wider than a line by itself (so a
// 64-hex plan-id stays intact wherever it fits). hard=true breaks anywhere
// and marks continuations with the cont glyph: code blocks, tables, diffs.
func Wrap(segs []text.Seg, width int, prefix, rest []text.Seg, hard bool) []text.Line {
	if rest == nil {
		rest = prefix
	}
	if width < 8 {
		width = 8
	}
	restW := text.SegsWidth(rest)
	var cont []text.Seg
	if hard {
		cont = []text.Seg{text.S(Glyphs.Cont+" ", "faint")}
	}
	contW := text.SegsWidth(cont)
	var lines []text.Line
	cur := append([]text.Seg(nil), prefix...)
	used := text.SegsWidth(prefix)

	newline := func() {
		lines = append(lines, text.Line{Segs: cur})
		cur = append(append([]text.Seg(nil), rest...), cont...)
		used = restW + contW
	}

	if hard {
		for _, p := range hardPieces(cleaned(segs), width-used, width-restW-contW) {
			cur = append(cur, p.segs...)
			if p.ends {
				newline()
			}
		}
		lines = append(lines, text.Line{Segs: cur})
		return lines
	}

	for _, w := range words(segs) {
		ww := wordWidth(w.toks)
		if w.space {
			if used+ww <= width {
				cur = append(cur, w.toks...)
				used += ww
			}
			continue // a space at the wrap point is dropped
		}
		if used+ww > width && used > restW && ww <= width-restW {
			newline()
		}
		if used+ww <= width {
			cur = append(cur, w.toks...)
			used += ww
			continue
		}
		if used > restW && width-used < firstRuneWidth(w.toks[0].Text) {
			newline() // not even its first cell fits here
		}
		for _, p := range hardPieces(w.toks, width-used, width-restW) {
			cur = append(cur, p.segs...)
			used += text.SegsWidth(p.segs)
			if p.ends {
				newline()
			}
		}
	}
	lines = append(lines, text.Line{Segs: cur})
	return lines
}
