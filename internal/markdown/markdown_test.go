package markdown

import (
	"math/rand"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/text"
)

var ID = strings.Repeat("a", 64)

func styles(lines []text.Line) []string {
	var out []string
	for _, ln := range lines {
		for _, s := range ln.Segs {
			out = append(out, s.Style)
		}
	}
	return out
}

func hasStyle(lines []text.Line, want ...string) bool {
	seen := map[string]bool{}
	for _, s := range styles(lines) {
		seen[s] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}

func countStyle(lines []text.Line, style string) int {
	n := 0
	for _, s := range styles(lines) {
		if s == style {
			n++
		}
	}
	return n
}

func txt(lines []text.Line) string {
	parts := make([]string, len(lines))
	for i, ln := range lines {
		parts[i] = ln.Text()
	}
	return strings.Join(parts, "\n")
}

func anyLine(lines []text.Line, f func(string) bool) bool {
	for _, ln := range lines {
		if f(ln.Text()) {
			return true
		}
	}
	return false
}

// chip is a rounded status chip: cap, text, cap (the list view's shape).
func chip(s, style string) []text.Seg {
	return []text.Seg{text.S("", "cap:"+style), text.S(s, style), text.S("", "cap:"+style)}
}

const (
	PLANSET = "| apply order | stack | plan-id | changes |\n|---|---|---|---:|\n" +
		"| 1 | prod/10-app-db | `aaaaaaaaaaaa…` | 6 add, 1 change |\n" +
		"| 2 | nonprod/02-iam | `bbbbbbbbbbbb…` | 1 add |"
	EVIDENCE = "| tier | capacity peak / p99 | 3×5-min fires at ≥ 80% (of the cap) " +
		"| cpu peak / p99 | fires at ≥ 70% | periods at the cap |\n" +
		"|---|---|---|---|---|---|\n" +
		"| dev | 20.0% / 6.0% | 0 | 12.0% / 5.0% | 0 | 1 |\n" +
		"| prod | 90.0% / 70.0% | 2 (two mornings, 00:15Z) | 34.0% / 23.0% | 0 " +
		"| 231 of 2,000 |"
)

type check struct {
	name string
	ok   func() bool
}

func run(t *testing.T, checks []check) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !c.ok() {
				t.Fail()
			}
		})
	}
}

func TestWrap(t *testing.T) {
	space := []text.Seg{text.S(" ", "text")}
	run(t, []check{
		{"fuzz: no wrapped line exceeds its width (300 cases x 2 modes)", func() bool {
			rnd := rand.New(rand.NewSource(7))
			words := []string{"alpha", "b", "中文", "Plan-id:", ID, strings.Repeat("x", 30), "--flag", "\U0001f916"}
			stylesList := []string{"text", "md.inline", "md.bold"}
			for trial := 0; trial < 300; trial++ {
				width := 8 + rnd.Intn(63)
				var segs []text.Seg
				for n := 1 + rnd.Intn(4); n > 0; n-- {
					var ws []string
					for k := 1 + rnd.Intn(12); k > 0; k-- {
						ws = append(ws, words[rnd.Intn(len(words))])
					}
					segs = append(segs, text.S(strings.Join(ws, " "), stylesList[rnd.Intn(3)]))
				}
				for _, hard := range []bool{false, true} {
					for _, ln := range Wrap(segs, width, space, nil, hard) {
						if text.SegsWidth(ln.Segs) > width {
							return false
						}
					}
				}
			}
			return true
		}},
		{"word wrap keeps words whole", func() bool {
			lines := Wrap([]text.Seg{text.S("alpha beta gamma delta epsilon", "text")}, 12, nil, nil, false)
			var got []string
			for _, ln := range lines {
				got = append(got, strings.TrimSpace(ln.Text()))
			}
			return reflect.DeepEqual(got, []string{"alpha beta", "gamma delta", "epsilon"})
		}},
		{"a 64-hex plan-id that fits moves whole, never splits", func() bool {
			lines := Wrap([]text.Seg{text.S("plan id ", "text"), text.S(ID, "md.inline")}, 80, nil, nil, false)
			return anyLine(lines, func(s string) bool { return strings.Contains(s, ID) })
		}},
		{"a chip never splits across lines", func() bool {
			segs := append([]text.Seg{text.S("xx ", "text")}, chip("awaiting review", "chip.await")...)
			segs = append(segs, text.S(" more words", "text"))
			lines := Wrap(segs, 18, nil, nil, false)
			return anyLine(lines, func(s string) bool { return strings.Contains(s, "awaiting review") })
		}},
		{"hard wrap marks continuations", func() bool {
			lines := Wrap([]text.Seg{text.S(strings.Repeat("abcdefghij", 3), "md.code")}, 12, nil, nil, true)
			if len(lines) <= 1 {
				return false
			}
			for _, ln := range lines[1:] {
				if !strings.HasPrefix(ln.Text(), Glyphs.Cont) {
					return false
				}
			}
			return true
		}},
		{"a CI cluster wraps as one piece", func() bool {
			ci := []text.Seg{text.S("+", "ci.pass"), text.S("+", "ci.pass"), text.S("~", "ci.wait"),
				text.S("x", "ci.fail"), text.S("-", "ci.skip")}
			lines := Wrap(append([]text.Seg{text.S("CI ", "faint")}, ci...), 6, nil, nil, false)
			return anyLine(lines, func(s string) bool { return strings.Contains(s, "++~x-") })
		}},
		{"wrap: a space at the wrap point is dropped", func() bool {
			lines := Wrap([]text.Seg{text.S("aaaa bbbb", "text")}, 8, nil, nil, false)
			return len(lines) == 2 && strings.TrimRight(lines[0].Text(), " ") == "aaaa" && lines[1].Text() == "bbbb"
		}},
		{"wrap: rest prefix on continuation lines", func() bool {
			head := []text.Seg{text.S("• ", "md.bullet")}
			rest := []text.Seg{text.S("  ", "text")}
			lines := Wrap([]text.Seg{text.S("one two three four", "text")}, 10, head, rest, false)
			return len(lines) == 3 && strings.TrimRight(lines[0].Text(), " ") == "• one two" &&
				strings.TrimRight(lines[1].Text(), " ") == "  three" && lines[2].Text() == "  four" &&
				lines[1].Segs[0] == rest[0] && lines[2].Segs[0] == rest[0]
		}},
		{"wrap: CJK counts cells, not characters", func() bool {
			lines := Wrap([]text.Seg{text.S("中文中文 中文中文", "text")}, 10, nil, nil, false)
			return len(lines) == 2 && strings.TrimRight(lines[0].Text(), " ") == "中文中文"
		}},
	})
}

func TestMarkdown(t *testing.T) {
	tf := TerraformHooks()
	run(t, []check{
		{"headings", func() bool {
			md := Render("# Title\n## Sub\n- item\n> quote\n1. first\n---\n| a | b |\n|---|---|\n| 1 | 2 |", 60, false, nil)
			return hasStyle(md, "h1", "h2")
		}},
		{"bullet + quote + numbered", func() bool {
			md := Render("# Title\n## Sub\n- item\n> quote\n1. first\n---\n| a | b |\n|---|---|\n| 1 | 2 |", 60, false, nil)
			return hasStyle(md, "md.bullet", "md.bar", "md.num")
		}},
		{"rule + table pipes", func() bool {
			md := Render("# Title\n## Sub\n- item\n> quote\n1. first\n---\n| a | b |\n|---|---|\n| 1 | 2 |", 60, false, nil)
			return hasStyle(md, "md.rule", "md.pipe")
		}},
		{"heading text takes the heading style, inline code keeps its own", func() bool {
			md := Render("### a `b` c", 60, false, nil)
			if len(md) != 1 || md[0].Text() != Glyphs.H3+" a b c" {
				return false
			}
			for _, s := range md[0].Segs {
				if s.Style != "h3" && s != text.S("b", "md.inline") {
					return false
				}
			}
			return hasStyle(md, "md.inline")
		}},
		{"horizontal rule is at most 60 cells", func() bool {
			md := Render("***", 100, false, nil)
			return len(md) == 1 && md[0].Text() == strings.Repeat(Glyphs.Rule, 60) && md[0].Segs[0].Style == "md.rule"
		}},
		{"blockquote keeps its bar on wrapped lines", func() bool {
			md := Render("> alpha beta gamma delta", 14, false, nil)
			if len(md) < 2 {
				return false
			}
			for _, ln := range md {
				if !strings.HasPrefix(ln.Text(), Glyphs.Quote+" ") || ln.Segs[0].Style != "md.bar" {
					return false
				}
			}
			return md[0].Segs[1].Style == "md.quote"
		}},
		{"nested bullets indent, continuation lines align under the text", func() bool {
			md := Render("- top\n  * alpha beta gamma delta", 16, false, nil)
			return len(md) == 3 && md[0].Text() == Glyphs.Bullet+" top" &&
				strings.TrimRight(md[1].Text(), " ") == "  "+Glyphs.Bullet+" alpha beta" &&
				md[2].Text() == "    gamma delta" && md[2].Segs[0].Style == "text"
		}},
		{"ordered lists keep their number, 1) too", func() bool {
			md := Render("1. first\n2) second", 40, false, nil)
			return len(md) == 2 && md[0].Text() == "1. first" && md[1].Text() == "2) second" &&
				md[0].Segs[0].Style == "md.num" && md[1].Segs[0].Style == "md.num"
		}},
		{"indented paragraph keeps up to 8 cells of its lead", func() bool {
			md := Render("    four\n            twelve", 40, false, nil)
			return len(md) == 2 && md[0].Text() == "    four" && md[1].Text() == "        twelve"
		}},
		{"blank lines stay blank, empty text says so", func() bool {
			md := Render("a\n\nb", 40, false, nil)
			none := Render("", 40, false, nil)
			return len(md) == 3 && len(md[1].Segs) == 0 && txt(none) == "(no text)"
		}},
		{"links keep the URL visible, dimmed", func() bool {
			segs := Inline("see [run](https://ci.example/1) now", "text", nil)
			return reflect.DeepEqual(segs, []text.Seg{text.S("see ", "text"), text.S("run", "md.link"),
				text.S(" <https://ci.example/1>", "md.url"), text.S(" now", "text")})
		}},
		{"fence lines are a code band", func() bool {
			plan := renderPlan()
			for _, ln := range plan {
				if !ln.Band {
					return false
				}
			}
			return true
		}},
		{"fence header names the language, or code", func() bool {
			a := Render("```go\nx\n```", 40, false, nil)
			b := Render("~~~\nx\n~~~", 40, false, nil)
			return a[0].Text() == " go" && a[0].Segs[0].Style == "md.lang" && b[0].Text() == " code" &&
				len(a) == 2 && a[1].Text() == " x" && a[1].Segs[1].Style == "md.code"
		}},
		{"a fence closes on its own marker kind, not the other", func() bool {
			md := Render("```\n~~~\nstill code\n```\nprose", 40, false, nil)
			return len(md) == 4 && md[1].Band && md[1].Text() == " ~~~" && md[2].Text() == " still code" &&
				!md[3].Band && md[3].Text() == "prose"
		}},
		{"terraform: create/destroy/replace/update/read", func() bool {
			return hasStyle(renderPlan(), "tf.create", "tf.destroy", "tf.replace", "tf.update", "tf.read")
		}},
		{"terraform: the '# will be created' note", func() bool {
			return countStyle(renderPlan(), "tf.create") >= 2
		}},
		{"plan summary: destroy count gold, zero gray", func() bool {
			return hasStyle(renderPlan(), "plan.destroy", "plan.zero", "plan.add")
		}},
		{"plan summary inline: each count in its own style", func() bool {
			segs := Inline("Plan: 1 to add, 0 to change, 2 to destroy.", "text", tf)
			return reflect.DeepEqual(segs, []text.Seg{
				text.S("Plan: ", "muted"), text.S("1", "plan.add"), text.S(" to add", "muted"),
				text.S(", ", "muted"), text.S("0", "plan.zero"), text.S(" to change", "muted"),
				text.S(", ", "muted"), text.S("2", "plan.destroy"), text.S(" to destroy", "muted"),
				text.S(".", "muted")})
		}},
		{"terraform coloring is a hook: plain rendering leaves code gray", func() bool {
			plain := Render("```\n  + resource \"a\" \"b\" {}\n```", 80, false, nil)
			return !hasStyle(plain, "tf.create") && hasStyle(plain, "md.code")
		}},
		{"diff fences", func() bool {
			diff := Render("```diff\n+added\n-removed\n@@ -1 +1 @@\n```", 40, false, tf)
			return hasStyle(diff, "diff.add", "diff.del", "diff.hunk") && !hasStyle(diff, "tf.create")
		}},
		{"/approve command line gets the approval style", func() bool {
			return hasStyle(Render("/approve "+ID, 100, false, tf), "approve.cmd")
		}},
		{"/approve inside prose is only a mention", func() bool {
			r := Render("To approve: `/approve "+ID+"`", 100, false, tf)
			return !hasStyle(r, "approve.cmd")
		}},
		{"/approve outside code is a mention", func() bool {
			r := Inline("run /approve "+ID+" later", "text", tf)
			return len(r) == 3 && r[1].Style == "approve.mention" && r[1].Text == "/approve "+ID
		}},
		{"dispatcher refusal is gold, not an approval", func() bool {
			r := Render("**/approve refused:** `s/x` holds a slot", 100, false, tf)
			return hasStyle(r, "approve.refused") && !hasStyle(r, "approve.cmd")
		}},
		{"window verdict 'non-disruptive' styled verdict.ok", func() bool {
			return hasStyle(Render("Window (policy; live): **non-disruptive** — x", 100, false, tf), "verdict.ok")
		}},
		{"window verdict 'disruptive · revertible' styled verdict.disruptive", func() bool {
			return hasStyle(Render("Window (policy; live): **disruptive · revertible** — x", 100, false, tf), "verdict.disruptive")
		}},
		{"window verdict 'disruptive · NOT revertible' styled verdict.notrev", func() bool {
			return hasStyle(Render("Window (policy; live): **disruptive · NOT revertible** — x", 100, false, tf), "verdict.notrev")
		}},
		{"window verdict line: icon, muted lead, verdict without its stars", func() bool {
			r := Render("Window (policy; live): **non-disruptive** — x", 100, false, tf)
			if len(r) != 1 || r[0].Text() != Glyphs.Window+Glyphs.IcoSep+"Window (policy; live): non-disruptive — x" {
				return false
			}
			found := false
			for _, s := range r[0].Segs {
				if s == text.S("non-disruptive", "verdict.ok") {
					found = true
				} else if s.Style != "muted" && s != text.S(Glyphs.Window+Glyphs.IcoSep, "verdict.ok") {
					return false
				}
			}
			return found
		}},
		{"<details><summary> collapses to a fold line", func() bool {
			r := Render("<details><summary>terraform plan (redacted)</summary>", 80, false, nil)
			return strings.Contains(txt(r), "terraform plan (redacted)") && !strings.Contains(txt(r), "<summary>") &&
				strings.HasPrefix(txt(r), Glyphs.Fold+" ")
		}},
		{"bare HTML tags are shown muted as html", func() bool {
			r := Render("</details>\n<br>", 80, false, nil)
			return len(r) == 2 && r[0].Text() == "</details>" && r[0].Segs[0].Style == "md.html" &&
				r[1].Segs[0].Style == "md.html"
		}},
		{"long HTML comments collapse", func() bool {
			r := Render("x <!-- window-queue {\"night\": \"2026-10-21\", \"revertible\": true, \"plan_id\": \""+ID+"\"} --> y", 200, false, nil)
			return !strings.Contains(txt(r), ID) && strings.Contains(txt(r), "window-queue")
		}},
		{"short HTML comments show as they are", func() bool {
			r := Render("<!-- note -->", 80, false, nil)
			return len(r) == 1 && r[0].Text() == "<!-- note -->" && r[0].Segs[0].Style == "md.html"
		}},
		{"raw mode shows the source", func() bool {
			raw := Render("**bold** `code`", 80, true, nil)
			return strings.Contains(txt(raw), "**bold** `code`")
		}},
		{"raw mode hard-wraps with the text style", func() bool {
			raw := Render(strings.Repeat("x", 20), 10, true, tf)
			return len(raw) == 3 && raw[0].Segs[0].Style == "text" && strings.HasPrefix(raw[1].Text(), Glyphs.Cont)
		}},
		{"inline code/bold markers dropped when rendered", func() bool {
			return strings.TrimSpace(txt(Render("**bold** `code`", 80, false, nil))) == "bold code"
		}},
		{"inline: code inside bold loses its backticks", func() bool {
			segs := Inline("**`metrics-api-exempt`** (new)", "text", nil)
			joined := ""
			found := false
			for _, s := range segs {
				joined += s.Text
				if s == text.S("metrics-api-exempt", "md.inline") {
					found = true
				}
			}
			return !strings.Contains(joined, "`") && found
		}},
		{"hooks: an inline pattern highlights, code wins the tie", func() bool {
			h := &Hooks{Inline: []InlinePattern{{
				Re: regexp.MustCompile(`#\d+`),
				Segs: func(m []string, _ string) []text.Seg {
					return []text.Seg{text.S(m[0], "issue.ref")}
				}}}}
			segs := Inline("see #12 and `#13`", "text", h)
			return reflect.DeepEqual(segs, []text.Seg{text.S("see ", "text"), text.S("#12", "issue.ref"),
				text.S(" and ", "text"), text.S("#13", "md.inline")})
		}},
		{"hooks: merged hooks chain lines, code lines and inline patterns", func() bool {
			a := &Hooks{Line: func(line string, _ int) []text.Line {
				if line == "A" {
					return []text.Line{{Segs: []text.Seg{text.S("a!", "x")}}}
				}
				return nil
			}}
			m := MergeHooks(a, nil, tf)
			r := Render("A\n/approve "+ID, 100, false, m)
			return len(m.Inline) == 2 && r[0].Text() == "a!" && hasStyle(r, "approve.cmd")
		}},
	})
}

func renderPlan() []text.Line {
	return Render("```\n  # aws_x.y will be created\n  + resource \"a\" \"b\" {}\n"+
		"  - resource \"c\" \"d\" {}\n-/+ resource \"e\" \"f\" {}\n"+
		"  ~ resource \"g\" \"h\" {}\n <= data \"i\" \"j\" {}\n"+
		"Plan: 1 to add, 0 to change, 2 to destroy.\n```", 80, false, TerraformHooks())
}

// cellLetters is every non-space character in a table's header and body
// cells, sorted.
func cellLetters(src string) []rune {
	lines := strings.Split(src, "\n")
	rows := append([]string{lines[0]}, lines[2:]...)
	var out []rune
	for _, r := range rows {
		for _, c := range tableCells(r) {
			c = strings.ReplaceAll(strings.ReplaceAll(c, "`", ""), " ", "")
			out = append(out, []rune(c)...)
		}
	}
	sortRunes(out)
	return out
}

func sortRunes(rs []rune) { sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] }) }

func TestTables(t *testing.T) {
	tbl := RenderTable(strings.Split(PLANSET, "\n"), 100)
	run(t, []check{
		{"table: header, rule, two rows", func() bool { return len(tbl) == 4 }},
		{"table: no source pipes left", func() bool {
			return !anyLine(tbl, func(s string) bool { return strings.Contains(s, "|") })
		}},
		{"table: the cell bars line up", func() bool {
			cols := map[int]bool{}
			for _, ln := range tbl {
				if i := strings.Index(ln.Text(), Glyphs.VBar); i >= 0 {
					cols[i] = true
				}
			}
			return len(cols) == 1
		}},
		{"table: header bold, code cells keep their style", func() bool {
			return hasStyle(tbl[:1], "md.bold") && hasStyle(tbl[2:], "md.inline")
		}},
		{"table: ---: right-aligns", func() bool {
			return strings.HasSuffix(strings.TrimRight(tbl[3].Text(), " "), " 1 add") &&
				strings.Index(tbl[3].Text(), "1 add") > strings.Index(tbl[2].Text(), "6 add")
		}},
		{"table: :---: centers", func() bool {
			r := RenderTable([]string{"| h |", "|:---:|", "| ab |", "| abcdef |"}, 40)
			return strings.Contains(r[2].Text(), "  ab  ") && strings.Contains(r[3].Text(), " abcdef")
		}},
		{"table: every width from 30 to 129 fits and keeps every character", func() bool {
			junk := map[rune]bool{' ': true}
			for _, r := range Glyphs.VBar + Glyphs.Rule + Glyphs.Cross {
				junk[r] = true
			}
			for _, src := range []string{PLANSET, EVIDENCE} {
				for width := 30; width < 130; width += 7 {
					lines := Render(src, width, false, nil)
					for _, ln := range lines {
						if text.Width(ln.Text()) > width {
							t.Errorf("table: a line exceeds width %d: %q", width, ln.Text())
							return false
						}
					}
					if !strings.Contains(txt(lines), Glyphs.VBar) {
						continue
					}
					var got []rune
					for _, ln := range lines {
						for _, ch := range ln.Text() {
							if !junk[ch] {
								got = append(got, ch)
							}
						}
					}
					sortRunes(got)
					if string(got) != string(cellLetters(src)) {
						t.Errorf("table: characters dropped at width %d", width)
						return false
					}
				}
			}
			return true
		}},
		{"table: words stay whole when the longest word fits its column", func() bool {
			return strings.Contains(txt(Render(PLANSET, 60, false, nil)), "prod/10-app-db")
		}},
		{"table: wrapped rows get a faint rule between them", func() bool {
			narrow := Render(PLANSET, 60, false, nil)
			n := 0
			for _, ln := range narrow {
				if strings.Contains(ln.Text(), Glyphs.Cross) {
					n++
				}
			}
			return n >= 2 && countStyle(narrow, "md.rule") > 0
		}},
		{"table: too many columns for the width falls back to raw pipes", func() bool {
			var cols []string
			for i := 0; i < 12; i++ {
				cols = append(cols, "c"+string(rune('0'+i%10)))
			}
			many := "|" + strings.Join(cols, "|") + "|\n|" + strings.Repeat("---|", 12)
			return strings.Contains(txt(Render(many, 50, false, nil)), "|")
		}},
		{"table: no separator row is no table (raw)", func() bool {
			return strings.Contains(txt(Render("| just | pipes |", 60, false, nil)), "|")
		}},
		{"table: an escaped pipe stays in its cell", func() bool {
			return reflect.DeepEqual(tableCells(`| a \| b | c |`), []string{"a | b", "c"})
		}},
		{"table: one inside a code fence stays code", func() bool {
			return strings.Contains(txt(Render("```\n| a | b |\n|---|---|\n```", 60, false, nil)), "|")
		}},
		{"table: the last thing in a body still renders", func() bool {
			return strings.Contains(txt(Render("intro\n\n"+PLANSET, 80, false, nil)), Glyphs.VBar)
		}},
		{"table widths: browser-style, longest word first", func() bool {
			w := tableWidths([]int{11, 21, 13, 15}, []int{5, 21, 13, 6}, 50)
			return w != nil && w[1] == 21
		}},
		{"table widths: natural when they fit", func() bool {
			return reflect.DeepEqual(tableWidths([]int{4, 5}, []int{2, 3}, 20), []int{4, 5})
		}},
		{"table widths: proportional slack sums to the room", func() bool {
			w := tableWidths([]int{11, 21, 13, 15}, []int{5, 21, 13, 6}, 50)
			return sum(w) == 50
		}},
		{"table widths: water-filling when words are too long", func() bool {
			return reflect.DeepEqual(tableWidths([]int{4, 40, 40}, []int{4, 40, 40}, 30), []int{4, 13, 13})
		}},
		{"table widths: nil when even the minimum does not fit", func() bool {
			return tableWidths([]int{10, 10, 10}, []int{10, 10, 10}, 17) == nil
		}},
		{"table: short rows pad, long rows truncate to the header", func() bool {
			r := RenderTable([]string{"| a | b |", "|---|---|", "| 1 |", "| 2 | 3 | 4 |"}, 40)
			return len(r) == 4 && !strings.Contains(r[3].Text(), "4") && strings.Count(r[2].Text(), Glyphs.VBar) == 1
		}},
	})
}

func TestVerdictStyle(t *testing.T) {
	cases := []struct{ verdict, want string }{
		{"non-disruptive", "verdict.ok"},
		{"Non-Disruptive", "verdict.ok"},
		{"disruptive · revertible", "verdict.disruptive"},
		{"disruptive · NOT revertible", "verdict.notrev"},
		{"something else", "verdict.ok"},
	}
	for _, c := range cases {
		t.Run(c.verdict, func(t *testing.T) {
			if got := VerdictStyle(c.verdict); got != c.want {
				t.Errorf("VerdictStyle(%q) = %q, want %q", c.verdict, got, c.want)
			}
		})
	}
}

func TestCodeSegs(t *testing.T) {
	tf := TerraformHooks()
	cases := []struct {
		name, line, lang string
		want             string
	}{
		{"create", "  + resource \"a\" \"b\" {}", "", "tf.create"},
		{"destroy", "  - resource \"a\" \"b\" {}", "", "tf.destroy"},
		{"replace -/+", "-/+ resource \"a\" \"b\" {}", "", "tf.replace"},
		{"replace +/-", "+/- resource \"a\" \"b\" {}", "", "tf.replace"},
		{"update", "  ~ x = 1", "hcl", "tf.update"},
		{"read", " <= data \"a\" \"b\" {}", "", "tf.read"},
		{"note created", "  # a.b will be created", "", "tf.create"},
		{"note destroyed", "  # a.b will be destroyed", "", "tf.destroy"},
		{"note replaced", "  # a.b must be replaced", "", "tf.replace"},
		{"note updated", "  # a.b will be updated in-place", "", "tf.update"},
		{"note moved", "  # a.b has moved to a.c", "", "tf.note"},
		{"note read", "  # a.b will be read during apply", "", "tf.note"},
		{"plain", "x = 1", "", "md.code"},
		{"a bare dash is not a symbol", "-x", "", "md.code"},
		{"diff add", "+added", "diff", "diff.add"},
		{"diff del", "-removed", "patch", "diff.del"},
		{"diff hunk", "@@ -1 +1 @@", "diff", "diff.hunk"},
		{"diff context", " same", "diff", "md.code"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs := CodeSegs(c.line, c.lang, tf)
			if len(segs) != 1 || segs[0].Style != c.want || segs[0].Text != c.line {
				t.Errorf("CodeSegs(%q, %q) = %+v, want one %s seg", c.line, c.lang, segs, c.want)
			}
		})
	}
	t.Run("plan summary in a code line", func(t *testing.T) {
		segs := CodeSegs("  Plan: 2 to add, 1 to change, 0 to destroy. (x)", "", tf)
		if segs[0] != text.S("  ", "md.code") || segs[len(segs)-1] != text.S(" (x)", "md.code") ||
			!hasStyle([]text.Line{{Segs: segs}}, "plan.add", "plan.change", "plan.zero") {
			t.Errorf("CodeSegs plan = %+v", segs)
		}
	})
}
