// Package releasepr is the application-release workflow: in every repo a
// release ruleset applies to, production is an open PR into the
// production branch from release/<date> (the go-tagged cut on the staging
// branch) or hotfix/<slug>. An approving review queues it for the
// maintenance window when one is enabled; a "<release-now command>
// "<reason>"" comment from an approver releases it at once (a hotfix is
// an emergency by definition). M merges it at its slot (the push to
// production is the deploy) and D approves the environment deployments
// that wait on the merge commit.
//
// The workflow adds a "releases (N)" section with one synthetic "stage"
// row per repo (what the staging branch carries ahead of production, by
// tag), takes over the a key on release PRs, gates M and D through the
// core's hooks, and adds a release card to the detail view.
package releasepr

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// Name is the workflow's registry and state name.
const Name = "release_pr"

// WindowName is the maintenance window workflow's name, looked up for
// the night a queued release joins.
const WindowName = "maintenance_window"

// Chip labels the workflow adds to the generic ones.
const (
	LabelNeeds = "needs approval"
	// LabelOther is an open PR into production whose branch is neither
	// release/* nor hotfix/*: listed so nothing reaches production
	// unseen, never offered for approval or merge.
	LabelOther = "not a release"
)

// Kinds of a release PR by head branch.
const (
	KindRelease = "release"
	KindHotfix  = "hotfix"
	KindOther   = "other"
)

// Row kinds the workflow defines.
const (
	RowRelease = "release"
	RowStage   = "stage"
)

// Ext keys the workflow writes on a PR.
const (
	ExtRel    = "rel"          // *Info
	ExtDeploy = "deploy"       // deploys.State (the core writes it too, after D)
	ExtCI     = workflow.CIKey // model.CICluster, written by the core (rollup or Actions fallback)
	ExtIcon   = "icon"         // "release" | "hotfix" (the detail header icon)
)

// mergeable are the GitHub mergeStateStatus values M accepts (UNKNOWN:
// GitHub has not computed it yet; the merge call itself still refuses a
// blocked PR).
var mergeable = map[string]bool{"CLEAN": true, "HAS_HOOKS": true, "UNSTABLE": true, "UNKNOWN": true, "": true}

// Info is what the workflow knows about a release PR (pr.Get("rel")).
type Info struct {
	Kind      string // release | hotfix | other
	Repo      string // the repo's short name
	MergedSHA string // the sha M got back this session, when GraphQL has none yet
}

// Units is what the staging branch carries ahead of production: one
// unit per staging PR, counted by its go/hold tag.
type Units struct {
	Ahead     int
	Units     int
	Go        int
	Hold      int
	Untagged  int
	Truncated bool // the compare page was shorter than ahead_by
}

// Stage is a repo's synthetic stage row.
type Stage struct {
	Repo  model.Repo
	Units *Units // nil when the tips were not read
}

// Ident identifies the stage row across refreshes.
func (s *Stage) Ident() model.Ident { return model.Ident{Repo: s.Repo, Number: 0} }

// State is one refresh's result: the release PRs of every enrolled
// repo (each annotated with ExtRel, ExtCI and, when merged, ExtDeploy),
// a stage row per repo, and the repos that answered.
type State struct {
	Releases []*model.PR
	Stages   []*Stage
	Repos    []model.Repo
	Warnings []string

	queuedLabel      string
	scheduledLabel   string    // the window's scheduled label, for ScheduledForWindow
	queuesByApproval bool      // approval_queues_for_window with the window on: a review queues
	w                *Workflow // the tracker, for inFlight
	byIdent          map[model.Ident]*model.PR
}

var (
	_ workflow.QueuedProvider = (*State)(nil)
	_ workflow.CIProvider     = (*State)(nil)
	_ workflow.DeployProvider = (*Workflow)(nil)
)

// CIPRs hands the releases to the core's CI pass, so a release row gets
// the Actions fallback when the token cannot read the check rollup, the
// same as a review PR (#5).
func (s *State) CIPRs() []*model.PR {
	if s == nil {
		return nil
	}
	return s.Releases
}

// ProvidesDeploys: merged releases wait on environment approvals.
func (w *Workflow) ProvidesDeploys() bool { return true }

// QueuedForWindow lists the releases queued for the maintenance window,
// for the window workflow to list under their night: those carrying the
// window's queued label, and, with approval_queues_for_window, those an
// approving review queues (the conductor labels nothing before the
// freeze: the review is the queue signal).
func (s *State) QueuedForWindow() []*model.PR {
	if s == nil || (s.queuedLabel == "" && !s.queuesByApproval) {
		return nil
	}
	now := clock.Now()
	var out []*model.PR
	for _, p := range s.Releases {
		kind := ""
		if i := InfoOf(p); i != nil {
			kind = i.Kind
		}
		if queuedFor(p, kind, s.queuedLabel, s.queuesByApproval, now) && !s.w.inFlight(p) {
			out = append(out, p)
		}
	}
	return out
}

// inFlight reports a release the tracker is following past its slot
// (dispatched, or running): it belongs in the releases section with its
// rail, not under the night. A scheduled one, or one waiting for its
// slot, still sits under the night; a finished one is queued again by
// its standing approval, as the conductor would.
func (w *Workflow) inFlight(pr *model.PR) bool {
	if w == nil {
		return false
	}
	p, ok := w.progressOf(pr)
	return ok && !p.Terminal && p.Phase != PhaseScheduled && p.Phase != PhaseWaitingSlot
}

// approvalQueueAge is how long an approving review keeps a release
// queued: the conductor's three nights; older, it asks for a fresh one.
const approvalQueueAge = 72 * time.Hour

// queuedFor reports a release queued for the window: by the queued label,
// or by an approving review when byApproval is set (an open, non-draft
// release, never a hotfix, with an APPROVED review no older than three
// nights; a review with no submittedAt counts).
func queuedFor(pr *model.PR, kind, queuedLabel string, byApproval bool, now time.Time) bool {
	if queuedLabel != "" && pr.Labels.Has(queuedLabel) {
		return true
	}
	if !byApproval || pr.IsDraft || kind != KindRelease || (pr.State != "" && pr.State != "OPEN") {
		return false
	}
	for _, r := range pr.Opinions.Nodes {
		if r.State == "APPROVED" && (r.SubmittedAt.IsZero() || now.Sub(r.SubmittedAt) <= approvalQueueAge) {
			return true
		}
	}
	return false
}

// Find returns the listed release PR with that identity, or nil.
func (s *State) Find(id model.Ident) *model.PR {
	if s == nil {
		return nil
	}
	return s.byIdent[id]
}

func (s *State) index() {
	s.byIdent = make(map[model.Ident]*model.PR, len(s.Releases))
	for _, p := range s.Releases {
		s.byIdent[p.Ident()] = p
	}
}

// lastGood is a repo's rows from its last successful read.
type lastGood struct {
	rows  []*model.PR
	stage *Stage
	at    string // "HH:MM"
}

type stageCached struct {
	prod, stage string
	compare     ghapi.CompareResult
}

type seenKey struct {
	repo, sha, env string
}

// Workflow is the release workflow.
type Workflow struct {
	workflow.Base
	cfg    *config.ReleasePRConfig
	win    *config.MaintenanceWindowConfig // nil: no window, M merges whenever GitHub allows
	window workflow.Workflow               // the window workflow when enabled
	org    string
	nowRE  *regexp.Regexp
	deploy deploys.Cache

	// worker goroutine only
	repoCache  []model.Repo
	repoAt     time.Time
	last       map[model.Repo]lastGood
	stageCache map[model.Repo]stageCached

	mu   sync.Mutex
	cur  *State           // the last applied state (UI goroutine reads)
	seen map[seenKey]bool // deploy notices already given
	// the releases in flight (progress.go), the outcomes announced, and
	// whether the manifest repo's unreadable Actions were mentioned
	progress       map[model.Ident]*track
	seenOutcome    map[model.Ident]string
	infraOffSaid   bool
	manifestTarget model.Repo // where the apply runs are, as AfterApply last resolved it
}

var _ workflow.Workflow = (*Workflow)(nil)

func init() {
	workflow.Register(Name, func(ctx *workflow.Context) (workflow.Workflow, error) {
		if ctx == nil || ctx.Config == nil || ctx.Config.ReleasePR == nil {
			return nil, nil
		}
		return New(ctx), nil
	})
}

// New builds the workflow from the context's config (which must have
// ReleasePR set).
func New(ctx *workflow.Context) *Workflow {
	cfg := ctx.Config.ReleasePR
	w := &Workflow{
		cfg:         cfg,
		org:         cfg.Org,
		last:        map[model.Repo]lastGood{},
		stageCache:  map[model.Repo]stageCached{},
		seen:        map[seenKey]bool{},
		progress:    map[model.Ident]*track{},
		seenOutcome: map[model.Ident]string{},
	}
	if w.org == "" {
		if len(ctx.Repos) > 0 {
			w.org = ctx.Repos[0].Owner
		} else if len(ctx.Config.Reviews.Repos) > 0 {
			w.org = ctx.Config.Reviews.Repos[0].Owner
		}
	}
	if win := ctx.Get(WindowName); win != nil {
		w.window = win
		w.win = ctx.Config.MaintenanceWindow
	}
	w.nowRE = ReleaseNowRE(cfg.ReleaseNowCommand, cfg.ReleaseNowMinReason)
	w.deploy.StuckAfter = time.Duration(cfg.DeployStuckSecs) * time.Second
	return w
}

// ReleaseNowRE is the grammar of a release-now comment: the command, a
// space, and a double-quoted reason of at least minLen characters with
// no quote or newline inside, nothing after.
func ReleaseNowRE(command string, minLen int) *regexp.Regexp {
	if command == "" {
		command = "/release-now"
	}
	if minLen < 1 {
		minLen = 1
	}
	return regexp.MustCompile(`^` + regexp.QuoteMeta(command) + fmt.Sprintf(` "([^"\r\n]{%d,})"\s*$`, minLen))
}

// Name is "release_pr".
func (w *Workflow) Name() string { return Name }

// Order places the releases section after the pull requests.
func (w *Workflow) Order() int { return 25 }

// Config is the workflow's config.
func (w *Workflow) Config() *config.ReleasePRConfig { return w.cfg }

// WindowEnabled reports whether the maintenance window workflow is on.
func (w *Workflow) WindowEnabled() bool { return w.win != nil }

// Current is the last applied state (nil before the first refresh).
func (w *Workflow) Current() *State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cur
}

// ---- refresh (worker goroutine) ----

// Refresh reads every enrolled repo's release PRs, the stage summary
// per repo and the deploy state of each merged release. Anything but a
// rate limit degrades to a warning; a repo that fails keeps its last
// good rows.
func (w *Workflow) Refresh(ctx context.Context, gh ghapi.Client, _ []model.Repo, _ []*model.PR, _ []*model.Issue) (workflow.State, []string, error) {
	now := clock.Now()
	st := &State{w: w}
	if w.win != nil {
		st.queuedLabel = w.win.QueuedLabel
		st.scheduledLabel = w.win.ScheduledLabel
		st.queuesByApproval = w.cfg.ApprovalQueuesForWindow
	}
	var warns []string
	names, warn := w.repoNames(ctx, gh, now)
	if warn != "" {
		warns = append(warns, warn)
	}
	data := map[model.Repo]ghapi.ReleaseRepo{}
	if len(names) > 0 {
		var err error
		data, err = gh.ReleasePRs(ctx, names, w.cfg.ProductionBranch, w.cfg.StagingBranch)
		if err != nil {
			// keep showing what we had rather than blank the section
			repos := make([]model.Repo, 0, len(w.last))
			for r := range w.last {
				repos = append(repos, r)
			}
			sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
			for _, r := range repos {
				lg := w.last[r]
				st.Releases = append(st.Releases, clonePRs(lg.rows)...)
				if lg.stage != nil {
					st.Stages = append(st.Stages, lg.stage)
				}
				st.Repos = append(st.Repos, r)
			}
			suffix := ""
			if len(w.last) > 0 {
				suffix = " (showing the last good data)"
			}
			warns = append(warns, "releases: "+err.Error()+suffix)
			st.Warnings = warns
			st.index()
			w.annotate(st)
			return st, warns, rateLimit(err)
		}
	}
	for _, repo := range names {
		info, ok := data[repo]
		if !ok {
			info = ghapi.ReleaseRepo{Err: fmt.Errorf("no data")}
		}
		if info.Err != nil {
			if lg, ok := w.last[repo]; ok {
				st.Releases = append(st.Releases, clonePRs(lg.rows)...)
				if lg.stage != nil {
					st.Stages = append(st.Stages, lg.stage)
				}
				st.Repos = append(st.Repos, repo)
				warns = append(warns, fmt.Sprintf("%s: %s (showing %s's data)", repo.Name, info.Err, lg.at))
			} else {
				warns = append(warns, fmt.Sprintf("%s: %s", repo.Name, info.Err))
			}
			continue
		}
		if info.Archived {
			continue
		}
		var rows []*model.PR
		for _, pr := range info.PRs {
			if pr.State == "MERGED" && !MergedRecently(pr, now, w.keep()) {
				continue
			}
			if pr.State != "" && pr.State != "OPEN" && pr.State != "MERGED" {
				continue
			}
			if pr.Repo.IsZero() {
				pr.Repo = repo
			}
			rows = append(rows, pr)
		}
		stage, err := w.stageSummary(ctx, gh, repo, info.ProdOid, info.StageOid)
		if err != nil {
			if lg, ok := w.last[repo]; ok {
				stage = lg.stage
			}
			warns = append(warns, fmt.Sprintf("%s stage: %s", repo.Name, err))
			if ghapi.IsRateLimit(err) {
				st.Warnings = warns
				st.index()
				w.annotate(st)
				return st, warns, err
			}
		}
		st.Releases = append(st.Releases, rows...)
		if stage != nil {
			st.Stages = append(st.Stages, stage)
		}
		st.Repos = append(st.Repos, repo)
		w.last[repo] = lastGood{rows: rows, stage: stage, at: now.Local().Format("15:04")}
	}
	st.index()
	w.annotate(st)
	for _, pr := range st.Releases {
		if pr.State != "MERGED" {
			continue
		}
		sha := MergeSHA(pr)
		if sha == "" {
			continue
		}
		dep, err := w.deploy.Fetch(ctx, gh, pr.Repo, sha, false, now)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s#%d deploys: %s", pr.Repo.Name, pr.Number, err))
			if ghapi.IsRateLimit(err) {
				st.Warnings = warns
				return st, warns, err
			}
			continue
		}
		pr.Set(ExtDeploy, dep)
	}
	warns = append(warns, w.noteTracks(ctx, gh, st, now)...)
	st.Warnings = warns
	return st, warns, nil
}

// rateLimit is err when it is a rate limit, else nil: the one error a
// Refresh returns rather than turns into a warning.
func rateLimit(err error) error {
	if ghapi.IsRateLimit(err) {
		return err
	}
	return nil
}

// clonePRs copies the last-good rows before they are re-annotated. The
// originals may be on screen: the UI goroutine reads their Ext maps
// while this goroutine would write them, and a Go map must not be
// written while it is read.
func clonePRs(in []*model.PR) []*model.PR {
	out := make([]*model.PR, len(in))
	for i, p := range in {
		c := *p
		c.Ext = maps.Clone(p.Ext)
		out[i] = &c
	}
	return out
}

func (w *Workflow) keep() time.Duration {
	if w.cfg.MergedKeepSecs <= 0 {
		return 12 * time.Hour
	}
	return time.Duration(w.cfg.MergedKeepSecs) * time.Second
}

// annotate writes the per-PR info the renderers and actions read. CI
// comes from the rollup here when the token can see it; the core fills
// ExtCI through the Actions fallback for the rest (State.CIPRs).
func (w *Workflow) annotate(st *State) {
	for _, pr := range st.Releases {
		kind := w.Kind(pr)
		old, _ := pr.Get(ExtRel).(*Info)
		info := &Info{Kind: kind, Repo: pr.Repo.Name}
		if old != nil {
			info.MergedSHA = old.MergedSHA
		}
		pr.Set(ExtRel, info)
		if kind == KindHotfix || kind == KindRelease {
			pr.Set(ExtIcon, kind)
		}
		if c, ok := model.ClusterCI(pr); ok {
			pr.Set(ExtCI, c)
		}
	}
}

// Annotate is a no-op: Refresh annotates the state it builds (the
// core's PRs are not releases), and a second pass would only redo it.
func (w *Workflow) Annotate(_ []*model.PR, _ []*model.Issue, _ workflow.State) {}

// Apply stores the state.
func (w *Workflow) Apply(app workflow.App, s workflow.State) {
	st, _ := s.(*State)
	w.mu.Lock()
	w.cur = st
	w.mu.Unlock()
	app.SetState(Name, st)
}

// AfterApply says so when a merged release's next environment starts
// waiting for me (once per environment), pulls the next refresh in while
// any deploy waits or runs, and rebuilds an open release's card.
func (w *Workflow) AfterApply(app workflow.App) {
	st := w.stateOf(app)
	if st == nil {
		return
	}
	live := false
	for _, pr := range st.Releases {
		dep, ok := pr.Get(ExtDeploy).(deploys.State)
		if !ok {
			continue
		}
		if dep.Phase == "waiting" || dep.Phase == "running" {
			live = true
		}
		for _, wt := range dep.Waits {
			if !wt.Mine {
				continue
			}
			key := seenKey{pr.Repo.Name, dep.SHA, wt.EnvName}
			w.mu.Lock()
			seen := w.seen[key]
			w.seen[key] = true
			w.mu.Unlock()
			if !seen {
				app.Notice(fmt.Sprintf("%s#%d: %s now waits for your approval - press D on its row", pr.Repo.Name, pr.Number, wt.EnvName))
			}
		}
	}
	if live {
		app.RefreshSoon(w.refreshEvery())
	}
	w.noteItems(app, st)
	for _, pr := range st.Releases {
		w.announce(app, pr)
	}
	if pr, ok := app.Detail().(*model.PR); ok && pr != nil && w.isRelease(pr, app.DetailListed(), st) {
		app.BumpDetail()
	}
}

func (w *Workflow) refreshEvery() time.Duration {
	return deploys.RefreshInterval(w.cfg.DeployRefreshSecs)
}

// stateOf is the applied state as the app holds it.
func (w *Workflow) stateOf(app workflow.App) *State {
	if st, ok := app.State(Name).(*State); ok {
		return st
	}
	return nil
}

// ---- helpers shared by the hooks ----

// Kind classifies a PR by its head branch: release, hotfix or other.
func (w *Workflow) Kind(pr *model.PR) string {
	return KindOf(pr.HeadRefName, w.cfg.ReleasePrefix, w.cfg.HotfixPrefix)
}

// KindOf classifies a head branch name by the configured prefixes.
func KindOf(head, releasePrefix, hotfixPrefix string) string {
	if hotfixPrefix != "" && strings.HasPrefix(head, hotfixPrefix) {
		return KindHotfix
	}
	if releasePrefix != "" && strings.HasPrefix(head, releasePrefix) {
		return KindRelease
	}
	return KindOther
}

// InfoOf is the release info on a PR, or nil.
func InfoOf(pr *model.PR) *Info {
	if pr == nil {
		return nil
	}
	i, _ := pr.Get(ExtRel).(*Info)
	return i
}

// infoFor is the release info of a detail PR: its own, its listed row's,
// or the state's row with its identity.
func (w *Workflow) infoFor(pr, listed *model.PR, st *State) *Info {
	if i := InfoOf(pr); i != nil {
		return i
	}
	if i := InfoOf(listed); i != nil {
		return i
	}
	if st != nil && pr != nil {
		return InfoOf(st.Find(pr.Ident()))
	}
	return nil
}

func (w *Workflow) isRelease(pr, listed *model.PR, st *State) bool {
	return w.infoFor(pr, listed, st) != nil
}

// NotARelease reports an open PR into production from a branch that is
// neither release/* nor hotfix/*: only release cuts and hotfixes ship to
// production, so a, M and D refuse it. A row with no head branch name is
// not judged.
func (w *Workflow) NotARelease(pr *model.PR) bool {
	kind := ""
	if i := InfoOf(pr); i != nil {
		kind = i.Kind
	}
	if kind == "" && pr.HeadRefName != "" {
		kind = w.Kind(pr)
	}
	return kind == KindOther
}

// NotAReleaseWhy explains the refusal.
func (w *Workflow) NotAReleaseWhy(pr *model.PR) string {
	head := pr.HeadRefName
	if head == "" {
		head = "its branch"
	}
	return fmt.Sprintf("not a release: %s is neither %s* nor %s*; only release cuts and hotfixes ship to %s",
		head, w.cfg.ReleasePrefix, w.cfg.HotfixPrefix, w.cfg.ProductionBranch)
}

// MergedRecently reports a MERGED PR merged within keep.
func MergedRecently(pr *model.PR, now time.Time, keep time.Duration) bool {
	if pr.State != "MERGED" || pr.MergedAt == nil || pr.MergedAt.IsZero() {
		return false
	}
	return now.Sub(*pr.MergedAt) <= keep
}

// MergeSHA is the merge commit of a merged release: GraphQL's
// mergeCommit, or the sha M got back this session.
func MergeSHA(pr *model.PR) string {
	if pr.MergeCommit != nil && pr.MergeCommit.Oid != "" {
		return pr.MergeCommit.Oid
	}
	if i := InfoOf(pr); i != nil {
		return i.MergedSHA
	}
	return ""
}

// Label is the chip on a release row. reviewDecision is null on a repo
// whose ruleset asks 0 approvals, so the opinionated reviews are the
// fallback.
func (w *Workflow) Label(pr *model.PR, me string, localApproved bool) string {
	if w.NotARelease(pr) && pr.State != "MERGED" {
		return LabelOther
	}
	if pr.IsDraft {
		return model.LabelDraft
	}
	if pr.State == "MERGED" {
		return model.LabelMerged
	}
	if localApproved {
		return model.LabelApprovedMe
	}
	if me != "" && pr.MyReviewState(me) == "APPROVED" {
		return model.LabelApprovedMe
	}
	if pr.ReviewDecision == "CHANGES_REQUESTED" {
		return model.LabelChanges
	}
	for _, r := range pr.Opinions.Nodes {
		if r.State == "APPROVED" {
			who := r.Author.Login
			if who == "" {
				who = "?"
			}
			return "approved by " + who
		}
	}
	if pr.ReviewDecision == "APPROVED" {
		return model.LabelApproved
	}
	return LabelNeeds
}

// LabelStyle is the chip style of a label.
func LabelStyle(label string) string {
	switch label {
	case LabelNeeds:
		return "chip.await"
	case LabelOther:
		return "chip.state"
	}
	if s, ok := model.ChipForLabel[label]; ok {
		return s
	}
	return "chip.approved"
}

// InReleaseWindow reports whether an approved release may be merged at
// its slot right now: from the window's release_from to its end, in its
// zone. Without a window workflow there is no slot: always true.
func (w *Workflow) InReleaseWindow(now time.Time) bool {
	if w.win == nil {
		return true
	}
	return w.win.Local().InSpan(now, w.win.ReleaseFrom, w.win.End)
}

// nightPhraser is what the window workflow may implement to name the
// night a plain approval queues for ("tonight's", "tomorrow night's").
type nightPhraser interface {
	QueueNightPhrase(now time.Time) string
}

// NightPhrase is whose window an approval queues for right now:
// "tonight's 23:00 ET" until the cutoff, "tomorrow night's 23:00 ET"
// after it; "the maintenance" when there is no window config.
func (w *Workflow) NightPhrase(now time.Time) string {
	if w.win == nil {
		return "the maintenance"
	}
	loc := w.win.Local()
	when := loc.T(w.win.Start)
	if np, ok := w.window.(nightPhraser); ok {
		if s := np.QueueNightPhrase(now); s != "" {
			return s + " " + when
		}
	}
	lt := loc.In(now)
	cur := clock.HM{H: lt.Hour(), M: lt.Minute()}
	if cur.Minutes() >= w.win.Cutoff.Minutes() {
		return "tomorrow night's " + when
	}
	return "tonight's " + when
}

// where is "repo#N": a release lives in another repo than the reviews.
func where(pr *model.PR) string { return model.Ref(pr.Repo, pr.Number, true) }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
