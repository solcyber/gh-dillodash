package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/markdown"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// hooks are the markdown hooks every enabled workflow contributes.
func (m *Model) hooks() *markdown.Hooks {
	if m.wf == nil || m.wf.Empty() {
		return nil
	}
	return &markdown.Hooks{
		Inline:   m.wf.InlinePatterns(),
		Line:     m.wf.LineRenderer,
		CodeLine: m.wf.CodeLineSegs,
	}
}

// section is a detail section header: '▌ description ────'.
func section(name string, width int, count string) text.Line {
	segs := []text.Seg{text.S(glyphs.G("section")+" ", "section"), text.S(name, "section")}
	if count != "" {
		segs = append(segs, text.S("  "+count, "section.count"))
	}
	segs = append(segs, text.S(" ", "text"))
	return text.Line{Segs: append(segs, text.S(strings.Repeat(glyphs.G("rule"), max(0, width-text.SegsWidth(segs))), "rule"))}
}

// ofTotal is " of N" when more exist than were fetched.
func ofTotal(shown, total int, noun string) string {
	if total > shown {
		if noun != "" {
			return fmt.Sprintf(" of %d %s", total, noun)
		}
		return fmt.Sprintf(" of %d", total)
	}
	return ""
}

type event struct {
	when time.Time
	who  string
	what string
	body string
}

// renderMD is markdown.Render for the open item, memoized (mdMemo).
func (m *Model) renderMD(body string, wrap int) []text.Line {
	k := mdKey{body, wrap, m.raw}
	if ls, ok := m.mdMemo[k]; ok {
		return ls
	}
	ls := markdown.Render(body, wrap, m.raw, m.hooks())
	if m.mdMemo == nil {
		m.mdMemo = map[mdKey][]text.Line{}
	}
	m.mdMemo[k] = ls
	return ls
}

// buildDetailLines renders the open item.
func (m *Model) buildDetailLines(width int) {
	m.detailStamp = detailStamp{m.detailVer, width, m.raw}
	m.hitsKey = ""
	wrap := max(30, width-3) // 1 margin + scrollbar column + 1
	switch d := m.detail.(type) {
	case *model.Issue:
		m.detailLines = m.issueLines(d, wrap)
	case *model.PR:
		m.detailLines = m.prLines(d, wrap)
	default:
		m.detailLines = nil
	}
}

func (m *Model) issueLines(iss *model.Issue, wrap int) []text.Line {
	lines := markdown.Wrap([]text.Seg{text.S(m.where(m.repoOf(iss), iss.Number)+" ", "num"), text.S(iss.Title, "strong")}, wrap, nil, nil, false)
	author := text.OrQ(iss.Author.Login)
	state := iss.State
	if state == "" {
		state = "OPEN"
	}
	meta := []text.Seg{text.S(glyphs.Ico("issue"), "icon.issue"), text.S("issue", "muted")}
	if state != "OPEN" {
		meta = append(meta, text.S("  ", "text"))
		meta = append(meta, scenes.Chip(strings.ToLower(state), "chip.state")...)
	}
	meta = append(meta, text.S("  "+glyphs.G("sep")+"  ", "rule"), text.S(glyphs.Ico("person"), "faint"),
		text.S(author, m.authorStyle(author)),
		text.S(fmt.Sprintf("  %s  opened %s", glyphs.G("sep"), clock.FmtTime(iss.CreatedAt)), "faint"))
	for _, lab := range iss.Labels.Names() {
		meta = append(meta, text.S("  ", "text"))
		meta = append(meta, scenes.Chip(lab, "chip.label")...)
	}
	lines = append(lines, markdown.Wrap(meta, wrap, nil, nil, false)...)
	lines = append(lines, text.Line{})
	lines = append(lines, section("description", wrap, ""))
	lines = append(lines, m.renderMD(iss.Body, wrap)...)
	var events []event
	for _, c := range iss.Comments.Nodes {
		events = append(events, event{c.CreatedAt, text.OrQ(c.Author.Login), "commented", c.Body})
	}
	lines = append(lines, text.Line{})
	lines = append(lines, section("comments", wrap, fmt.Sprintf("%d%s", len(events), ofTotal(len(iss.Comments.Nodes), iss.Comments.TotalCount, ""))))
	lines = append(lines, m.timeline(events, wrap)...)
	return lines
}

func (m *Model) prLines(pr *model.PR, wrap int) []text.Line {
	lines := markdown.Wrap([]text.Seg{text.S(m.where(m.repoOf(pr), pr.Number)+" ", "num"), text.S(pr.Title, "strong")}, wrap, nil, nil, false)
	author := text.OrQ(pr.Author.Login)
	state := pr.State
	if state == "" {
		state = "OPEN"
	}
	listed := m.detailListed
	if listed == nil {
		for _, p := range m.allPRs {
			if p.Ident() == pr.Ident() {
				listed = p
				break
			}
		}
	}
	label, style, _, _ := m.decision(pr, listed)
	meta := []text.Seg{text.S(glyphs.Ico("person"), "faint"), text.S(author, m.authorStyle(author)),
		text.S("  "+glyphs.G("sep")+"  ", "rule"), text.S(glyphs.Ico("branch"), "faint"),
		text.S(pr.HeadRefName, "md.inline"), text.S(" "+glyphs.G("arrow")+" ", "faint"), text.S(pr.BaseRefName, "muted"),
		text.S("  "+glyphs.G("sep")+"  ", "rule")}
	if state == "OPEN" {
		meta = append(meta, scenes.Chip(label, style)...)
	} else {
		meta = append(meta, scenes.Chip(strings.ToLower(state), "chip.state")...)
	}
	if stat := diffSegs(pr); len(stat) > 0 {
		meta = append(meta, text.S("  "+glyphs.G("sep")+"  ", "rule"))
		meta = append(meta, stat...)
		if pr.ChangedFiles > 0 || pr.Additions > 0 || pr.Deletions > 0 {
			s := "s"
			if pr.ChangedFiles == 1 {
				s = ""
			}
			meta = append(meta, text.S(fmt.Sprintf(" in %d file%s", pr.ChangedFiles, s), "faint"))
		}
	}
	if listed != nil {
		if c, ok := m.ciOf(listed); ok && c.Glyphs != "" {
			meta = append(meta, text.S("  "+glyphs.G("sep")+"  ", "rule"), text.S("CI ", "faint"))
			meta = append(meta, m.ciSegs(c.Glyphs)...)
		}
	}
	lines = append(lines, markdown.Wrap(meta, wrap, nil, nil, false)...)
	if cards := m.wf.DetailCards(m, pr, listed, wrap); len(cards) > 0 {
		lines = append(lines, text.Line{})
		lines = append(lines, cards...)
	}
	lines = append(lines, text.Line{})
	lines = append(lines, section("description", wrap, ""))
	lines = append(lines, m.renderMD(pr.Body, wrap)...)

	// merge issue comments and reviews into one timeline
	var events []event
	for _, c := range pr.Comments.Nodes {
		events = append(events, event{c.CreatedAt, text.OrQ(c.Author.Login), "commented", c.Body})
	}
	for _, r := range pr.Reviews.Nodes {
		rs := humanize(r.State)
		if rs == "" {
			rs = "?"
		}
		if rs == "pending" {
			rs = "pending (yours, not submitted)"
		}
		events = append(events, event{r.SubmittedAt, text.OrQ(r.Author.Login), "review: " + rs, r.Body})
	}
	// an unsubmitted (pending) review has no timestamp: sort it LAST
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i].when, events[j].when
		if a.IsZero() != b.IsZero() {
			return !a.IsZero()
		}
		return a.Before(b)
	})
	lines = append(lines, text.Line{})
	// "12" when everything is here, "12 of 49" when either list was cut
	shown := len(events)
	total := max(pr.Comments.TotalCount, len(pr.Comments.Nodes)) + max(pr.Reviews.TotalCount, len(pr.Reviews.Nodes))
	lines = append(lines, section("comments & reviews", wrap, fmt.Sprintf("%d%s", shown, ofTotal(shown, total, ""))))
	lines = append(lines, m.timeline(events, wrap)...)
	return lines
}

// timeline renders comments and reviews, each under a dot and a bar.
func (m *Model) timeline(events []event, width int) []text.Line {
	if len(events) == 0 {
		return []text.Line{{Segs: []text.Seg{text.S("  (none yet)", "faint")}}}
	}
	var lines []text.Line
	bar := text.S(glyphs.G("quote")+" ", "tl.bar")
	for _, e := range events {
		lines = append(lines, text.Line{})
		bot := m.authorStyle(e.who) == "author.bot"
		dot := "* "
		if g := glyphs.G("live"); g != "" {
			dot = g + " "
		}
		dotStyle := "tl.dot"
		if bot {
			dotStyle = "tl.dot.bot"
		}
		when := clock.FmtTime(e.when)
		head := []text.Seg{text.S(dot, dotStyle), text.S(e.who, m.authorStyle(e.who)),
			text.S("  "+glyphs.G("sep")+"  ", "rule"), text.S(when, "tl.when"), text.S("  ", "text")}
		if strings.HasPrefix(e.what, "review: ") {
			rs := e.what[8:]
			st := "chip.rev.other"
			switch rs {
			case "approved":
				st = "chip.rev.approved"
			case "changes requested":
				st = "chip.rev.changes"
			}
			head = append(head, scenes.Chip(rs, st)...)
		} else {
			head = append(head, text.S(e.what, "tl.verb"))
		}
		lines = append(lines, markdown.Wrap(head, width, nil, nil, false)...)
		if strings.TrimSpace(e.body) == "" && strings.HasPrefix(e.what, "review: ") {
			continue // a bare approval has no text to show
		}
		for _, ln := range m.renderMD(e.body, width-2) {
			lines = append(lines, text.Line{Segs: append([]text.Seg{bar}, ln.Segs...), Band: ln.Band})
		}
	}
	return lines
}

// ---- patch lines ----

func (m *Model) buildPatchLines(width int) {
	m.patchStamp = patchStamp{m.fileSel, width}
	m.hitsKey = ""
	if m.fileSel < 0 || m.fileSel >= len(m.files) {
		m.patchLines = nil
		return
	}
	f := m.files[m.fileSel]
	kind := fileKind(f.Status)
	minus := "−"
	if glyphs.Active() == glyphs.ASCII {
		minus = "-"
	}
	head := []text.Seg{text.S(glyphs.Ico("file."+kind), "file."+kind), text.S(f.Filename, "path.base"), text.S("  ", "text")}
	head = append(head, scenes.Chip(f.Status, "chip.label")...)
	head = append(head, text.S(fmt.Sprintf("  +%d", f.Additions), "add"), text.S(fmt.Sprintf(" %s%d", minus, f.Deletions), "del"))
	lines := markdown.Wrap(head, width, nil, nil, false)
	lines = append(lines, text.Line{})
	if f.Patch == "" {
		lines = append(lines, text.Line{Segs: []text.Seg{text.S("(no text diff — binary or too large; v opens the full PR diff in $PAGER)", "faint")}})
	} else {
		for _, ln := range strings.Split(f.Patch, "\n") {
			var segs []text.Seg
			switch {
			case strings.HasPrefix(ln, "@@"):
				if i := strings.Index(ln[2:], "@@"); i >= 0 {
					cut := i + 4
					segs = []text.Seg{text.S(ln[:cut], "diff.hunk"), text.S(ln[cut:], "diff.func")}
				} else {
					segs = []text.Seg{text.S(ln, "diff.hunk")}
				}
			case strings.HasPrefix(ln, "+"):
				segs = []text.Seg{text.S(ln, "diff.add")}
			case strings.HasPrefix(ln, "-"):
				segs = []text.Seg{text.S(ln, "diff.del")}
			case strings.HasPrefix(ln, "\\"):
				segs = []text.Seg{text.S(ln, "diff.meta")}
			default:
				segs = []text.Seg{text.S(ln, "diff.ctx")}
			}
			lines = append(lines, markdown.Wrap(segs, width, nil, nil, true)...)
		}
	}
	m.patchLines = lines
}

// ---- search (detail and diff views) ----

func (m *Model) searchLines() []text.Line {
	if m.view == ViewPatch {
		return m.patchLines
	}
	return m.detailLines
}

// currentHits: [(line, start, end)] for the query in the current view;
// smartcase (an uppercase letter makes it case-sensitive).
func (m *Model) currentHits() []hit {
	lines := m.searchLines()
	key := fmt.Sprintf("%s|%d|%s", m.view, len(lines), m.searchQ)
	if m.hitsKey != key {
		m.hitsKey = key
		m.hits = nil
		if m.searchQ != "" {
			q := m.searchQ
			fold := strings.ToLower(q) == q
			for i, ln := range lines {
				t := ln.Text()
				hay := t
				needle := q
				if fold {
					hay, needle = foldRunes(t), foldRunes(q)
				}
				off := 0
				for {
					j := strings.Index(hay[off:], needle)
					if j < 0 {
						break
					}
					start := len([]rune(hay[:off+j]))
					m.hits = append(m.hits, hit{i, start, start + len([]rune(needle))})
					off += j + max(1, len(needle))
				}
			}
		}
		if m.searchCur > len(m.hits)-1 {
			m.searchCur = len(m.hits) - 1
		}
	}
	return m.hits
}

func (m *Model) hitsByLine() map[int][]text.Span {
	out := map[int][]text.Span{}
	for k, h := range m.currentHits() {
		out[h.line] = append(out[h.line], text.Span{Start: h.start, End: h.end, Current: k == m.searchCur})
	}
	return out
}

// searchStep moves to the next/previous hit.
func (m *Model) searchStep(step int) {
	if m.searchQ == "" {
		m.notice("no search yet — / searches this page")
		return
	}
	hits := m.currentHits()
	if len(hits) == 0 {
		m.notice(fmt.Sprintf("no match for %q", m.searchQ))
		return
	}
	m.searchCur = ((m.searchCur+step)%len(hits) + len(hits)) % len(hits)
	target := max(0, hits[m.searchCur].line-4) // the hit near the top, with context
	if m.view == ViewPatch {
		m.patchScroll = target
	} else {
		m.scroll = target
	}
}

// foldRunes lower-cases rune by rune: unlike strings.ToLower it never
// changes the rune count (İ, ß), so a hit's rune offsets stay aligned
// with the line they overlay.
func foldRunes(s string) string {
	r := []rune(s)
	for i, c := range r {
		r[i] = unicode.ToLower(c)
	}
	return string(r)
}

// humanize turns a GitHub enum into words: CHANGES_REQUESTED -> changes
// requested.
func humanize(enum string) string {
	return strings.ReplaceAll(strings.ToLower(enum), "_", " ")
}

// applySearch sets a new query (after the prompt) and jumps to the first
// hit at or below the current scroll position.
func (m *Model) applySearch(q string) {
	if q == "" {
		m.searchQ = ""
		return
	}
	m.searchQ = q
	m.hitsKey = ""
	m.searchCur = -1
	top := m.scroll
	if m.view == ViewPatch {
		top = m.patchScroll
	}
	hits := m.currentHits()
	first := 0
	for k, h := range hits {
		if h.line >= top {
			first = k
			break
		}
	}
	m.searchCur = first - 1
	m.searchStep(+1)
}
