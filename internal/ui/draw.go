package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// draw paints one frame.
func (m *Model) draw(c *canvas.Canvas) {
	h, w := c.H, c.W
	fx.Ambient = fx.Motion == "full" && fx.Now()-m.lastKey < fx.AmbientIdleSecs
	// mono has no color effects to tick for, only the empty list's walk
	m.ambient = fx.Ambient && (theme.Current() != theme.Mono || (m.view == ViewList && m.loaded && len(m.rows) == 0))
	m.animating = m.refreshing
	if _, splash := m.fx.Elapsed("splash"); splash {
		if scenes.Splash(c, m.fx, h, w, scenes.SplashInfo{ReposText: m.reposText(), Loaded: m.loaded, Failed: m.errText != ""}) {
			return
		}
		m.endSplash()
	}
	if m.drawn != m.view {
		depth := map[string]float64{ViewList: 0, ViewFortress: 0.5, ViewDetail: 1, ViewFiles: 2, ViewPatch: 3}
		if m.drawn != "" {
			dir := -1
			if depth[m.view] > depth[m.drawn] {
				dir = 1
			}
			m.fx.Start("wipe", 0.22, map[string]any{"dir": dir})
		}
		m.drawn = m.view
	}
	m.drawHeader(c, w)
	switch m.view {
	case ViewList:
		m.drawList(c, h, w)
	case ViewFortress:
		m.drawFortress(c, h, w)
	case ViewDetail:
		m.drawDetail(c, h, w)
	case ViewFiles:
		m.drawFiles(c, h, w)
	default:
		m.drawPatch(c, h, w)
	}
	if m.view != ViewFortress {
		m.drawSubheader(c, w)
	}
	scenes.Wipe(c, m.fx, h, w)
	m.drawStatus(c, h, w)
	m.drawKeys(c, h, w)
	if m.overlay == "help" {
		m.drawHelp(c, h, w)
	}
	if m.modal != nil {
		m.modal.Draw(c, m.fx)
	}
	scenes.Confetti(c, m.fx, h, w)
	scenes.Launch(c, m.fx, h, w)
}

// endSplash: the splash is over (time, data, a key, or no room): the
// list cascades in.
func (m *Model) endSplash() {
	m.fx.Stop("splash")
	if m.loaded {
		m.startCascade()
	}
}

func (m *Model) startCascade() {
	scenes.StartCascade(m.fx, len(m.rows))
}

// pl is a powerline separator: the left segment's color pointing into
// the right one's (a plain space below 256 colors).
func pl(style string) []text.Seg {
	g := " "
	if theme.Current() >= theme.Color256 && glyphs.G("pl") != "" {
		g = glyphs.G("pl")
	}
	return []text.Seg{text.S(g, style)}
}

// appNameSegs: "dillodash" in the header; a highlight runs through it
// while a refresh is in flight, and catches it now and then otherwise.
func (m *Model) appNameSegs(refreshing bool) []text.Seg {
	name := "dillodash"
	hi := -2
	if fx.Motion == "full" && theme.Current() != theme.Mono {
		if refreshing {
			hi = int(fx.Wave(0.9, 0)*float64(len(name)+3)) - 1
		} else if fx.Ambient {
			if p := fx.Wave(12.0, 0); p < 0.1 {
				hi = int(p/0.1*float64(len(name)+3)) - 1
			}
		}
	}
	if hi == -2 {
		return []text.Seg{text.S(name, "hdr.app")}
	}
	var segs []text.Seg
	for i, ch := range name {
		st := "hdr.app"
		if i == hi {
			st = "hdr.app.hi"
		}
		segs = append(segs, text.S(string(ch), st))
	}
	return segs
}

type part struct {
	prio int
	segs []text.Seg
}

// drawHeader: row 0, powerline style. Left: [ logo dillodash ][ repos ]
// as me (list view), or [ icon #N ] and the title (inside a PR). Right,
// by priority as the terminal narrows: the version, the refresh state,
// the GraphQL budget meter, the draining countdown to the next refresh.
func (m *Model) drawHeader(c *canvas.Canvas, w int) {
	refreshing := m.refreshing
	backoff := int(time.Until(m.rateLimitedUntil).Seconds())
	var parts []part
	if m.detail == nil {
		parts = append(parts, part{0, []text.Seg{text.S("  "+m.ver, "build")}})
	}
	if refreshing {
		parts = append(parts, part{1, []text.Seg{text.S("  "+glyphs.SpinnerFrame()+" refreshing", "spin")}})
	} else if m.loaded {
		parts = append(parts, part{1, []text.Seg{text.S("  "+glyphs.Ico("sync")+m.lastRefreshWall, "muted")}})
	}
	if backoff > 0 {
		parts = append(parts, part{1, []text.Seg{text.S(fmt.Sprintf("  held %ds", backoff), "budget.low")}})
	}
	if rate := m.gh.LastRate(); rate != nil && rate.Limit > 0 {
		low := rate.Remaining < rate.Limit/5
		st, fill := "budget.ok", "meter.ok"
		if low {
			st, fill = "budget.low", "meter.short"
		}
		segs := []text.Seg{text.S("  "+glyphs.Ico("budget")+"gql ", st)}
		segs = append(segs, fx.MeterSegs(float64(rate.Remaining)/float64(rate.Limit), 5, fill, "meter.track")...)
		segs = append(segs, text.S(fmt.Sprintf(" %s/%s", commas(rate.Remaining), commas(rate.Limit)), st))
		parts = append(parts, part{2, segs})
	}
	if !refreshing && backoff <= 0 && m.loaded {
		left := max(0, int(m.nextRefreshAt-fx.Now()))
		segs := []text.Seg{text.S("  next ", "faint")}
		segs = append(segs, fx.MeterSegs(float64(left)/float64(max(1, m.cfg.Display.RefreshSecs)), 5, "meter.ok", "meter.track")...)
		segs = append(segs, text.S(fmt.Sprintf(" %d:%02d", left/60, left%60), "faint"))
		parts = append(parts, part{3, segs})
	}
	logo := "hdr.logo.0"
	if fx.Ambient && theme.Current() >= theme.Color256 {
		logo = fx.Ramp("hdr.logo.", 3, fx.Tri(fx.Wave(3.0, 0)))
	}
	var left []text.Seg
	var minLeft int
	if m.detail != nil {
		kind := "pr"
		num := ""
		switch d := m.detail.(type) {
		case *model.Issue:
			kind = "issue"
			num = m.where(m.repoOf(d), d.Number) + " "
		case *model.PR:
			if d.IsDraft {
				kind = "draft"
			}
			if k, ok := m.detailIcon(); ok {
				kind = k
			}
			num = m.where(m.repoOf(d), d.Number) + " "
		}
		left = append([]text.Seg{text.S(" "+glyphs.Ico(kind), logo), text.S(num, "hdr.app")}, pl("hdr.sep.app.k")...)
		left = append(left, text.S(" ", "text"))
		minLeft = text.SegsWidth(left) + 12
	} else {
		left = append([]text.Seg{text.S(" "+glyphs.Ico("logo"), logo)}, m.appNameSegs(refreshing)...)
		left = append(left, text.S(" ", "hdr.app"))
		left = append(left, pl("hdr.sep.app")...)
		left = append(left, text.S(" "+m.reposText()+" ", "hdr.repo"))
		left = append(left, pl("hdr.sep.repo")...)
		minLeft = text.SegsWidth(left)
	}
	// drop right-side parts, least important first, until the left fits
	keep := append([]part(nil), parts...)
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].prio < keep[j].prio })
	total := func() int {
		n := 0
		for _, p := range keep {
			n += text.SegsWidth(p.segs)
		}
		return n
	}
	for len(keep) > 0 && total()+minLeft+2 > w {
		keep = keep[:len(keep)-1]
	}
	// display order: refresh, budget, next, then the version last
	sort.SliceStable(keep, func(i, j int) bool {
		ai, aj := keep[i].prio == 0, keep[j].prio == 0
		if ai != aj {
			return !ai
		}
		return keep[i].prio < keep[j].prio
	})
	var right []text.Seg
	for _, p := range keep {
		right = append(right, p.segs...)
	}
	right = append(right, text.S(" ", "text"))
	rw := text.SegsWidth(right)
	avail := max(0, w-rw)
	if m.detail != nil {
		left = append(left, text.S(text.Ellip(m.detailTitle(), avail-text.SegsWidth(left)-1), "strong"))
	} else {
		me := []text.Seg{text.S(" as ", "faint"), text.S(m.me, "author.me")}
		if text.SegsWidth(left)+text.SegsWidth(me) < avail {
			left = append(left, me...) // only whole: a dangling "as" read as a glitch
		}
	}
	c.PutSegs(0, 0, left, avail, false, false)
	c.PutSegs(0, max(0, w-rw), right, 0, false, false)
}

// detailIcon lets a workflow override the header icon of the open PR
// (e.g. a release); it reads Row.Meta of the listed row.
func (m *Model) detailIcon() (string, bool) {
	if m.detailListed != nil {
		if k, ok := m.detailListed.Get("icon").(string); ok && k != "" {
			return k, true
		}
	}
	return "", false
}

func (m *Model) detailTitle() string {
	switch d := m.detail.(type) {
	case *model.PR:
		return d.Title
	case *model.Issue:
		return d.Title
	}
	return ""
}

func commas(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, ch := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, ch)
	}
	return string(out)
}

// drawSubheader: row 1, summary chips in the list, a breadcrumb inside a PR.
func (m *Model) drawSubheader(c *canvas.Canvas, w int) {
	var segs []text.Seg
	if m.view == ViewList {
		segs = m.summarySegs()
	} else {
		here := func(v string) string {
			if m.view == v {
				return "crumb.here"
			}
			return "crumb"
		}
		crumb := " " + glyphs.G("crumb") + " "
		segs = []text.Seg{text.S(" list", "crumb"), text.S(crumb, "crumb"), text.S(m.detailRef(), here(ViewDetail))}
		if m.view == ViewFiles || m.view == ViewPatch {
			segs = append(segs, text.S(crumb, "crumb"), text.S("files", here(ViewFiles)))
		}
		if m.view == ViewPatch && len(m.files) > 0 {
			segs = append(segs, text.S(crumb, "crumb"), text.S(m.files[m.fileSel].Filename, "crumb.here"))
		}
		if m.view == ViewDetail && m.raw {
			segs = append(segs, text.S("   raw", "budget.low"))
		}
	}
	right := m.positionSegs()
	rw := text.SegsWidth(right)
	c.PutSegs(1, 0, segs, max(0, w-rw-2), false, false)
	c.PutSegs(1, max(0, w-rw-1), right, 0, false, false)
}

// summarySegs: the list's summary row.
func (m *Model) summarySegs() []text.Seg {
	me := strings.ToLower(m.me)
	need := 0
	for _, p := range m.prs {
		if p.IsDraft || strings.ToLower(p.Author.Login) == me {
			continue
		}
		if _, _, needsYou, _ := m.decision(p, nil); needsYou {
			need++
		}
	}
	failing := 0
	for _, p := range m.allPRs {
		if c, ok := m.ciOf(p); ok && len(c.Failed) > 0 {
			failing++
		}
	}
	issues := 0
	for _, i := range m.issues {
		if !m.wf.Claims(workflow.Row{Kind: "issue", Item: i}) {
			issues++
		}
	}
	sep := text.S("  "+glyphs.G("sep")+"  ", "rule")
	attn := func(n int, yes string) string {
		if n > 0 {
			return yes
		}
		return "sum.muted"
	}
	segs := []text.Seg{text.S(fmt.Sprintf(" %s%d need you", glyphs.Ico("shield"), need), attn(need, "sum.attn")), sep}
	if wf := m.wf.SummarySegs(m); len(wf) > 0 {
		segs = append(segs, wf...)
		segs = append(segs, sep)
	}
	segs = append(segs,
		text.S(fmt.Sprintf("%s%d failing CI", glyphs.Ico("fail"), failing), attn(failing, "sum.fail")), sep,
		text.S(fmt.Sprintf("%s%d issues", glyphs.Ico("issue"), issues), "sum.muted"))
	if m.showAll {
		segs = append(segs, sep, text.S("showing approved (A)", "sum.info"))
	}
	if m.filter != "" {
		segs = append(segs, text.S("  ", "text"))
		segs = append(segs, scenes.Chip(glyphs.Ico("filter")+m.filter, "chip.filter")...)
	}
	return segs
}

// positionSegs: right side of row 1, search state and scroll position.
func (m *Model) positionSegs() []text.Seg {
	var segs []text.Seg
	if (m.view == ViewDetail || m.view == ViewPatch) && m.searchQ != "" {
		hits := m.currentHits()
		segs = append(segs, text.S(glyphs.Ico("search")+m.searchQ, "md.inline"))
		cur := 0
		if len(hits) > 0 {
			cur = m.searchCur + 1
		}
		st := "budget.low"
		if len(hits) > 0 {
			st = "faint"
		}
		segs = append(segs, text.S(fmt.Sprintf(" %d/%d  ", cur, len(hits)), st))
	}
	switch m.view {
	case ViewDetail, ViewPatch:
		lines, top := m.detailLines, m.scroll
		if m.view == ViewPatch {
			lines, top = m.patchLines, m.patchScroll
		}
		total := max(1, len(lines))
		segs = append(segs, text.S(fmt.Sprintf("L%d/%d %d%%", min(total, top+1), total, min(100, 100*(top+1)/total)), "pos"))
	case ViewFiles:
		if len(m.files) > 0 {
			segs = append(segs, text.S(fmt.Sprintf("%d/%d", m.fileSel+1, len(m.files)), "pos"))
		}
	}
	return segs
}

// drawStatus: the status line (second from the bottom). A message stays
// up for ToastSecs (types itself in, then fades) or until a newer one
// replaces it; results are blue, refusals and errors gold (with a shake),
// and an error stays until something supersedes it.
func (m *Model) drawStatus(c *canvas.Canvas, h, w int) {
	backoff := int(time.Until(m.rateLimitedUntil).Seconds())
	age := fx.Now() - m.statusAt
	var segs []text.Seg
	switch {
	case backoff > 0:
		segs = []text.Seg{text.S(fmt.Sprintf(" %sGitHub rate limit hit — refresh paused %ds (auto-resumes; raise refresh_secs if this recurs)", glyphs.Ico("warn"), backoff), "toast.warn")}
	case m.errText != "":
		segs = []text.Seg{text.S(" ", "text"), text.S(" "+glyphs.G("err")+" ", "chip.err"), text.S(" "+m.errText, "toast.warn")}
	case m.status != "" && m.statusKind == "spin":
		segs = []text.Seg{text.S(" "+glyphs.SpinnerFrame()+" "+m.status, "spin")}
	case m.status != "" && age < fx.ToastSecs:
		kind := m.statusKind
		style := "toast." + kind
		if fx.Motion == "full" && age > fx.ToastSecs-0.8 {
			style = "toast.fade"
		}
		icon := "info"
		if kind == "ok" || kind == "warn" {
			icon = kind
		}
		segs = []text.Seg{text.S(" "+glyphs.Ico(icon)+m.status, style)}
	case m.warn != "":
		segs = []text.Seg{text.S(" "+glyphs.Ico("warn")+m.warn, "toast.warn")}
	default:
		return
	}
	if reveal, ok := m.fx.T("toast"); ok { // types itself in
		segs = text.ClipSegs(segs, 2+int(fx.EaseOut(reveal)*float64(text.SegsWidth(segs))))
	}
	x := 0
	if shake, ok := m.fx.T("toast.shake"); ok {
		x = max(0, fx.ShakeDX(shake))
	}
	c.PutSegs(h-2, x, segs, w, false, false)
}

// drawKeys: an htop-style key bar from the key table, trimmed to the
// width by priority (navigation goes first, q and ? never go).
func (m *Model) drawKeys(c *canvas.Canvas, h, w int) {
	view := m.view
	if view == ViewDetail && m.detailKind == "issue" {
		view = "issue"
	}
	help := []text.Seg{text.S(" ? ", "chip.key"), text.S(" help ", "keylabel")}
	room := w - 1 - text.SegsWidth(help)
	cells := func(it barItem) int { return text.Width(" "+it.key+" ") + text.Width(" "+it.label+" ") }
	var segs []text.Seg
	for _, it := range trimBar(m.barFor(view), room, cells) {
		segs = append(segs, text.S(" "+it.key+" ", "chip.key"), text.S(" "+it.label+" ", "keylabel"))
	}
	if text.SegsWidth(segs) > room {
		segs = nil // not even q fits: the help chip alone
	}
	c.PutSegs(h-1, 0, append(segs, help...), 0, false, false)
}

// drawHelp paints the help overlay.
func (m *Model) drawHelp(c *canvas.Canvas, h, w int) {
	label := func(t string) text.Seg { return text.S(text.Pad(t, 10), "section") }
	sp := text.S("  ", "text")
	names := map[byte]string{'+': "pass", 'x': "fail", '.': "queued", '~': "running", '-': "skipped", '?': "not visible"}
	ci := []text.Seg{label("CI")}
	for _, code := range []byte("+x.~-?") {
		ci = append(ci, m.ciSegs(string(code))...)
		ci = append(ci, text.S(" "+names[code], "muted"), sp)
	}
	var rows [][]text.Seg
	// the key table's groups, the workflows' rows after detail, each
	// wrapped to the box so a narrow terminal gets more rows, not less
	textW := max(20, min(w-2, 92)-14)
	addLines := func(name string, lines []string) {
		for i, line := range lines {
			l := name
			if i > 0 {
				l = ""
			}
			rows = append(rows, []text.Seg{label(l), text.S(line, "text")})
		}
	}
	for _, g := range helpGroups() {
		addLines(g.label, packEntries(g.entries, textW))
		if g.label == "detail" {
			for _, kv := range m.wf.HelpRows() {
				addLines(kv[0], widgets.Wrap(kv[1], textW))
			}
		}
	}
	status := []text.Seg{label("status")}
	status = append(status, scenes.Chip("awaiting review", "chip.await")...)
	status = append(status, sp)
	status = append(status, scenes.Chip("approved by me", "chip.mine")...)
	status = append(status, sp)
	status = append(status, scenes.Chip("changes requested", "chip.changes")...)
	status = append(status, sp)
	status = append(status, scenes.Chip("DRAFT", "chip.draft")...)
	rows = append(rows, nil, ci, status)
	rows = append(rows, m.wf.LegendRows()...)
	rows = append(rows,
		[]text.Seg{label("age"), text.S("<1h ", "age.0"), text.S("<1d ", "age.1"), text.S("<1w ", "age.2"), text.S("older", "age.3")},
		[]text.Seg{label("colors"), text.S("gold", "sum.attn"), text.S(" needs a look   ", "muted"), text.S("blue", "sum.info"), text.S(" fine or done   ", "muted"), text.S("gray", "muted"), text.S(" everything else", "muted")},
		[]text.Seg{label("motion"), text.S(string([]rune(glyphs.G("spark"))[min(3, len([]rune(glyphs.G("spark")))-1)])+" ", "num.new"), text.S("new since the last refresh   ", "muted"), text.S("--motion off", "md.inline"), text.S(" stills it all", "muted")},
		nil,
		[]text.Seg{text.S(fmt.Sprintf("%s %s icons %s %s theme %s %s motion %s %s refresh every %ds", m.ver, glyphs.G("sep"), glyphs.Active(), glyphs.G("sep"), theme.Current(), glyphs.G("sep"), fx.Motion, glyphs.G("sep"), m.cfg.Display.RefreshSecs), "faint")},
	)
	widest := 0
	for _, r := range rows {
		widest = max(widest, text.SegsWidth(r))
	}
	bw := min(w-2, max(92, widest+4))
	full := min(h-2, len(rows)+4)
	t, ok := m.fx.T("help")
	bh := min(h-2, widgets.PopHeight(full, t, ok))
	ants := -1.0
	if fx.Ambient {
		ants = fx.Wave(2.5, 0)
	}
	top, left, iw := widgets.DrawBox(c, max(1, (h-bh)/2), max(0, (w-bw)/2), bh, bw, glyphs.Ico("logo")+"dillodash keys", "logo", "menu.title", ants)
	for i, segs := range rows {
		if i >= bh-4 {
			break
		}
		c.PutSegs(top+1+i, left, segs, left+iw, false, false)
	}
	if bh == full {
		c.PutSegs(top+bh-3, left, []text.Seg{text.S("any key closes", "menu.hint")}, left+iw, false, false)
	}
}

// drawList paints the list view.
func (m *Model) drawList(c *canvas.Canvas, h, w int) {
	if !m.loaded {
		if m.errText != "" {
			wait := max(0, int(m.nextRefreshAt-fx.Now()))
			c.PutSegs(3, 2, []text.Seg{text.S(fmt.Sprintf("%scouldn't load %s — retrying in %ds (r retries now); the error is on the status line", glyphs.Ico("warn"), m.reposText(), wait), "toast.warn")}, 0, false, false)
		} else {
			c.PutSegs(3, 2, []text.Seg{text.S(glyphs.SpinnerFrame()+" loading "+m.reposText()+" ...", "spin")}, 0, false, false)
			if fx.Motion == "full" {
				c.PutSegs(5, 2, fx.ScannerSegs(max(10, min(48, w-6)), fx.Wave(1.3, 0)), 0, false, false)
			}
		}
		return
	}
	if len(m.rows) == 0 {
		note := ""
		switch {
		case m.filter != "":
			note = "nothing matches the filter (Esc clears it)"
		case m.showAll:
			note = "nothing open in " + m.reposText()
		default:
			note = "all clear " + glyphs.G("sep") + " nothing awaiting your attention (A shows PRs approved by others)"
		}
		scenes.Empty(c, m.fx, h, w, note, theme.Current())
		return
	}
	top := 2
	visible := max(1, h-4)
	n := len(m.rows)
	// sticky viewport: scroll only when the cursor leaves it, and keep a
	// section's header in view above its first row
	wantTop := m.selected
	if m.selected > 0 && m.rows[m.selected-1].Kind == "header" {
		wantTop = m.selected - 1
	}
	if wantTop < m.listTop {
		m.listTop = wantTop
	} else if m.selected >= m.listTop+visible {
		m.listTop = m.selected - visible + 1
	}
	m.listTop = max(0, min(m.listTop, max(0, n-visible)))
	first := max(0, min(m.glideTo("list", m.listTop), max(0, n-visible)))
	L := m.listLayout(w)
	now := fx.Now()
	trail := map[int]float64{}
	for _, t := range m.trail {
		if now-t.when < 0.3 {
			trail[t.row] = now - t.when
		}
	}
	breathe := scenes.Breathe(fx.Ambient, theme.Current() == theme.Mono)
	for i := 0; i < visible && first+i < n; i++ {
		idx := first + i
		row := m.rows[idx]
		y := top + i
		right, skip := scenes.CascadeRight(m.fx, i, w)
		if skip {
			continue
		}
		if row.Kind == "header" {
			c.PutSegs(y, 0, m.headerSegs(row, w, i), right, false, false)
		} else {
			sel := idx == m.selected
			if sel {
				c.Fill(y, 0, min(w, right), "sel", true, false)
			}
			age, has := trail[idx]
			gut := scenes.Gutter(m.fx, identKey(row.Ident()), sel, breathe, age, has)
			c.PutSegs(y, 0, append(gut, text.S(" ", "text")), right, sel, false)
			c.PutSegs(y, 2, m.rowSegs(row, L[row.Kind], w, i), min(w-1, right), sel, false)
			if p, ok := row.Item.(*model.PR); ok && row.Kind == "pr" {
				if cl, ok := m.ciOf(p); ok && strings.Contains(cl.Glyphs, "~") {
					m.animating = true
				}
			}
		}
		if right < w-1 {
			c.PutSegs(y, right, []text.Seg{text.S(glyphs.G("sel"), "fx.b6")}, 0, false, false)
		}
	}
	if n > visible {
		scenes.Scrollbar(c, top, visible, n, first, w-1)
	}
}

// drawDetail paints the open item's lines.
func (m *Model) drawDetail(c *canvas.Canvas, h, w int) {
	if m.detailStamp != (detailStamp{m.detailVer, w, m.raw}) {
		m.buildDetailLines(w) // new data, an approval, raw, resize
	}
	top := 2
	visible := max(1, h-4)
	m.scroll = max(0, min(m.scroll, max(0, len(m.detailLines)-visible)))
	m.drawLines(c, m.detailLines, m.glideTo("detail", m.scroll), top, visible, w)
}

func (m *Model) drawLines(c *canvas.Canvas, lines []text.Line, first, top, visible, w int) {
	hits := map[int][]text.Span{}
	if m.searchQ != "" {
		hits = m.hitsByLine()
	}
	for i := 0; i < visible && first+i < len(lines); i++ {
		line := lines[first+i]
		y := top + i
		segs := text.OverlayHits(line.Segs, hits[first+i])
		x := 1
		if line.Band {
			// a code band starts after any timeline gutter
			if len(segs) > 0 && segs[0].Style == "tl.bar" {
				x = c.PutSegs(y, x, segs[:1], w-1, false, false)
				segs = segs[1:]
			}
			c.Fill(y, x, w-1, "band", false, true)
		}
		c.PutSegs(y, x, segs, w-1, false, line.Band)
	}
	scenes.Scrollbar(c, top, visible, len(lines), first, w-1)
}

// drawFiles paints the changed-files list.
func (m *Model) drawFiles(c *canvas.Canvas, h, w int) {
	if len(m.files) == 0 {
		c.PutSegs(3, 2, []text.Seg{text.S("(no files reported)", "faint")}, 0, false, false)
		return
	}
	top := 2
	visible := max(1, h-4)
	n := len(m.files)
	if m.fileSel < m.filesTop {
		m.filesTop = m.fileSel
	} else if m.fileSel >= m.filesTop+visible {
		m.filesTop = m.fileSel - visible + 1
	}
	m.filesTop = max(0, min(m.filesTop, max(0, n-visible)))
	first := m.glideTo("files", m.filesTop)
	aw, dw := 0, 0
	for _, f := range m.files {
		aw = max(aw, len(fmt.Sprintf("+%d", f.Additions)))
		dw = max(dw, len(fmt.Sprintf("-%d", f.Deletions)))
	}
	minus := "−"
	if glyphs.Active() == glyphs.ASCII {
		minus = "-"
	}
	for i := 0; i < visible && first+i < n; i++ {
		idx := first + i
		f := m.files[idx]
		y := top + i
		sel := idx == m.fileSel
		if sel {
			c.Fill(y, 0, w, "sel", true, false)
		}
		kind := fileKind(f.Status)
		a, d := f.Additions, f.Deletions
		total := a + d
		cells := 5
		onA, onD := 0, 0
		if total > 0 {
			onA = int(float64(cells)*float64(a)/float64(total) + 0.5)
			onD = cells - onA
		}
		bar := []text.Seg{
			text.S(strings.Repeat(glyphs.G("bar_on"), onA), "bar.add"),
			text.S(strings.Repeat(glyphs.G("bar_on"), onD), "bar.del"),
			text.S(strings.Repeat(glyphs.G("bar_off"), cells-onA-onD), "bar.none"),
		}
		dir, base := splitPath(f.Filename)
		gut := " "
		if sel {
			gut = glyphs.G("sel")
		}
		addS, delS := "zero", "zero"
		if a > 0 {
			addS = "add"
		}
		if d > 0 {
			delS = "del"
		}
		segs := []text.Seg{text.S(gut, "gutter"), text.S(" ", "text"),
			text.S(glyphs.Ico("file."+kind), "file."+kind),
			text.S(text.Pad(f.Status, 9), "file."+kind),
			text.S(text.Pad(fmt.Sprintf("+%d", a), aw)+" ", addS),
			text.S(text.Pad(fmt.Sprintf("%s%d", minus, d), dw)+" ", delS)}
		segs = append(segs, bar...)
		segs = append(segs, text.S("  ", "text"), text.S(dir, "path.dir"), text.S(base, "path.base"))
		c.PutSegs(y, 0, segs, w-1, sel, false)
	}
	if n > visible {
		scenes.Scrollbar(c, top, visible, n, first, w-1)
	}
}

func fileKind(status string) string {
	switch status {
	case "added", "removed", "modified", "renamed":
		return status
	}
	return "other"
}

func splitPath(p string) (dir, base string) {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i+1], p[i+1:]
	}
	return "", p
}

// drawPatch paints one file's diff.
func (m *Model) drawPatch(c *canvas.Canvas, h, w int) {
	if m.patchStamp != (patchStamp{m.fileSel, w}) {
		m.buildPatchLines(max(20, w-3))
	}
	top := 2
	visible := max(1, h-4)
	m.patchScroll = max(0, min(m.patchScroll, max(0, len(m.patchLines)-visible)))
	m.drawLines(c, m.patchLines, m.glideTo("patch", m.patchScroll), top, visible, w)
}
