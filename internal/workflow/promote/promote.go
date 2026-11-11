// Package promote follows a change through a branch-per-tier repo: a PR
// merged into the first tier (dev), the PR that carries it into staging
// (stage) and the go/hold tag it gets there, to production (main), which
// it reaches through the release PR (the release_pr workflow). One row
// per change shows where it is and what its tier's deploy did; p moves it
// one tier on (opens the promotion PR, merges it, tags the unit) and D
// approves the deployment its tier waits on.
package promote

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// Name is the workflow's config table and state key.
const Name = "promote"

// RowChange is a change row's kind.
const RowChange = "promote.change"

// releaseName is the release workflow's state key: its open release PRs
// are the last hop.
const releaseName = "release_pr"

// RepoState is one repo's changes and how many reached production in
// the lookback.
type RepoState struct {
	Repo     model.Repo
	Changes  []*Change
	Released int
}

// State is one refresh's repos and warnings.
type State struct {
	Repos    []RepoState
	Warnings []string
}

// Changes is every listed change, repo by repo.
func (s *State) Changes() []*Change {
	if s == nil {
		return nil
	}
	var out []*Change
	for _, r := range s.Repos {
		out = append(out, r.Changes...)
	}
	return out
}

type runsCached struct {
	runs    []model.WorkflowRun
	settled bool
}

// Workflow is the promote workflow.
type Workflow struct {
	workflow.Base
	cfg *config.PromoteConfig

	mu   sync.Mutex
	last map[model.Repo]RepoState // worker: each repo's last good read
	// worker caches: ancestry answers never change; a commit's runs once
	// every run on it completed
	ancestry map[string]bool
	runs     map[string]runsCached
}

var (
	_ workflow.Workflow       = (*Workflow)(nil)
	_ workflow.DeployProvider = (*Workflow)(nil)
	_ workflow.RowDeployer    = (*Workflow)(nil)
)

func init() {
	workflow.Register(Name, func(ctx *workflow.Context) (workflow.Workflow, error) {
		if ctx == nil || ctx.Config == nil || ctx.Config.Promote == nil {
			return nil, nil
		}
		return New(ctx.Config.Promote), nil
	})
}

// New builds the workflow from its config.
func New(cfg *config.PromoteConfig) *Workflow {
	return &Workflow{cfg: cfg, last: map[model.Repo]RepoState{}, ancestry: map[string]bool{}, runs: map[string]runsCached{}}
}

// Name is "promote".
func (w *Workflow) Name() string { return Name }

// Order puts the section after the releases (the last hop is theirs),
// before the maintenance window.
func (w *Workflow) Order() int { return 26 }

// Config is the workflow's config.
func (w *Workflow) Config() *config.PromoteConfig { return w.cfg }

// ProvidesDeploys: a change's tier can wait on an environment, so the
// list's key bar shows D.
func (w *Workflow) ProvidesDeploys() bool { return true }

// ---- refresh (worker goroutine) ----

// ListFree: Refresh reads its own repos, not the list, so it starts
// beside the list reads.
func (w *Workflow) ListFree() bool { return true }

// Refresh reads each repo's promotion PRs, staging's units ahead of
// production and their tags, places every change, and reads the runs on
// each change's tier commit. A repo that can't be read keeps its last
// rows with a warning; a rate limit stops the refresh with the last
// state.
func (w *Workflow) Refresh(ctx context.Context, gh ghapi.Client, _ []model.Repo, _ []*model.PR, _ []*model.Issue) (workflow.State, []string, error) {
	st := &State{}
	var warns []string
	since := clock.Now().AddDate(0, 0, -w.cfg.LookbackDays)
	// the repos side by side (the client caps what is in flight); a rate
	// limit cancels the rest, which would only meet the same wall
	type read struct {
		rs    RepoState
		notes []string
		err   error
	}
	reads := make([]read, len(w.cfg.Repos))
	g, gctx := errgroup.WithContext(ctx)
	for i, repo := range w.cfg.Repos {
		g.Go(func() error {
			rs, notes, err := w.readRepo(gctx, gh, repo, since)
			reads[i] = read{rs, notes, err}
			if ghapi.IsRateLimit(err) {
				return err
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return w.lastState(warns), warns, err
	}
	for i, repo := range w.cfg.Repos {
		rs, notes, err := reads[i].rs, reads[i].notes, reads[i].err
		if err != nil {
			warns = append(warns, fmt.Sprintf("promote: %s: %s", repo.Name, err))
			if prev, ok := w.lastRepo(repo); ok {
				st.Repos = append(st.Repos, prev)
			}
			continue
		}
		warns = append(warns, notes...)
		w.mu.Lock()
		w.last[repo] = rs
		w.mu.Unlock()
		st.Repos = append(st.Repos, rs)
	}
	st.Warnings = warns
	return st, warns, nil
}

func (w *Workflow) lastRepo(repo model.Repo) (RepoState, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	rs, ok := w.last[repo]
	return rs, ok
}

func (w *Workflow) lastState(warns []string) *State {
	st := &State{Warnings: warns}
	for _, repo := range w.cfg.Repos {
		if rs, ok := w.lastRepo(repo); ok {
			st.Repos = append(st.Repos, rs)
		}
	}
	return st
}

func (w *Workflow) readRepo(ctx context.Context, gh ghapi.Client, repo model.Repo, since time.Time) (RepoState, []string, error) {
	rs := RepoState{Repo: repo}
	// four independent reads at once; the first error in this order wins
	var (
		set              ghapi.PromoteSet
		cmp              ghapi.CompareResult
		goTags, holdTags []string
		errs             [4]error
		wg               sync.WaitGroup
	)
	wg.Add(4)
	go func() {
		defer wg.Done()
		set, errs[0] = gh.PromotePRs(ctx, repo, w.cfg.First(), w.cfg.Stage(), w.cfg.Prod(), since)
	}()
	go func() { defer wg.Done(); cmp, errs[1] = gh.Compare(ctx, repo, w.cfg.Prod(), w.cfg.Stage()) }()
	go func() { defer wg.Done(); goTags, errs[2] = gh.TagRefs(ctx, repo, w.cfg.GoTagPrefix) }()
	go func() { defer wg.Done(); holdTags, errs[3] = gh.TagRefs(ctx, repo, w.cfg.HoldTagPrefix) }()
	wg.Wait()
	if errs[1] != nil {
		errs[1] = fmt.Errorf("comparing %s...%s: %w", w.cfg.Prod(), w.cfg.Stage(), errs[1])
	}
	if err := firstErr(errs[:]...); err != nil {
		return rs, nil, err
	}
	units, ahead, ok := StageOrder(cmp)
	in := Inputs{
		Repo: repo, First: w.cfg.First(), Stage: w.cfg.Stage(), Prod: w.cfg.Prod(),
		Set: set, Units: units, Ahead: ahead, AheadOK: ok,
		Go: tagNumbers(goTags, w.cfg.GoTagPrefix), Hold: tagNumbers(holdTags, w.cfg.HoldTagPrefix),
	}
	if err := w.prefetchAncestry(ctx, gh, in); err != nil {
		return rs, nil, err
	}
	var limit error
	in.Contains = func(sha, head string) bool {
		if sha == "" || head == "" || limit != nil {
			return false
		}
		if v, hit := w.ancestryOf(repo, sha, head); hit {
			return v
		}
		// the prefetch asks for every pair Locate can reach; this is the
		// backstop, one at a time as before
		v, err := gh.IsAncestor(ctx, repo, sha, head)
		if err != nil {
			if ghapi.IsRateLimit(err) {
				limit = err
			}
			return false
		}
		w.noteAncestry(repo, sha, head, v)
		return v
	}
	changes, released := Locate(in)
	if limit != nil {
		return rs, nil, limit
	}
	// each change's tier runs side by side
	runs := make([][]model.WorkflowRun, len(changes))
	runErrs := make([]error, len(changes))
	g, gctx := errgroup.WithContext(ctx)
	for i, c := range changes {
		g.Go(func() error {
			runs[i], runErrs[i] = w.tierRuns(gctx, gh, repo, c.SHA())
			if ghapi.IsRateLimit(runErrs[i]) {
				return runErrs[i]
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return rs, nil, err
	}
	for i, c := range changes {
		if runErrs[i] != nil {
			set.Notes = append(set.Notes, fmt.Sprintf("promote: %s #%d's runs: %s", repo.Name, c.Number(), runErrs[i]))
			continue
		}
		c.Runs = runs[i]
	}
	rs.Changes, rs.Released = changes, released
	return rs, set.Notes, nil
}

// ancestryRounds bounds the prefetch's dry runs of Locate.
const ancestryRounds = 3

// prefetchAncestry asks, side by side, every ancestry question Locate
// will ask that the cache can't answer: a dry run of Locate records the
// misses (answering no, which only widens what it asks), they are
// fetched at once, and the dry run repeats until nothing is missing.
// The answers are fixed points (two commits), so they are kept for good.
// Only a rate limit is an error; any other failure leaves the pair to
// Locate's own backstop.
func (w *Workflow) prefetchAncestry(ctx context.Context, gh ghapi.Client, in Inputs) error {
	type pair struct{ sha, head string }
	for round := 0; round < ancestryRounds; round++ {
		var misses []pair
		seen := map[pair]bool{}
		in.Contains = func(sha, head string) bool {
			if sha == "" || head == "" {
				return false
			}
			if v, hit := w.ancestryOf(in.Repo, sha, head); hit {
				return v
			}
			if p := (pair{sha, head}); !seen[p] {
				seen[p] = true
				misses = append(misses, p)
			}
			return false
		}
		Locate(in)
		if len(misses) == 0 {
			return nil
		}
		g, gctx := errgroup.WithContext(ctx)
		for _, p := range misses {
			g.Go(func() error {
				v, err := gh.IsAncestor(gctx, in.Repo, p.sha, p.head)
				if err != nil {
					if ghapi.IsRateLimit(err) {
						return err
					}
					return nil
				}
				w.noteAncestry(in.Repo, p.sha, p.head, v)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return err
		}
	}
	return nil
}

func (w *Workflow) ancestryOf(repo model.Repo, sha, head string) (bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	v, hit := w.ancestry[repo.String()+"|"+sha+"|"+head]
	return v, hit
}

func (w *Workflow) noteAncestry(repo model.Repo, sha, head string, v bool) {
	w.mu.Lock()
	w.ancestry[repo.String()+"|"+sha+"|"+head] = v
	w.mu.Unlock()
}

// firstErr is the first non-nil error.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// tierRuns is the newest run of each workflow on sha, cached once every
// run on it completed.
func (w *Workflow) tierRuns(ctx context.Context, gh ghapi.Client, repo model.Repo, sha string) ([]model.WorkflowRun, error) {
	if sha == "" {
		return nil, nil
	}
	key := repo.String() + "@" + sha
	w.mu.Lock()
	c, ok := w.runs[key]
	w.mu.Unlock()
	if ok && c.settled {
		return c.runs, nil
	}
	all, err := gh.WorkflowRuns(ctx, repo, sha)
	if err != nil {
		return nil, err
	}
	runs := latestPerWorkflow(all)
	settled := len(runs) > 0
	for _, r := range runs {
		if r.Status != "completed" {
			settled = false
		}
	}
	w.mu.Lock()
	w.runs[key] = runsCached{runs: runs, settled: settled}
	w.mu.Unlock()
	return runs, nil
}

// latestPerWorkflow keeps the first (newest) run of each workflow name.
func latestPerWorkflow(all []model.WorkflowRun) []model.WorkflowRun {
	seen := map[string]bool{}
	var out []model.WorkflowRun
	for _, r := range all {
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Apply stores the state.
func (w *Workflow) Apply(app workflow.App, s workflow.State) {
	if st, ok := s.(*State); ok && st != nil {
		app.SetState(Name, st)
	}
}

func state(app workflow.App) *State {
	st, _ := app.State(Name).(*State)
	return st
}

// ---- deploy state ----

// Deploy is a change's tier deploy, summed over its runs.
type Deploy int

const (
	DeployNone Deploy = iota
	DeployOK
	DeployRunning
	DeployWaiting
	DeployFailed
)

// deployOf sums the runs: waiting beats failed beats running beats ok.
func deployOf(runs []model.WorkflowRun) (Deploy, string) {
	d, name := DeployNone, ""
	rank := func(x Deploy) int {
		return map[Deploy]int{DeployNone: 0, DeployOK: 1, DeployRunning: 2, DeployFailed: 3, DeployWaiting: 4}[x]
	}
	for _, r := range runs {
		var x Deploy
		switch {
		case r.Status == "waiting":
			x = DeployWaiting
		case r.Status != "completed":
			x = DeployRunning
		case r.Conclusion == "success" || r.Conclusion == "skipped" || r.Conclusion == "neutral":
			x = DeployOK
		default:
			x = DeployFailed
		}
		if rank(x) > rank(d) {
			d, name = x, r.Name
		}
	}
	return d, name
}

// ---- the list (UI goroutine) ----

func (w *Workflow) tierName(c *Change) string {
	switch c.Where {
	case OnFirst:
		return w.cfg.First()
	case Promoting, OnStage:
		return w.cfg.Stage()
	}
	return w.cfg.Prod()
}

// whereChip is the change's place: "on dev", "→ stage #224 · p merges",
// "stage · untagged", "stage · go", "stage · go · after #612".
func (w *Workflow) whereChip(c *Change) (string, string) {
	switch c.Where {
	case OnFirst:
		return "on " + w.cfg.First(), "chip.state"
	case Promoting:
		lbl := fmt.Sprintf("%s %s #%d", glyphs.G("arrow"), w.cfg.Stage(), c.Promo.Number)
		switch strings.ToUpper(c.Promo.MergeStateStatus) {
		case "CLEAN", "HAS_HOOKS":
			return lbl + " · ready", "chip.await"
		case "DIRTY":
			return lbl + " · conflicts", "chip.fail"
		case "BEHIND":
			return lbl + " · behind", "chip.state"
		case "DRAFT":
			return lbl + " · draft", "chip.draft"
		default:
			return lbl + " · checks", "chip.state"
		}
	case OnStage:
		switch c.Tag {
		case "go":
			if c.BlockedBy > 0 {
				return fmt.Sprintf("%s · go · after #%d", w.cfg.Stage(), c.BlockedBy), "chip.state"
			}
			return w.cfg.Stage() + " · go", "chip.approved"
		case "hold":
			return w.cfg.Stage() + " · hold", "chip.state"
		default:
			return w.cfg.Stage() + " · untagged", "chip.await"
		}
	}
	return "on " + w.cfg.Prod(), "chip.released"
}

// deployChip is the tier deploy: "dev ✓", "stage deploying", "stage
// waits · D", "stage ✗ <workflow>", or nothing without runs.
func (w *Workflow) deployChip(c *Change) (string, string) {
	d, name := deployOf(c.Runs)
	tier := w.tierName(c)
	switch d {
	case DeployOK:
		return glyphs.Ico("pass") + tier, "chip.merged"
	case DeployRunning:
		return glyphs.Ico("wait") + tier + " deploying", "chip.queued"
	case DeployWaiting:
		return glyphs.Ico("wait") + tier + " waits · D", "chip.await"
	case DeployFailed:
		return glyphs.Ico("fail") + tier + " " + text.Ellip(name, 18), "chip.fail"
	}
	return "", ""
}

func searchParts(w *Workflow, c *Change) []string {
	where, _ := w.whereChip(c)
	return []string{"promote", c.Repo.String(), fmt.Sprintf("#%d", c.Number()), c.Title(), c.Author(), where, w.tierName(c), c.Tag}
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

// SearchText is what the filter matches a change row on.
func (w *Workflow) SearchText(row workflow.Row) string {
	c, ok := row.Item.(*Change)
	if !ok || row.Kind != RowChange {
		return ""
	}
	return strings.Join(searchParts(w, c), " ")
}

// Sections is the "promote (N)" section: the changes on their way, repo
// by repo, furthest along first.
func (w *Workflow) Sections(app workflow.App, terms []string, _ string) []workflow.Row {
	var rows []workflow.Row
	for _, c := range state(app).Changes() {
		if matches(searchParts(w, c), terms) {
			rows = append(rows, workflow.Row{Kind: RowChange, Item: c})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	head := workflow.Row{Kind: "header", Item: fmt.Sprintf("promote (%d)", len(rows)), Meta: map[string]any{"icon": "branch"}}
	return append([]workflow.Row{head}, rows...)
}

// SummarySegs is "N to promote" in the header, gold with ", M wait (D)"
// when a tier deploy waits on an environment.
func (w *Workflow) SummarySegs(app workflow.App) []text.Seg {
	cs := state(app).Changes()
	if len(cs) == 0 {
		return nil
	}
	waiting := 0
	for _, c := range cs {
		if d, _ := deployOf(c.Runs); d == DeployWaiting {
			waiting++
		}
	}
	txt := fmt.Sprintf("%s%d to promote", glyphs.Ico("branch"), len(cs))
	if waiting == 0 {
		return []text.Seg{text.S(txt, "sum.muted")}
	}
	return []text.Seg{text.S(fmt.Sprintf("%s, %d deploy waits (D)", txt, waiting), "sum.attn")}
}

// RowKinds renders the change rows.
func (w *Workflow) RowKinds() map[string]workflow.RowKind {
	return map[string]workflow.RowKind{
		RowChange: {
			Icon:   "branch",
			Layout: w.layout,
			Segs:   w.segs,
			URL: func(app workflow.App, row workflow.Row) string {
				c, ok := row.Item.(*Change)
				if !ok {
					return ""
				}
				host := ""
				if cfg := app.Config(); cfg != nil {
					host = cfg.GitHub.Host
				}
				if c.Where != OnFirst && c.Promo != nil {
					return ghapi.PRURL(host, c.Repo, c.Promo.Number)
				}
				return ghapi.PRURL(host, c.Repo, c.Number())
			},
			Open: func(app workflow.App, row workflow.Row) bool {
				c, ok := row.Item.(*Change)
				if !ok {
					return false
				}
				n := c.Number()
				if c.Where == Promoting && c.Promo != nil {
					n = c.Promo.Number // the PR to review and merge
				}
				app.OpenDetail(c.Repo, n, "pr", nil)
				return true
			},
			Title: func(row workflow.Row) string {
				if c, ok := row.Item.(*Change); ok {
					return c.Title()
				}
				return ""
			},
			NumText: func(row workflow.Row) string {
				if c, ok := row.Item.(*Change); ok {
					return fmt.Sprintf("#%d", c.Number())
				}
				return ""
			},
		},
	}
}

func (w *Workflow) layout(app workflow.App, items []workflow.Row, width, numW, repoW int) workflow.Layout {
	whereW, deployW, authorW := 0, 0, 0
	for _, r := range items {
		c, ok := r.Item.(*Change)
		if !ok {
			continue
		}
		wl, _ := w.whereChip(c)
		dl, _ := w.deployChip(c)
		whereW = max(whereW, text.SegsWidth(scenes.Chip(wl, "chip.state")))
		if dl != "" {
			deployW = max(deployW, text.SegsWidth(scenes.Chip(dl, "chip.state")))
		}
		authorW = max(authorW, text.Width(c.Author()))
	}
	cols := workflow.Layout{"where": min(34, whereW), "author": min(14, authorW), "age": 3}
	if deployW > 0 {
		cols["deploy"] = min(30, deployW)
	}
	if len(w.cfg.Repos) > 1 || app.MultiRepo() {
		maxRepo := 0
		for _, r := range items {
			if c, ok := r.Item.(*Change); ok {
				maxRepo = max(maxRepo, text.Width(c.Repo.Name))
			}
		}
		cols["repo"] = min(24, max(repoW, maxRepo))
	}
	return fit(cols, width, numW, "age", "author", "deploy", "repo")
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

func (w *Workflow) segs(app workflow.App, row workflow.Row, L workflow.Layout, width, pos int) []text.Seg {
	c, ok := row.Item.(*Change)
	if !ok {
		return nil
	}
	segs := []text.Seg{text.S(text.Pad(glyphs.G("branch"), 1)+" ", "icon.queued")}
	segs = append(segs, text.S(text.Pad(fmt.Sprintf("#%d", c.Number()), L["num"])+" ", "num"))
	if rw, ok := L["repo"]; ok {
		segs = append(segs, text.S(text.Pad(text.Ellip(c.Repo.Name, rw), rw)+" ", "stack"))
	}
	if ww, ok := L["where"]; ok {
		lbl, st := w.whereChip(c)
		segs = append(segs, text.PadSegs(text.ClipSegs(scenes.Chip(lbl, st), ww), ww)...)
		segs = append(segs, text.S(" ", "text"))
	}
	if dw, ok := L["deploy"]; ok {
		lbl, st := w.deployChip(c)
		var chip []text.Seg
		if lbl != "" {
			chip = scenes.Chip(lbl, st)
		}
		segs = append(segs, text.PadSegs(text.ClipSegs(chip, dw), dw)...)
		segs = append(segs, text.S(" ", "text"))
	}
	if aw, ok := L["author"]; ok {
		a := text.OrQ(c.Author())
		st := "author"
		if strings.EqualFold(a, app.Me()) {
			st = "author.me"
		} else if cfg := app.Config(); cfg != nil && cfg.Bots.IsBot(a) {
			st = "author.bot"
		}
		segs = append(segs, text.S(text.Pad(text.Ellip(a, aw), aw)+" ", st))
	}
	if aw, ok := L["age"]; ok {
		age, st := clock.ShortAge(c.Since(), app.Now())
		segs = append(segs, text.S(text.Pad(age, aw)+" ", st))
	}
	tstyle := "title"
	if d, _ := deployOf(c.Runs); d == DeployWaiting {
		tstyle = "title.attn"
	}
	room := width - 3 - text.SegsWidth(segs)
	return append(segs, text.S(text.Ellip(c.Title(), room), tstyle))
}

// HelpRows are the workflow's help lines.
func (w *Workflow) HelpRows() [][2]string {
	return [][2]string{
		{"promote", fmt.Sprintf("P  on a change: move it one tier on (%s: open the promotion PR · → %s: merge it · %s: tag go or hold)   D  approve the deploy its tier waits on   ↵  open its PR",
			w.cfg.First(), w.cfg.Stage(), w.cfg.Stage())},
	}
}

// LegendRows adds the promote chips to the help legend.
func (w *Workflow) LegendRows() [][]text.Seg {
	row := []text.Seg{text.S(text.Pad("promote", 10), "section")}
	row = append(row, scenes.Chip("on "+w.cfg.First(), "chip.state")...)
	row = append(row, text.S(" P opens the promotion  ", "muted"))
	row = append(row, scenes.Chip(w.cfg.Stage()+" · untagged", "chip.await")...)
	row = append(row, text.S(" P tags it  ", "muted"))
	row = append(row, scenes.Chip(w.cfg.Stage()+" · go", "chip.approved")...)
	row = append(row, text.S(" rides the next release", "muted"))
	return [][]text.Seg{row}
}

// Keys is P.
func (w *Workflow) Keys() []workflow.Key {
	return []workflow.Key{{View: "list", Ch: "P", Label: "promote", Priority: 25, Handler: w.doPromote, Offer: w.offerPromote}}
}

// offerPromote: P acts on a change row, and says which step it takes.
func (w *Workflow) offerPromote(f workflow.Focus) (string, bool) {
	if !f.HasRow || f.Row.Kind != RowChange {
		return "", false
	}
	c, ok := f.Row.Item.(*Change)
	if !ok || c == nil {
		return "", false
	}
	switch c.Where {
	case OnFirst:
		return "open the promotion PR", true
	case Promoting:
		return "merge the promotion", true
	case OnStage:
		return "tag go / hold", true
	}
	return "", false
}
