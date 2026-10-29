// Package waitingruns lists the Actions runs that wait on an environment
// approval and hang off no PR the dashboard lists: a hand-dispatched
// production deploy, an ops workflow run from main. The core's D finds
// runs only through a merged PR's merge commit, so those stayed invisible
// until someone opened the Actions page.
//
// The workflow adds a "waiting approvals (N)" section with one row per
// run (its workflow, title, branch, who started it, its age and a chip
// per environment), counts the runs in the header summary, and answers D
// on its rows: it approves the environments the viewer is a reviewer of,
// with approval_comment.
package waitingruns

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// Name is the workflow's config table and state name.
const Name = "waiting_runs"

// RowRun is the row kind of a waiting run.
const RowRun = "waiting_run"

// releaseName is the release workflow's state name. Its state lists the
// merged releases (through workflow.CIProvider) whose deploys D already
// approves from their own row; this package does not import it.
const releaseName = "release_pr"

// Item is one run waiting on environments: where it runs, the run, and
// the environments it waits on (nil when they couldn't be read).
type Item struct {
	Repo    model.Repo
	Run     model.WorkflowRun
	Pending []model.PendingDeployment
}

// Ident is (repo, run id): a run has no number, so its id stands in for
// one. Run ids are far above any PR or issue number, so the two never
// meet; on a 32-bit build the id wraps, which at worst moves the cursor
// between two runs whose ids collide.
func (it *Item) Ident() model.Ident { return model.Ident{Repo: it.Repo, Number: int(it.Run.ID)} }

// Name is the run's workflow name, "?" when GitHub gave none.
func (it *Item) Name() string { return text.OrQ(it.Run.Name) }

// Title is the run's display title (its run-name), else the workflow name.
func (it *Item) Title() string {
	if it.Run.DisplayTitle != "" {
		return it.Run.DisplayTitle
	}
	return it.Name()
}

// Mine is the pending deployments the viewer can approve.
func (it *Item) Mine() []model.PendingDeployment {
	var out []model.PendingDeployment
	for _, p := range it.Pending {
		if p.CurrentUserCanApprove {
			out = append(out, p)
		}
	}
	return out
}

// Envs is every environment name the run waits on, in order.
func (it *Item) Envs() []string {
	out := make([]string, 0, len(it.Pending))
	for _, p := range it.Pending {
		out = append(out, envName(p))
	}
	return out
}

func envName(p model.PendingDeployment) string { return text.OrQ(p.Environment.Name) }

// where names the run in status text: "infra deploy run 4242".
func (it *Item) where() string {
	return fmt.Sprintf("%s %s run %d", it.Repo.Name, text.Ellip(it.Name(), 40), it.Run.ID)
}

// State is one refresh's waiting runs, oldest first, and the warnings
// the refresh met.
type State struct {
	Items    []*Item
	Warnings []string
}

// Workflow is the waiting runs workflow.
type Workflow struct {
	workflow.Base
	cfg *config.WaitingRunsConfig

	last *State // worker goroutine only: the last state without a rate limit
}

var (
	_ workflow.Workflow       = (*Workflow)(nil)
	_ workflow.DeployProvider = (*Workflow)(nil)
	_ workflow.RowDeployer    = (*Workflow)(nil)
)

func init() {
	workflow.Register(Name, func(ctx *workflow.Context) (workflow.Workflow, error) {
		if ctx == nil || ctx.Config == nil || ctx.Config.WaitingRuns == nil {
			return nil, nil
		}
		return New(ctx.Config.WaitingRuns), nil
	})
}

// New builds the workflow from its config.
func New(cfg *config.WaitingRunsConfig) *Workflow { return &Workflow{cfg: cfg} }

// Name is "waiting_runs".
func (w *Workflow) Name() string { return Name }

// Order puts the section between the pull requests and the releases.
func (w *Workflow) Order() int { return 22 }

// Config is the workflow's config.
func (w *Workflow) Config() *config.WaitingRunsConfig { return w.cfg }

// ProvidesDeploys: the rows wait on environment approvals, so the list's
// key bar shows D.
func (w *Workflow) ProvidesDeploys() bool { return true }

// ---- refresh (worker goroutine) ----

// Refresh reads every configured repo's waiting runs and the pending
// deployments of each. A repo or a run that can't be read is a warning;
// a rate limit is returned, with the last good state, so the rows don't
// blink out while GitHub says wait.
func (w *Workflow) Refresh(ctx context.Context, gh ghapi.Client, _ []model.Repo, prs []*model.PR, _ []*model.Issue) (workflow.State, []string, error) {
	listed := listedSHAs(prs)
	st := &State{}
	var warns []string
	limited := func(err error) (workflow.State, []string, error) {
		if w.last != nil {
			return w.last, warns, err
		}
		sortItems(st.Items)
		st.Warnings = warns
		return st, warns, err
	}
	for _, repo := range w.cfg.Repos {
		runs, err := gh.RepoWaitingRuns(ctx, repo)
		if err != nil {
			if ghapi.IsRateLimit(err) {
				return limited(err)
			}
			warns = append(warns, fmt.Sprintf("waiting runs in %s: %s%s", repo.Name, err, tokenHint(err, "Actions: Read", repo)))
			continue
		}
		for _, r := range runs {
			if r.Status != "" && r.Status != "waiting" {
				continue
			}
			// a run on a listed PR's head or merge commit shows under
			// that PR; reading its deployments here would be wasted
			if listed[r.HeadSHA] {
				continue
			}
			pend, err := gh.PendingDeployments(ctx, repo, r.ID)
			if err != nil {
				if ghapi.IsRateLimit(err) {
					return limited(err)
				}
				// still listed: D reads the environments again anyway
				warns = append(warns, fmt.Sprintf("waiting runs in %s: run %d's environments: %s%s", repo.Name, r.ID, err, tokenHint(err, "Actions: Read", repo)))
			}
			sort.SliceStable(pend, func(i, j int) bool { return envName(pend[i]) < envName(pend[j]) })
			st.Items = append(st.Items, &Item{Repo: repo, Run: r, Pending: pend})
		}
	}
	sortItems(st.Items)
	st.Warnings = warns
	w.last = st
	return st, warns, nil
}

// sortItems puts the run waiting longest first.
func sortItems(items []*Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].Run, items[j].Run
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
}

// tokenHint names the permission a 403 points at.
func tokenHint(err error, perm string, repo model.Repo) string {
	if e, ok := ghapi.AsError(err); (ok && e.Status == 403) || strings.Contains(err.Error(), "not accessible by personal access token") {
		return fmt.Sprintf(" — your token needs %s on %s", perm, repo.Name)
	}
	return ""
}

// listedSHAs is the merge commits of the merged PRs: D on such a PR
// approves the runs on its merge commit, so they are not listed again. A
// run on an open PR's head stays: D on an open PR refuses (not merged),
// and this row is then the only place the run can be approved.
func listedSHAs(prs []*model.PR) map[string]bool {
	out := map[string]bool{}
	addSHAs(out, prs)
	return out
}

func addSHAs(set map[string]bool, prs []*model.PR) {
	for _, p := range prs {
		if p == nil {
			continue
		}
		if p.State == "MERGED" && p.MergeCommit != nil && p.MergeCommit.Oid != "" {
			set[p.MergeCommit.Oid] = true
		}
	}
}

// Apply stores the state. A refresh that stopped at a rate limit before
// this workflow ran hands it nil: the last state stays.
func (w *Workflow) Apply(app workflow.App, s workflow.State) {
	if st, ok := s.(*State); ok && st != nil {
		app.SetState(Name, st)
	}
}

// ---- the list (UI goroutine) ----

// visible is the applied state's runs minus those on a PR the dashboard
// lists: a review PR's head or merge commit, or a merged release's (the
// release workflow's rows). Those already show under their PR, whose D
// approves them through its merge commit; this is what keeps a release's
// deploy from being listed twice. The worker drops the review PRs' runs
// already; the releases are only known here, after every workflow ran.
func (w *Workflow) visible(app workflow.App) []*Item {
	st, _ := app.State(Name).(*State)
	if st == nil || len(st.Items) == 0 {
		return nil
	}
	listed := listedSHAs(app.AllPRs())
	if p, ok := app.State(releaseName).(workflow.CIProvider); ok && p != nil {
		addSHAs(listed, p.CIPRs())
	}
	var out []*Item
	for _, it := range st.Items {
		if !listed[it.Run.HeadSHA] {
			out = append(out, it)
		}
	}
	return out
}

// SearchText is what the filter matches a run row on: the repo, the
// workflow, the title, the branch, who started it and the environments.
func (w *Workflow) SearchText(row workflow.Row) string {
	if row.Kind != RowRun {
		return ""
	}
	it, ok := row.Item.(*Item)
	if !ok {
		return ""
	}
	return strings.Join(searchParts(it), " ")
}

func searchParts(it *Item) []string {
	return append([]string{"waiting run", it.Repo.String(), it.Name(), it.Run.DisplayTitle, it.Run.HeadBranch,
		it.Run.Actor.Login, fmt.Sprint(it.Run.ID)}, it.Envs()...)
}

func matches(parts, terms []string) bool {
	hay := strings.ToLower(strings.Join(parts, " "))
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// Sections is the "waiting approvals (N)" section: the runs that match
// the filter, the one waiting longest first.
func (w *Workflow) Sections(app workflow.App, terms []string, _ string) []workflow.Row {
	var rows []workflow.Row
	for _, it := range w.visible(app) {
		if matches(searchParts(it), terms) {
			rows = append(rows, workflow.Row{Kind: RowRun, Item: it})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	head := workflow.Row{Kind: "header", Item: fmt.Sprintf("waiting approvals (%d)", len(rows)), Meta: map[string]any{"icon": "shield"}}
	return append([]workflow.Row{head}, rows...)
}

// RowKinds renders the run rows.
func (w *Workflow) RowKinds() map[string]workflow.RowKind {
	return map[string]workflow.RowKind{
		RowRun: {
			Icon:   "shield",
			Layout: w.layout,
			Segs:   w.segs,
			URL: func(app workflow.App, row workflow.Row) string {
				it, ok := row.Item.(*Item)
				if !ok {
					return ""
				}
				if it.Run.HTMLURL != "" {
					return it.Run.HTMLURL
				}
				host := ""
				if cfg := app.Config(); cfg != nil {
					host = cfg.GitHub.Host
				}
				return ghapi.RunURL(host, it.Repo, it.Run.ID)
			},
			Open: func(app workflow.App, row workflow.Row) bool {
				it, ok := row.Item.(*Item)
				if !ok {
					return false
				}
				app.Notice(it.where() + " is an Actions run, not a PR — o opens it on GitHub; D approves it")
				return true
			},
			Title: func(row workflow.Row) string {
				if it, ok := row.Item.(*Item); ok {
					return it.Title()
				}
				return ""
			},
		},
	}
}

// multi reports whether the rows name their repo.
func (w *Workflow) multi(app workflow.App) bool { return app.MultiRepo() || len(w.cfg.Repos) > 1 }

func maxw(items []workflow.Row, f func(*Item) string, limit int) int {
	n := 0
	for _, r := range items {
		if it, ok := r.Item.(*Item); ok {
			n = max(n, text.Width(f(it)))
		}
	}
	return min(limit, n)
}

func branchText(it *Item) string {
	if it.Run.HeadBranch == "" {
		return ""
	}
	return glyphs.Ico("branch") + it.Run.HeadBranch
}

// chipSegs is a run's environment chips: "approve: <env>" (gold) for one
// the viewer can approve, "waits: <env>" for one that waits on someone
// else, "waiting" when the environments couldn't be read. pos < 0 draws
// no glint (for measuring).
func chipSegs(it *Item, pos int) []text.Seg {
	if len(it.Pending) == 0 {
		return scenes.Chip("waiting", "chip.state")
	}
	mono := theme.Current() == theme.Mono
	var segs []text.Seg
	for i, p := range it.Pending {
		if i > 0 {
			segs = append(segs, text.S(" ", "text"))
		}
		if p.CurrentUserCanApprove {
			segs = append(segs, scenes.GlintChip("approve: "+envName(p), "chip.await", pos, fx.Ambient, mono)...)
		} else {
			segs = append(segs, scenes.Chip("waits: "+envName(p), "chip.state")...)
		}
	}
	return segs
}

func (w *Workflow) layout(app workflow.App, items []workflow.Row, width, numW, repoW int) workflow.Layout {
	chipsW := 0
	for _, r := range items {
		if it, ok := r.Item.(*Item); ok {
			chipsW = max(chipsW, text.SegsWidth(chipSegs(it, -1)))
		}
	}
	cols := workflow.Layout{
		"chips":    min(48, chipsW),
		"workflow": maxw(items, (*Item).Name, 24),
		"branch":   maxw(items, branchText, 20),
		"actor":    maxw(items, func(it *Item) string { return text.OrQ(it.Run.Actor.Login) }, 14),
		"age":      3,
	}
	if w.multi(app) {
		cols["repo"] = min(24, max(repoW, maxw(items, func(it *Item) string { return it.Repo.Name }, 24)))
	}
	// narrow: the chips, what D acts on, never go
	return fit(cols, width, numW, "age", "branch", "actor", "workflow", "repo")
}

// fit drops columns in order until the title keeps 24 cells.
func fit(cols workflow.Layout, w, numW int, dropOrder ...string) workflow.Layout {
	fixed := func() int { // gutter 2 + icon 2 + the number column + each column and its gap
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

func (w *Workflow) segs(app workflow.App, row workflow.Row, L workflow.Layout, width, pos int) []text.Seg {
	it, ok := row.Item.(*Item)
	if !ok {
		return nil
	}
	// the icon cell stays when the glyph set has no shield (ascii), so
	// the row lines up with the PR rows' columns
	segs := []text.Seg{text.S(text.Pad(glyphs.G("shield"), 1)+" ", "icon.queued")}
	segs = append(segs, text.S(text.Pad("run", L["num"])+" ", "num.issue"))
	if rw, ok := L["repo"]; ok {
		segs = append(segs, text.S(text.Pad(text.Ellip(it.Repo.Name, rw), rw)+" ", "stack"))
	}
	if cw, ok := L["chips"]; ok {
		segs = append(segs, text.PadSegs(text.ClipSegs(chipSegs(it, pos), cw), cw)...)
		segs = append(segs, text.S(" ", "text"))
	}
	if ww, ok := L["workflow"]; ok {
		segs = append(segs, text.S(text.Pad(text.Ellip(it.Name(), ww), ww)+" ", "stack"))
	}
	if bw, ok := L["branch"]; ok {
		segs = append(segs, text.S(text.Pad(text.Ellip(branchText(it), bw), bw)+" ", "muted"))
	}
	if aw, ok := L["actor"]; ok {
		a := text.OrQ(it.Run.Actor.Login)
		segs = append(segs, text.S(text.Pad(text.Ellip(a, aw), aw)+" ", authorStyle(app, a)))
	}
	if aw, ok := L["age"]; ok {
		age, st := clock.ShortAge(it.Run.CreatedAt, app.Now())
		segs = append(segs, text.S(text.Pad(age, aw)+" ", st))
	}
	tstyle := "title"
	if len(it.Mine()) > 0 {
		tstyle = "title.attn"
	}
	room := width - 3 - text.SegsWidth(segs)
	return append(segs, text.S(text.Ellip(it.Title(), room), tstyle))
}

// SummarySegs is "N runs waiting" in the header, gold with ", M on you
// (D)" when some wait on the viewer; nothing when none wait.
func (w *Workflow) SummarySegs(app workflow.App) []text.Seg {
	items := w.visible(app)
	if len(items) == 0 {
		return nil
	}
	mine := 0
	for _, it := range items {
		if len(it.Mine()) > 0 {
			mine++
		}
	}
	txt := fmt.Sprintf("%s%s waiting", glyphs.Ico("shield"), plural(len(items), "run"))
	if mine == 0 {
		return []text.Seg{text.S(txt, "sum.muted")}
	}
	return []text.Seg{text.S(fmt.Sprintf("%s, %d on you (D)", txt, mine), "sum.attn")}
}

// HelpRows are the workflow's help lines.
func (w *Workflow) HelpRows() [][2]string {
	return [][2]string{
		{"runs", "D  on a waiting run: approve the environments you review   o  open the run on GitHub"},
	}
}

// LegendRows adds the run chips to the help legend.
func (w *Workflow) LegendRows() [][]text.Seg {
	row := []text.Seg{text.S(text.Pad("runs", 10), "section")}
	row = append(row, scenes.Chip("approve: prod", "chip.await")...)
	row = append(row, text.S(" a run waits on you (D)  ", "muted"))
	row = append(row, scenes.Chip("waits: prod", "chip.state")...)
	row = append(row, text.S(" on another reviewer", "muted"))
	return [][]text.Seg{row}
}

// ---- D (action goroutine) ----

// DeployRow is D on a run row: it reads the run's environments again
// (someone may have approved one since the refresh), refuses with who
// can when none is the viewer's, and otherwise approves each of the
// viewer's after a confirm, with approval_comment.
func (w *Workflow) DeployRow(app workflow.App, row workflow.Row) bool {
	if row.Kind != RowRun {
		return false
	}
	it, ok := row.Item.(*Item)
	if !ok {
		return false
	}
	where := it.where()
	app.Flash(fmt.Sprintf("reading %s's environments...", where))
	pend, err := app.GH().PendingDeployments(app.Context(), it.Repo, it.Run.ID)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't read %s's environments: %s%s", where, err, tokenHint(err, "Actions: Read", it.Repo)))
		return true
	}
	sort.SliceStable(pend, func(i, j int) bool { return envName(pend[i]) < envName(pend[j]) })
	cur := &Item{Repo: it.Repo, Run: it.Run, Pending: pend}
	if len(pend) == 0 {
		app.Notice(where + " waits on no environment now — it was approved or rejected since the last refresh")
		app.Refresh(true)
		return true
	}
	me := app.Me()
	mine := cur.Mine()
	if len(mine) == 0 {
		if selfReview(it.Run, pend, me) {
			// GitHub's prevent_self_review: the dispatcher is a reviewer but
			// may not approve their own run; someone else has to, or the run
			// is re-dispatched by another account.
			app.Notice(fmt.Sprintf("%s waits on %s, but %s dispatched it and the environment forbids self-review%s", where, strings.Join(cur.Envs(), ", "), me, reviewersNote(pend)))
			return true
		}
		app.Notice(fmt.Sprintf("%s waits on %s, but %s is not a reviewer%s", where, strings.Join(cur.Envs(), ", "), me, reviewersNote(pend)))
		return true
	}
	lines := make([]string, 0, len(pend))
	var theirs []string
	for _, p := range pend {
		if p.CurrentUserCanApprove {
			lines = append(lines, fmt.Sprintf("%s (%s, run %d)", envName(p), text.Ellip(it.Name(), 40), it.Run.ID))
		} else {
			theirs = append(theirs, envName(p))
		}
	}
	if len(theirs) > 0 {
		lines = append(lines, "not yours: "+strings.Join(theirs, ", "))
	}
	if !app.Confirm(fmt.Sprintf("approve %s of %s as %s?\n%s", plural(len(mine), "deployment"), where, me, strings.Join(lines, "\n"))) {
		app.Notice("deployment approval cancelled")
		return true
	}
	comment := w.cfg.Comment(it.Repo, it.Name(), it.Run.ID)
	var done, failed []string
	for _, p := range mine {
		if err := app.GH().ApproveDeployments(app.Context(), it.Repo, it.Run.ID, []int64{p.Environment.ID}, comment); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %s%s", envName(p), err, tokenHint(err, "Deployments: Read and write", it.Repo)))
			continue
		}
		done = append(done, envName(p))
	}
	if len(failed) > 0 {
		app.Fail(fmt.Sprintf("approved %d of %d; failed: %s", len(done), len(mine), strings.Join(failed, "; ")))
	} else {
		app.OK(fmt.Sprintf("approved %s of %s (%s)", plural(len(done), "deployment"), where, strings.Join(done, ", ")))
		app.Celebrate(false)
	}
	app.Refresh(true)
	return true
}

// selfReview reports a run me dispatched that waits on an environment
// naming me as a reviewer (so the refusal is GitHub's self-review rule,
// not a missing reviewer).
func selfReview(run model.WorkflowRun, pend []model.PendingDeployment, me string) bool {
	if me == "" || !strings.EqualFold(run.Actor.Login, me) {
		return false
	}
	for _, p := range pend {
		for _, r := range p.Reviewers {
			if strings.EqualFold(r.Reviewer.Login, me) {
				return true
			}
		}
	}
	return false
}

// reviewersNote is " (prod: bob, team deployers)": who can approve each
// environment, when GitHub said; "" when it named nobody.
func reviewersNote(pend []model.PendingDeployment) string {
	var parts []string
	for _, p := range pend {
		var names []string
		for _, r := range p.Reviewers {
			if n := r.Name(); n != "" && n != "team " {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			parts = append(parts, envName(p)+": "+strings.Join(names, ", "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

// plural is "1 run" / "3 runs".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
