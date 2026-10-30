package releasepr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

var (
	infra  = model.Repo{Owner: "acme", Name: "infra"}
	portal = model.Repo{Owner: "acme", Name: "web-portal"}
	etl    = model.Repo{Owner: "acme", Name: "etl-jobs"}
)

const (
	reason    = "prod login broken since 14:00" // 30 chars, no quotes
	headSHA   = "634f45480da1b2fe02cdede9e62e00bffc518966"
	mergedSHA = "abc123def4567890abc123def4567890abc123de"
)

// 2026-10-27 is EDT (UTC-4): 23:05 ET == 03:05Z next day
func utc(h, m int) time.Time { return time.Date(2026, 10, 28, h, m, 0, 0, time.UTC) }

var (
	inWindow  = utc(3, 5)  // 23:05 ET
	outWindow = utc(19, 0) // 15:00 ET
)

// winStub stands in for the maintenance window workflow: enabled, no
// night phrase of its own.
type winStub struct{ workflow.Base }

func (winStub) Name() string { return WindowName }

func newCfg(window bool) *config.Config {
	cfg := config.Default()
	cfg.Reviews.Repos = []model.Repo{infra}
	rel := config.DefaultReleasePR()
	rel.Auto = false
	rel.Repos = []model.Repo{portal, etl}
	rel.Org = "acme"
	rel.ApprovalQueuesForWindow = true
	cfg.ReleasePR = rel
	if window {
		cfg.MaintenanceWindow = config.DefaultMaintenanceWindow()
	}
	return cfg
}

func newWF(t *testing.T, window bool) (*Workflow, *wftest.App) {
	t.Helper()
	cfg := newCfg(window)
	ctx := &workflow.Context{Config: cfg, Repos: cfg.Reviews.Repos}
	if window {
		if _, err := workflow.NewSet(ctx, winStub{}); err != nil {
			t.Fatal(err)
		}
	}
	w := New(ctx)
	app := wftest.New(cfg, "alice")
	return w, app
}

func patchNow(t *testing.T, at time.Time) {
	t.Helper()
	old := clock.Now
	clock.Now = func() time.Time { return at }
	t.Cleanup(func() { clock.Now = old })
}

type relOpts struct {
	kind   string
	number int
	repo   model.Repo
	author string
	draft  bool
	state  string
	head   string
}

func makeRelease(o relOpts) *model.PR {
	if o.kind == "" {
		o.kind = KindRelease
	}
	if o.number == 0 {
		o.number = 42
	}
	if o.repo.IsZero() {
		o.repo = portal
	}
	if o.author == "" {
		o.author = "bob"
	}
	if o.state == "" {
		o.state = "OPEN"
	}
	if o.head == "" {
		o.head = "relsha"
	}
	pr := &model.PR{Repo: o.repo, Number: o.number, Title: "Release 2026-10-27", State: o.state, IsDraft: o.draft,
		HeadRefOid: o.head, BaseRefName: "main", Author: model.Actor{Login: o.author},
		UpdatedAt: utc(1, 0), CreatedAt: utc(0, 0)}
	switch o.kind {
	case KindHotfix:
		pr.HeadRefName, pr.Title = "hotfix/login", "hotfix: login"
	case KindOther:
		pr.HeadRefName, pr.Title = "docs/upgrade-notes", "docs: database upgrade notes"
	default:
		pr.HeadRefName = "release/2026-10-27"
	}
	pr.Set(ExtRel, &Info{Kind: o.kind, Repo: o.repo.Name})
	return pr
}

func approved(pr *model.PR, by string) *model.PR {
	pr.ReviewDecision = "APPROVED"
	pr.Opinions.Nodes = []model.Review{{Author: model.Actor{Login: by}, State: "APPROVED"}}
	return pr
}

func logHas(app *wftest.App, kind, sub string) bool {
	for _, l := range app.Log {
		if strings.HasPrefix(l, kind+": ") && strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func comments(app *wftest.App) []string {
	var out []string
	for _, c := range app.Fake.CommentsPosted {
		out = append(out, c.Body)
	}
	return out
}

// ---- a: approve ----

func TestApproveQueuePath(t *testing.T) {
	patchNow(t, utc(14, 0)) // 10:00 ET: before the cutoff
	w, app := newWF(t, true)
	app.Picks, app.Confirms = []int{0}, []bool{true}
	pr := makeRelease(relOpts{})
	if !w.Approve(app, pr, nil) {
		t.Fatal("a release PR is handled")
	}
	if got := app.Fake.Approvals; len(got) != 1 || got[0] != 42 || len(app.Fake.CommentsPosted) != 0 {
		t.Errorf("queue path posts the approving review only: %v %v", got, comments(app))
	}
	if c := app.Fake.Calls[0]; c.Repo != portal {
		t.Errorf("posted on the release's own repo: %+v", c)
	}
	if m := app.Menus[0]; !strings.HasPrefix(m[1], "queue it for tonight's") || !strings.HasPrefix(m[2], "release NOW") {
		t.Errorf("the menu offers queue first, then NOW: %q", m)
	}
	if head := app.Approved[pr.Ident()]; head != "relsha" {
		t.Errorf("marked approved at this head: %v", app.Approved)
	}
	if !strings.Contains(app.Last(), "queued for tonight's") || len(app.Refreshes) != 1 || !app.Refreshes[0] {
		t.Errorf("says it is queued + refresh re-queued: %q %v", app.Last(), app.Refreshes)
	}
	if len(app.Celebrated) != 1 || !app.Celebrated[0] || app.Launched != 0 {
		t.Errorf("a queue celebrates big, never launches: %v %d", app.Celebrated, app.Launched)
	}
}

func TestApproveNowPath(t *testing.T) {
	w, app := newWF(t, true)
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	w.Approve(app, makeRelease(relOpts{}), nil)
	want := `/release-now "` + reason + `"`
	if got := app.Fake.Approvals; len(got) != 1 || got[0] != 42 {
		t.Errorf("review first: %v", got)
	}
	if got := comments(app); len(got) != 1 || got[0] != want || app.Fake.CommentsPosted[0].Number != 42 {
		t.Errorf("then exactly the release-now comment: %q", got)
	}
	if o := app.PromptOps[0]; o.MinLen != 20 || o.Forbid != `"` {
		t.Errorf("the prompt enforces 20 chars and no quote: %+v", o)
	}
	if q := app.Questions[len(app.Questions)-1]; !strings.Contains(q, want) {
		t.Errorf("the confirm shows the comment: %q", q)
	}
	for _, b := range comments(app) {
		if !w.nowRE.MatchString(b) {
			t.Errorf("every posted body matches the grammar: %q", b)
		}
	}
	if app.Launched != 1 || !strings.Contains(app.Last(), "posted /release-now") {
		t.Errorf("a release-now launches: %d %q", app.Launched, app.Last())
	}
	// Then the manifest repo is told, naming the comment just posted.
	d := app.Fake.Dispatches
	if len(d) != 1 || d[0].Repo != infra || d[0].Event != "release-now" {
		t.Fatalf("one release-now dispatch on the manifest repo: %+v", d)
	}
	if p := d[0].Payload; p["repo"] != portal.Name || p["pr"] != 42 || p["comment_id"] != app.Fake.CommentsPosted[0].ID {
		t.Errorf("the payload names the release repo, the PR and the comment: %v", p)
	}
	if last := app.Fake.Calls[len(app.Fake.Calls)-1]; last.Op != "dispatch" {
		t.Errorf("the dispatch comes after the comment: %+v", app.Fake.Calls)
	}
	if !strings.Contains(app.Last(), "told "+infra.String()) {
		t.Errorf("the status line says the manifest repo was told: %q", app.Last())
	}
}

// A release you approved earlier (queued, then bumped from the window)
// still needs NOW: a offers only that, and posts the command with no
// second review.
func TestApproveAlreadyApprovedOffersNowOnly(t *testing.T) {
	patchNow(t, utc(14, 0))
	w, app := newWF(t, true)
	pr := approved(makeRelease(relOpts{}), "alice")
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true}
	if !w.Approve(app, pr, nil) {
		t.Fatal("an approved release PR is still handled")
	}
	want := `/release-now "` + reason + `"`
	if m := app.Menus[0]; len(m) != 2 || !strings.HasPrefix(m[1], "release NOW") || !strings.Contains(m[0], "already approved by you") {
		t.Errorf("the menu offers NOW alone and says why: %q", m)
	}
	if len(app.Fake.Approvals) != 0 {
		t.Errorf("no second review: %v", app.Fake.Approvals)
	}
	if got := comments(app); len(got) != 1 || got[0] != want {
		t.Errorf("exactly the release-now comment: %q", got)
	}
	if q := app.Questions[len(app.Questions)-1]; !strings.Contains(q, want) || !strings.Contains(q, "no second review") {
		t.Errorf("the confirm shows the comment and that no review follows: %q", q)
	}
	d := app.Fake.Dispatches
	if len(d) != 1 || d[0].Payload["comment_id"] != app.Fake.CommentsPosted[0].ID {
		t.Fatalf("the manifest repo is told about that comment: %+v", d)
	}
	if app.Launched != 1 || !strings.Contains(app.Last(), "posted /release-now") || !strings.Contains(app.Last(), "told "+infra.String()) {
		t.Errorf("a release-now launches and says who was told: %d %q", app.Launched, app.Last())
	}

	// Pressed again while it is on its way: nothing left to post.
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true}
	w.Approve(app, pr, nil)
	if len(comments(app)) != 1 || len(app.Menus) != 1 || !strings.Contains(app.Last(), "already on its way") {
		t.Errorf("an in-flight release posts nothing more: %q menus %d %q", comments(app), len(app.Menus), app.Last())
	}
}

func TestApproveAlreadyApprovedThisSession(t *testing.T) {
	patchNow(t, utc(14, 0))
	w, app := newWF(t, true)
	pr := makeRelease(relOpts{})
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, pr, nil) // queue it
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true}
	w.Approve(app, pr, nil) // then NOW, on the same head
	if len(app.Fake.Approvals) != 1 || len(comments(app)) != 1 {
		t.Errorf("one review, then the command alone: %v %q", app.Fake.Approvals, comments(app))
	}
	if m := app.Menus[1]; len(m) != 2 || !strings.HasPrefix(m[1], "release NOW") {
		t.Errorf("the second a offers NOW alone: %q", m)
	}

	// A new head is a new release: the old approval no longer counts.
	w, app = newWF(t, true)
	pr = makeRelease(relOpts{})
	app.Approved[pr.Ident()] = "oldsha"
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, pr, nil)
	if len(app.Fake.Approvals) != 1 || len(app.Menus[0]) != 3 {
		t.Errorf("a moved head is approved afresh, with both paths: %v %q", app.Fake.Approvals, app.Menus[0])
	}
}

func TestApproveAlreadyApprovedCancelAndFailure(t *testing.T) {
	w, app := newWF(t, true)
	pr := approved(makeRelease(relOpts{}), "alice")
	w.Approve(app, pr, nil) // no pick scripted: Esc
	if len(comments(app)) != 0 || !strings.Contains(app.Last(), "approve cancelled") {
		t.Errorf("Esc at the menu posts nothing: %q %q", comments(app), app.Last())
	}
	w, app = newWF(t, true)
	app.Fake.Fail["comment"] = errors.New("HTTP 502")
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true}
	w.Approve(app, pr, nil)
	if len(app.Fake.Dispatches) != 0 || !logHas(app, "fail", "/release-now NOT posted") {
		t.Errorf("a failed comment says so and tells nobody: %+v %v", app.Fake.Dispatches, app.Log)
	}
	w, app = newWF(t, true)
	app.Fake.Fail["dispatch"] = errors.New("HTTP 403")
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true}
	w.Approve(app, pr, nil)
	if !logHas(app, "fail", "was not told") || !strings.Contains(app.Last(), "press M to merge by hand") {
		t.Errorf("a failed dispatch points at M: %v", app.Log)
	}
}

func TestApproveNowDispatchFailure(t *testing.T) {
	w, app := newWF(t, true)
	app.Fake.Fail["dispatch"] = errors.New("HTTP 403")
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	pr := makeRelease(relOpts{})
	w.Approve(app, pr, nil)
	if len(app.Fake.Approvals) != 1 || len(comments(app)) != 1 {
		t.Errorf("the review and the comment still landed: %v %v", app.Fake.Approvals, comments(app))
	}
	if p, ok := w.progressOf(pr); !ok || p.Phase != PhaseDispatched || !strings.Contains(p.Detail, "not told") {
		t.Errorf("the card says nobody will pick it up: %+v", p)
	}
	if !logHas(app, "fail", "was not told") || !strings.Contains(app.Last(), "press M to merge by hand") {
		t.Errorf("a failed dispatch says so and points at M: %v", app.Log)
	}
	if app.Launched != 0 || len(app.Refreshes) != 1 {
		t.Errorf("no launch; a refresh: %d %v", app.Launched, app.Refreshes)
	}
}

func TestApproveNowDispatchOff(t *testing.T) {
	w, app := newWF(t, true)
	w.cfg.ReleaseNowDispatch = false
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	w.Approve(app, makeRelease(relOpts{}), nil)
	if len(app.Fake.Dispatches) != 0 || len(comments(app)) != 1 || app.Launched != 1 {
		t.Errorf("release_now_dispatch = false posts the comment and nothing else: %+v", app.Fake.Calls)
	}
	w, app = newWF(t, false)
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	w.Approve(app, makeRelease(relOpts{}), nil)
	if len(app.Fake.Dispatches) != 0 {
		t.Errorf("without the window workflow there is nobody to tell: %+v", app.Fake.Dispatches)
	}
}

func TestApproveHotfixOffersNowFirst(t *testing.T) {
	w, app := newWF(t, true)
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true}
	w.Approve(app, makeRelease(relOpts{kind: KindHotfix, number: 43}), nil)
	if m := app.Menus[0]; !strings.HasPrefix(m[1], "release NOW") || !strings.Contains(m[0], "emergency by definition") {
		t.Errorf("NOW is the first menu entry: %q", m)
	}
	if m := app.Menus[0]; !strings.HasPrefix(m[2], "approve review only") {
		t.Errorf("a hotfix never queues, so the menu does not offer it: %q", m)
	}
	if got := comments(app); len(app.Fake.Approvals) != 1 || app.Fake.Approvals[0] != 43 || len(got) != 1 || got[0] != `/release-now "`+reason+`"` {
		t.Errorf("pick 0 is the NOW path: %v %q", app.Fake.Approvals, got)
	}
	w, app = newWF(t, true)
	app.Picks, app.Confirms = []int{1}, []bool{true}
	w.Approve(app, makeRelease(relOpts{kind: KindHotfix, number: 43}), nil)
	if len(app.Fake.Approvals) != 1 || len(app.Fake.CommentsPosted) != 0 {
		t.Errorf("pick 1 is the queue path: %v %q", app.Fake.Approvals, comments(app))
	}
}

func TestApproveRefusals(t *testing.T) {
	cases := map[string]*model.PR{
		"draft":  makeRelease(relOpts{draft: true}),
		"own":    makeRelease(relOpts{author: "alice"}),
		"merged": makeRelease(relOpts{state: "MERGED"}),
		"other":  makeRelease(relOpts{kind: KindOther, number: 951}),
	}
	for name, pr := range cases {
		w, app := newWF(t, true)
		app.Picks, app.Confirms = []int{0}, []bool{true}
		if !w.Approve(app, pr, nil) {
			t.Errorf("%s: still handled", name)
		}
		if len(app.Questions) != 0 || len(app.Menus) != 0 || len(app.Fake.Calls) != 0 || !strings.HasPrefix(app.Last(), "notice: ") {
			t.Errorf("%s: refused, nothing asked, nothing posted: %v %v %v %q", name, app.Questions, app.Menus, app.Fake.Calls, app.Last())
		}
		if name == "other" && (!strings.Contains(app.Last(), "not a release") || !strings.Contains(app.Last(), "docs/upgrade-notes")) {
			t.Errorf("a on a non-release names the branch: %q", app.Last())
		}
	}
	w, app := newWF(t, true)
	if w.Approve(app, &model.PR{Repo: infra, Number: 7, State: "OPEN", HeadRefName: "feature-x"}, nil) {
		t.Error("a plain PR is not handled")
	}
}

func TestApproveReasonChecks(t *testing.T) {
	w, app := newWF(t, true)
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{"too short"}, []bool{true}
	w.Approve(app, makeRelease(relOpts{}), nil)
	if len(app.Fake.Calls) != 0 || !logHas(app, "fail", "20") {
		t.Errorf("a short reason posts nothing and names 20 characters: %v %v", app.Fake.Calls, app.Log)
	}
	w, app = newWF(t, true)
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{`a "quoted" reason that is long`}, []bool{true}
	w.Approve(app, makeRelease(relOpts{}), nil)
	if len(app.Fake.Calls) != 0 {
		t.Errorf("a quote in the reason posts nothing: %v", app.Fake.Calls)
	}
	w, app = newWF(t, true)
	app.Picks = []int{1} // no prompt answer: Esc
	w.Approve(app, makeRelease(relOpts{}), nil)
	if len(app.Fake.Calls) != 0 || app.Last() != "notice: approve cancelled" {
		t.Errorf("Esc at the reason cancels: %v %q", app.Fake.Calls, app.Last())
	}
	w, app = newWF(t, true)
	app.Picks, app.Confirms = []int{0}, []bool{false}
	w.Approve(app, makeRelease(relOpts{}), nil)
	if len(app.Fake.Calls) != 0 {
		t.Errorf("n at the confirm posts nothing: %v", app.Fake.Calls)
	}
	w, app = newWF(t, true) // Esc at the menu
	w.Approve(app, makeRelease(relOpts{}), nil)
	if len(app.Fake.Calls) != 0 || app.Last() != "notice: approve cancelled" {
		t.Errorf("Esc at the menu cancels: %v %q", app.Fake.Calls, app.Last())
	}
}

func TestApproveFailures(t *testing.T) {
	w, app := newWF(t, true)
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	app.Fake.Fail["approve"] = &ghapi.Error{Status: 500, Message: "boom"}
	pr := makeRelease(relOpts{})
	w.Approve(app, pr, nil)
	if len(app.Fake.CommentsPosted) != 0 || !logHas(app, "fail", "failed") || len(app.Approved) != 0 {
		t.Errorf("a failed review posts no comment: %q %v %v", comments(app), app.Log, app.Approved)
	}
	w, app = newWF(t, true)
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	app.Fake.Fail["comment"] = &ghapi.Error{Status: 500, Message: "boom"}
	w.Approve(app, pr, nil)
	if len(app.Fake.Approvals) != 1 || !logHas(app, "fail", "NOT posted") || app.Approved[pr.Ident()] != "relsha" {
		t.Errorf("approved but the comment failed says so, still marked: %v %v %v", app.Fake.Approvals, app.Log, app.Approved)
	}
}

func TestApproveWithoutWindow(t *testing.T) {
	w, app := newWF(t, false)
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, makeRelease(relOpts{}), nil)
	if len(app.Fake.Approvals) != 1 || strings.Contains(app.Menus[0][1], "window") || !strings.Contains(app.Last(), "M merges it") {
		t.Errorf("without a window the approval is plain: %v %q %q", app.Fake.Approvals, app.Menus[0], app.Last())
	}
}

func TestReleaseNowRE(t *testing.T) {
	rx := ReleaseNowRE("/release-now", 20)
	x := strings.Repeat("x", 20)
	if !rx.MatchString(`/release-now "`+x+`"`) || rx.MatchString(`/release-now "`+x[:19]+`"`) ||
		rx.MatchString(`/release-now "a "b" `+x+`"`) || rx.MatchString(`/release-now "`+x+`" please`) {
		t.Error("20 chars pass, 19 fail, a quote inside fails, trailing text fails")
	}
}

// ---- helpers ----

func TestKindAndLabel(t *testing.T) {
	w, _ := newWF(t, true)
	if KindOf("hotfix/x", "release/", "hotfix/") != KindHotfix || KindOf("release/2026-10-27", "release/", "hotfix/") != KindRelease || KindOf("feat/x", "release/", "hotfix/") != KindOther {
		t.Error("release kind by head branch")
	}
	rel := func() *model.PR { return makeRelease(relOpts{}) }
	if got := w.Label(rel(), "alice", false); got != LabelNeeds {
		t.Errorf("needs approval by default: %q", got)
	}
	pr := rel()
	pr.Opinions.Nodes = []model.Review{{Author: model.Actor{Login: "carol"}, State: "APPROVED"}}
	if got := w.Label(pr, "alice", false); got != "approved by carol" {
		t.Errorf("names the approver when reviewDecision is null: %q", got)
	}
	if got := w.Label(approved(rel(), "alice"), "alice", false); got != model.LabelApprovedMe {
		t.Errorf("mine wins: %q", got)
	}
	if got := w.Label(rel(), "alice", true); got != model.LabelApprovedMe {
		t.Errorf("a local approval wins: %q", got)
	}
	pr = rel()
	pr.ReviewDecision = "CHANGES_REQUESTED"
	if got := w.Label(pr, "alice", false); got != model.LabelChanges {
		t.Errorf("changes requested: %q", got)
	}
	if got := w.Label(makeRelease(relOpts{draft: true}), "", false); got != model.LabelDraft {
		t.Errorf("draft: %q", got)
	}
	pr = rel()
	pr.ReviewDecision = "APPROVED"
	if got := w.Label(pr, "alice", false); got != model.LabelApproved {
		t.Errorf("approved with no named reviewer: %q", got)
	}
	other := makeRelease(relOpts{kind: KindOther, number: 951})
	if !w.NotARelease(other) || w.NotARelease(rel()) || w.NotARelease(makeRelease(relOpts{kind: KindHotfix})) {
		t.Error("a docs/ branch into main is not a release")
	}
	if w.NotARelease(&model.PR{}) {
		t.Error("a row with no head branch is not judged")
	}
	if got := w.Label(other, "alice", false); got != LabelOther {
		t.Errorf("'not a release', never 'needs approval': %q", got)
	}
	if got := w.Label(makeRelease(relOpts{kind: KindOther, draft: true}), "", false); got != LabelOther {
		t.Errorf("a draft feature PR into main says 'not a release' too: %q", got)
	}
	if got := w.Label(approved(makeRelease(relOpts{kind: KindOther}), "carol"), "alice", false); got != LabelOther {
		t.Errorf("an approved one still says 'not a release': %q", got)
	}
	if got := w.Label(makeRelease(relOpts{kind: KindOther, state: "MERGED"}), "", false); got != model.LabelMerged {
		t.Errorf("a merged one says 'merged': %q", got)
	}
	why := w.NotAReleaseWhy(other)
	if !strings.Contains(why, "docs/upgrade-notes") || !strings.Contains(why, "ship to") {
		t.Errorf("the why names the branch and dev: %q", why)
	}
	if LabelStyle(LabelNeeds) != "chip.await" || LabelStyle(LabelOther) != "chip.state" || LabelStyle("approved by carol") != "chip.approved" || LabelStyle(model.LabelApprovedMe) != "chip.mine" {
		t.Error("label styles")
	}
}

func TestMergedRecently(t *testing.T) {
	now := utc(4, 0)
	fresh := makeRelease(relOpts{state: "MERGED"})
	at := utc(3, 10)
	fresh.MergedAt = &at
	stale := makeRelease(relOpts{state: "MERGED"})
	old := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	stale.MergedAt = &old
	keep := 12 * time.Hour
	if !MergedRecently(fresh, now, keep) || MergedRecently(stale, now, keep) || MergedRecently(makeRelease(relOpts{}), now, keep) {
		t.Error("keeps a 50-minute-old merge and drops an 18-hour-old one")
	}
	if got := (&Workflow{}).Label(fresh, "alice", false); got != model.LabelMerged {
		t.Errorf("a merged release's chip is 'merged': %q", got)
	}
}

func TestDiscoveryCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "release-repos-acme.json")
	if _, ok := LoadCache(path, "acme", 100*time.Second, time.Unix(1000, 0)); ok {
		t.Error("no cache file -> not ok")
	}
	SaveCache(path, "acme", []model.Repo{etl, portal}, time.Unix(1000, 0))
	if fi, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("written 0600: %v %v", fi, err) // Windows has no Unix modes
	}
	got, ok := LoadCache(path, "acme", 100*time.Second, time.Unix(1050, 0))
	if !ok || len(got) != 2 || got[0] != etl || got[1] != portal {
		t.Errorf("a fresh cache is read back sorted: %v %v", got, ok)
	}
	if _, ok := LoadCache(path, "acme", 100*time.Second, time.Unix(1200, 0)); ok {
		t.Error("a stale cache is ignored")
	}
	if _, ok := LoadCache(path, "other-org", 100*time.Second, time.Unix(1050, 0)); ok {
		t.Error("another org's cache is ignored")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"org":"acme"`) || !strings.Contains(string(raw), `"acme/etl-jobs"`) {
		t.Errorf("the file shape: %s", raw)
	}
	repos := []model.RepoListing{{Name: "web-portal"}, {Name: "admin-portal"}, {Name: "etl-jobs", Archived: true}, {Name: "db-migrations"}}
	rs := map[string][]model.Ruleset{
		"web-portal": {{Name: "Release Standard"}}, "admin-portal": {{Name: "Release Standard"}},
		"etl-jobs": {{Name: "Release Standard"}}, "db-migrations": {{Name: "Production Code"}},
	}
	got = ReposFrom("acme", "Release Standard", repos, rs)
	if len(got) != 2 || got[0].Name != "admin-portal" || got[1].Name != "web-portal" {
		t.Errorf("enrolled, not archived, sorted: %v", got)
	}
}

func TestDiscoveryThroughTheClient(t *testing.T) {
	w, app := newWF(t, false)
	w.cfg.Auto = true
	w.cfg.Repos = nil
	w.cfg.Cache = filepath.Join(t.TempDir(), "c.json")
	w.cfg.Ruleset = "Release Standard"
	app.Fake.Orgs = map[string][]model.RepoListing{"acme": {{Name: "web-portal"}, {Name: "infra"}, {Name: "etl-jobs", Archived: true}}}
	app.Fake.RepoRulesets = map[model.Repo][]model.Ruleset{portal: {{Name: "Release Standard"}}, infra: {{Name: "Other"}}}
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {}}
	now := utc(1, 0)
	names, warn := w.repoNames(context.Background(), app.Fake, now)
	if warn != "" || len(names) != 1 || names[0] != portal {
		t.Errorf("discovers by ruleset: %v %q", names, warn)
	}
	if app.Fake.Reads["rulesets"] != 2 {
		t.Errorf("archived repos are not asked for rulesets: %v", app.Fake.Reads)
	}
	names, _ = w.repoNames(context.Background(), app.Fake, now.Add(time.Minute))
	if app.Fake.Reads["org_repos"] != 1 || len(names) != 1 {
		t.Errorf("the memory cache serves within the ttl: %v", app.Fake.Reads)
	}
	w2 := New(&workflow.Context{Config: app.Cfg, Repos: app.Cfg.Reviews.Repos})
	names, _ = w2.repoNames(context.Background(), app.Fake, now.Add(time.Minute))
	if app.Fake.Reads["org_repos"] != 1 || len(names) != 1 {
		t.Errorf("the disk cache serves a fresh process: %v %v", app.Fake.Reads, names)
	}
	w3 := New(&workflow.Context{Config: app.Cfg, Repos: app.Cfg.Reviews.Repos})
	app.Fake.Fail["org_repos"] = &ghapi.Error{Status: 502, Message: "Bad Gateway"}
	names, warn = w3.repoNames(context.Background(), app.Fake, now.Add(48*time.Hour))
	if len(names) != 0 || !strings.Contains(warn, "not discovered") {
		t.Errorf("a failed discovery with nothing cached warns: %v %q", names, warn)
	}
}

// ---- M ----

func mergeableRelease() *model.PR {
	rel := approved(makeRelease(relOpts{number: 133, repo: model.Repo{Owner: "acme", Name: "admin-portal"}, head: headSHA}), "alice")
	rel.MergeStateStatus = "CLEAN"
	return rel
}

func TestMergeBlock(t *testing.T) {
	w, _ := newWF(t, true)
	rel := mergeableRelease()
	unapproved := mergeableRelease()
	unapproved.ReviewDecision, unapproved.Opinions.Nodes = "", nil
	if got := w.MergeBlock(unapproved, "alice"); !strings.Contains(got, "approving review") {
		t.Errorf("refuses an unapproved release: %q", got)
	}
	draft := mergeableRelease()
	draft.IsDraft = true
	if got := w.MergeBlock(draft, "alice"); !strings.Contains(got, "draft") {
		t.Errorf("refuses a draft: %q", got)
	}
	blocked := mergeableRelease()
	blocked.MergeStateStatus = "BLOCKED"
	if got := w.MergeBlock(blocked, "alice"); !strings.Contains(got, "BLOCKED") {
		t.Errorf("refuses a BLOCKED PR: %q", got)
	}
	merged := mergeableRelease()
	merged.State = "MERGED"
	if got := w.MergeBlock(merged, "alice"); !strings.Contains(got, "already merged") {
		t.Errorf("refuses an already-merged PR: %q", got)
	}
	closed := mergeableRelease()
	closed.State = "CLOSED"
	if got := w.MergeBlock(closed, "alice"); got != "the PR is closed" {
		t.Errorf("a closed PR: %q", got)
	}
	if got := w.MergeBlock(&model.PR{Repo: infra, Number: 7, State: "OPEN"}, "alice"); got != "" {
		t.Errorf("a non-release row is the core's business: %q", got)
	}
	unknown := mergeableRelease()
	unknown.MergeStateStatus = "UNKNOWN"
	if got := w.MergeBlock(unknown, "alice"); got != "" {
		t.Errorf("UNKNOWN mergeStateStatus is allowed: %q", got)
	}
	if got := w.MergeBlock(rel, "alice"); got != "" {
		t.Errorf("an approved CLEAN release may merge: %q", got)
	}
	other := approved(makeRelease(relOpts{kind: KindOther, number: 951}), "alice")
	other.MergeStateStatus = "CLEAN"
	if got := w.MergeBlock(other, "alice"); !strings.Contains(got, "not a release") || !strings.Contains(got, "docs/upgrade-notes") || !strings.Contains(got, "ship to") {
		t.Errorf("a non-release, even approved and CLEAN, names the branch and dev: %q", got)
	}
}

func TestMergeConfirm(t *testing.T) {
	// inside the window: one confirm that says it DEPLOYS production
	patchNow(t, inWindow)
	w, app := newWF(t, true)
	app.Confirms = []bool{true}
	ok, handled := w.MergeConfirm(app, mergeableRelease())
	if !ok || !handled || len(app.Questions) != 1 || !strings.Contains(app.Questions[0], "DEPLOYS production") ||
		!strings.Contains(app.Questions[0], "634f454") || !strings.Contains(app.Questions[0], "admin-portal#133") ||
		!strings.Contains(app.Questions[0], "as a merge commit") {
		t.Errorf("in the window: %v %v %q", ok, handled, app.Questions)
	}
	w, app = newWF(t, true)
	app.Confirms = []bool{false}
	if ok, handled := w.MergeConfirm(app, mergeableRelease()); ok || !handled {
		t.Error("n at the confirm merges nothing")
	}
	if ok, handled := w.MergeConfirm(app, &model.PR{Repo: infra, Number: 7}); ok || handled {
		t.Error("a plain PR is not handled")
	}
	// outside the window: a menu first; cancel merges nothing; NOW then confirms
	patchNow(t, outWindow)
	w, app = newWF(t, true)
	app.Picks = []int{0}
	if ok, handled := w.MergeConfirm(app, mergeableRelease()); ok || !handled {
		t.Error("cancel merges nothing")
	}
	if m := app.Menus[0]; !strings.HasPrefix(m[1], "cancel") || !strings.Contains(m[0], "emergency") || !strings.Contains(m[2], "/release-now") || len(app.Questions) != 0 {
		t.Errorf("offers cancel first: %q %q", m, app.Questions)
	}
	w, app = newWF(t, true)
	app.Picks, app.Confirms = []int{1}, []bool{true}
	if ok, _ := w.MergeConfirm(app, mergeableRelease()); !ok || len(app.Menus) != 1 || len(app.Questions) != 1 {
		t.Errorf("NOW + confirm merges: %v %v", app.Menus, app.Questions)
	}
	// no window workflow: there is no slot, one confirm whenever
	w, app = newWF(t, false)
	app.Confirms = []bool{true}
	if ok, handled := w.MergeConfirm(app, mergeableRelease()); !ok || !handled || len(app.Menus) != 0 || len(app.Questions) != 1 {
		t.Errorf("without a window M asks once: %v %v", app.Menus, app.Questions)
	}
	w, app = newWF(t, true)
	w.cfg.MergeMethod = "squash"
	app.Confirms = []bool{true}
	patchNow(t, inWindow)
	w.MergeConfirm(app, mergeableRelease())
	if !strings.Contains(app.Questions[0], "with a squash") {
		t.Errorf("the merge method is named: %q", app.Questions[0])
	}
}

func TestInReleaseWindow(t *testing.T) {
	w, _ := newWF(t, true)
	if !w.InReleaseWindow(inWindow) || w.InReleaseWindow(outWindow) || !w.InReleaseWindow(utc(2, 59)) || w.InReleaseWindow(utc(3, 30)) {
		t.Error("23:05 ET yes, 15:00 ET no, 22:59 ET yes, 23:30 ET no")
	}
	w, _ = newWF(t, false)
	if !w.InReleaseWindow(outWindow) {
		t.Error("no window: always")
	}
}

func TestNightPhrase(t *testing.T) {
	w, _ := newWF(t, true)
	if got := w.NightPhrase(utc(14, 0)); !strings.HasPrefix(got, "tonight's 23:00") {
		t.Errorf("before the cutoff: %q", got)
	}
	if got := w.NightPhrase(utc(1, 30)); !strings.HasPrefix(got, "tomorrow night's 23:00") {
		t.Errorf("after the 21:00 ET cutoff: %q", got)
	}
	w, _ = newWF(t, false)
	if got := w.NightPhrase(utc(14, 0)); got != "the maintenance" {
		t.Errorf("no window: %q", got)
	}
}

// ---- D ----

func mergedRelease() *model.PR {
	m := mergeableRelease()
	m.State = "MERGED"
	m.MergeCommit = &model.MergeCommit{Oid: mergedSHA}
	return m
}

func TestDeployTargetAndComment(t *testing.T) {
	w, app := newWF(t, true)
	m := mergedRelease()
	app.ViewName = "list"
	app.RowList = []workflow.Row{{Kind: "header", Item: "releases (1)"}, {Kind: RowRelease, Item: m}}
	app.Sel = 1
	if got := w.DeployTarget(app); got != m {
		t.Error("D works from the list on a merged release row")
	}
	app.RowList[1] = workflow.Row{Kind: RowRelease, Item: mergeableRelease()}
	if w.DeployTarget(app) != nil {
		t.Error("an unmerged release is not a target (the core says M merges it)")
	}
	app.RowList[1] = workflow.Row{Kind: "pr", Item: m}
	if w.DeployTarget(app) != nil {
		t.Error("a core PR row is not ours")
	}
	other := makeRelease(relOpts{kind: KindOther, state: "MERGED"})
	other.MergeCommit = &model.MergeCommit{Oid: mergedSHA}
	app.RowList[1] = workflow.Row{Kind: RowRelease, Item: other}
	if w.DeployTarget(app) != nil {
		t.Error("a merged non-release is not a target")
	}
	app.ViewName = "detail"
	app.RowList[1] = workflow.Row{Kind: RowRelease, Item: m}
	if w.DeployTarget(app) != nil {
		t.Error("in the detail the core uses the open PR")
	}
	if got := w.DeployComment(m, "abc123de"); got != "dillodash: release acme/admin-portal#133 merged as abc123de" {
		t.Errorf("the deployment comment: %q", got)
	}
	if got := w.DeployComment(&model.PR{Repo: infra, Number: 7}, "x"); got != "" {
		t.Errorf("no comment for a plain PR: %q", got)
	}
	if got := deploys.TokenHint(&ghapi.Error{Status: 403, Message: "Resource not accessible by personal access token"}, "Contents", portal); !strings.Contains(got, "Contents: Read and write on web-portal") {
		t.Errorf("the token hint names the permission and the repo: %q", got)
	}
	if got := deploys.TokenHint(&ghapi.Error{Status: 405, Message: "not mergeable"}, "Contents", portal); got != "" {
		t.Errorf("silent on any other error: %q", got)
	}
}

// ---- refresh, sections, summary ----

func fixture(t *testing.T, window bool) (*Workflow, *wftest.App, *State) {
	t.Helper()
	patchNow(t, utc(4, 0))
	w, app := newWF(t, window)
	open := makeRelease(relOpts{number: 44})
	open.Set(ExtRel, nil)
	hot := makeRelease(relOpts{kind: KindHotfix, number: 45, author: "carol"})
	hot.Set(ExtRel, nil)
	queued := approved(makeRelease(relOpts{number: 42}), "carol")
	queued.Labels.Nodes = []model.Label{{Name: "window:queued"}}
	queued.Set(ExtRel, nil)
	merged := mergedRelease()
	merged.Repo = portal
	at := utc(3, 10)
	merged.MergedAt = &at
	merged.Set(ExtRel, nil)
	stale := mergedRelease()
	stale.Repo, stale.Number = portal, 7
	old := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	stale.MergedAt = &old
	stale.Set(ExtRel, nil)
	other := makeRelease(relOpts{kind: KindOther, number: 951})
	other.Set(ExtRel, nil)
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{
		portal: {PRs: []*model.PR{open, hot, queued, merged, stale, other}, ProdOid: "m", StageOid: "s"},
	}
	app.Fake.Compares = map[string]ghapi.CompareResult{"main...stage": {AheadBy: 4, Commits: stageCommits()}}
	app.Fake.Tags = map[model.Repo][]string{portal: {"go/610", "go/999", "hold/611"}}
	app.Fake.Runs = map[string][]model.WorkflowRun{mergedSHA: {{ID: 31, Name: "Migrate", Status: "waiting", HeadSHA: mergedSHA}}}
	app.Fake.Pending = map[int64][]model.PendingDeployment{31: {{Environment: struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}{4, "prod"}, CurrentUserCanApprove: true}}}
	s, warns, _ := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	st := s.(*State)
	if len(warns) != 1 || !strings.Contains(warns[0], "etl-jobs") {
		t.Errorf("the repo that failed warns: %v", warns)
	}
	w.Apply(app, st)
	return w, app, st
}

func TestRefreshAndSections(t *testing.T) {
	w, app, st := fixture(t, true)
	nums := func(rows []workflow.Row) []string {
		var out []string
		for _, r := range rows {
			switch v := r.Item.(type) {
			case string:
				out = append(out, v)
			case *model.PR:
				out = append(out, r.Kind+":"+model.Ref(v.Repo, v.Number, true))
			case *Stage:
				out = append(out, r.Kind+":"+v.Repo.Name)
			}
		}
		return out
	}
	got := nums(w.Sections(app, nil, "alice"))
	want := []string{"releases (3)", "release:web-portal#44", "release:web-portal#45", "release:web-portal#133", "release:web-portal#951", "stage:web-portal"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the section: releases first, not-a-release last, then the stage row; the queued one left to the window:\n got %v\nwant %v", got, want)
	}
	if q := st.QueuedForWindow(); len(q) != 1 || q[0].Number != 42 {
		t.Errorf("QueuedForWindow: %v", q)
	}
	if len(st.Releases) != 5 || st.Find(model.Ident{Repo: portal, Number: 7}) != nil {
		t.Errorf("an 18-hour-old merge is dropped: %d", len(st.Releases))
	}
	for _, pr := range st.Releases {
		if InfoOf(pr) == nil || InfoOf(pr).Repo != "web-portal" {
			t.Errorf("#%d carries its rel info", pr.Number)
		}
	}
	if InfoOf(st.Find(model.Ident{Repo: portal, Number: 45})).Kind != KindHotfix || st.Find(model.Ident{Repo: portal, Number: 45}).Get(ExtIcon) != "hotfix" {
		t.Error("the hotfix is a hotfix with its icon")
	}
	if InfoOf(st.Find(model.Ident{Repo: portal, Number: 951})).Kind != KindOther {
		t.Error("the docs branch is 'other'")
	}
	m := st.Find(model.Ident{Repo: portal, Number: 133})
	dep, ok := m.Get(ExtDeploy).(deploys.State)
	if !ok || dep.Phase != "waiting" || dep.Text != "approve: prod" || dep.SHA != mergedSHA {
		t.Errorf("the merged release's deploy state: %+v %v", dep, ok)
	}
	if len(st.Stages) != 1 || st.Stages[0].Units == nil || st.Stages[0].Units.Units != 3 {
		t.Errorf("one stage row with its units: %+v", st.Stages)
	}
	// filters
	if got := nums(w.Sections(app, []string{"hotfix"}, "alice")); strings.Join(got, ",") != "releases (1),release:web-portal#45" {
		t.Errorf("the filter matches a release's kind: %v", got)
	}
	if got := nums(w.Sections(app, []string{"web-portal"}, "alice")); len(got) != 6 {
		t.Errorf("the filter matches the repo name on releases and the stage line: %v", got)
	}
	if got := nums(w.Sections(app, []string{"carol"}, "alice")); strings.Join(got, ",") != "releases (1),release:web-portal#45" {
		t.Errorf("the filter matches authors: %v", got)
	}
	if got := w.Sections(app, []string{"nothing-matches-this"}, "alice"); got != nil {
		t.Errorf("no match, no section: %v", got)
	}
	// the summary
	sum := text.SegsText(w.SummarySegs(app))
	if !strings.Contains(sum, "2 releases need approval") || !strings.Contains(sum, "1 deploy waits on you (D)") {
		t.Errorf("summary: %q", sum)
	}
	// AfterApply: the waiting environment is announced once, refresh speeds up
	w.AfterApply(app)
	if !logHas(app, "notice", "web-portal#133: prod now waits for your approval — press D on its row") {
		t.Errorf("prod waiting is announced with D: %v", app.Log)
	}
	if len(app.Soon) != 1 || app.Soon[0] != 30*time.Second {
		t.Errorf("a live deploy refreshes every 30s: %v", app.Soon)
	}
	n := len(app.Log)
	w.AfterApply(app)
	if len(app.Log) != n {
		t.Errorf("announced once, not every refresh: %v", app.Log[n:])
	}
	app.DetailItm, app.Listed = m, m
	w.AfterApply(app)
	if app.Bumps != 1 {
		t.Errorf("an open release's card is rebuilt: %d", app.Bumps)
	}
	if got := w.kindChipText(m); got != "approve: prod" {
		t.Errorf("a merged row's kind chip shows the deploy, not merged twice: %q", got)
	}
	// claims: a core row for the same PR is ours
	if !w.Claims(workflow.Row{Kind: "pr", Item: &model.PR{Repo: portal, Number: 44}}) || w.Claims(workflow.Row{Kind: "pr", Item: &model.PR{Repo: infra, Number: 44}}) {
		t.Error("Claims dedupes a release listed by the core")
	}
	if w.FortressLaneLabel(portal) != "web-portal" || w.FortressLaneLabel(infra) != "" {
		t.Error("the fortress lane label is the release repo's name")
	}
}

func TestSectionsWithoutWindowListQueued(t *testing.T) {
	w, app, st := fixture(t, false)
	if st.QueuedForWindow() != nil {
		t.Error("no window, nothing queued")
	}
	rows := w.Sections(app, nil, "alice")
	if rows[0].Item != "releases (4)" {
		t.Errorf("the labelled release is listed like any other: %v", rows[0].Item)
	}
}

func TestRefreshKeepsLastGood(t *testing.T) {
	w, app, _ := fixture(t, true)
	app.Fake.Fail["releases"] = &ghapi.Error{Message: "network error: timed out"}
	s, warns, _ := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	st := s.(*State)
	if len(st.Releases) != 5 || len(st.Stages) != 1 || len(warns) != 1 || !strings.Contains(warns[0], "last good") {
		t.Errorf("a failed read keeps every repo's last rows: %d %d %v", len(st.Releases), len(st.Stages), warns)
	}
	delete(app.Fake.Fail, "releases")
	delete(app.Fake.Releases, portal)
	s, warns, _ = w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	st = s.(*State)
	if len(st.Releases) != 5 || len(st.Stages) != 1 || len(warns) != 2 || !strings.Contains(warns[0], "showing") || !strings.Contains(warns[0], "'s data") {
		t.Errorf("a failed repo keeps its last good rows, not a blank: %d %v", len(st.Releases), warns)
	}
	if app.Fake.Reads["compare"] != 1 {
		t.Errorf("the compare is read once per (prod, stage) pair: %v", app.Fake.Reads)
	}
}

// stageCommits is a compare page (prod...stage): merge commits chained by
// first parent, a squash, and a side commit that is not on the
// first-parent line (it is inside the branch c3 merged).
func stageCommits() []ghapi.CompareCommit {
	return []ghapi.CompareCommit{
		{SHA: "c1", Message: "Merge pull request #610 from acme/dev\n\nfeat", Parents: []string{"m0", "f1"}},
		{SHA: "f2", Message: "fix: a side commit", Parents: []string{"c1"}},
		{SHA: "c2", Message: "feat: squash merged thing (#611)", Parents: []string{"c1"}},
		{SHA: "c3", Message: "Merge pull request #612 from acme/feat-x", Parents: []string{"c2", "f2"}},
	}
}

func TestStageUnitsAndText(t *testing.T) {
	compare := ghapi.CompareResult{AheadBy: 4, Commits: stageCommits()}
	st := StageUnits(compare, []string{"go/610", "go/999"}, []string{"hold/611"}, "go/", "hold/")
	if st != (Units{Ahead: 4, Units: 3, Go: 1, Hold: 1, Untagged: 1}) {
		t.Errorf("walks first parents, counts by tag, untagged = hold: %+v", st)
	}
	compare.AheadBy = 300
	st2 := StageUnits(compare, nil, nil, "go/", "hold/")
	if !st2.Truncated || st2.Untagged != 3 {
		t.Errorf("a page shorter than ahead_by is truncated: %+v", st2)
	}
	// a side commit on a merged branch whose subject names a PR is still
	// not a unit: only the first-parent chain counts
	side := ghapi.CompareResult{AheadBy: 3, Commits: []ghapi.CompareCommit{
		{SHA: "c1", Message: "Merge pull request #610 from acme/dev", Parents: []string{"m0", "f1"}},
		{SHA: "f2", Message: "chore: bump deps (#999)", Parents: []string{"c1"}},
		{SHA: "c3", Message: "Merge pull request #612 from acme/feat-x", Parents: []string{"c1", "f2"}},
	}}
	if got := StageUnits(side, []string{"go/999"}, nil, "go/", "hold/"); got != (Units{Ahead: 3, Units: 2, Untagged: 2}) {
		t.Errorf("a side commit on a merged branch does not count, nor its tag: %+v", got)
	}
	// a first-parent commit without a PR number is an untagged unit
	plain := ghapi.CompareResult{AheadBy: 1, Commits: []ghapi.CompareCommit{{SHA: "d1", Message: "hotfix: direct push", Parents: []string{"m0"}}}}
	if got := StageUnits(plain, nil, nil, "go/", "hold/"); got != (Units{Ahead: 1, Units: 1, Untagged: 1}) {
		t.Errorf("a plain first-parent commit is an untagged unit: %+v", got)
	}
	if got := StageUnits(ghapi.CompareResult{}, nil, nil, "go/", "hold/"); got != (Units{}) {
		t.Errorf("an empty page: %+v", got)
	}
	txt := StageText(&st, "main", "stage")
	if !strings.Contains(txt, "3 units ahead of main") || !strings.Contains(txt, "1 go") || !strings.Contains(txt, "1 hold") || !strings.Contains(txt, "1 untagged") {
		t.Errorf("stage text: %q", txt)
	}
	if got := StageText(&Units{}, "main", "stage"); got != "stage == main" {
		t.Errorf("even: %q", got)
	}
	if got := StageText(&Units{Ahead: 2}, "main", "stage"); got != "stage: nothing ahead of main" {
		t.Errorf("ahead without units: %q", got)
	}
	if got := StageText(&Units{Ahead: 1, Units: 1, Go: 1}, "prod", "staging"); !strings.HasPrefix(got, "staging: 1 unit ahead of prod") {
		t.Errorf("branch names from the config, singular: %q", got)
	}
	if got := StageText(&st2, "main", "stage"); !strings.Contains(got, "≥3 units") {
		t.Errorf("truncated reads >=: %q", got)
	}
}

func TestRowsRenderAndFit(t *testing.T) {
	w, app, st := fixture(t, true)
	rows := w.Sections(app, nil, "alice")
	kinds := w.RowKinds()
	for _, width := range []int{40, 60, 80, 120, 200} {
		byKind := map[string][]workflow.Row{}
		for _, r := range rows {
			if r.Kind != "header" {
				byKind[r.Kind] = append(byKind[r.Kind], r)
			}
		}
		for kind, items := range byKind {
			L := kinds[kind].Layout(app, items, width, 4, 10)
			for i, r := range items {
				segs := kinds[kind].Segs(app, r, L, width, i)
				if got := 2 + text.SegsWidth(segs); got > width-1 {
					t.Errorf("%s row %d at %d columns is %d wide", kind, i, width, got)
				}
			}
		}
	}
	items := []workflow.Row{{Kind: RowRelease, Item: st.Find(model.Ident{Repo: portal, Number: 951})}}
	L := kinds[RowRelease].Layout(app, items, 120, 4, 0)
	segs := kinds[RowRelease].Segs(app, items[0], L, 120, 0)
	txt := text.SegsText(segs)
	if !strings.Contains(txt, "not a release") || !strings.Contains(txt, "not a cut") || !strings.Contains(txt, "web-portal") {
		t.Errorf("the non-release row says 'not a release' and 'not a cut': %q", txt)
	}
	hasChip, hasDone := false, false
	for _, s := range segs {
		if s.Style == "chip.state" {
			hasChip = true
		}
		if s.Style == "title.done" && strings.Contains(s.Text, "upgrade notes") {
			hasDone = true
		}
	}
	if !hasChip || !hasDone {
		t.Errorf("its chip is the dim state chip and its title is dimmed: %+v", segs)
	}
	L200 := kinds[RowRelease].Layout(app, items, 200, 4, 0)
	if _, ok := L200["repo"]; !ok {
		t.Error("wide: the repo column is shown")
	}
	L40 := kinds[RowRelease].Layout(app, items, 40, 4, 0)
	if _, ok := L40["chip"]; !ok {
		t.Error("narrow: the chip never drops")
	}
	if _, ok := L40["age"]; ok {
		t.Error("narrow: the age drops first")
	}
	hot := []workflow.Row{{Kind: RowRelease, Item: st.Find(model.Ident{Repo: portal, Number: 45})}}
	segs = kinds[RowRelease].Segs(app, hot[0], kinds[RowRelease].Layout(app, hot, 120, 4, 0), 120, 0)
	txt = text.SegsText(segs)
	attn := false
	for _, s := range segs {
		if s.Style == "title.attn" {
			attn = true
		}
	}
	if !strings.Contains(txt, "needs approval") || !strings.Contains(txt, "hotfix") || !attn || !strings.Contains(txt, "carol") {
		t.Errorf("the hotfix row: %q", txt)
	}
	if segs[1].Style != "num" {
		t.Errorf("a settled row's number is plain: %q", segs[1].Style)
	}
	app.Effects.Start("new:web-portal#45", 10, nil)
	segs = kinds[RowRelease].Segs(app, hot[0], kinds[RowRelease].Layout(app, hot, 120, 4, 0), 120, 0)
	if segs[1].Style != "num.new" && segs[1].Style != "num.pulse" {
		t.Errorf("a new row's number pulses like a core row: %q", segs[1].Style)
	}
	// the stage row
	srow := workflow.Row{Kind: RowStage, Item: st.Stages[0]}
	sl := kinds[RowStage].Layout(app, []workflow.Row{srow}, 120, 4, 0)
	segs = kinds[RowStage].Segs(app, srow, sl, 120, 0)
	txt = text.SegsText(segs)
	if !strings.HasPrefix(strings.TrimLeft(txt, " ️"+string([]rune{0x1F680})), "") || !strings.Contains(txt, "stage") || !strings.Contains(txt, "3 units ahead of main") {
		t.Errorf("the stage row: %q", txt)
	}
	if segs[len(segs)-1].Style != "rev.attend" {
		t.Errorf("a hold or untagged unit asks for attention: %q", segs[len(segs)-1].Style)
	}
	if got := kinds[RowStage].URL(app, srow); got != "https://github.com/acme/web-portal/compare/main...stage" {
		t.Errorf("the stage row's URL is the compare page: %q", got)
	}
	if !kinds[RowStage].Open(app, srow) || !strings.Contains(app.Last(), "branch, not a PR") {
		t.Errorf("Enter on a stage row explains: %q", app.Last())
	}
	if kinds[RowRelease].URL != nil {
		t.Error("a release row's URL is the core's PR URL")
	}
	if got := w.SearchText(srow); !strings.Contains(got, "stage web-portal") {
		t.Errorf("stage search text: %q", got)
	}
	if got := w.SearchText(hot[0]); got != "web-portal hotfix" {
		t.Errorf("release search text: %q", got)
	}
}

func TestClassifyAndChips(t *testing.T) {
	w, app, st := fixture(t, true)
	_ = app
	c := w.Classify(st.Find(model.Ident{Repo: portal, Number: 44}), nil, "alice", false)
	if c == nil || c.Label != LabelNeeds || c.Style != "chip.await" || !c.NeedsYou || c.Done {
		t.Errorf("an open release needs approval: %+v", c)
	}
	c = w.Classify(st.Find(model.Ident{Repo: portal, Number: 951}), nil, "alice", false)
	if c == nil || c.Label != LabelOther || c.Style != "chip.state" || c.NeedsYou || !c.Done {
		t.Errorf("not a release: %+v", c)
	}
	c = w.Classify(st.Find(model.Ident{Repo: portal, Number: 42}), nil, "alice", false)
	if c == nil || c.Label != "approved by carol" || c.Style != "chip.approved" {
		t.Errorf("approved by carol: %+v", c)
	}
	// a detail PR without annotations, matched by identity
	detail := &model.PR{Repo: portal, Number: 44, State: "OPEN", HeadRefName: "release/2026-10-27"}
	if c = w.Classify(detail, nil, "alice", false); c == nil || c.Label != LabelNeeds {
		t.Errorf("the detail PR is classified through the state: %+v", c)
	}
	if w.Classify(&model.PR{Repo: infra, Number: 3543}, nil, "alice", false) != nil {
		t.Error("a plain PR is not ours")
	}
	if w.ChipStyles()[LabelNeeds] != "chip.await" || w.NeedsYou()[0] != LabelNeeds {
		t.Error("chip styles and needs-you labels")
	}
	w.EnrichDetail(context.Background(), app.Fake, detail, nil)
	if InfoOf(detail) == nil || InfoOf(detail).Kind != KindRelease {
		t.Error("EnrichDetail copies the rel info by identity")
	}
	detail2 := &model.PR{Repo: portal, Number: 133}
	w.EnrichDetail(context.Background(), app.Fake, detail2, st.Find(model.Ident{Repo: portal, Number: 133}))
	if _, ok := detail2.Get(ExtDeploy).(deploys.State); !ok || detail2.Get(ExtIcon) != "release" {
		t.Error("EnrichDetail copies the deploy state and icon from the listed row")
	}
}

func TestDetailCards(t *testing.T) {
	w, app, st := fixture(t, true)
	flat := func(lines []text.Line) string {
		var b strings.Builder
		for _, l := range lines {
			b.WriteString(l.Text())
			b.WriteString("\n")
		}
		return b.String()
	}
	m := st.Find(model.Ident{Repo: portal, Number: 133})
	card := flat(w.DetailCards(app, m, m, 80))
	if !strings.Contains(card, "deploys on "+model.ShortSHA(mergedSHA)) || !strings.Contains(card, "approve: prod") || !strings.Contains(card, "Migrate") || !strings.Contains(card, "waiting for prod") {
		t.Errorf("the merged card lists the deploy and its runs:\n%s", card)
	}
	for _, l := range w.DetailCards(app, m, m, 80) {
		if wd := text.SegsWidth(l.Segs); wd != 80 {
			t.Errorf("card line %d wide: %q", wd, l.Text())
		}
	}
	unread := mergedRelease()
	unread.Repo, unread.Number = portal, 134
	if card = flat(w.DetailCards(app, unread, nil, 80)); !strings.Contains(card, "not read yet") {
		t.Errorf("a merged release without a read says D reads it:\n%s", card)
	}
	hot := st.Find(model.Ident{Repo: portal, Number: 45})
	if card = flat(w.DetailCards(app, hot, hot, 80)); !strings.Contains(card, "emergency by definition") || !strings.Contains(card, "hotfix/login") {
		t.Errorf("the hotfix card:\n%s", card)
	}
	other := st.Find(model.Ident{Repo: portal, Number: 951})
	if card = flat(w.DetailCards(app, other, other, 80)); !strings.Contains(card, "refuse anything else") || !strings.Contains(card, "not a release") {
		t.Errorf("the non-release card:\n%s", card)
	}
	rel := st.Find(model.Ident{Repo: portal, Number: 44})
	if card = flat(w.DetailCards(app, rel, rel, 80)); !strings.Contains(card, "queues it for tonight's") || !strings.Contains(card, "/release-now") {
		t.Errorf("the release card says what a does:\n%s", card)
	}
	q := st.Find(model.Ident{Repo: portal, Number: 42})
	if card = flat(w.DetailCards(app, q, q, 80)); !strings.Contains(card, "queued") {
		t.Errorf("a queued release shows its window chip:\n%s", card)
	}
	if w.DetailCards(app, &model.PR{Repo: infra, Number: 1}, nil, 80) != nil {
		t.Error("no card for a plain PR")
	}
	stuck := mergedRelease()
	stuck.Repo, stuck.Number = portal, 135
	stuck.Set(ExtDeploy, deploys.State{SHA: mergedSHA, Phase: "stuck", Text: "stuck? 9h43m", Runs: []deploys.Row{{Name: "Deploy", What: "in progress 9h43m - stuck?", Style: "chip.rolled", RunID: 77}}})
	if card = flat(w.DetailCards(app, stuck, nil, 80)); !strings.Contains(card, "run 77 has gone longer than 45 minutes") {
		t.Errorf("a stuck run is called out:\n%s", card)
	}
	if rows := w.HelpRows(); len(rows) != 3 || rows[0][0] != "releases" || !strings.Contains(rows[0][1], "/release-now") || !strings.Contains(rows[2][1], "rail") {
		t.Errorf("help rows: %v", rows)
	}
	if rows := w.LegendRows(); len(rows) != 2 || !strings.Contains(text.SegsText(rows[1]), "released") {
		t.Errorf("two legend rows, the second the rail's: %v", rows)
	}
}

func TestConstructor(t *testing.T) {
	ctor := workflow.Registry[Name]
	if ctor == nil {
		t.Fatal("registered in init")
	}
	cfg := config.Default()
	if w, err := ctor(&workflow.Context{Config: cfg}); w != nil || err != nil {
		t.Error("nil without [workflows.release_pr]")
	}
	cfg = newCfg(true)
	w, err := ctor(&workflow.Context{Config: cfg, Repos: cfg.Reviews.Repos})
	if err != nil || w == nil || w.Name() != Name || w.Order() != 25 {
		t.Errorf("built from the config: %v %v", w, err)
	}
	if w.(*Workflow).WindowEnabled() {
		t.Error("the window workflow is not enabled unless it is in the context")
	}
	if kinds := w.RowKinds(); kinds[RowRelease].Icon != "release" || kinds[RowStage].Icon != "stage" {
		t.Error("row kinds")
	}
	if _, err := workflow.NewSet(nil, w); err != nil {
		t.Errorf("no key or kind clashes with the core: %v", err)
	}
	cfg.ReleasePR.Org = ""
	w2 := New(&workflow.Context{Config: cfg, Repos: cfg.Reviews.Repos})
	if w2.org != "acme" {
		t.Errorf("the org falls back to the review repos' owner: %q", w2.org)
	}
}

// TestLastGoodRowsAreFreshCopies: when a read fails and the last good
// rows are shown again, they are copies. The originals may be on screen,
// and re-annotating them would write their Ext maps under the UI's feet.
func TestLastGoodRowsAreFreshCopies(t *testing.T) {
	w, app, first := fixture(t, true)
	app.Fake.Fail["releases"] = &ghapi.Error{Status: 502, Message: "Bad Gateway"}
	s, _, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	if err != nil {
		t.Fatalf("a 502 is a warning, not an error: %v", err)
	}
	st := s.(*State)
	if len(st.Releases) != len(first.Releases) {
		t.Fatalf("rows: %d vs %d", len(st.Releases), len(first.Releases))
	}
	for i, p := range st.Releases {
		if p == first.Releases[i] {
			t.Fatalf("row %d is the same pointer as the previous state's", i)
		}
		if p.Ext != nil && first.Releases[i].Ext != nil && &p.Ext == &first.Releases[i].Ext {
			t.Fatalf("row %d shares its Ext map", i)
		}
		p.Set("probe", true)
		if first.Releases[i].Get("probe") != nil {
			t.Fatalf("row %d: writing the copy wrote the original", i)
		}
	}
}

func TestReleaseRefreshReturnsRateLimit(t *testing.T) {
	w, app, _ := fixture(t, true)
	app.Fake.Fail["releases"] = &ghapi.Error{Status: 403, Message: "secondary rate limit", RetryAt: time.Now().Add(time.Minute)}
	s, warns, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	if !ghapi.IsRateLimit(err) {
		t.Fatalf("a rate limit is returned so the refresh backs off: %v", err)
	}
	if st := s.(*State); st == nil || len(st.Releases) == 0 || len(warns) == 0 {
		t.Error("the last good state still comes back with it")
	}
}

func TestStateCIPRs(t *testing.T) {
	var none *State
	if none.CIPRs() != nil {
		t.Error("a nil state contributes nothing")
	}
	_, _, st := fixture(t, true)
	if got := st.CIPRs(); len(got) != len(st.Releases) {
		t.Errorf("CIPRs = %d, Releases = %d", len(got), len(st.Releases))
	}
	w, _ := newWF(t, false)
	if !w.ProvidesDeploys() {
		t.Error("the release workflow provides deploys (D in the list bar)")
	}
}

func TestListOtherPRsFalseHidesThem(t *testing.T) {
	w, app, _ := fixture(t, true)
	all := w.Sections(app, nil, "alice")
	has := func(rows []workflow.Row, n int) bool {
		for _, r := range rows {
			if p, ok := r.Item.(*model.PR); ok && p.Number == n {
				return true
			}
		}
		return false
	}
	if !has(all, 951) {
		t.Fatal("the fixture's docs PR (951) lists by default")
	}
	w.cfg.ListOtherPRs = false
	some := w.Sections(app, nil, "alice")
	if has(some, 951) {
		t.Error("list_other_prs = false hides PRs that are not releases")
	}
	if !has(some, 44) || !has(some, 45) {
		t.Error("releases and hotfixes still list")
	}
	if all[0].Item != some[0].Item {
		t.Errorf("the header count is releases only either way: %v vs %v", all[0].Item, some[0].Item)
	}
}

func TestDeployStuckSecsReachesTheCache(t *testing.T) {
	cfg := newCfg(false)
	cfg.ReleasePR.DeployStuckSecs = 600
	w := New(&workflow.Context{Config: cfg, Repos: cfg.Reviews.Repos})
	if w.deploy.Stuck() != 10*time.Minute {
		t.Errorf("deploy stuck = %v", w.deploy.Stuck())
	}
	cfg.ReleasePR.DeployRefreshSecs = 0
	if w.refreshEvery() != deploys.RefreshEvery {
		t.Errorf("refreshEvery default = %v", w.refreshEvery())
	}
}
