package promote

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

var (
	web = model.Repo{Owner: "acme", Name: "web"}
	now = time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)
)

func at(h time.Duration) *time.Time { t := now.Add(-h); return &t }

// merged is a PR merged into base h hours ago with merge commit sha.
func merged(n int, base, head, title, sha string, h time.Duration) *model.PR {
	return &model.PR{Repo: web, Number: n, Title: title, State: "MERGED", BaseRefName: base, HeadRefName: head,
		MergedAt: at(h), CreatedAt: now.Add(-h - time.Hour), MergeCommit: &model.MergeCommit{Oid: sha},
		Author: model.Actor{Login: "bob"}}
}

func open(n int, base, head, title, headOid string, h time.Duration) *model.PR {
	return &model.PR{Repo: web, Number: n, Title: title, State: "OPEN", BaseRefName: base, HeadRefName: head,
		HeadRefOid: headOid, CreatedAt: now.Add(-h), Author: model.Actor{Login: "alice"}, MergeStateStatus: "CLEAN"}
}

// compare is prod...stage carrying the units (squashes "title (#N)"),
// oldest first, each on the one before.
func compare(units ...int) ghapi.CompareResult {
	var c ghapi.CompareResult
	parent := "base"
	for _, u := range units {
		sha := "s" + itoa(u)
		c.Commits = append(c.Commits, ghapi.CompareCommit{SHA: sha, Message: "unit (#" + itoa(u) + ")", Parents: []string{parent}})
		parent = sha
	}
	c.AheadBy = len(units)
	return c
}

func itoa(n int) string { return strconv.Itoa(n) }

func patchNow(t *testing.T) {
	t.Helper()
	old := clock.Now
	clock.Now = func() time.Time { return now }
	t.Cleanup(func() { clock.Now = old })
}

func newWF(t *testing.T) (*Workflow, *wftest.App) {
	t.Helper()
	patchNow(t)
	cfg := config.Default()
	cfg.Reviews.Requested = []model.Repo{web}
	pc := config.DefaultPromote()
	pc.Repos = []model.Repo{web}
	cfg.Promote = pc
	app := wftest.New(cfg, "alice", web)
	app.ViewName = "list"
	workflow.ResetMembershipCache()
	return New(pc), app
}

func refresh(t *testing.T, w *Workflow, app *wftest.App) []string {
	t.Helper()
	st, warns, err := w.Refresh(context.Background(), app.Fake, app.RepoL, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.Apply(app, st)
	return warns
}

func numbers(cs []*Change) []int {
	var out []int
	for _, c := range cs {
		out = append(out, c.Number())
	}
	return out
}

// TestStageOrder: the first-parent chain, oldest first; a side commit
// inside a merged branch is not a unit; a short page is reported.
func TestStageOrder(t *testing.T) {
	c := compare(12, 14)
	c.Commits = append(c.Commits,
		ghapi.CompareCommit{SHA: "side", Message: "wip", Parents: []string{"s14"}},
		ghapi.CompareCommit{SHA: "m15", Message: "Merge pull request #15 from acme/x\n\nbody", Parents: []string{"s14", "side"}})
	c.AheadBy = 4
	units, ahead, ok := StageOrder(c)
	if !reflect.DeepEqual(units, []int{12, 14, 15}) || !ok || !ahead["side"] || !ahead["s12"] {
		t.Fatalf("units %v ok %v ahead %v", units, ok, ahead)
	}
	c.AheadBy = 300
	if _, _, ok := StageOrder(c); ok {
		t.Error("a compare cut short is not ok")
	}
}

// TestLocate places each kind of change.
func TestLocate(t *testing.T) {
	set := ghapi.PromoteSet{
		FirstMerged: []*model.PR{
			merged(201, "dev", "feat/a", "first tier only", "d201", 30),
			merged(202, "dev", "feat/b", "named by a merged unit", "d202", 40),
			merged(203, "dev", "feat/c", "in an open promotion", "d203", 20),
			merged(204, "dev", "feat/d", "in a whole-tier promotion", "d204", 50),
			merged(205, "dev", "feat/e", "released", "d205", 90),
			merged(206, "dev", "main", "back-merge", "d206", 10),
		},
		StageMerged: []*model.PR{
			merged(301, "stage", "promote/202-stage", "Promote #202 to stage", "s301", 35),
			merged(302, "stage", "dev", "Promote dev → stage", "s302", 45),
			merged(303, "stage", "stage-only", "a unit of its own", "s303", 25),
			merged(304, "stage", "promote/205-stage", "Promote #205", "s304", 80),
			merged(305, "stage", "main", "Back-merge main → stage", "s305", 5),
		},
		StageOpen: []*model.PR{open(306, "stage", "promote/203-stage", "Promote #203 (see acme/other#201)", "h306", 2)},
	}
	_, ahead, _ := StageOrder(compare(302, 301, 303))
	in := Inputs{Repo: web, First: "dev", Stage: "stage", Prod: "main", Set: set,
		Units: []int{302, 301, 303}, Ahead: ahead, AheadOK: true,
		Go: map[int]bool{301: true, 303: true}, Hold: map[int]bool{302: true},
		Contains: func(sha, head string) bool { return sha == "d204" && head == "h302" },
	}
	set.StageMerged[1].HeadRefOid = "h302"
	changes, released := Locate(in)
	if released != 1 {
		t.Errorf("released = %d, want 1 (#205 via #304)", released)
	}
	by := map[int]*Change{}
	for _, c := range changes {
		by[c.Number()] = c
	}
	want := map[int]struct {
		where Where
		promo int
		tag   string
		block int
	}{
		201: {OnFirst, 0, "", 0},
		202: {OnStage, 301, "go", 302},
		203: {Promoting, 306, "", 0},
		204: {OnStage, 302, "hold", 0},
		303: {OnStage, 303, "go", 302},
	}
	if len(by) != len(want) {
		t.Fatalf("changes %v, want %v", numbers(changes), want)
	}
	for n, w := range want {
		c := by[n]
		if c == nil {
			t.Errorf("#%d missing", n)
			continue
		}
		promo := 0
		if c.Promo != nil {
			promo = c.Promo.Number
		}
		if c.Where != w.where || promo != w.promo || c.Tag != w.tag || c.BlockedBy != w.block {
			t.Errorf("#%d: where %d promo %d tag %q blocked %d; want %+v", n, c.Where, promo, c.Tag, c.BlockedBy, w)
		}
	}
	// furthest along first
	if changes[0].Where != OnStage || changes[len(changes)-1].Where != OnFirst {
		t.Errorf("order %v", numbers(changes))
	}
}

// TestRefreshAndRows: a refresh reads the repo, the runs on each change's
// tier commit, and draws the rows with their chips.
func TestRefreshAndRows(t *testing.T) {
	w, app := newWF(t)
	app.Fake.Promote = map[model.Repo]ghapi.PromoteSet{web: {
		FirstMerged: []*model.PR{merged(201, "dev", "feat/a", "opt-out flag", "d201", 3)},
		StageMerged: []*model.PR{merged(301, "stage", "promote/150-stage", "Promote #150 to stage", "s301", 2)},
	}}
	app.Fake.Compares = map[string]ghapi.CompareResult{"main...stage": compare(301)}
	app.Fake.Runs = map[string][]model.WorkflowRun{
		"d201": {{ID: 1, Name: "deploy", Status: "completed", Conclusion: "success", HeadSHA: "d201"}},
		"s301": {{ID: 3, Name: "migrate", Status: "waiting", HeadSHA: "s301"}, {ID: 2, Name: "migrate", Status: "completed", Conclusion: "failure"}},
	}
	if warns := refresh(t, w, app); len(warns) != 0 {
		t.Fatalf("warns %v", warns)
	}
	rows := w.Sections(app, nil, "alice")
	if len(rows) != 3 || rows[0].Item != "promote (2)" {
		t.Fatalf("rows %+v", rows)
	}
	first := rows[1].Item.(*Change)
	if first.Number() != 301 || first.Where != OnStage || len(first.Runs) != 1 || first.Runs[0].ID != 3 {
		t.Fatalf("first row %+v runs %+v", first, first.Runs)
	}
	L := w.layout(app, rows[1:], 140, 5, 4)
	line := text.SegsText(w.segs(app, rows[1], L, 140, 0))
	for _, want := range []string{"#301", "stage · untagged", "stage waits · D", "Promote #150 to stage"} {
		if !strings.Contains(line, want) {
			t.Errorf("row lacks %q: %q", want, line)
		}
	}
	line = text.SegsText(w.segs(app, rows[2], L, 140, 0))
	for _, want := range []string{"#201", "on dev", "dev", "opt-out flag"} {
		if !strings.Contains(line, want) {
			t.Errorf("row lacks %q: %q", want, line)
		}
	}
	if s := text.SegsText(w.SummarySegs(app)); !strings.Contains(s, "2 to promote, 1 deploy waits (D)") {
		t.Errorf("summary %q", s)
	}
	// a settled commit's runs are cached; the waiting one is read again
	before := app.Fake.Reads["workflow_runs"]
	refresh(t, w, app)
	if got := app.Fake.Reads["workflow_runs"] - before; got != 1 {
		t.Errorf("workflow_runs reads on the second refresh = %d, want 1", got)
	}
	if rows := w.Sections(app, []string{"opt-out"}, "alice"); len(rows) != 2 {
		t.Errorf("filter: %d rows", len(rows))
	}
}

// TestRefreshKeepsLastOnError: a repo that fails keeps its last rows.
func TestRefreshKeepsLastOnError(t *testing.T) {
	w, app := newWF(t)
	app.Fake.Promote = map[model.Repo]ghapi.PromoteSet{web: {FirstMerged: []*model.PR{merged(201, "dev", "feat/a", "x", "d201", 3)}}}
	refresh(t, w, app)
	app.Fake.Fail = map[string]error{"promote_prs": &ghapi.Error{Message: "boom"}}
	warns := refresh(t, w, app)
	if len(warns) != 1 || !strings.Contains(warns[0], "boom") {
		t.Errorf("warns %v", warns)
	}
	if cs := state(app).Changes(); len(cs) != 1 {
		t.Errorf("kept %d changes", len(cs))
	}
}

func selectChange(app *wftest.App, c *Change) {
	app.RowList = []workflow.Row{{Kind: RowChange, Item: c}}
	app.Sel = 0
}

func pickFixture(app *wftest.App) *Change {
	f := app.Fake
	f.Heads = map[string]string{"acme/web@stage": "stagehead"}
	f.Changes = map[string]model.CommitChange{"d201": {SHA: "d201", Parents: []string{"p201"}, Files: []model.ChangedFile{
		{Filename: "a.sql", Status: "modified", SHA: "a2"},
		{Filename: "new.go", Status: "added", SHA: "n1"},
		{Filename: "old.txt", Status: "removed", SHA: ""},
		{Filename: "b/new.md", PreviousFilename: "b/old.md", Status: "renamed", SHA: "r1"},
		{Filename: "same.go", Status: "modified", SHA: "s2"},
	}}}
	f.BlobSHAs = map[string]string{
		"p201:a.sql": "a1", "stagehead:a.sql": "a1",
		"p201:old.txt": "o1", "stagehead:old.txt": "o1",
		"p201:b/old.md": "r0", "stagehead:b/old.md": "r0",
		"p201:same.go": "s1", "stagehead:same.go": "s2",
	}
	f.BlobData = map[string]string{"a2": "QQ==", "n1": "Tg==", "r1": "Ug=="}
	return &Change{Repo: web, First: merged(201, "dev", "feat/a", "opt-out flag", "d201", 3), Where: OnFirst}
}

// TestPromoteOpensThePromotionPR: p on a first-tier change applies its
// files to the staging head on a new branch, in one commit, and opens the
// PR; a file already on staging is skipped.
func TestPromoteOpensThePromotionPR(t *testing.T) {
	w, app := newWF(t)
	c := pickFixture(app)
	selectChange(app, c)
	app.Confirms = []bool{true}
	w.doPromote(app)
	ops := []string{}
	for _, call := range app.Fake.Calls {
		ops = append(ops, call.Op)
	}
	if !reflect.DeepEqual(ops, []string{"create_branch", "commit_on_branch", "create_pr"}) {
		t.Fatalf("calls %v; log %v", ops, app.Log)
	}
	commit := app.Fake.Calls[1]
	if commit.Ref != "promote/201-stage" || commit.SHA != "stagehead" || commit.Title != "Promote #201 to stage: opt-out flag" {
		t.Errorf("commit %+v", commit)
	}
	if want := []string{"+a.sql", "+new.go", "+b/new.md", "-old.txt", "-b/old.md"}; !reflect.DeepEqual(commit.Names, want) {
		t.Errorf("files %v, want %v", commit.Names, want)
	}
	pr := app.Fake.Calls[2]
	if pr.Event != "stage" || pr.Ref != "promote/201-stage" || !strings.Contains(pr.Body, "Promotes #201 from `dev` to `stage`") {
		t.Errorf("pr %+v", pr)
	}
	if !strings.HasPrefix(app.Last(), "ok: opened #900 promoting #201 to stage") {
		t.Errorf("last %q", app.Last())
	}
}

// TestPromoteRefusesAConflict: a file staging changed on its own stops
// the promotion before anything is written.
func TestPromoteRefusesAConflict(t *testing.T) {
	w, app := newWF(t)
	c := pickFixture(app)
	app.Fake.BlobSHAs["stagehead:a.sql"] = "a-stage-only"
	selectChange(app, c)
	app.Confirms = []bool{true}
	w.doPromote(app)
	if len(app.Fake.Calls) != 0 || !strings.Contains(app.Last(), "doesn't apply cleanly to stage: a.sql") {
		t.Errorf("calls %v last %q", app.Fake.Calls, app.Last())
	}
	if len(app.Questions) != 0 {
		t.Errorf("asked %v before refusing", app.Questions)
	}
}

// TestPromoteCleansUpAFailedCommit: the branch goes when the commit
// fails; an existing branch is said, not overwritten.
func TestPromoteCleansUpAFailedCommit(t *testing.T) {
	w, app := newWF(t)
	c := pickFixture(app)
	selectChange(app, c)
	app.Confirms = []bool{true}
	app.Fake.Fail = map[string]error{"commit_on_branch": &ghapi.Error{Message: "expected head moved"}}
	w.doPromote(app)
	if n := len(app.Fake.Calls); n != 2 || app.Fake.Calls[1].Op != "delete_ref" || app.Fake.Calls[1].Ref != "heads/promote/201-stage" {
		t.Fatalf("calls %+v", app.Fake.Calls)
	}
	app.Fake.Fail = nil
	app.Fake.Heads["acme/web@promote/201-stage"] = "x"
	app.Confirms = []bool{true}
	w.doPromote(app)
	if !strings.Contains(app.Last(), "branch promote/201-stage exists") {
		t.Errorf("last %q", app.Last())
	}
}

// TestPromoteMergesAReadyPromotion: p on an open promotion merges it when
// GitHub says it can; a blocked one is said, not merged.
func TestPromoteMergesAReadyPromotion(t *testing.T) {
	w, app := newWF(t)
	p := open(306, "stage", "promote/203-stage", "Promote #203", "h306", 1)
	app.Fake.PRs = map[model.Ident]*model.PR{p.Ident(): p}
	c := &Change{Repo: web, First: merged(203, "dev", "feat/c", "c", "d203", 3), Promo: p, Where: Promoting}
	selectChange(app, c)
	p.MergeStateStatus = "BLOCKED"
	w.doPromote(app)
	if len(app.Fake.Merges) != 0 || !strings.Contains(app.Last(), "can't merge yet") {
		t.Fatalf("merges %v last %q", app.Fake.Merges, app.Last())
	}
	p.MergeStateStatus = "CLEAN"
	app.Confirms = []bool{true}
	w.doPromote(app)
	if len(app.Fake.Merges) != 1 || app.Fake.Merges[0].Number != 306 || app.Fake.Merges[0].SHA != "h306" {
		t.Fatalf("merges %+v; log %v", app.Fake.Merges, app.Log)
	}
}

// TestPromoteTagsAUnit: p on a staging unit tags it go (an annotated tag
// on its merge commit); flipping hold to go deletes hold first; a deploy
// that isn't green asks first.
func TestPromoteTagsAUnit(t *testing.T) {
	w, app := newWF(t)
	unit := merged(301, "stage", "promote/202-stage", "Promote #202", "s301", 2)
	c := &Change{Repo: web, Promo: unit, Where: OnStage, Runs: []model.WorkflowRun{{Name: "migrate", Status: "completed", Conclusion: "success"}}}
	selectChange(app, c)
	app.Picks = []int{0}
	app.Prompts = []string{"go: ready"}
	app.Confirms = []bool{true}
	w.doPromote(app)
	if len(app.Fake.Calls) != 1 || app.Fake.Calls[0].Op != "create_tag" || app.Fake.Calls[0].Ref != "go/301" || app.Fake.Calls[0].SHA != "s301" {
		t.Fatalf("calls %+v; log %v", app.Fake.Calls, app.Log)
	}
	if !strings.HasPrefix(app.Fake.Calls[0].Body, "go: ready\n\nTagged with dillodash by alice.") {
		t.Errorf("message %q", app.Fake.Calls[0].Body)
	}
	if !strings.Contains(app.Last(), "tagged go/301 — it rides the next release cut into main") {
		t.Errorf("last %q", app.Last())
	}

	app.Fake.Calls = nil
	c.Tag = "hold"
	c.Runs = []model.WorkflowRun{{Name: "migrate", Status: "completed", Conclusion: "failure"}}
	app.Picks = []int{0}
	app.Prompts = []string{"go"}
	app.Confirms = []bool{true, true} // deploy not green: go anyway; then the tag
	w.doPromote(app)
	if len(app.Fake.Calls) != 2 || app.Fake.Calls[0].Op != "delete_ref" || app.Fake.Calls[0].Ref != "tags/hold/301" || app.Fake.Calls[1].Ref != "go/301" {
		t.Fatalf("flip calls %+v; log %v", app.Fake.Calls, app.Log)
	}
	if q := app.Questions[len(app.Questions)-2]; !strings.Contains(q, "deploy failed (migrate). Tag it go anyway?") {
		t.Errorf("asked %q", q)
	}
}

// TestPromoteRefusesABot: p posts nothing as a [bots] login.
func TestPromoteRefusesABot(t *testing.T) {
	w, app := newWF(t)
	app.User = "github-actions"
	c := pickFixture(app)
	selectChange(app, c)
	app.Confirms = []bool{true}
	w.doPromote(app)
	if len(app.Fake.Calls) != 0 || !strings.Contains(app.Last(), "is a bot login") {
		t.Errorf("calls %v last %q", app.Fake.Calls, app.Last())
	}
}

// TestDeployRowApprovesTheTierDeploy: D on a change approves the
// environments its tier's run waits on that the viewer reviews.
func TestDeployRowApprovesTheTierDeploy(t *testing.T) {
	w, app := newWF(t)
	unit := merged(301, "stage", "promote/202-stage", "Promote #202", "s301", 2)
	c := &Change{Repo: web, Promo: unit, Where: OnStage}
	app.Fake.Runs = map[string][]model.WorkflowRun{"s301": {{ID: 77, Name: "Migrate", Status: "waiting", HeadSHA: "s301"}}}
	var p model.PendingDeployment
	p.Environment.ID, p.Environment.Name, p.CurrentUserCanApprove = 5, "stg", true
	app.Fake.Pending = map[int64][]model.PendingDeployment{77: {p}}
	app.Confirms = []bool{true}
	if !w.DeployRow(app, workflow.Row{Kind: RowChange, Item: c}) {
		t.Fatal("not handled")
	}
	if len(app.Fake.DeployApprovals) != 1 || app.Fake.DeployApprovals[0].RunID != 77 ||
		app.Fake.DeployApprovals[0].Body != "dillodash: approved Migrate run 77 in acme/web" {
		t.Fatalf("approvals %+v; log %v", app.Fake.DeployApprovals, app.Log)
	}
	// not a change row: left to the next handler
	if w.DeployRow(app, workflow.Row{Kind: "pr"}) {
		t.Error("handled a PR row")
	}
}

var _ ghapi.Client = (*fake.Client)(nil)
