package maintenancewindow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// Name is the workflow's config table name.
const Name = "maintenance_window"

// ReleaseState is the state name of the release workflow, whose state
// may implement workflow.QueuedProvider (queued releases carry the
// queued label but no queue marker of their own).
const ReleaseState = "release_pr"

// RowKind is the row kind of a queued PR.
const RowKind = "queued"

// Workflow is the maintenance-window workflow.
type Workflow struct {
	workflow.Base
	cfg *config.MaintenanceWindowConfig
	ta  *config.TerraformApproveConfig
	// relQueued remembers, from the last Apply, whether the release
	// workflow had queued releases: the worker then fetches the
	// manifests even when no review PR is queued.
	relQueued atomic.Bool
}

// State is what a refresh computes: the open manifests by night, their
// issues (with comments) and the queued PRs' annotations. Nights is
// filled on Apply, where the viewer's login is known.
type State struct {
	Manifests map[string]*Manifest
	Nights    map[string]*Night
	Issues    []*model.Issue
	infos     map[model.Ident]*Info
}

func init() {
	workflow.Register(Name, func(ctx *workflow.Context) (workflow.Workflow, error) {
		if ctx.Config == nil || ctx.Config.MaintenanceWindow == nil {
			return nil, nil
		}
		return New(ctx.Config.MaintenanceWindow, ctx.Config.TerraformApprove), nil
	})
}

// New builds the workflow; ta is the terraform workflow's config (its
// plan parser tells a queued plan's fate), the defaults when nil.
func New(cfg *config.MaintenanceWindowConfig, ta *config.TerraformApproveConfig) *Workflow {
	if ta == nil {
		ta = config.DefaultTerraformApprove()
	}
	return &Workflow{cfg: cfg, ta: ta}
}

// Name is "maintenance_window".
func (w *Workflow) Name() string { return Name }

// Order places the workflow after the terraform one.
func (w *Workflow) Order() int { return 30 }

// Config is the workflow's config.
func (w *Workflow) Config() *config.MaintenanceWindowConfig { return w.cfg }

// BotLogins names the conductor.
func (w *Workflow) BotLogins() []string { return []string{w.cfg.ConductorBot} }

// QueueNightPhrase is whose window a plain approve queues for right
// now ("tonight's" / "tomorrow night's"); other workflows find it on the
// window workflow through workflow.Context.Get(Name).
func (w *Workflow) QueueNightPhrase(now time.Time) string { return QueueNightPhrase(now, w.cfg) }

// InReleaseWindow reports whether now is inside the release span.
func (w *Workflow) InReleaseWindow(now time.Time) bool { return InReleaseWindow(now, w.cfg) }

// manifestRepo is where the manifests live: the config's, else the first
// review repo.
func (w *Workflow) manifestRepo(repos []model.Repo) model.Repo {
	if !w.cfg.ManifestRepo.IsZero() {
		return w.cfg.ManifestRepo
	}
	if len(repos) > 0 {
		return repos[0]
	}
	return model.Repo{}
}

// ---- refresh ----

// Refresh fetches the open manifests and each queued PR's comments, when
// some PR (or a queued release) carries the queued label. Best-effort: a
// failure leaves the queue section showing labels only.
func (w *Workflow) Refresh(ctx context.Context, gh ghapi.Client, repos []model.Repo, prs []*model.PR, issues []*model.Issue) (workflow.State, []string, error) {
	var queued []*model.PR
	for _, p := range prs {
		if HasQueuedLabel(p, w.cfg) {
			queued = append(queued, p)
		}
	}
	if len(queued) == 0 && !w.relQueued.Load() {
		return &State{}, nil, nil
	}
	st := &State{Manifests: map[string]*Manifest{}, infos: map[model.Ident]*Info{}}
	var warns []string
	repo := w.manifestRepo(repos)
	if !repo.IsZero() {
		labelled, err := gh.IssuesWithLabel(ctx, repo, w.cfg.ManifestLabel, 10)
		if err != nil {
			warns = append(warns, "maintenance window: manifests not read: "+err.Error())
			if ghapi.IsRateLimit(err) {
				return st, warns, err
			}
		} else {
			st.Issues = labelled
			st.Manifests = ParseManifests(labelled, w.cfg)
		}
	}
	for _, p := range queued {
		comments, err := gh.PRComments(ctx, p.Repo, p.Number)
		if err != nil {
			if ghapi.IsRateLimit(err) {
				return st, warns, err
			}
			warns = append(warns, fmt.Sprintf("maintenance window: %s#%d comments not read: %s", p.Repo.Name, p.Number, err))
			continue
		}
		marker := ParseMarker(comments, w.cfg)
		fate := QueuedPlanState(marker, comments, p, w.ta)
		st.infos[p.Ident()] = &Info{Marker: marker, Stack: LatestPlanStack(comments, marker),
			Stale: fate != FateQueued, Reapprove: fate == FateReplanned}
	}
	return st, warns, nil
}

// Annotate attaches each queued PR's Info (pr.Get("window")).
func (w *Workflow) Annotate(prs []*model.PR, issues []*model.Issue, s workflow.State) {
	st, _ := s.(*State)
	if st == nil {
		return
	}
	for _, p := range prs {
		if info, ok := st.infos[p.Ident()]; ok {
			p.Set(Ext, info)
		}
	}
}

// Apply stores the state and reads the nights' attendance for the viewer.
func (w *Workflow) Apply(app workflow.App, s workflow.State) {
	st, _ := s.(*State)
	if st == nil {
		st = &State{}
	}
	if st.Manifests == nil {
		st.Manifests = map[string]*Manifest{}
	}
	st.Nights = WindowNights(st.Issues, app.Me(), w.cfg)
	app.SetState(Name, st)
}

// AfterApply notes whether the release workflow holds queued or
// scheduled releases: the manifests are read while it does.
func (w *Workflow) AfterApply(app workflow.App) {
	scheduled := 0
	if sp, ok := app.State(ReleaseState).(workflow.ScheduledProvider); ok && sp != nil {
		scheduled = len(sp.ScheduledForWindow())
	}
	w.relQueued.Store(len(w.queuedReleases(app)) > 0 || scheduled > 0)
}

func (w *Workflow) state(app workflow.App) *State {
	st, _ := app.State(Name).(*State)
	if st == nil {
		return &State{Manifests: map[string]*Manifest{}, Nights: map[string]*Night{}}
	}
	return st
}

// queuedReleases are the release workflow's PRs queued for the window:
// one carrying the queued label must still be queued (its plan alive);
// one the release workflow queues by its approving review (the
// conductor labels nothing before the freeze) is taken at its word.
func (w *Workflow) queuedReleases(app workflow.App) []*model.PR {
	qp, ok := app.State(ReleaseState).(workflow.QueuedProvider)
	if !ok || qp == nil {
		return nil
	}
	var out []*model.PR
	for _, p := range qp.QueuedForWindow() {
		if !HasQueuedLabel(p, w.cfg) || IsQueued(p, w.cfg) {
			out = append(out, p)
		}
	}
	return out
}

// ---- classification ----

// Claims takes queued PRs and manifest issues out of the core sections.
func (w *Workflow) Claims(row workflow.Row) bool {
	switch v := row.Item.(type) {
	case *model.PR:
		return row.Kind == "pr" && IsQueued(v, w.cfg)
	case *model.Issue:
		return row.Kind == "issue" && ManifestIssueNight(v, w.cfg) != ""
	}
	return false
}

// SearchText is the stack of a queued row.
func (w *Workflow) SearchText(row workflow.Row) string {
	if pr, ok := row.Item.(*model.PR); ok {
		if info := InfoOf(pr); info != nil {
			return info.Stack
		}
	}
	return ""
}

// matches: every term appears in the PR's number, title, author, labels,
// repo or stack (case-insensitive).
func (w *Workflow) matches(pr *model.PR, terms []string) bool {
	parts := []string{fmt.Sprintf("#%d", pr.Number), pr.Title, pr.Author.Login,
		strings.Join(pr.Labels.Names(), " "), pr.Repo.Name}
	if info := InfoOf(pr); info != nil {
		parts = append(parts, info.Stack)
	}
	return termsIn(terms, parts)
}

func (w *Workflow) matchesIssue(i *model.Issue, terms []string) bool {
	return termsIn(terms, []string{fmt.Sprintf("#%d", i.Number), i.Title, i.Author.Login,
		strings.Join(i.Labels.Names(), " "), i.Repo.Name})
}

func termsIn(terms, parts []string) bool {
	hay := strings.ToLower(strings.Join(parts, " "))
	for _, t := range terms {
		if !strings.Contains(hay, strings.ToLower(t)) {
			return false
		}
	}
	return true
}

// ---- list ----

// Sections is the queued-for-the-window section: the review repos'
// queued PRs and the release workflow's queued releases under their
// nights, each night's manifest issue first.
func (w *Workflow) Sections(app workflow.App, terms []string, me string) []workflow.Row {
	st := w.state(app)
	var queued []*model.PR
	for _, p := range app.AllPRs() {
		if IsQueued(p, w.cfg) && w.matches(p, terms) {
			queued = append(queued, p)
		}
	}
	var rels []*model.PR
	for _, p := range w.queuedReleases(app) {
		if w.matches(p, terms) {
			rels = append(rels, p)
		}
	}
	var manifests []*model.Issue
	for _, i := range app.Issues() {
		if ManifestIssueNight(i, w.cfg) != "" {
			manifests = append(manifests, i)
		}
	}
	return QueueRows(queued, rels, st.Manifests, app.Now(), manifests,
		func(i *model.Issue) bool { return w.matchesIssue(i, terms) }, w.cfg)
}

// revText is a queued row's revertibility column.
func revText(pr *model.PR) string {
	info := InfoOf(pr)
	if info == nil || info.Marker == nil || info.Marker.Revertible == nil {
		return ""
	}
	if *info.Marker.Revertible {
		return glyphs.Ico("rev") + "revertible"
	}
	return glyphs.Ico("attend") + "attended night needed"
}

func statusOf(pr *model.PR) string {
	if info := InfoOf(pr); info != nil && info.Status != "" {
		return text.Ellip(info.Status, 30)
	}
	return "queued"
}

func stackOf(pr *model.PR) string {
	if info := InfoOf(pr); info != nil && info.Stack != "" {
		return info.Stack
	}
	return "?"
}

// fit drops columns in order until the title keeps 24 cells.
func fit(cols workflow.Layout, w, numW int, dropOrder ...string) workflow.Layout {
	fixed := func() int {
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

// RowKinds defines the "queued" row: chip, stack, revertibility, title.
func (w *Workflow) RowKinds() map[string]workflow.RowKind {
	return map[string]workflow.RowKind{RowKind: {
		Icon: "queued",
		Layout: func(app workflow.App, items []workflow.Row, width, numW, repoW int) workflow.Layout {
			chipW, stackW, revW := 0, 0, 0
			for _, r := range items {
				pr := r.Item.(*model.PR)
				chipW = max(chipW, text.Width(statusOf(pr)))
				stackW = max(stackW, text.Width(stackOf(pr)))
				revW = max(revW, text.Width(revText(pr)))
			}
			cols := workflow.Layout{"chip": chipW + 2, "stack": min(34, stackW), "rev": revW}
			return fit(cols, width, numW, "rev", "stack")
		},
		Segs: func(app workflow.App, row workflow.Row, L workflow.Layout, width, pos int) []text.Seg {
			pr := row.Item.(*model.PR)
			status := statusOf(pr)
			// gold: rolled, or held until an attendee posts the release
			cstyle := "chip.queued"
			switch {
			case strings.HasPrefix(status, "scheduled"):
				cstyle = "chip.sched"
			case strings.HasPrefix(status, "rolled"), strings.HasPrefix(status, "awaiting "+w.cfg.ReleaseCommand):
				cstyle = "chip.rolled"
			}
			var segs []text.Seg
			if g := glyphs.G("queued"); g != "" {
				segs = append(segs, text.S(g+" ", "icon.queued"))
			}
			segs = append(segs, text.S(text.Pad(model.Ref(pr.Repo, pr.Number, app.MultiRepo()), L["num"])+" ", "num"))
			segs = append(segs, text.PadSegs(scenes.Chip(status, cstyle), L["chip"])...)
			segs = append(segs, text.S(" ", "text"))
			if sw, ok := L["stack"]; ok {
				segs = append(segs, text.S(text.Pad(text.Ellip(stackOf(pr), sw), sw)+" ", "stack"))
			}
			if rw, ok := L["rev"]; ok {
				style := "rev.ok"
				if info := InfoOf(pr); info != nil && info.Marker != nil && info.Marker.Revertible != nil && !*info.Marker.Revertible {
					style = "rev.attend"
				}
				segs = append(segs, text.S(text.Pad(revText(pr), rw)+" ", style))
			}
			room := width - 3 - text.SegsWidth(segs)
			return append(segs, text.S(text.Ellip(pr.Title, room), "title"))
		},
		Title: func(row workflow.Row) string {
			if pr, ok := row.Item.(*model.PR); ok {
				return pr.Title
			}
			return ""
		},
	}}
}

// HeaderExtra gives a night sub-header its countdown to the window's
// start and the attendance: who attends, who checked in, and gold when
// someone has to act for a not-revertible item.
func (w *Workflow) HeaderExtra(app workflow.App, row workflow.Row, width int) (string, []text.Seg) {
	night, _ := row.Meta[workflow.NightRow].(string)
	if row.Kind != "header" || night == "" {
		return "", nil
	}
	now := app.Now()
	extra := ""
	if cd := Countdown(night, now, w.cfg); cd != "" {
		extra = "  " + glyphs.G("sep") + " " + cd
	}
	return extra, w.NightSegs(app, night, now)
}

// NightSegs is what a night sub-header adds after its label.
func (w *Workflow) NightSegs(app workflow.App, night string, now time.Time) []text.Seg {
	info := w.state(app).Nights[night]
	if info == nil {
		return nil
	}
	local := w.cfg.Local()
	needs := false
	for _, it := range info.Items {
		if it.Revertible != nil && !*it.Revertible && (it.Flag == "scheduled" || it.Flag == "attended-only") {
			needs = true
		}
	}
	if !needs {
		for _, p := range app.AllPRs() {
			i := InfoOf(p)
			if i != nil && i.Night == night && IsQueued(p, w.cfg) && i.Marker != nil && i.Marker.Revertible != nil && !*i.Marker.Revertible {
				needs = true
				break
			}
		}
	}
	start, until := ReleaseSpan(info, night, w.cfg)
	segs := []text.Seg{text.S(fmt.Sprintf("  #%d", info.Issue), "faint")}
	live := info.Live()
	switch {
	case len(info.CheckedIn) > 0:
		var who []string
		for _, c := range info.CheckedIn {
			who = append(who, "@"+c.Login)
		}
		segs = append(segs, text.S("  checked in: "+strings.Join(who, ", "), "sum.info"))
	case len(info.Attendees) > 0:
		if needs && live && !now.Before(start) {
			// the action first: a narrow screen clips from the right
			s := "  " + w.cfg.ReleaseCommand + " now (w)"
			if !until.IsZero() {
				s += " by " + local.TAt(until)
			}
			segs = append(segs, text.S(s, "sum.attn"))
		}
		segs = append(segs, text.S("  attending: "+mentions(info.Attendees), "sum.info"))
	case needs && live:
		cutoff, _ := WallToUTC(night, w.cfg.Cutoff, w.cfg)
		if now.Before(cutoff) {
			segs = append(segs, text.S(fmt.Sprintf("  needs %s (w) by %s", w.cfg.AttendCommand, local.T(w.cfg.Cutoff)), "sum.attn"))
		} else {
			segs = append(segs, text.S("  nobody attending: not-revertible items roll", "sum.attn"))
		}
	}
	return segs
}

// SummarySegs is the "N queued tonight" chip.
func (w *Workflow) SummarySegs(app workflow.App) []text.Seg {
	today := fmtNight(w.cfg.Local().Today(app.Now()))
	tonight, in := 0, false
	for _, r := range app.Rows() {
		if r.Kind == "header" {
			n, _ := r.Meta[workflow.NightRow].(string)
			in = n == today
		} else if in && (r.Kind == RowKind || r.Kind == "release") {
			tonight++
		}
	}
	style := "sum.muted"
	if tonight > 0 {
		style = "sum.info"
	}
	return []text.Seg{text.S(fmt.Sprintf("%s%d queued tonight", glyphs.Ico("window"), tonight), style)}
}

// ---- the w key ----

// Keys binds w in the list and the detail view.
func (w *Workflow) Keys() []workflow.Key {
	return []workflow.Key{
		{View: "list", Ch: "w", Label: "window", Priority: 50, Handler: w.DoWindow},
		{View: "detail", Ch: "w", Label: "window", Priority: 50, Handler: w.DoWindow},
	}
}

// Target is (night, queued PR or nil) for the w key: the selected or
// open PR's night when it is queued for a window, a manifest row's
// night, else the next night that has a manifest issue.
func (w *Workflow) Target(app workflow.App) (string, *model.PR) {
	st := w.state(app)
	var pr *model.PR
	night := ""
	if app.CurrentView() == "list" {
		if row, ok := app.Selected(); ok {
			switch v := row.Item.(type) {
			case *model.PR:
				if row.Kind == RowKind || (row.Kind == "release" && IsQueued(v, w.cfg)) {
					pr = v
				}
			case *model.Issue:
				night, _ = row.Meta[workflow.NightRow].(string)
			}
		}
	} else {
		switch d := app.Detail().(type) {
		case *model.PR:
			for _, p := range append(append([]*model.PR{}, app.AllPRs()...), w.queuedReleases(app)...) {
				if p.Ident() == d.Ident() && IsQueued(p, w.cfg) {
					pr = p
					break
				}
			}
		case *model.Issue:
			for n, info := range st.Nights {
				if info.Issue == d.Number {
					night = n
					break
				}
			}
		}
	}
	if night == "" && pr != nil {
		if info := InfoOf(pr); info != nil {
			night = info.Night
		}
	}
	if night == "" {
		today := fmtNight(w.cfg.Local().Today(app.Now()))
		var ahead []string
		for n := range st.Nights {
			if n >= today {
				ahead = append(ahead, n)
			}
		}
		sort.Strings(ahead)
		if len(ahead) > 0 {
			night = ahead[0]
		}
	}
	return night, pr
}

// CancelText is the cancel command for a PR: "#N" for a PR of the
// manifest repo; for a release of another repo the manifest's own
// rel:<repo>#<pr> id.
func (w *Workflow) CancelText(pr *model.PR, item *Item) string {
	if r := prRef(pr, w.cfg); r.repo != "" {
		iid := ""
		if item != nil {
			iid = item.ID
		}
		if !strings.HasPrefix(iid, "rel:") {
			iid = fmt.Sprintf("rel:%s#%d", r.repo, pr.Number)
		}
		return w.cfg.CancelCommand + " " + iid
	}
	return fmt.Sprintf("%s #%d", w.cfg.CancelCommand, pr.Number)
}

// DoWindow is the w key: attend, release or cancel on the night's
// manifest issue, or open it. Each command is checked against the
// conductor's rules first, confirmed, then posted as a plain comment
// for the conductor to judge.
func (w *Workflow) DoWindow(app workflow.App) {
	st := w.state(app)
	night, pr := w.Target(app)
	var info *Night
	if night != "" {
		info = st.Nights[night]
	}
	if info == nil {
		if night == "" {
			app.Notice("nothing is queued for a maintenance window")
		} else {
			app.Notice(fmt.Sprintf("no manifest issue for the night of %s yet (r refreshes)", night))
		}
		return
	}
	me := app.Me()
	now := app.Now()
	local := w.cfg.Local()
	var item *Item
	if pr != nil {
		want := prRef(pr, w.cfg)
		for k := range info.Items {
			if itemRef(info.Items[k]) == want {
				item = &info.Items[k]
				break
			}
		}
	}
	type cmd struct{ name, text string }
	cmds := []cmd{{CmdAttend, w.cfg.AttendCommand}, {CmdRelease, w.cfg.ReleaseCommand}}
	if pr != nil {
		cmds = append(cmds, cmd{CmdCancel, w.CancelText(pr, item)})
	}
	var labels []string
	for _, c := range cmds {
		ok, why := CommandCheck(c.name, info, night, me, now, item, w.cfg)
		t := c.text
		if !ok {
			t += " (not now)"
		}
		labels = append(labels, fmt.Sprintf("%-22s %s", t, why))
	}
	labels = append(labels, fmt.Sprintf("open manifest #%d", info.Issue))
	title := fmt.Sprintf("window %s %s, manifest #%d: %s", night, local.T(w.cfg.Start), info.Issue, AttendanceText(info, w.cfg))
	if w.cfg.Team != "" {
		title += fmt.Sprintf(" (commands count only from @%s)", w.cfg.Team)
	}
	i := app.Pick(title, labels)
	if i < 0 || i > len(cmds) {
		app.Notice("window: nothing posted")
		return
	}
	repo := w.manifestRepo(app.Repos())
	if i == len(cmds) {
		app.OpenDetail(repo, info.Issue, "issue", nil)
		return
	}
	c := cmds[i]
	// the clock moved while the menu was up: check again
	if ok, why := CommandCheck(c.name, info, night, me, app.Now(), item, w.cfg); !ok {
		app.Notice(fmt.Sprintf("%s not posted: %s", c.text, why))
		return
	}
	if !app.Confirm(fmt.Sprintf("post %s on manifest #%d (window %s %s)?", c.text, info.Issue, night, local.T(w.cfg.Start))) {
		app.Notice(c.text + " not posted")
		return
	}
	if err := app.GH().Comment(app.Context(), repo, info.Issue, c.text); err != nil {
		app.Fail(fmt.Sprintf("%s NOT posted: %s", c.text, err))
		return
	}
	if c.name != CmdCancel {
		info.Mine.set(c.name, StatePosted)
	}
	app.OK(fmt.Sprintf("posted %s on #%d; the conductor replies there within a minute", c.text, info.Issue))
	if c.name == CmdRelease {
		app.Launch()
	} else {
		app.Celebrate(false)
	}
	app.Refresh(true)
	// the conductor's reply lands ~30-60 s later: fetch it then, not at
	// the next refresh tick
	app.RefreshSoon(45 * time.Second)
}

// ---- help, legend ----

// HelpRows describes the w key.
func (w *Workflow) HelpRows() [][2]string {
	return [][2]string{{"window", fmt.Sprintf("w  %s, %s or %s on the night's manifest (list or a PR)",
		w.cfg.AttendCommand, w.cfg.ReleaseCommand, w.cfg.CancelCommand)}}
}

// LegendRows shows the window chips.
func (w *Workflow) LegendRows() [][]text.Seg {
	sp := text.S("  ", "text")
	row := []text.Seg{text.S(text.Pad("window", 10), "section")}
	row = append(row, scenes.Chip("queued", "chip.queued")...)
	row = append(row, sp)
	row = append(row, scenes.Chip("scheduled "+w.cfg.Local().T(w.cfg.Start), "chip.sched")...)
	row = append(row, sp)
	row = append(row, scenes.Chip("rolled", "chip.rolled")...)
	row = append(row, sp, text.S(glyphs.Ico("rev")+"revertible", "rev.ok"), sp,
		text.S(glyphs.Ico("attend")+"attended night needed", "rev.attend"))
	return [][]text.Seg{row}
}
