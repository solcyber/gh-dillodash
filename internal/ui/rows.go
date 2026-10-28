package ui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// ---- classification ----

// decision is a PR's chip label (workflows first, then the generic rule).
func (m *Model) decision(pr *model.PR, listed *model.PR) (label, style string, needsYou, done bool) {
	local := m.localApproved(pr)
	if pr.IsDraft {
		return model.LabelDraft, "chip.draft", false, true
	}
	if local {
		return model.LabelApprovedMe, "chip.mine", false, true
	}
	if c := m.wf.Classify(pr, listed, m.me, local); c != nil {
		return c.Label, c.Style, c.NeedsYou, c.Done
	}
	label = model.DecisionLabel(pr, m.me, local)
	style = model.ChipForLabel[label]
	if style == "" {
		style = "chip.await"
	}
	return label, style, label == model.LabelAwaiting, label == model.LabelApprovedMe
}

// authorStyle is "author.me", "author.bot" or "author".
func (m *Model) authorStyle(login string) string {
	l := strings.ToLower(login)
	if l == strings.ToLower(m.me) {
		return "author.me"
	}
	if m.cfg.Bots.IsBot(l) {
		return "author.bot"
	}
	for _, b := range m.wf.BotLogins() {
		if strings.HasPrefix(l, b) {
			return "author.bot"
		}
	}
	return "author"
}

// ---- the filter ----

// rowMatches: every term must appear in the row's number, title, author,
// labels or a workflow's own text. Case-insensitive.
func (m *Model) rowMatches(row workflow.Row, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	var parts []string
	switch v := row.Item.(type) {
	case *model.PR:
		parts = append(parts, fmt.Sprintf("#%d", v.Number), v.Title, v.Author.Login,
			strings.Join(v.Labels.Names(), " "), v.Repo.Name)
	case *model.Issue:
		parts = append(parts, fmt.Sprintf("#%d", v.Number), v.Title, v.Author.Login,
			strings.Join(v.Labels.Names(), " "), v.Repo.Name)
	}
	parts = append(parts, m.wf.SearchText(row))
	hay := strings.ToLower(strings.Join(parts, " "))
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// refilter rebuilds the unified row list and keeps the cursor on the
// same item across a refresh.
func (m *Model) refilter() {
	var prevKey *struct {
		kind string
		id   model.Ident
	}
	if m.selected >= 0 && m.selected < len(m.rows) && m.rows[m.selected].Kind != "header" {
		r := m.rows[m.selected]
		prevKey = &struct {
			kind string
			id   model.Ident
		}{r.Kind, r.Ident()}
	}
	terms := strings.Fields(strings.ToLower(m.filter))
	var rest []*model.PR
	for _, p := range m.allPRs {
		row := workflow.Row{Kind: "pr", Item: p}
		if m.wf.Claims(row) {
			continue
		}
		if m.rowMatches(row, terms) {
			rest = append(rest, p)
		}
	}
	var issues []*model.Issue
	for _, i := range m.issues {
		row := workflow.Row{Kind: "issue", Item: i}
		if m.wf.Claims(row) {
			continue
		}
		if m.rowMatches(row, terms) {
			issues = append(issues, i)
		}
	}
	if m.showAll {
		m.prs = rest
	} else {
		// hide PRs approved by others, but keep ones I approved visible
		// (marked, dimmed) until they're merged, so it's always evident
		// what I have and haven't approved
		m.prs = m.prs[:0:0]
		for _, p := range rest {
			if p.ReviewDecision != "APPROVED" || m.iApproved(p) {
				m.prs = append(m.prs, p)
			}
		}
	}
	var rows []workflow.Row
	if len(m.prs) > 0 {
		rows = append(rows, workflow.Row{Kind: "header", Item: fmt.Sprintf("pull requests (%d)", len(m.prs))})
		for _, p := range m.prs {
			rows = append(rows, workflow.Row{Kind: "pr", Item: p})
		}
	}
	rows = append(rows, m.wf.Sections(m, terms, m.me)...)
	if len(issues) > 0 {
		rows = append(rows, workflow.Row{Kind: "header", Item: fmt.Sprintf("issues (%d)", len(issues))})
		for _, i := range issues {
			rows = append(rows, workflow.Row{Kind: "issue", Item: i})
		}
	}
	m.rows = rows
	if prevKey != nil {
		found := -1
		for i, r := range rows {
			if r.Kind != "header" && r.Kind == prevKey.kind && r.Ident() == prevKey.id {
				found = i
				break
			}
		}
		if found < 0 { // the item moved to another kind (PR -> queued)
			for i, r := range rows {
				if r.Kind != "header" && r.Ident() == prevKey.id {
					found = i
					break
				}
			}
		}
		if found >= 0 {
			m.selected = found
		}
	}
	if m.selected > len(rows)-1 {
		m.selected = len(rows) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
	m.parkCursor()
}

// parkCursor settles the cursor on a selectable row near where it is:
// down first, then up, without wrapping to the far end (a row that
// vanished under the cursor must not send it to the top of the list).
func (m *Model) parkCursor() {
	n := len(m.rows)
	if n == 0 {
		m.selected = 0
		return
	}
	for i := m.selected; i < n; i++ {
		if m.rows[i].Kind != "header" {
			m.selected = i
			return
		}
	}
	for i := m.selected; i >= 0; i-- {
		if m.rows[i].Kind != "header" {
			m.selected = i
			return
		}
	}
	m.selected = 0
}

// cursorOffHeaders parks the cursor on a selectable row.
func (m *Model) cursorOffHeaders(direction int) {
	if len(m.rows) == 0 {
		m.selected = 0
		return
	}
	n := len(m.rows)
	i := m.selected
	for k := 0; k < n; k++ {
		if m.rows[i].Kind != "header" {
			m.selected = i
			return
		}
		i = ((i+direction)%n + n) % n
	}
	m.selected = 0
}

func (m *Model) moveCursor(direction int, wrap bool) {
	if len(m.rows) == 0 {
		return
	}
	n := len(m.rows)
	i := m.selected
	for k := 0; k < n; k++ {
		i += direction
		if i < 0 || i >= n {
			if !wrap {
				return
			}
			i = (i%n + n) % n
		}
		if m.rows[i].Kind != "header" {
			m.selected = i
			return
		}
	}
}

// leaveTrail: the cursor just left row; a comet tail fades out behind it.
func (m *Model) leaveTrail(row int) {
	if fx.Motion != "full" {
		return
	}
	now := fx.Now()
	var keep []trailEntry
	for _, t := range m.trail {
		if now-t.when < 0.3 && t.row != row {
			keep = append(keep, t)
		}
	}
	m.trail = append(keep, trailEntry{row, now})
	m.fx.Start("trail", 0.3, nil)
}

// glideTo is the scroll offset to draw for key: eases from where the
// screen was to target on a page-sized jump; a step of one row lands at
// once. The logical position is always target.
func (m *Model) glideTo(key string, target int) int {
	now := fx.Now()
	g, ok := m.glide[key]
	if !ok {
		g = glideEntry{target, target, 0}
	}
	p := fx.Clamp01((now - g.start) / fx.GlideSecs)
	shown := int(float64(g.from) + float64(g.to-g.from)*fx.EaseOut(p) + 0.5)
	if g.to != target {
		if fx.Motion != "full" || abs(target-shown) <= 1 {
			m.glide[key] = glideEntry{target, target, 0}
			return target
		}
		m.glide[key] = glideEntry{shown, target, now}
		m.fx.Start("glide", fx.GlideSecs, nil)
		return shown
	}
	if p < 1 {
		return shown
	}
	return target
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ---- layout ----

// numText is the "#N" / "repo#N" / "owner/repo#N" column text of an item
// (the same rule as where, so a row and its heading agree).
func (m *Model) numText(row workflow.Row) string {
	switch v := row.Item.(type) {
	case *model.PR:
		return m.where(v.Repo, v.Number)
	case *model.Issue:
		return m.where(v.Repo, v.Number)
	}
	return ""
}

// listLayout computes column widths per row kind for this frame. When
// the title would get under 24 cells, columns drop in order.
func (m *Model) listLayout(w int) map[string]workflow.Layout {
	lay := map[string]workflow.Layout{}
	byKind := map[string][]workflow.Row{}
	numW := 0
	for _, r := range m.rows {
		if r.Kind == "header" {
			continue
		}
		byKind[r.Kind] = append(byKind[r.Kind], r)
		if n := text.Width(m.numText(r)); n > numW {
			numW = n
		}
	}
	if numW < 2 {
		numW = 2
	}
	repoW := 0
	if m.multi {
		for _, r := range m.rows {
			if p, ok := r.Item.(*model.PR); ok && text.Width(p.Repo.Name) > repoW {
				repoW = text.Width(p.Repo.Name)
			}
		}
		repoW = min(24, repoW)
	}
	maxw := func(items []workflow.Row, f func(workflow.Row) string, cap int) int {
		n := 0
		for _, it := range items {
			if w := text.Width(f(it)); w > n {
				n = w
			}
		}
		return min(cap, n)
	}
	author := func(r workflow.Row) string {
		switch v := r.Item.(type) {
		case *model.PR:
			return orQ(v.Author.Login)
		case *model.Issue:
			return orQ(v.Author.Login)
		}
		return ""
	}
	if prs := byKind["pr"]; len(prs) > 0 {
		cols := workflow.Layout{
			"chip": maxw(prs, func(r workflow.Row) string {
				l, _, _, _ := m.decision(r.Item.(*model.PR), nil)
				return l
			}, 99) + 2,
			"ci": maxw(prs, func(r workflow.Row) string {
				c, _ := m.ciOf(r.Item.(*model.PR))
				return text.SegsText(m.ciSegs(c.Glyphs))
			}, 14),
			"diff":   maxw(prs, func(r workflow.Row) string { return text.SegsText(diffSegs(r.Item.(*model.PR))) }, 99),
			"author": maxw(prs, author, 14),
			"age":    3,
		}
		lay["pr"] = fit(cols, w, numW, "diff", "age", "author")
	}
	if iss := byKind["issue"]; len(iss) > 0 {
		cols := workflow.Layout{
			"chip": maxw(iss, func(r workflow.Row) string { return m.issueLabel(r) }, 18) + 2,
			"count": maxw(iss, func(r workflow.Row) string {
				return glyphs.Ico("comment") + fmt.Sprint(r.Item.(*model.Issue).Comments.TotalCount)
			}, 99),
			"author": maxw(iss, author, 14),
			"age":    3,
		}
		lay["issue"] = fit(cols, w, numW, "age", "author")
	}
	for kind, rk := range m.wf.RowKinds() {
		if items := byKind[kind]; len(items) > 0 && rk.Layout != nil {
			lay[kind] = rk.Layout(m, items, w, numW, repoW)
		}
	}
	return lay
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// fit drops columns in order until the title keeps 24 cells.
func fit(cols workflow.Layout, w, numW int, dropOrder ...string) workflow.Layout {
	fixed := func() int { // gutter 2 + icon 2 + "#num " + each column + its gap
		n := 4 + numW + 1
		for _, v := range cols {
			n += v + 1
		}
		return n
	}
	for _, name := range dropOrder {
		if w-fixed() >= 24 {
			break
		}
		delete(cols, name)
	}
	cols["num"] = numW
	return cols
}

// issueLabel is an issue row's chip: its first label, or "issue"; a
// workflow may override it through Row.Meta["label"].
func (m *Model) issueLabel(r workflow.Row) string {
	if l, ok := r.Meta["label"].(string); ok && l != "" {
		return clipRunes(l, 16)
	}
	i := r.Item.(*model.Issue)
	if names := i.Labels.Names(); len(names) > 0 {
		return clipRunes(names[0], 16)
	}
	return "issue"
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// ---- row pieces ----

var ciCount = regexp.MustCompile(`^\d+`)

var ciGlyph = map[byte][2]string{
	'+': {"pass", "ci.pass"}, 'x': {"fail", "ci.fail"}, '.': {"wait", "ci.wait"},
	'~': {"", "ci.run"}, '-': {"skip", "ci.skip"}, '?': {"unknown", "ci.unknown"},
}

// ciSegs renders CI cluster codes ('++~x', or compressed '9+1x2.') as
// styled glyphs; running checks get the animated spinner.
func (m *Model) ciSegs(codes string) []text.Seg {
	var out []text.Seg
	i := 0
	for i < len(codes) {
		count := ""
		if c := ciCount.FindString(codes[i:]); c != "" {
			count = c
			i += len(c)
			if i >= len(codes) {
				break
			}
		}
		g, ok := ciGlyph[codes[i]]
		if !ok {
			g = ciGlyph['?']
		}
		var glyph string
		if g[0] == "" {
			glyph = glyphs.SpinnerFrame()
		} else {
			glyph = glyphs.G(g[0])
		}
		out = append(out, text.S(count+glyph, g[1]))
		i++
	}
	return out
}

// diffSegs is the +N −N diffstat.
func diffSegs(pr *model.PR) []text.Seg {
	if pr.Additions == 0 && pr.Deletions == 0 && pr.ChangedFiles == 0 && pr.HeadRefOid == "" {
		return nil
	}
	minus := "−"
	if glyphs.Active() == glyphs.ASCII {
		minus = "-"
	}
	addS, delS := "zero", "zero"
	if pr.Additions > 0 {
		addS = "add"
	}
	if pr.Deletions > 0 {
		delS = "del"
	}
	return []text.Seg{
		text.S(fmt.Sprintf("+%d", pr.Additions), addS), text.S(" ", "text"),
		text.S(fmt.Sprintf("%s%d", minus, pr.Deletions), delS),
	}
}

// rowSegs renders one list row (after the gutter). pos is the row's place
// on screen, which staggers its ambient effects.
func (m *Model) rowSegs(row workflow.Row, L workflow.Layout, w, pos int) []text.Seg {
	ambient := m.ambient
	mono := theme.Current() == theme.Mono
	switch row.Kind {
	case "pr":
		item := row.Item.(*model.PR)
		label, style, needsYou, done := m.decision(item, nil)
		icon := "pr"
		if item.IsDraft {
			icon = "draft"
		}
		var segs []text.Seg
		if g := glyphs.G(icon); g != "" {
			segs = append(segs, text.S(g+" ", "icon."+icon))
		}
		segs = append(segs, text.S(text.Pad(m.numText(row), L["num"])+" ", scenes.NumStyle(m.fx, identKey(item.Ident()), "num")))
		segs = append(segs, text.PadSegs(scenes.GlintChip(label, style, pos, ambient, mono), L["chip"])...)
		segs = append(segs, text.S(" ", "text"))
		c, _ := m.ciOf(item)
		segs = append(segs, text.PadSegs(m.ciSegs(c.Glyphs), L["ci"])...)
		segs = append(segs, text.S(" ", "text"))
		if dw, ok := L["diff"]; ok {
			segs = append(segs, text.PadSegs(diffSegs(item), dw)...)
			segs = append(segs, text.S(" ", "text"))
		}
		if aw, ok := L["author"]; ok {
			a := orQ(item.Author.Login)
			segs = append(segs, text.S(text.Pad(text.Ellip(a, aw), aw)+" ", m.authorStyle(a)))
		}
		if aw, ok := L["age"]; ok {
			age, st := clock.ShortAge(item.UpdatedAt, clock.Now())
			segs = append(segs, text.S(text.Pad(age, aw)+" ", st))
		}
		tstyle := "title"
		if done {
			tstyle = "title.done"
		} else if needsYou {
			tstyle = "title.attn"
		}
		suffix := ""
		if len(c.Failed) > 0 {
			suffix = "  " + glyphs.G("fail") + " " + strings.Join(c.Failed, ", ")
		}
		room := w - 3 - text.SegsWidth(segs)
		sw := min(text.Width(suffix), max(0, room-16))
		segs = append(segs, text.S(text.Ellip(item.Title, room-sw), tstyle))
		if suffix != "" {
			st := "failnames"
			if t, ok := m.fx.T("cifail:" + identKey(item.Ident())); ok && int(t*8)%2 == 0 {
				st = "ci.fail.flash"
			}
			segs = append(segs, text.S(text.Ellip(suffix, sw), st))
		}
		return segs
	case "issue":
		item := row.Item.(*model.Issue)
		var segs []text.Seg
		if g := glyphs.G("issue"); g != "" {
			segs = append(segs, text.S(g+" ", "icon.issue"))
		}
		segs = append(segs, text.S(text.Pad(m.numText(row), L["num"])+" ", scenes.NumStyle(m.fx, identKey(item.Ident()), "num.issue")))
		segs = append(segs, text.PadSegs(scenes.Chip(m.issueLabel(row), "chip.label"), L["chip"])...)
		segs = append(segs, text.S(" ", "text"))
		cnt := ""
		if n := item.Comments.TotalCount; n > 0 {
			cnt = glyphs.Ico("comment") + fmt.Sprint(n)
		}
		segs = append(segs, text.S(text.Pad(cnt, L["count"])+" ", "count"))
		if aw, ok := L["author"]; ok {
			a := orQ(item.Author.Login)
			segs = append(segs, text.S(text.Pad(text.Ellip(a, aw), aw)+" ", m.authorStyle(a)))
		}
		if aw, ok := L["age"]; ok {
			age, st := clock.ShortAge(item.UpdatedAt, clock.Now())
			segs = append(segs, text.S(text.Pad(age, aw)+" ", st))
		}
		room := w - 3 - text.SegsWidth(segs)
		segs = append(segs, text.S(text.Ellip(item.Title, room), "title"))
		return segs
	}
	if rk, ok := m.wf.RowKinds()[row.Kind]; ok && rk.Segs != nil {
		return rk.Segs(m, row, L, w, pos)
	}
	return []text.Seg{text.S(fmt.Sprintf("(%s row)", row.Kind), "faint")}
}

var headerCount = regexp.MustCompile(`^(.*) \((\d+)\)$`)

// headerSegs draws section bars ('▌ pull requests  6 ────') and the
// indented sub-headers (a night), which get a workflow's countdown.
func (m *Model) headerSegs(row workflow.Row, w, pos int) []text.Seg {
	txt, _ := row.Item.(string)
	var segs []text.Seg
	if strings.HasPrefix(txt, "  ") {
		label := strings.TrimSpace(txt)
		extra, more := m.wf.HeaderExtra(m, row, w)
		segs = []text.Seg{
			text.S("   "+boxGlyph(2)+glyphs.G("rule")+" ", "rule"),
			text.S(glyphs.Ico("clock"), "subsection"),
		}
		room := w - 2 - text.SegsWidth(segs)
		if text.Width(label+extra)+text.SegsWidth(more) > room {
			extra = "" // the countdown goes first on a narrow screen
		}
		more = text.ClipSegs(more, max(0, room-text.Width(label)))
		segs = append(segs, text.S(text.Ellip(label, room), "subsection"))
		segs = append(segs, more...)
		segs = append(segs, text.S(extra, "faint"), text.S(" ", "text"))
	} else {
		name, count := txt, ""
		if mm := headerCount.FindStringSubmatch(txt); mm != nil {
			name, count = mm[1], mm[2]
		}
		icon := "pr"
		switch {
		case strings.Contains(name, "window"):
			icon = "queued"
		case strings.HasPrefix(name, "releases"):
			icon = "release"
		case strings.HasPrefix(name, "issues"):
			icon = "issue"
		}
		if ic, ok := row.Meta["icon"].(string); ok && ic != "" {
			icon = ic
		}
		segs = []text.Seg{text.S(" "+glyphs.Ico(icon), "section")}
		tail := text.S("  "+count+" ", "section.count")
		segs = append(segs, text.S(text.Ellip(name, w-2-text.SegsWidth(segs)-text.Width(tail.Text)), "section"), tail)
	}
	return append(segs, scenes.GleamSegs(w-1-text.SegsWidth(segs), pos)...)
}

// boxGlyph is one of the box-drawing set's characters.
func boxGlyph(i int) string {
	r := []rune(glyphs.G("box"))
	if i < len(r) {
		return string(r[i])
	}
	return "|"
}
