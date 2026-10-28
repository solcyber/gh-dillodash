package markdown

import (
	"regexp"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/text"
)

var (
	fenceRE       = regexp.MustCompile("^(`{3,}|~{3,})\\s*([\\w+.-]*)")
	headingRE     = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	hrRE          = regexp.MustCompile(`^(-{3,}|\*{3,}|_{3,})$`)
	quoteRE       = regexp.MustCompile(`^>\s?(.*)$`)
	bulletRE      = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	orderedRE     = regexp.MustCompile(`^(\s*)(\d+[.)])\s+(.*)$`)
	summaryRE     = regexp.MustCompile(`(?i)^\s*(?:<details[^>]*>)?\s*<summary>(.*?)</summary>\s*$`)
	htmlLineRE    = regexp.MustCompile(`(?i)^\s*</?(details|summary|br|p|sub|sup|img|div)\b[^>]*>\s*$`)
	htmlCommentRE = regexp.MustCompile(`(?s)<!--(.*?)-->`)
)

// collapseComment shrinks a long HTML comment (the window-queue marker's
// JSON) to its first word; short ones show as they are.
func collapseComment(m string) string {
	inner := strings.TrimSuffix(strings.TrimPrefix(m, "<!--"), "-->")
	if runeCount(inner) <= 40 {
		return m
	}
	first := ""
	if f := strings.Fields(inner); len(f) > 0 {
		first = f[0]
	}
	return "<!-- " + first + " " + Glyphs.More + " -->"
}

// splitLines is Python's str.splitlines for \n, \r\n and \r: no trailing
// empty line, and none at all for "".
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			out = append(out, s[start:i])
			start = i + 1
		case '\r':
			out = append(out, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Render turns PR/issue/comment text into styled lines wrapped to width.
// raw=true shows the source untouched (hard-wrapped), for when rendering
// hides something. hooks may be nil.
func Render(src string, width int, raw bool, hooks *Hooks) []text.Line {
	var out []text.Line
	if raw {
		if src == "" {
			src = "(no text)"
		}
		lines := splitLines(src)
		if len(lines) == 0 {
			lines = []string{"(no text)"}
		}
		for _, s := range lines {
			out = append(out, Wrap([]text.Seg{text.S(s, "text")}, width, nil, nil, true)...)
		}
		return out
	}
	src = htmlCommentRE.ReplaceAllStringFunc(src, collapseComment)
	inFence := false
	fenceMark := ""
	lang := ""
	var table []string // consecutive table rows, laid out together
	lines := splitLines(src)
	if len(lines) == 0 {
		lines = []string{"(no text)"}
	}
	for _, line := range lines {
		stripped := strings.TrimSpace(line)
		if !inFence && isTableRow(stripped) {
			table = append(table, stripped)
			continue
		}
		if len(table) > 0 {
			out = append(out, renderTable(table, width, hooks)...)
			table = nil
		}
		if m := fenceRE.FindStringSubmatch(stripped); m != nil &&
			(!inFence || strings.HasPrefix(stripped, prefixRunes(fenceMark, 3))) {
			if !inFence {
				inFence, fenceMark, lang = true, m[1], strings.ToLower(m[2])
				name := lang
				if name == "" {
					name = "code"
				}
				out = append(out, text.Line{Segs: []text.Seg{text.S(" "+name, "md.lang")}, Band: true})
			} else {
				inFence = false
			}
			continue
		}
		if inFence {
			wrapped := Wrap(CodeSegs(line, lang, hooks), width,
				[]text.Seg{text.S(" ", "md.code")}, nil, true)
			for i := range wrapped {
				wrapped[i].Band = true
			}
			out = append(out, wrapped...)
			continue
		}
		if stripped == "" {
			out = append(out, text.Line{})
			continue
		}
		if hooks != nil && hooks.Line != nil {
			if r := hooks.Line(stripped, width); r != nil {
				out = append(out, r...)
				continue
			}
		}
		if m := headingRE.FindStringSubmatch(stripped); m != nil {
			level := len(m[1])
			style, mark := "h3", Glyphs.H3
			if level == 1 {
				style, mark = "h1", Glyphs.H1
			} else if level == 2 {
				style, mark = "h2", Glyphs.H2
			}
			body := Inline(m[2], "text", hooks)
			for i := range body {
				if body[i].Style == "text" {
					body[i].Style = style
				}
			}
			segs := append([]text.Seg{text.S(mark+" ", style)}, body...)
			out = append(out, Wrap(segs, width, nil, nil, false)...)
			continue
		}
		if hrRE.MatchString(stripped) {
			out = append(out, text.Line{Segs: []text.Seg{
				text.S(strings.Repeat(Glyphs.Rule, minInt(width, 60)), "md.rule")}})
			continue
		}
		if m := summaryRE.FindStringSubmatch(stripped); m != nil {
			out = append(out, Wrap([]text.Seg{text.S(Glyphs.Fold+" ", "md.html"),
				text.S(m[1], "muted")}, width, nil, nil, false)...)
			continue
		}
		if htmlLineRE.MatchString(stripped) ||
			(strings.HasPrefix(stripped, "<!--") && strings.HasSuffix(stripped, "-->")) {
			out = append(out, text.Line{Segs: []text.Seg{text.S(stripped, "md.html")}})
			continue
		}
		if m := quoteRE.FindStringSubmatch(stripped); m != nil {
			bar := []text.Seg{text.S(Glyphs.Quote+" ", "md.bar")}
			out = append(out, Wrap(Inline(m[1], "md.quote", hooks), width, bar, nil, false)...)
			continue
		}
		if m := bulletRE.FindStringSubmatch(line); m != nil {
			indent := strings.Repeat(" ", minInt(runeCount(m[1]), 8))
			head := []text.Seg{text.S(indent+Glyphs.Bullet+" ", "md.bullet")}
			rest := []text.Seg{text.S(strings.Repeat(" ", text.SegsWidth(head)), "text")}
			out = append(out, Wrap(Inline(m[2], "text", hooks), width, head, rest, false)...)
			continue
		}
		if m := orderedRE.FindStringSubmatch(line); m != nil {
			indent := strings.Repeat(" ", minInt(runeCount(m[1]), 8))
			head := []text.Seg{text.S(indent+m[2]+" ", "md.num")}
			rest := []text.Seg{text.S(strings.Repeat(" ", text.SegsWidth(head)), "text")}
			out = append(out, Wrap(Inline(m[3], "text", hooks), width, head, rest, false)...)
			continue
		}
		lead := len(line) - len(strings.TrimLeft(line, " "))
		var pre []text.Seg
		if lead > 0 {
			pre = []text.Seg{text.S(strings.Repeat(" ", minInt(lead, 8)), "text")}
		}
		out = append(out, Wrap(Inline(stripped, "text", hooks), width, pre, nil, false)...)
	}
	if len(table) > 0 {
		out = append(out, renderTable(table, width, hooks)...)
	}
	return out
}

// prefixRunes is the first n characters of s.
func prefixRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
