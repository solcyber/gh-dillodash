package markdown

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/solcyber/gh-dillodash/internal/text"
)

// InlinePattern is an extra inline highlighter: wherever Re matches
// (outside code, bold and links, which win ties), Segs renders the match.
// m is the submatch slice (m[0] the whole match); base is the style of the
// surrounding text. Empty matches are ignored.
type InlinePattern struct {
	Re   *regexp.Regexp
	Segs func(m []string, base string) []text.Seg
}

// Hooks let a workflow extend the renderer without the renderer knowing
// it. Every field is optional.
type Hooks struct {
	// Inline adds highlighters to the inline pass, after code, bold and
	// links (e.g. "/approve <id>" commands, "Plan: N to add..." summaries).
	Inline []InlinePattern
	// Line may take over a whole (stripped, non-blank) source line; nil
	// means not handled (e.g. "Window (...): **verdict**" lines).
	Line func(line string, width int) []text.Line
	// CodeLine colors a line inside a code fence; nil means default
	// (e.g. terraform +/-/~ symbols, "# ... will be created" notes).
	CodeLine func(line, lang string) []text.Seg
}

// MergeHooks combines hooks: inline patterns concatenate in order, and the
// first Line or CodeLine hook that handles a line wins.
func MergeHooks(hs ...*Hooks) *Hooks {
	out := &Hooks{}
	var lines []func(string, int) []text.Line
	var codes []func(string, string) []text.Seg
	for _, h := range hs {
		if h == nil {
			continue
		}
		out.Inline = append(out.Inline, h.Inline...)
		if h.Line != nil {
			lines = append(lines, h.Line)
		}
		if h.CodeLine != nil {
			codes = append(codes, h.CodeLine)
		}
	}
	if len(lines) > 0 {
		out.Line = func(line string, width int) []text.Line {
			for _, f := range lines {
				if r := f(line, width); r != nil {
					return r
				}
			}
			return nil
		}
	}
	if len(codes) > 0 {
		out.CodeLine = func(line, lang string) []text.Seg {
			for _, f := range codes {
				if r := f(line, lang); r != nil {
					return r
				}
			}
			return nil
		}
	}
	return out
}

var (
	inlineCodeRE = regexp.MustCompile("`[^`]+`")
	inlineBoldRE = regexp.MustCompile(`\*\*[^*]+\*\*`)
	inlineLinkRE = regexp.MustCompile(`\[[^\]]+\]\([^)\s]+\)`)
)

type matcher struct {
	re   *regexp.Regexp
	segs func(m []string, base string, hooks *Hooks) []text.Seg
}

// builtinInline is set in init: its bold matcher recurses into Inline.
var builtinInline []matcher

func init() { builtinInline = builtins() }

func builtins() []matcher {
	return []matcher{
		{inlineCodeRE, func(m []string, _ string, _ *Hooks) []text.Seg {
			tok := m[0]
			return []text.Seg{text.S(tok[1:len(tok)-1], "md.inline")}
		}},
		{inlineBoldRE, func(m []string, _ string, hooks *Hooks) []text.Seg {
			// bold may hold code (**`name`**): render the inside too, or its
			// backticks show (the bold pattern has no `*`, so this ends)
			tok := m[0]
			return Inline(tok[2:len(tok)-2], "md.bold", hooks)
		}},
		{inlineLinkRE, func(m []string, _ string, _ *Hooks) []text.Seg {
			parts := strings.SplitN(m[0][1:], "](", 2)
			label, url := parts[0], parts[1]
			return []text.Seg{
				text.S(label, "md.link"),
				text.S(" <"+url[:len(url)-1]+">", "md.url"),
			}
		}},
	}
}

// Inline renders inline markdown: `code`, **bold**, [links](url), plus the
// hooks' patterns. Code/bold markers are dropped; link URLs stay visible
// (dimmed), since people copy run links from here. Text between matches
// takes the base style.
func Inline(s string, base string, hooks *Hooks) []text.Seg {
	pats := builtinInline
	if hooks != nil && len(hooks.Inline) > 0 {
		pats = append(append([]matcher(nil), builtinInline...), hookMatchers(hooks)...)
	}
	var out []text.Seg
	pos := 0
	locs := make([][]int, len(pats)) // absolute match indices, nil = stale
	done := make([]bool, len(pats))
	for pos < len(s) {
		best := -1
		for i, p := range pats {
			if done[i] {
				continue
			}
			if locs[i] == nil || locs[i][0] < pos {
				l := p.re.FindStringSubmatchIndex(s[pos:])
				if l == nil || l[1] == l[0] {
					done[i] = true
					continue
				}
				for k := range l {
					if l[k] >= 0 {
						l[k] += pos
					}
				}
				locs[i] = l
			}
			if best < 0 || locs[i][0] < locs[best][0] {
				best = i
			}
		}
		if best < 0 {
			break
		}
		l := locs[best]
		if l[0] > pos {
			out = append(out, text.S(s[pos:l[0]], base))
		}
		m := make([]string, len(l)/2)
		for k := range m {
			if l[2*k] >= 0 {
				m[k] = s[l[2*k]:l[2*k+1]]
			}
		}
		out = append(out, pats[best].segs(m, base, hooks)...)
		pos = l[1]
	}
	if pos < len(s) {
		out = append(out, text.S(s[pos:], base))
	}
	return out
}

func hookMatchers(hooks *Hooks) []matcher {
	out := make([]matcher, 0, len(hooks.Inline))
	for _, p := range hooks.Inline {
		if p.Re == nil || p.Segs == nil {
			continue
		}
		segs := p.Segs
		out = append(out, matcher{p.Re, func(m []string, base string, _ *Hooks) []text.Seg {
			return segs(m, base)
		}})
	}
	return out
}

// CodeSegs colors a line inside a code fence: the hooks' CodeLine first,
// then unified-diff lines for diff/patch fences, everything else code-gray.
func CodeSegs(line, lang string, hooks *Hooks) []text.Seg {
	if hooks != nil && hooks.CodeLine != nil {
		if segs := hooks.CodeLine(line, lang); segs != nil {
			return segs
		}
	}
	if lang == "diff" || lang == "patch" {
		switch {
		case strings.HasPrefix(line, "+"):
			return []text.Seg{text.S(line, "diff.add")}
		case strings.HasPrefix(line, "-"):
			return []text.Seg{text.S(line, "diff.del")}
		case strings.HasPrefix(line, "@@"):
			return []text.Seg{text.S(line, "diff.hunk")}
		}
	}
	return []text.Seg{text.S(line, "md.code")}
}

// runeCount is the character (code point) count.
func runeCount(s string) int { return utf8.RuneCountInString(s) }
