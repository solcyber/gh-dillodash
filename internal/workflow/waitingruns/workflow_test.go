package waitingruns

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

var (
	infra = model.Repo{Owner: "acme", Name: "infra"}
	etl   = model.Repo{Owner: "acme", Name: "etl-jobs"}
	now   = time.Date(2026, 10, 29, 15, 0, 0, 0, time.UTC)
)

func patchNow(t *testing.T) {
	t.Helper()
	old := clock.Now
	clock.Now = func() time.Time { return now }
	t.Cleanup(func() { clock.Now = old })
}

func run(id int64, name, sha string, age time.Duration) model.WorkflowRun {
	return model.WorkflowRun{ID: id, Name: name, HeadSHA: sha, Status: "waiting", Event: "workflow_dispatch",
		HeadBranch: "main", Actor: model.Actor{Login: "bob"}, DisplayTitle: name + " from main",
		HTMLURL: fmt.Sprintf("https://github.com/acme/infra/actions/runs/%d", id), CreatedAt: now.Add(-age)}
}

func pending(env string, id int64, mine bool, reviewers ...string) model.PendingDeployment {
	var p model.PendingDeployment
	p.Environment.ID, p.Environment.Name, p.CurrentUserCanApprove = id, env, mine
	for _, r := range reviewers {
		var dr model.DeploymentReviewer
		if t, ok := strings.CutPrefix(r, "team:"); ok {
			dr.Type, dr.Reviewer.Slug = "Team", t
		} else {
			dr.Type, dr.Reviewer.Login = "User", r
		}
		p.Reviewers = append(p.Reviewers, dr)
	}
	return p
}

// newWF is the workflow on acme/infra and acme/etl-jobs with an app as
// alice in the list view.
func newWF(t *testing.T) (*Workflow, *wftest.App) {
	t.Helper()
	patchNow(t)
	cfg := config.Default()
	cfg.Reviews.Repos = []model.Repo{infra}
	wr := config.DefaultWaitingRuns()
	wr.Repos = []model.Repo{infra, etl}
	cfg.WaitingRuns = wr
	app := wftest.New(cfg, "alice")
	app.ViewName = "list"
	return New(wr), app
}

// refresh runs a refresh and applies it, as the dashboard does.
func refresh(t *testing.T, w *Workflow, app *wftest.App, prs ...*model.PR) ([]string, error) {
	t.Helper()
	st, warns, err := w.Refresh(context.Background(), app.Fake, app.RepoL, prs, nil)
	w.Apply(app, st)
	return warns, err
}

func ids(rows []workflow.Row) []int64 {
	var out []int64
	for _, r := range rows {
		if it, ok := r.Item.(*Item); ok {
			out = append(out, it.Run.ID)
		}
	}
	return out
}

// TestRegisteredAndProvidesDeploys: the constructor is registered, on
// only with the table, and the set shows D in the list while it is on.
func TestRegisteredAndProvidesDeploys(t *testing.T) {
	ctor := workflow.Registry[Name]
	if ctor == nil {
		t.Fatal("not registered")
	}
	ctx := &workflow.Context{Config: config.Default()}
	if got, _ := ctor(ctx); got != nil {
		t.Error("off without [workflows.waiting_runs]")
	}
	ctx.Config.WaitingRuns = config.DefaultWaitingRuns()
	got, err := ctor(ctx)
	if err != nil || got == nil || got.Name() != Name {
		t.Fatalf("on with it: %v %v", got, err)
	}
	set, err := workflow.NewSet(nil, got)
	if err != nil {
		t.Fatal(err)
	}
	if !set.ProvidesDeploys() {
		t.Error("D shows in the list bar while waiting_runs is on")
	}
}

// TestRefreshListsWaitingRunsAcrossRepos: every configured repo's waiting
// runs with their environments, the one waiting longest first.
func TestRefreshListsWaitingRunsAcrossRepos(t *testing.T) {
	w, app := newWF(t)
	app.Fake.RepoRuns = map[model.Repo][]model.WorkflowRun{
		infra: {run(4242, "deploy", "s1", 10*time.Minute), {ID: 4243, Status: "in_progress", HeadSHA: "s9"}},
		etl:   {run(5151, "backfill", "s2", 2*time.Hour)},
	}
	app.Fake.Pending = map[int64][]model.PendingDeployment{
		4242: {pending("staging", 56, false), pending("prod", 55, true)},
		5151: {pending("prod", 77, false)},
	}
	warns, err := refresh(t, w, app)
	if err != nil || len(warns) != 0 {
		t.Fatalf("err %v warns %v", err, warns)
	}
	st := app.State(Name).(*State)
	if len(st.Items) != 2 || st.Items[0].Run.ID != 5151 || st.Items[1].Run.ID != 4242 {
		t.Fatalf("items: %+v", st.Items)
	}
	if st.Items[0].Repo != etl || st.Items[1].Repo != infra {
		t.Errorf("repos: %v %v", st.Items[0].Repo, st.Items[1].Repo)
	}
	if got := st.Items[1].Envs(); !reflect.DeepEqual(got, []string{"prod", "staging"}) {
		t.Errorf("envs sorted: %v", got)
	}
	if app.Fake.Reads["pending"] != 2 {
		t.Errorf("pending reads: %v", app.Fake.Reads)
	}
	rows := w.Sections(app, nil, "alice")
	if len(rows) != 3 || rows[0].Kind != "header" || rows[0].Item != "waiting approvals (2)" || rows[0].Meta["icon"] != "shield" {
		t.Fatalf("rows: %+v", rows)
	}
	if got := ids(rows); !reflect.DeepEqual(got, []int64{5151, 4242}) {
		t.Errorf("row order: %v", got)
	}
	if rows[1].Kind != RowRun || rows[1].Ident() != (model.Ident{Repo: etl, Number: 5151}) {
		t.Errorf("row ident: %+v", rows[1].Ident())
	}
}

// TestNothingWaitingNoSection: no runs, no header, no summary.
func TestNothingWaitingNoSection(t *testing.T) {
	w, app := newWF(t)
	if _, err := refresh(t, w, app); err != nil {
		t.Fatal(err)
	}
	if rows := w.Sections(app, nil, "alice"); rows != nil {
		t.Errorf("rows: %+v", rows)
	}
	if segs := w.SummarySegs(app); segs != nil {
		t.Errorf("summary: %+v", segs)
	}
}

// TestRunsOnListedPRsAreDropped: a run on a listed PR's head or merge
// commit shows under that PR (its D approves it), not here: the review
// PRs at refresh, and the release workflow's merged releases at
// Sections time.
func TestRunsOnListedPRsAreDropped(t *testing.T) {
	w, app := newWF(t)
	app.Fake.RepoRuns = map[model.Repo][]model.WorkflowRun{infra: {
		run(1001, "plan", "headsha", time.Minute),
		run(1002, "deploy", "mergesha", 2*time.Minute),
		run(1003, "deploy", "relsha", 3*time.Minute),
		run(1004, "rotate", "loose", 4*time.Minute),
		run(1005, "rotate", "laterlisted", 5*time.Minute),
	}}
	open := &model.PR{Repo: infra, Number: 3543, State: "OPEN", HeadRefOid: "headsha"}
	merged := &model.PR{Repo: infra, Number: 3550, State: "MERGED", HeadRefOid: "branchtip", MergeCommit: &model.MergeCommit{Oid: "mergesha"}}
	if _, err := refresh(t, w, app, open, merged); err != nil {
		t.Fatal(err)
	}
	st := app.State(Name).(*State)
	var got []int64
	for _, it := range st.Items {
		got = append(got, it.Run.ID)
	}
	// 1001 (an open PR's head) stays: D on an open PR does not approve it
	if !reflect.DeepEqual(got, []int64{1005, 1004, 1003, 1001}) {
		t.Fatalf("refresh kept %v", got)
	}
	if app.Fake.Reads["pending"] != 4 {
		t.Errorf("a dropped run's environments are not read: %v", app.Fake.Reads)
	}
	// at Sections time: the listed PRs as the app holds them now, and the
	// releases (the release workflow's state, through CIProvider)
	app.PRs = []*model.PR{{Repo: infra, Number: 3560, State: "MERGED", HeadRefOid: "tip", MergeCommit: &model.MergeCommit{Oid: "laterlisted"}}}
	app.SetState(releaseName, releases{{Repo: etl, Number: 42, State: "MERGED", MergeCommit: &model.MergeCommit{Oid: "relsha"}}})
	if got := ids(w.Sections(app, nil, "alice")); !reflect.DeepEqual(got, []int64{1004, 1001}) {
		t.Errorf("sections kept %v", got)
	}
	if s := text.SegsText(w.SummarySegs(app)); !strings.Contains(s, "2 runs waiting") {
		t.Errorf("the summary counts what is shown: %q", s)
	}
}

// releases stands in for the release workflow's state.
type releases []*model.PR

func (r releases) CIPRs() []*model.PR { return r }

// TestRepoReadErrorIsAWarning: a repo the token can't read is a warning
// naming the permission; the other repo still lists. A run whose
// environments can't be read is listed with a plain "waiting" chip.
func TestRepoReadErrorIsAWarning(t *testing.T) {
	w, app := newWF(t)
	app.Fake.RepoRuns = map[model.Repo][]model.WorkflowRun{etl: {run(5151, "backfill", "s2", time.Hour), run(5152, "backfill", "s3", time.Minute)}}
	app.Fake.RepoFail = map[model.Repo]error{infra: &ghapi.Error{Status: 403, Message: "Resource not accessible by personal access token"}}
	app.Fake.Pending = map[int64][]model.PendingDeployment{5151: {pending("prod", 77, true)}}
	warns, err := refresh(t, w, app)
	if err != nil {
		t.Fatalf("a read error is not the refresh's error: %v", err)
	}
	want := "waiting runs in infra: GitHub 403: Resource not accessible by personal access token — your token needs Actions: Read on infra"
	if !reflect.DeepEqual(warns, []string{want}) {
		t.Errorf("warns:\n got %q\nwant %q", warns, want)
	}
	if got := ids(w.Sections(app, nil, "alice")); !reflect.DeepEqual(got, []int64{5151, 5152}) {
		t.Errorf("etl-jobs still lists: %v", got)
	}

	app.Fake.Fail["pending"] = &ghapi.Error{Status: 502, Message: "Bad Gateway"}
	warns, err = refresh(t, w, app)
	if err != nil || len(warns) != 3 || warns[1] != "waiting runs in etl-jobs: run 5151's environments: GitHub 502 Bad Gateway" {
		t.Fatalf("warns %q err %v", warns, err)
	}
	st := app.State(Name).(*State)
	if len(st.Items) != 2 || st.Items[0].Pending != nil {
		t.Fatalf("items: %+v", st.Items)
	}
	if s := text.SegsText(chipSegs(st.Items[0], -1)); !strings.Contains(s, "waiting") {
		t.Errorf("chip without environments: %q", s)
	}
}

// TestRateLimitIsTheError: a rate limit is the refresh's error (the
// interface contract), the last good state comes back with it, and a
// refresh that never reached the workflow (nil) keeps what is shown.
func TestRateLimitIsTheError(t *testing.T) {
	w, app := newWF(t)
	app.Fake.RepoRuns = map[model.Repo][]model.WorkflowRun{infra: {run(4242, "deploy", "s1", time.Minute)}}
	app.Fake.Pending = map[int64][]model.PendingDeployment{4242: {pending("prod", 55, true)}}
	if _, err := refresh(t, w, app); err != nil {
		t.Fatal(err)
	}
	limit := &ghapi.Error{Status: 403, Message: "API rate limit exceeded", RetryAt: now.Add(time.Minute)}
	app.Fake.RepoFail = map[model.Repo]error{infra: limit}
	st, _, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	if !ghapi.IsRateLimit(err) {
		t.Fatalf("err = %v", err)
	}
	if s, ok := st.(*State); !ok || len(s.Items) != 1 || s.Items[0].Run.ID != 4242 {
		t.Errorf("the last good state comes back: %+v", st)
	}
	app.Fake.RepoFail = nil
	app.Fake.Fail["pending"] = limit
	if _, _, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil); !ghapi.IsRateLimit(err) {
		t.Errorf("a rate limit on the environments: %v", err)
	}
	w.Apply(app, nil)
	if got := ids(w.Sections(app, nil, "alice")); !reflect.DeepEqual(got, []int64{4242}) {
		t.Errorf("Apply(nil) keeps the rows: %v", got)
	}
	// no good state yet: the partial one, still with the error
	w2, _ := newWF(t)
	if st, _, err := w2.Refresh(context.Background(), app.Fake, nil, nil, nil); !ghapi.IsRateLimit(err) || st.(*State) == nil {
		t.Errorf("first refresh: %v %v", st, err)
	}
}

// render lays out and draws the section's run rows at width.
func render(app *wftest.App, w *Workflow, rows []workflow.Row, width int) [][]text.Seg {
	var items []workflow.Row
	for _, r := range rows {
		if r.Kind == RowRun {
			items = append(items, r)
		}
	}
	rk := w.RowKinds()[RowRun]
	L := rk.Layout(app, items, width, 6, 0)
	var out [][]text.Seg
	for i, r := range items {
		out = append(out, rk.Segs(app, r, L, width, i))
	}
	return out
}

func styleOf(segs []text.Seg, txt string) string {
	for _, s := range segs {
		if s.Text == txt {
			return s.Style
		}
	}
	return ""
}

// TestRowsRenderApproveAndWaitsChips: an environment the viewer can
// approve is a gold "approve: <env>" chip (and a gold title), one that
// waits on someone else a "waits: <env>" chip; the row carries the
// repo, the workflow, the branch, who started it, its age and title.
func TestRowsRenderApproveAndWaitsChips(t *testing.T) {
	w, app := newWF(t)
	app.Fake.RepoRuns = map[model.Repo][]model.WorkflowRun{
		infra: {run(4242, "deploy", "s1", 10*time.Minute)},
		etl:   {run(5151, "backfill", "s2", 2*time.Hour)},
	}
	app.Fake.Pending = map[int64][]model.PendingDeployment{
		4242: {pending("prod", 55, true), pending("staging", 56, false)},
		5151: {pending("prod", 77, false)},
	}
	if _, err := refresh(t, w, app); err != nil {
		t.Fatal(err)
	}
	rows := w.Sections(app, nil, "alice")
	lines := render(app, w, rows, 160)
	if len(lines) != 2 {
		t.Fatalf("lines: %d", len(lines))
	}
	etlLine, infraLine := lines[0], lines[1]
	if st := styleOf(infraLine, "approve: prod"); st != "chip.await" {
		t.Errorf("approve chip style %q in %+v", st, infraLine)
	}
	if st := styleOf(infraLine, "waits: staging"); st != "chip.state" {
		t.Errorf("waits chip style %q", st)
	}
	if st := styleOf(etlLine, "waits: prod"); st != "chip.state" {
		t.Errorf("theirs: %q in %+v", st, etlLine)
	}
	s := text.SegsText(infraLine)
	for _, want := range []string{"run", "infra", "deploy", "main", "bob", "10m", "deploy from main"} {
		if !strings.Contains(s, want) {
			t.Errorf("row lacks %q: %q", want, s)
		}
	}
	if st := styleOf(infraLine, "deploy from main"); st != "title.attn" {
		t.Errorf("my run's title: %q", st)
	}
	if st := styleOf(etlLine, "backfill from main"); st != "title" {
		t.Errorf("their run's title: %q", st)
	}
	// one repo: no repo column
	w.cfg.Repos = []model.Repo{infra}
	if s := text.SegsText(render(app, w, rows, 160)[1]); strings.Contains(s, "infra") {
		t.Errorf("single repo row names it: %q", s)
	}
	// narrow: the chips stay, the age and branch go first
	w.cfg.Repos = []model.Repo{infra, etl}
	if s := text.SegsText(render(app, w, rows, 70)[1]); !strings.Contains(s, "approve: prod") || strings.Contains(s, "10m") {
		t.Errorf("narrow row: %q", s)
	}
	// the summary counts the runs and the ones on me
	if s := w.SummarySegs(app); len(s) != 1 || !strings.HasSuffix(s[0].Text, "2 runs waiting, 1 on you (D)") || s[0].Style != "sum.attn" {
		t.Errorf("summary: %+v", s)
	}
}

// TestRowURLOpenAndFilter: o and y give the run's page, Enter explains,
// and the filter matches the repo, workflow, title, environments and
// who started it.
func TestRowURLOpenAndFilter(t *testing.T) {
	w, app := newWF(t)
	r := run(4242, "deploy", "s1", time.Minute)
	r.Actor.Login = "carol"
	app.Fake.RepoRuns = map[model.Repo][]model.WorkflowRun{infra: {r}, etl: {run(5151, "backfill", "s2", time.Hour)}}
	app.Fake.Pending = map[int64][]model.PendingDeployment{4242: {pending("prod", 55, true)}, 5151: {pending("staging", 77, false)}}
	if _, err := refresh(t, w, app); err != nil {
		t.Fatal(err)
	}
	rows := w.Sections(app, nil, "alice")
	rk := w.RowKinds()[RowRun]
	if u := rk.URL(app, rows[2]); u != r.HTMLURL {
		t.Errorf("URL = %q", u)
	}
	it := rows[2].Item.(*Item)
	it.Run.HTMLURL = ""
	if u := rk.URL(app, rows[2]); u != "https://github.com/acme/infra/actions/runs/4242" {
		t.Errorf("built URL = %q", u)
	}
	if !rk.Open(app, rows[2]) || app.Last() != "notice: infra deploy run 4242 is an Actions run, not a PR — o opens it on GitHub; D approves it" {
		t.Errorf("open: %q", app.Last())
	}
	if rk.Title(rows[2]) != "deploy from main" {
		t.Errorf("title %q", rk.Title(rows[2]))
	}
	for terms, want := range map[string][]int64{
		"carol": {4242}, "staging": {5151}, "etl": {5151}, "deploy prod": {4242}, "backfill from": {5151},
		"waiting": {5151, 4242}, "nothing": nil,
	} {
		if got := ids(w.Sections(app, strings.Fields(terms), "alice")); !reflect.DeepEqual(got, want) {
			t.Errorf("filter %q: %v, want %v", terms, got, want)
		}
	}
	if s := w.SearchText(rows[2]); !strings.Contains(s, "acme/infra") || !strings.Contains(s, "carol") {
		t.Errorf("SearchText %q", s)
	}
	if w.SearchText(workflow.Row{Kind: "pr", Item: &model.PR{}}) != "" {
		t.Error("other rows are not ours")
	}
}

// deployRow sets up one infra run waiting on prod (mine) and staging
// (someone else's) and selects its row.
func deployRow(t *testing.T) (*Workflow, *wftest.App, workflow.Row) {
	t.Helper()
	w, app := newWF(t)
	app.Fake.RepoRuns = map[model.Repo][]model.WorkflowRun{infra: {run(4242, "deploy", "s1", time.Minute)}}
	app.Fake.Pending = map[int64][]model.PendingDeployment{4242: {pending("prod", 55, true), pending("staging", 56, false, "carol")}}
	if _, err := refresh(t, w, app); err != nil {
		t.Fatal(err)
	}
	app.RowList = w.Sections(app, nil, "alice")
	app.Sel = 1
	row, ok := app.Selected()
	if !ok || row.Kind != RowRun {
		t.Fatalf("selected %+v", row)
	}
	app.Refreshes = nil
	return w, app, row
}

// TestDeployApprovesMyEnvironments: D confirms with the environments the
// viewer can approve, then approves only those, each with the rendered
// approval_comment.
func TestDeployApprovesMyEnvironments(t *testing.T) {
	w, app, row := deployRow(t)
	app.Confirms = []bool{true}
	if !w.DeployRow(app, row) {
		t.Fatal("not handled")
	}
	wantQ := "approve 1 deployment of infra deploy run 4242 as alice?\nprod (deploy, run 4242)\nnot yours: staging"
	if len(app.Questions) != 1 || app.Questions[0] != wantQ {
		t.Errorf("question:\n got %q\nwant %q", app.Questions, wantQ)
	}
	calls := app.Fake.DeployApprovals
	if len(calls) != 1 || calls[0].Repo != infra || calls[0].RunID != 4242 || !reflect.DeepEqual(calls[0].EnvIDs, []int64{55}) {
		t.Fatalf("approvals: %+v", calls)
	}
	if calls[0].Body != "dillodash: approved deploy run 4242 in acme/infra" {
		t.Errorf("comment = %q", calls[0].Body)
	}
	if app.Last() != "ok: approved 1 deployment of infra deploy run 4242 (prod)" {
		t.Errorf("status = %q", app.Last())
	}
	if len(app.Celebrated) != 1 || !reflect.DeepEqual(app.Refreshes, []bool{true}) {
		t.Errorf("celebrated %v refreshes %v", app.Celebrated, app.Refreshes)
	}
	if app.Fake.Reads["pending"] != 2 {
		t.Errorf("D re-reads the environments: %v", app.Fake.Reads)
	}

	// a configured comment, two environments of mine: one call each
	w.cfg.ApprovalComment = "ok {workflow} #{run} ({repo})"
	app.Fake.Pending[4242] = []model.PendingDeployment{pending("prod", 55, true), pending("dr", 57, true)}
	app.Fake.DeployApprovals, app.Fake.Calls = nil, nil
	app.Confirms = []bool{true}
	w.DeployRow(app, row)
	calls = app.Fake.DeployApprovals
	if len(calls) != 2 || calls[0].EnvIDs[0] != 57 || calls[1].EnvIDs[0] != 55 || calls[0].Body != "ok deploy #4242 (acme/infra)" || calls[1].Body != calls[0].Body {
		t.Errorf("approvals: %+v", calls)
	}
	if app.Last() != "ok: approved 2 deployments of infra deploy run 4242 (dr, prod)" {
		t.Errorf("status = %q", app.Last())
	}
}

// TestDeployRefusesWhenNoneIsMine: nothing approvable by the viewer is
// a notice naming the environments and who can approve them.
func TestDeployRefusesWhenNoneIsMine(t *testing.T) {
	w, app, row := deployRow(t)
	app.Fake.Pending[4242] = []model.PendingDeployment{pending("prod", 55, false, "bob", "team:deployers"), pending("staging", 56, false, "carol")}
	if !w.DeployRow(app, row) {
		t.Fatal("not handled")
	}
	want := "notice: infra deploy run 4242 waits on prod, staging, but alice is not a reviewer (prod: bob, team deployers; staging: carol)"
	if app.Last() != want {
		t.Errorf("status:\n got %q\nwant %q", app.Last(), want)
	}
	if len(app.Questions) != 0 || len(app.Fake.DeployApprovals) != 0 {
		t.Errorf("asked %v, approved %v", app.Questions, app.Fake.DeployApprovals)
	}
	// alice dispatched it and is a reviewer: GitHub's self-review rule
	row.Item.(*Item).Run.Actor = model.Actor{Login: "alice"}
	app.Fake.Pending[4242] = []model.PendingDeployment{pending("prod", 55, false, "alice", "bob")}
	w.DeployRow(app, row)
	if want := "notice: infra deploy run 4242 waits on prod, but alice dispatched it and the environment forbids self-review (prod: alice, bob)"; app.Last() != want {
		t.Errorf("status:\n got %q\nwant %q", app.Last(), want)
	}
	row.Item.(*Item).Run.Actor = model.Actor{}
	// reviewers GitHub didn't name: just the environments
	app.Fake.Pending[4242] = []model.PendingDeployment{pending("prod", 55, false)}
	w.DeployRow(app, row)
	if app.Last() != "notice: infra deploy run 4242 waits on prod, but alice is not a reviewer" {
		t.Errorf("status = %q", app.Last())
	}
	// approved meanwhile: nothing waits, and the list refreshes
	app.Fake.Pending[4242] = nil
	w.DeployRow(app, row)
	if app.Last() != "notice: infra deploy run 4242 waits on no environment now — it was approved or rejected since the last refresh" ||
		!reflect.DeepEqual(app.Refreshes, []bool{true}) {
		t.Errorf("status = %q refreshes %v", app.Last(), app.Refreshes)
	}
}

// TestDeployConfirmNoCancels: n at the confirm approves nothing.
func TestDeployConfirmNoCancels(t *testing.T) {
	w, app, row := deployRow(t)
	app.Confirms = []bool{false}
	w.DeployRow(app, row)
	if app.Last() != "notice: deployment approval cancelled" || len(app.Fake.DeployApprovals) != 0 || len(app.Refreshes) != 0 {
		t.Errorf("status %q approvals %v refreshes %v", app.Last(), app.Fake.DeployApprovals, app.Refreshes)
	}
}

// TestDeployFailures: a failed read or approval says what and, for a
// 403, which permission.
func TestDeployFailures(t *testing.T) {
	w, app, row := deployRow(t)
	app.Fake.Fail["approve_deployments"] = &ghapi.Error{Status: 403, Message: "Resource not accessible by personal access token"}
	app.Confirms = []bool{true}
	w.DeployRow(app, row)
	want := "fail: approved 0 of 1; failed: prod: GitHub 403: Resource not accessible by personal access token — your token needs Deployments: Read and write on infra"
	if app.Last() != want {
		t.Errorf("status:\n got %q\nwant %q", app.Last(), want)
	}
	if len(app.Celebrated) != 0 || !reflect.DeepEqual(app.Refreshes, []bool{true}) {
		t.Errorf("celebrated %v refreshes %v", app.Celebrated, app.Refreshes)
	}
	app.Fake.Fail["pending"] = errors.New("network error: connection reset")
	w.DeployRow(app, row)
	if app.Last() != "fail: couldn't read infra deploy run 4242's environments: network error: connection reset" {
		t.Errorf("status = %q", app.Last())
	}
}

// TestDeployRowOnlyTakesItsRows: any other row goes on to the core's D.
func TestDeployRowOnlyTakesItsRows(t *testing.T) {
	w, app := newWF(t)
	if w.DeployRow(app, workflow.Row{Kind: "pr", Item: &model.PR{State: "MERGED"}}) {
		t.Error("a PR row is the core's")
	}
	if w.DeployRow(app, workflow.Row{Kind: RowRun, Item: "not an item"}) {
		t.Error("a row without an item")
	}
	// the set asks every workflow that implements it
	set, err := workflow.NewSet(nil, w)
	if err != nil {
		t.Fatal(err)
	}
	_, app, row := deployRow(t)
	app.Confirms = []bool{true}
	if !set.DeployRow(app, row) || len(app.Fake.DeployApprovals) != 1 {
		t.Errorf("Set.DeployRow: %v", app.Fake.DeployApprovals)
	}
	if set.DeployRow(app, workflow.Row{Kind: "pr"}) {
		t.Error("Set.DeployRow on a PR row")
	}
	var none *workflow.Set
	if none.DeployRow(app, row) {
		t.Error("nil set")
	}
}

// TestHelpAndLegend: the help says what D does on a run, the legend
// shows both chips.
func TestHelpAndLegend(t *testing.T) {
	w, _ := newWF(t)
	if h := w.HelpRows(); len(h) != 1 || !strings.HasPrefix(h[0][1], "D  on a waiting run") {
		t.Errorf("help: %v", h)
	}
	l := text.SegsText(w.LegendRows()[0])
	if !strings.Contains(l, "approve: prod") || !strings.Contains(l, "waits: prod") {
		t.Errorf("legend: %q", l)
	}
}
