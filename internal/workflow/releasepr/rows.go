package releasepr

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// ---- classification ----

// Claims takes a core PR row out of the generic sections when the same
// PR is listed as a release (a review repo that is also a release repo).
func (w *Workflow) Claims(row workflow.Row) bool {
	if row.Kind != "pr" {
		return false
	}
	pr, ok := row.Item.(*model.PR)
	if !ok {
		return false
	}
	return w.Current().Find(pr.Ident()) != nil
}

// Classify is the chip of a release PR; nil for any other.
func (w *Workflow) Classify(pr, listed *model.PR, me string, localApproved bool) *workflow.Classification {
	if pr == nil {
		return nil
	}
	src := pr
	if InfoOf(src) == nil {
		if InfoOf(listed) != nil {
			src = listed
		} else if s := w.Current().Find(pr.Ident()); s != nil {
			src = s
		} else {
			return nil
		}
	}
	// the detail PR carries the fresh reviews; the listed row the kind
	label := w.labelWith(pr, InfoOf(src), me, localApproved)
	return &workflow.Classification{
		Label:    label,
		Style:    LabelStyle(label),
		NeedsYou: label == LabelNeeds,
		Done:     label == model.LabelApprovedMe || label == LabelOther || pr.IsDraft,
	}
}

// labelWith is Label with the kind read from info rather than the PR's
// own annotation.
func (w *Workflow) labelWith(pr *model.PR, info *Info, me string, local bool) string {
	if InfoOf(pr) != nil || info == nil {
		return w.Label(pr, me, local)
	}
	if info.Kind == KindOther && pr.State != "MERGED" {
		return LabelOther
	}
	return w.Label(pr, me, local)
}

// ChipStyles maps the workflow's labels to their styles.
func (w *Workflow) ChipStyles() map[string]string {
	return map[string]string{LabelNeeds: "chip.await", LabelOther: "chip.state"}
}

// NeedsYou counts "needs approval" rows in the summary.
func (w *Workflow) NeedsYou() []string { return []string{LabelNeeds} }

// SearchText adds the repo, the kind and "stage" to the filter's haystack.
func (w *Workflow) SearchText(row workflow.Row) string {
	switch row.Kind {
	case RowRelease:
		pr, ok := row.Item.(*model.PR)
		if !ok {
			return ""
		}
		kind := w.Kind(pr)
		if i := InfoOf(pr); i != nil && i.Kind != "" {
			kind = i.Kind
		}
		if p, ok := w.progressOf(pr); ok {
			kind += " " + p.Phase
		}
		return pr.Repo.Name + " " + kind
	case RowStage:
		s, ok := row.Item.(*Stage)
		if !ok {
			return ""
		}
		return "stage " + s.Repo.Name + " " + w.stageText(s)
	}
	return ""
}

// ---- the list ----

// queued reports a release queued for the window: by the conductor's
// queued label, or, with approval_queues_for_window, by its approving
// review (what the conductor's freeze picks up).
func (w *Workflow) queued(pr *model.PR) bool {
	if w.win == nil {
		return false
	}
	kind := w.Kind(pr)
	if i := InfoOf(pr); i != nil && i.Kind != "" {
		kind = i.Kind
	}
	return queuedFor(pr, kind, w.win.QueuedLabel, w.cfg.ApprovalQueuesForWindow, clock.Now()) && !w.inFlight(pr)
}

func matches(parts []string, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	hay := strings.ToLower(strings.Join(parts, " "))
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

func (w *Workflow) prParts(pr *model.PR) []string {
	return []string{fmt.Sprintf("#%d", pr.Number), pr.Title, pr.Author.Login,
		strings.Join(pr.Labels.Names(), " "), pr.Repo.Name,
		w.SearchText(workflow.Row{Kind: RowRelease, Item: pr})}
}

// Sections is the "releases (N)" section: per repo, the releases first,
// a PR into production from any other branch last (dimmed), then the
// stage row. A release queued for the window is left to the window
// workflow. N counts the releases proper.
func (w *Workflow) Sections(app workflow.App, terms []string, _ string) []workflow.Row {
	st := w.stateOf(app)
	if st == nil {
		return nil
	}
	byRepo := map[model.Repo][]*model.PR{}
	stages := map[model.Repo]*Stage{}
	count := 0
	for _, pr := range st.Releases {
		if w.queued(pr) {
			continue
		}
		if !matches(w.prParts(pr), terms) {
			continue
		}
		if !w.NotARelease(pr) {
			count++
		} else if !w.cfg.ListOtherPRs {
			continue // list_other_prs = false: only releases and hotfixes
		}
		byRepo[pr.Repo] = append(byRepo[pr.Repo], pr)
	}
	for _, s := range st.Stages {
		if !matches([]string{"stage", s.Repo.Name, w.stageText(s)}, terms) {
			continue
		}
		stages[s.Repo] = s
	}
	if len(byRepo) == 0 && len(stages) == 0 {
		return nil
	}
	set := map[model.Repo]bool{}
	for r := range byRepo {
		set[r] = true
	}
	for r := range stages {
		set[r] = true
	}
	repos := make([]model.Repo, 0, len(set))
	for r := range set {
		repos = append(repos, r)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	rows := []workflow.Row{{Kind: "header", Item: fmt.Sprintf("releases (%d)", count), Meta: map[string]any{"icon": "release"}}}
	for _, repo := range repos {
		mine := byRepo[repo]
		for _, pr := range mine {
			if !w.NotARelease(pr) {
				rows = append(rows, workflow.Row{Kind: RowRelease, Item: pr})
			}
		}
		for _, pr := range mine {
			if w.NotARelease(pr) {
				rows = append(rows, workflow.Row{Kind: RowRelease, Item: pr})
			}
		}
		if s := stages[repo]; s != nil {
			rows = append(rows, workflow.Row{Kind: RowStage, Item: s})
		}
	}
	return rows
}

// RowKinds renders the release and stage rows.
func (w *Workflow) RowKinds() map[string]workflow.RowKind {
	return map[string]workflow.RowKind{
		RowRelease: {
			Icon:   "release",
			Layout: w.releaseLayout,
			Segs:   w.releaseSegs,
			Live:   w.rowLive,
			Title: func(row workflow.Row) string {
				if pr, ok := row.Item.(*model.PR); ok {
					return pr.Title
				}
				return ""
			},
		},
		RowStage: {
			Icon:   "stage",
			Layout: w.stageLayout,
			Segs:   w.stageSegs,
			URL: func(app workflow.App, row workflow.Row) string {
				s, ok := row.Item.(*Stage)
				if !ok {
					return ""
				}
				return ghapi.CompareURL(app.Config().GitHub.Host, s.Repo, w.cfg.ProductionBranch, w.cfg.StagingBranch)
			},
			Open: func(app workflow.App, _ workflow.Row) bool {
				app.Notice(fmt.Sprintf("%s is a branch, not a PR — o opens its compare with %s on GitHub", w.cfg.StagingBranch, w.cfg.ProductionBranch))
				return true
			},
			Title: func(_ workflow.Row) string { return "stage" },
		},
	}
}

func maxw(items []workflow.Row, f func(workflow.Row) string, limit int) int {
	n := 0
	for _, it := range items {
		if wd := text.Width(f(it)); wd > n {
			n = wd
		}
	}
	return min(limit, n)
}

// titleRoom is what the columns leave the title.
func titleRoom(cols workflow.Layout, w, numW int) int {
	n := 4 + numW + 1 // gutter 2 + icon 2 + "#num "
	for name, v := range cols {
		if name != "num" {
			n += v + 1 // the column and its gap
		}
	}
	return w - n
}

// fit drops columns in order until the title keeps 24 cells.
func fit(cols workflow.Layout, w, numW int, dropOrder ...string) workflow.Layout {
	for _, name := range dropOrder {
		if titleRoom(cols, w, numW) >= 24 {
			break
		}
		delete(cols, name)
	}
	cols["num"] = numW
	return cols
}

func repoWidth(items []workflow.Row) int {
	n := 0
	for _, it := range items {
		var name string
		switch v := it.Item.(type) {
		case *model.PR:
			name = v.Repo.Name
		case *Stage:
			name = v.Repo.Name
		}
		if wd := text.Width(name); wd > n {
			n = wd
		}
	}
	return min(24, n)
}

// maxSegs is the widest rendering among the rows, capped.
func maxSegs(items []workflow.Row, f func(workflow.Row) []text.Seg, limit int) int {
	n := 0
	for _, r := range items {
		if wdt := text.SegsWidth(f(r)); wdt > n {
			n = wdt
		}
	}
	return min(limit, n)
}

func (w *Workflow) releaseLayout(app workflow.App, items []workflow.Row, width, numW, repoW int) workflow.Layout {
	if rw := repoWidth(items); rw > repoW {
		repoW = rw
	}
	me := app.Me()
	cols := workflow.Layout{
		"repo": repoW,
		"chip": maxw(items, func(r workflow.Row) string {
			pr := r.Item.(*model.PR)
			return w.Label(pr, me, app.LocalApproved(pr))
		}, 99) + 2,
		"kind": maxSegs(items, func(r workflow.Row) []text.Seg { return w.kindChip(app, r.Item.(*model.PR), -1, false) }, 99),
		"ci": maxw(items, func(r workflow.Row) string {
			c, _ := r.Item.(*model.PR).Get(ExtCI).(model.CICluster)
			return text.SegsText(ciSegs(c.Glyphs))
		}, 14),
		"author": maxw(items, func(r workflow.Row) string { return text.OrQ(r.Item.(*model.PR).Author.Login) }, 14),
		"age":    3,
	}
	// narrow: the repo goes last; the chip (needs approval) never. A
	// release in flight sheds its rail before its phase goes.
	cols = fit(cols, width, numW, "age", "author", "ci")
	if titleRoom(cols, width, numW) < 24 {
		cols["kind"] = maxSegs(items, func(r workflow.Row) []text.Seg { return w.kindChip(app, r.Item.(*model.PR), -1, true) }, 99)
	}
	return fit(cols, width, numW, "kind", "repo")
}

func (w *Workflow) stageLayout(_ workflow.App, items []workflow.Row, _, numW, repoW int) workflow.Layout {
	if rw := repoWidth(items); rw > repoW {
		repoW = rw
	}
	return workflow.Layout{"num": numW, "repo": repoW}
}

// kindChipText is the kind chip of a release row; a queued one shows
// its window status instead (the night sub-header already says where),
// a merged one its deploy.
func (w *Workflow) kindChipText(pr *model.PR) string {
	if w.queued(pr) {
		return "queued"
	}
	if pr.State == "MERGED" {
		if d, ok := deployOf(pr, nil); ok && d.Text != "" {
			return d.Text
		}
		return "merged"
	}
	if w.NotARelease(pr) {
		return "not a cut"
	}
	if i := InfoOf(pr); i != nil && i.Kind != "" {
		return i.Kind
	}
	return w.Kind(pr)
}

func (w *Workflow) kindChipStyle(pr *model.PR, ktext string) string {
	if pr.State == "MERGED" {
		if d, ok := deployOf(pr, nil); ok {
			if s, ok := deploys.Chip[d.Phase]; ok {
				return s
			}
		}
		return "chip.label"
	}
	switch {
	case strings.HasPrefix(ktext, "scheduled"):
		return "chip.sched"
	case w.Kind(pr) == KindHotfix, strings.HasPrefix(ktext, "rolled"), strings.HasPrefix(ktext, "awaiting"):
		return "chip.rolled"
	case w.queued(pr):
		return "chip.queued"
	}
	return "chip.label"
}

func authorStyle(app workflow.App, login string) string {
	l := strings.ToLower(login)
	if l == strings.ToLower(app.Me()) {
		return "author.me"
	}
	if cfg := app.Config(); cfg != nil && cfg.Bots.IsBot(l) {
		return "author.bot"
	}
	return "author"
}

var ciCount = regexp.MustCompile(`^\d+`)

var ciGlyph = map[byte][2]string{
	'+': {"pass", "ci.pass"}, 'x': {"fail", "ci.fail"}, '.': {"wait", "ci.wait"},
	'~': {"", "ci.run"}, '-': {"skip", "ci.skip"}, '?': {"unknown", "ci.unknown"},
}

// ciSegs renders CI cluster codes ('++~x', or compressed '9+1x2.') as
// styled glyphs; running checks get the spinner.
func ciSegs(codes string) []text.Seg {
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

func (w *Workflow) releaseSegs(app workflow.App, row workflow.Row, L workflow.Layout, width, pos int) []text.Seg {
	pr, ok := row.Item.(*model.PR)
	if !ok {
		return nil
	}
	me := app.Me()
	label := w.Label(pr, me, app.LocalApproved(pr))
	kind := w.Kind(pr)
	if i := InfoOf(pr); i != nil && i.Kind != "" {
		kind = i.Kind
	}
	icon := "release"
	if kind == KindHotfix {
		icon = "hotfix"
	}
	var segs []text.Seg
	if g := glyphs.G(icon); g != "" {
		segs = append(segs, text.S(g+" ", "icon.pr"))
	}
	numStyle := "num"
	if f := app.Fx(); f != nil {
		numStyle = scenes.NumStyle(f, fmt.Sprintf("%s#%d", pr.Repo.Name, pr.Number), "num")
	}
	segs = append(segs, text.S(text.Pad(fmt.Sprintf("#%d", pr.Number), L["num"])+" ", numStyle))
	if rw, ok := L["repo"]; ok {
		segs = append(segs, text.S(text.Pad(text.Ellip(pr.Repo.Name, rw), rw)+" ", "stack"))
	}
	mono := theme.Current() == theme.Mono
	segs = append(segs, text.PadSegs(scenes.GlintChip(label, LabelStyle(label), pos, fx.Ambient, mono), L["chip"])...)
	segs = append(segs, text.S(" ", "text"))
	if kw, ok := L["kind"]; ok {
		kc := w.kindChip(app, pr, pos, false)
		if text.SegsWidth(kc) > kw {
			kc = w.kindChip(app, pr, pos, true) // the narrow list's chip
		}
		segs = append(segs, text.PadSegs(kc, kw)...)
		segs = append(segs, text.S(" ", "text"))
	}
	c, _ := pr.Get(ExtCI).(model.CICluster)
	if cw, ok := L["ci"]; ok {
		segs = append(segs, text.PadSegs(ciSegs(c.Glyphs), cw)...)
		segs = append(segs, text.S(" ", "text"))
	}
	if aw, ok := L["author"]; ok {
		a := text.OrQ(pr.Author.Login)
		segs = append(segs, text.S(text.Pad(text.Ellip(a, aw), aw)+" ", authorStyle(app, a)))
	}
	if aw, ok := L["age"]; ok {
		age, st := clock.ShortAge(pr.UpdatedAt, app.Now())
		segs = append(segs, text.S(text.Pad(age, aw)+" ", st))
	}
	tstyle := "title"
	switch {
	case label == model.LabelApprovedMe || label == LabelOther || pr.IsDraft:
		tstyle = "title.done"
	case label == LabelNeeds:
		tstyle = "title.attn"
	}
	suffix := ""
	if len(c.Failed) > 0 {
		suffix = "  " + glyphs.G("fail") + " " + strings.Join(c.Failed, ", ")
	}
	room := width - 3 - text.SegsWidth(segs)
	sw := min(text.Width(suffix), max(0, room-16))
	segs = append(segs, text.S(text.Ellip(pr.Title, room-sw), tstyle))
	if suffix != "" {
		segs = append(segs, text.S(text.Ellip(suffix, sw), "failnames"))
	}
	return segs
}

func (w *Workflow) stageSegs(_ workflow.App, row workflow.Row, L workflow.Layout, width, _ int) []text.Seg {
	s, ok := row.Item.(*Stage)
	if !ok {
		return nil
	}
	var segs []text.Seg
	if g := glyphs.G("stage"); g != "" {
		segs = append(segs, text.S(g+" ", "icon.queued"))
	}
	segs = append(segs, text.S(text.Pad("stage", L["num"])+" ", "num.issue"))
	if rw, ok := L["repo"]; ok {
		segs = append(segs, text.S(text.Pad(text.Ellip(s.Repo.Name, rw), rw)+" ", "stack"))
	}
	room := width - 3 - text.SegsWidth(segs)
	txt := w.stageText(s)
	if s.Units == nil || s.Units.Units == 0 {
		return append(segs, text.S(text.Ellip(txt, room), "faint"))
	}
	style := "rev.ok"
	if s.Units.Hold > 0 || s.Units.Untagged > 0 {
		style = "rev.attend"
	}
	return append(segs, text.S(text.Ellip(txt, room), style))
}

// SummarySegs: "N releases need approval", "N deploys wait on you (D)",
// "N deploys stuck or failed".
func (w *Workflow) SummarySegs(app workflow.App) []text.Seg {
	st := w.stateOf(app)
	me := strings.ToLower(app.Me())
	need, waits, stuck := 0, 0, 0
	if st != nil {
		for _, pr := range st.Releases {
			if !w.queued(pr) && !pr.IsDraft && strings.ToLower(pr.Author.Login) != me &&
				w.Label(pr, app.Me(), app.LocalApproved(pr)) == LabelNeeds {
				need++
			}
			if d, ok := deployOf(pr, nil); ok {
				mine := false
				for _, wt := range d.Waits {
					if wt.Mine {
						mine = true
						break
					}
				}
				if mine {
					waits++
				}
				if d.Phase == "stuck" || d.Phase == "failed" {
					stuck++
				}
			}
		}
	}
	sep := text.S("  "+glyphs.G("sep")+"  ", "rule")
	attn := func(n int, yes string) string {
		if n > 0 {
			return yes
		}
		return "sum.muted"
	}
	segs := []text.Seg{text.S(fmt.Sprintf("%s%d release%s need%s approval", glyphs.Ico("release"), need, plural(need, "", "s"), plural(need, "s", "")), attn(need, "sum.attn"))}
	if waits > 0 {
		segs = append(segs, sep, text.S(fmt.Sprintf("%s%d deploy%s wait%s on you (D)", glyphs.Ico("release"), waits, plural(waits, "", "s"), plural(waits, "s", "")), "sum.attn"))
	}
	if stuck > 0 {
		segs = append(segs, sep, text.S(fmt.Sprintf("%s%d deploy%s stuck or failed", glyphs.Ico("fail"), stuck, plural(stuck, "", "s")), "sum.fail"))
	}
	return segs
}

// HelpRows are the workflow's help lines.
func (w *Workflow) HelpRows() [][2]string {
	return [][2]string{
		{"releases", fmt.Sprintf("a  on an app release PR: approve (queues it for the window) or approve + %s", w.cfg.ReleaseNowCommand)},
		{"", "M  merge an APPROVED release at its slot (= the prod deploy)   D  approve its deployments (row or detail)"},
		{"", "a release in flight shows its rail on the row and its steps, clocks and run in the detail; it says when it lands"},
	}
}

// LegendRows adds the release chips to the help legend.
func (w *Workflow) LegendRows() [][]text.Seg {
	sp := text.S("  ", "text")
	row := []text.Seg{text.S(text.Pad("releases", 10), "section")}
	row = append(row, scenes.Chip(LabelNeeds, "chip.await")...)
	row = append(row, sp)
	row = append(row, scenes.Chip("hotfix", "chip.rolled")...)
	row = append(row, sp)
	row = append(row, scenes.Chip(LabelOther, "chip.state")...)
	row = append(row, sp)
	row = append(row, scenes.Chip("approve: prod", "chip.await")...)
	row = append(row, text.S(" a deploy waits on you (D)", "muted"))
	rail := []text.Seg{text.S(text.Pad("", 10), "section")}
	deploying := Progress{Phase: PhaseDeploying, DeployDone: 3, DeployTotal: 9}
	rail = append(rail, text.S(glyphs.G("capl"), "cap:chip.queued"))
	rail = append(rail, railSegs(deploying, time.Time{}, nil, "", railCells, false)...)
	rail = append(rail, text.S(" deploying 3/9 · 2m", "chip.queued"), text.S(glyphs.G("capr"), "cap:chip.queued"))
	rail = append(rail, text.S(" a release in flight: its rail fills from setup to finishing", "muted"))
	rail = append(rail, sp)
	rail = append(rail, scenes.Chip("released", "chip.approved")...)
	rail = append(rail, sp)
	rail = append(rail, scenes.Chip("release FAILED", "chip.rolled")...)
	return [][]text.Seg{row, rail}
}

// FortressLaneLabel names a release repo's lane.
func (w *Workflow) FortressLaneLabel(repo model.Repo) string {
	st := w.Current()
	if st == nil {
		return ""
	}
	for _, r := range st.Repos {
		if r == repo {
			return repo.Name
		}
	}
	return ""
}

// ---- the detail card ----

// DetailCards is a release PR's card: its kind, branches, window state,
// what the a key does, and the deploy runs once it is merged.
func (w *Workflow) DetailCards(app workflow.App, pr, listed *model.PR, width int) []text.Line {
	st := w.stateOf(app)
	info := w.infoFor(pr, listed, st)
	if info == nil {
		return nil
	}
	if listed == nil && st != nil {
		listed = st.Find(pr.Ident())
	}
	inner := max(20, width-4)
	kind := info.Kind
	if kind == "" {
		kind = w.Kind(pr)
	}
	out := []text.Line{scenes.CardTop(glyphs.Ico("release")+"release", width)}
	icon := "release"
	kstyle, ktext := "chip.label", kind
	switch kind {
	case KindHotfix:
		icon, kstyle = "hotfix", "chip.rolled"
	case KindOther:
		kstyle, ktext = "chip.state", LabelOther
	}
	segs := []text.Seg{text.S(glyphs.Ico(icon), "plan.live")}
	segs = append(segs, scenes.Chip(ktext, kstyle)...)
	base := pr.BaseRefName
	if base == "" {
		base = w.cfg.ProductionBranch
	}
	segs = append(segs, text.S("  ", "text"), text.S(text.OrQ(pr.HeadRefName), "md.inline"),
		text.S(" "+glyphs.G("arrow")+" ", "faint"), text.S(base, "muted"))
	src := listed
	if src == nil {
		src = pr
	}
	if w.queued(src) {
		segs = append(segs, text.S("  ", "text"))
		segs = append(segs, scenes.Chip("queued", "chip.queued")...)
	}
	out = append(out, scenes.CardLines(segs, width, inner)...)
	merged := src.State == "MERGED" || pr.State == "MERGED"
	cmd := w.cfg.ReleaseNowCommand
	var rule string
	switch {
	case merged:
		rule = fmt.Sprintf("merged: D approves the environment deployments waiting on its merge commit; dillodash refreshes every %ds while one waits or runs and says when the next one waits", int(w.refreshEvery().Seconds()))
	case kind == KindHotfix:
		rule = fmt.Sprintf("a hotfix is an emergency by definition: a approves and posts %s with a reason", cmd)
	case kind == KindOther:
		rule = fmt.Sprintf("only %s<date> (the go-tagged cut on %s) and %s<slug> ship to %s; a, M and D refuse anything else",
			w.cfg.ReleasePrefix, w.cfg.StagingBranch, w.cfg.HotfixPrefix, w.cfg.ProductionBranch)
	case w.cfg.ApprovalQueuesForWindow && w.win != nil:
		rule = fmt.Sprintf("a: an approving review queues it for %s window; %s \"<reason>\" releases it now; M merges it at its slot (the deploy); D approves the deployments waiting on the merge",
			w.NightPhrase(app.Now()), cmd)
	default:
		rule = fmt.Sprintf("a: an approving review lets M merge it (the deploy); %s \"<reason>\" marks an emergency release; D approves the deployments waiting on the merge", cmd)
	}
	out = append(out, scenes.CardLines([]text.Seg{text.S(glyphs.Ico("info"), "muted"), text.S(rule, "muted")}, width, inner)...)
	if p, ok := w.progressOf(src); ok {
		out = append(out, w.progressCard(app, src, p, width, inner)...)
	}
	if merged {
		sha := MergeSHA(src)
		if sha == "" {
			sha = MergeSHA(pr)
		}
		sha = model.ShortSHA(sha)
		dep, ok := deployOf(pr, listed)
		if !ok {
			out = append(out, scenes.CardLines([]text.Seg{text.S(fmt.Sprintf("deploys on %s: not read yet (D reads them now)", sha), "faint")}, width, inner)...)
		} else {
			style := deploys.Chip[dep.Phase]
			if style == "" {
				style = "chip.label"
			}
			line := []text.Seg{text.S(fmt.Sprintf("deploys on %s  ", sha), "muted")}
			line = append(line, scenes.Chip(dep.Text, style)...)
			out = append(out, scenes.CardLines(line, width, inner)...)
			dot := "  * "
			if g := glyphs.G("live"); g != "" {
				dot = "  " + g + " "
			}
			var stuck []string
			for _, r := range dep.Runs {
				rl := []text.Seg{text.S(dot, "faint"), text.S(r.Name+"  ", "text")}
				rl = append(rl, scenes.Chip(r.What, r.Style)...)
				out = append(out, scenes.CardLines(rl, width, inner)...)
				if strings.HasSuffix(r.What, "stuck?") {
					stuck = append(stuck, fmt.Sprint(r.RunID))
				}
			}
			if len(stuck) > 0 {
				mins := int(w.deploy.Stuck().Minutes()) // the threshold detection used
				out = append(out, scenes.CardLines([]text.Seg{text.S(glyphs.Ico("warn"), "toast.warn"),
					text.S(fmt.Sprintf("run %s has gone longer than %d minutes: check it in %s's Actions and cancel + re-run it if it hangs",
						strings.Join(stuck, ", "), mins, pr.Repo.Name), "toast.warn")}, width, inner)...)
			}
		}
	}
	out = append(out, scenes.CardBottom(width))
	return out
}
