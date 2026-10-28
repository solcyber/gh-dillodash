package ui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plain(s string) string { return ansiRE.ReplaceAllString(s, "") }

var (
	infra  = model.Repo{Owner: "acme", Name: "infra"}
	portal = model.Repo{Owner: "acme", Name: "web-portal"}
	t0     = time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC)
)

func pr(repo model.Repo, n int, title, author string, draft bool) *model.PR {
	p := &model.PR{Repo: repo, Number: n, Title: title, IsDraft: draft, State: "OPEN",
		HeadRefName: "feature-" + author, BaseRefName: "main", HeadRefOid: strings.Repeat("a", 40),
		CreatedAt: t0, UpdatedAt: t0, Additions: 10, Deletions: 2, ChangedFiles: 1}
	p.Author.Login = author
	p.Body = "## Summary\n\nA change by " + author + ".\n\n```hcl\nresource \"x\" \"y\" {}\n```\n"
	return p
}

func issue(repo model.Repo, n int, title, author string) *model.Issue {
	i := &model.Issue{Repo: repo, Number: n, Title: title, State: "OPEN", CreatedAt: t0, UpdatedAt: t0}
	i.Author.Login = author
	i.Labels.Nodes = []model.Label{{Name: "ops"}}
	i.Body = "Please rotate the keys."
	return i
}

// harness builds a model on the acme fixture with scripted prompts and a
// message hook, and drives Update directly.
type harness struct {
	t    *testing.T
	m    *Model
	gh   *fake.Client
	msgs chan tea.Msg
}

func newHarness(t *testing.T, repos ...model.Repo) *harness {
	t.Helper()
	fx.Motion = "off"
	glyphs.Use(glyphs.ASCII)
	theme.Setup(colorprofile.Ascii, false)
	if len(repos) == 0 {
		repos = []model.Repo{infra}
	}
	cfg := config.Default()
	cfg.Reviews.Repos = repos
	gh := fake.New("alice")
	gh.Open = map[model.Repo]ghapi.ListResult{}
	gh.PRs = map[model.Ident]*model.PR{}
	gh.Issues = map[model.Ident]*model.Issue{}
	gh.Users = map[model.Repo][]model.Actor{}
	for _, r := range repos {
		p1 := pr(r, 101, "widen the deploy role for "+r.Name, "bob", false)
		p2 := pr(r, 102, "wip: theme for "+r.Name, "infra-bot", true)
		i1 := issue(r, 7, "rotate the signing keys", "carol")
		gh.Open[r] = ghapi.ListResult{PRs: []*model.PR{p1, p2}, Issues: []*model.Issue{i1}}
		d1 := *p1
		d1.Comments.Nodes = []model.Comment{{Author: model.Actor{Login: "carol"}, CreatedAt: t0, Body: "looks fine"}}
		gh.PRs[p1.Ident()] = &d1
		gh.Issues[i1.Ident()] = i1
		gh.Users[r] = []model.Actor{{Login: "alice"}, {Login: "bob", Name: "Bob"}, {Login: "carol"}}
	}
	set, err := workflow.NewSet(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), Options{Config: cfg, GH: gh, Workflows: set, Me: "alice", Host: "github.com", Version: "test"})
	h := &harness{t: t, m: m, gh: gh, msgs: make(chan tea.Msg, 64)}
	m.sendFn = func(msg tea.Msg) { h.msgs <- msg }
	m.tp = &testPrompter{}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return h
}

// pump applies messages from the goroutines until the model is idle (or
// a modal waits for the test to answer it).
func (h *harness) pump() {
	h.t.Helper()
	if err := settle(h.m, h.msgs, 5*time.Second); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) load() {
	h.m.Init()
	h.pump()
}

func (h *harness) key(r rune) {
	h.m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	h.pump()
}

func (h *harness) named(name string) {
	var code rune
	switch name {
	case "enter":
		code = tea.KeyEnter
	case "esc":
		code = tea.KeyEscape
	}
	h.m.Update(tea.KeyPressMsg{Code: code})
	h.pump()
}

func (h *harness) screen() string { return plain(h.m.View().Content) }

func TestListLoadsAndRenders(t *testing.T) {
	h := newHarness(t)
	h.load()
	s := h.screen()
	for _, want := range []string{"pull requests  2", "#101", "widen the deploy role", "bob", "issues  1", "rotate the signing keys", "1 need you", "acme/infra", "alice"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "DRAFT") {
		t.Errorf("draft chip missing")
	}
}

func TestOpenDetailAndBack(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	if h.m.view != ViewDetail {
		t.Fatalf("view = %s", h.m.view)
	}
	s := h.screen()
	for _, want := range []string{"#101", "description", "A change by bob", "comments & reviews", "carol", "looks fine", "feature-bob", "main"} {
		if !strings.Contains(s, want) {
			t.Errorf("detail lacks %q:\n%s", want, s)
		}
	}
	h.key('q')
	if h.m.view != ViewList {
		t.Fatalf("view after q = %s", h.m.view)
	}
}

func TestApproveIsAPlainReview(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	h.m.tp.Confirm = func(q string) bool {
		if !strings.Contains(q, "#101") || !strings.Contains(q, "alice") {
			t.Errorf("confirm text: %q", q)
		}
		return true
	}
	h.key('a')
	if got := h.gh.Approvals; len(got) != 1 || got[0] != 101 {
		t.Fatalf("approvals = %v", got)
	}
	if !strings.Contains(h.m.status, "review-approved #101") {
		t.Errorf("status = %q", h.m.status)
	}
	if !h.m.localApproved(h.gh.PRs[model.Ident{Repo: infra, Number: 101}]) {
		t.Error("not marked approved locally")
	}
	// a declined confirmation posts nothing
	h.m.tp.Confirm = func(string) bool { return false }
	h.key('a')
	if len(h.gh.Approvals) != 1 {
		t.Errorf("approvals after cancel = %v", h.gh.Approvals)
	}
}

func TestApproveRefusesDraftAndSelf(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.key('j') // the draft
	h.gh.PRs[model.Ident{Repo: infra, Number: 102}] = h.gh.Open[infra].PRs[1]
	h.named("enter")
	h.key('a')
	if len(h.gh.Approvals) != 0 || !strings.Contains(h.m.status, "draft") {
		t.Errorf("draft: approvals=%v status=%q", h.gh.Approvals, h.m.status)
	}
}

func TestCommentInline(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	h.m.tp.Prompt = func(p string, _ workflow.PromptOpts) (string, bool) { return "ship it", true }
	h.key('c')
	posted := h.gh.CommentsPosted
	if len(posted) != 1 {
		t.Fatalf("comments = %+v", posted)
	}
	if posted[0].Body != "ship it" || posted[0].Number != 101 {
		t.Errorf("comment = %+v", posted[0])
	}
}

func TestFilterAndShowAll(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.mu.Lock()
	h.m.filter = "rotate"
	h.m.refilter()
	h.m.mu.Unlock()
	s := h.screen()
	if strings.Contains(s, "widen the deploy") || !strings.Contains(s, "rotate the signing") {
		t.Errorf("filter not applied:\n%s", s)
	}
	h.named("esc")
	if h.m.filter != "" {
		t.Error("esc should clear the filter")
	}
}

func TestMultiRepoRows(t *testing.T) {
	h := newHarness(t, infra, portal)
	h.load()
	s := h.screen()
	if !strings.Contains(s, "infra#101") || !strings.Contains(s, "web-portal#101") {
		t.Errorf("multi-repo refs missing:\n%s", s)
	}
	if !strings.Contains(s, "pull requests  4") {
		t.Errorf("expected four PRs:\n%s", s)
	}
}

func TestGotoOpensIssue(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "7", true }
	h.key(':')
	if h.m.view != ViewDetail || h.m.detailKind != "issue" {
		t.Fatalf("view=%s kind=%s err=%q", h.m.view, h.m.detailKind, h.m.errText)
	}
	if !strings.Contains(h.screen(), "rotate the signing keys") {
		t.Error("issue detail not shown")
	}
}

func TestRefreshErrorBacksOff(t *testing.T) {
	h := newHarness(t)
	h.gh.Fail = map[string]error{"list": &ghapi.Error{Method: "POST", URL: "x", Status: 502, Message: "bad gateway"}}
	h.load()
	if h.m.loaded || h.m.errText == "" {
		t.Fatalf("loaded=%v err=%q", h.m.loaded, h.m.errText)
	}
	if !strings.Contains(h.screen(), "couldn't load acme/infra") {
		t.Errorf("screen:\n%s", h.screen())
	}
	if h.m.failStreak != 1 {
		t.Errorf("failStreak = %d", h.m.failStreak)
	}
}

// TestPartialListFailureKeepsOtherRepos: one repo the token cannot read
// is a warning on its line, not a failed refresh.
func TestPartialListFailureKeepsOtherRepos(t *testing.T) {
	h := newHarness(t, infra, portal)
	delete(h.gh.Open, portal)
	h.load()
	if !h.m.loaded || h.m.errText != "" {
		t.Fatalf("loaded=%v err=%q", h.m.loaded, h.m.errText)
	}
	if !strings.Contains(h.m.warn, "web-portal") {
		t.Errorf("warn %q does not name the failed repo", h.m.warn)
	}
	if s := h.screen(); !strings.Contains(s, "infra#101") || strings.Contains(s, "couldn't load") {
		t.Errorf("the readable repo still lists:\n%s", s)
	}
}

// TestManualRefreshLiftsCILatch: a token that could not read Actions
// latches CI off for the repo; r re-tries it once the token is fixed.
func TestManualRefreshLiftsCILatch(t *testing.T) {
	h := newHarness(t)
	h.gh.Fail["workflow_runs"] = &ghapi.Error{Status: 403, Message: "Resource not accessible by personal access token"}
	h.load()
	p := h.gh.Open[infra].PRs[0]
	if c, ok := h.m.ciOf(p); !ok || c.Glyphs != "?" || !strings.Contains(h.m.warn, "degraded") || !strings.Contains(h.m.warn, "Actions: read") {
		t.Fatalf("latched: %+v %v warn=%q", c, ok, h.m.warn)
	}
	delete(h.gh.Fail, "workflow_runs")
	h.gh.Runs = map[string][]model.WorkflowRun{p.HeadRefOid: {{ID: 1, Name: "ci", Path: ".github/workflows/ci.yml", Status: "completed", Conclusion: "success"}}}
	h.key('r')
	if c, ok := h.m.ciOf(p); !ok || c.Glyphs != "+" || strings.Contains(h.m.warn, "degraded") {
		t.Errorf("after r: %+v %v warn=%q", c, ok, h.m.warn)
	}
}

// rateLimitedWorkflow is a workflow whose Refresh hits the wall.
type rateLimitedWorkflow struct {
	workflow.Base
	until time.Time
}

func (rateLimitedWorkflow) Name() string { return "stub" }
func (w rateLimitedWorkflow) Refresh(context.Context, ghapi.Client, []model.Repo, []*model.PR, []*model.Issue) (workflow.State, []string, error) {
	return nil, []string{"stub: partial"}, &ghapi.Error{Status: 403, Message: "secondary rate limit", RetryAt: w.until}
}

// TestWorkflowRateLimitPausesRefresh: a rate limit met by a workflow
// pauses the next refresh like one met by the list, while the list
// itself still lands.
func TestWorkflowRateLimitPausesRefresh(t *testing.T) {
	h := newHarness(t)
	until := time.Now().Add(90 * time.Second)
	set, err := workflow.NewSet(nil, rateLimitedWorkflow{until: until})
	if err != nil {
		t.Fatal(err)
	}
	h.m.wf = set
	h.load()
	if !h.m.loaded || h.m.errText != "" {
		t.Fatalf("the list still lands: loaded=%v err=%q", h.m.loaded, h.m.errText)
	}
	if !h.m.rateLimitedUntil.Equal(until) {
		t.Errorf("rateLimitedUntil = %v, want %v", h.m.rateLimitedUntil, until)
	}
	if !strings.Contains(h.m.warn, "rate limited") || !strings.Contains(h.m.warn, "stub: partial") {
		t.Errorf("warn %q", h.m.warn)
	}
	// and the manual refresh refuses to hammer the wall
	h.key('r')
	if !strings.Contains(h.m.status, "held") {
		t.Errorf("r while rate limited: status %q", h.m.status)
	}
}

// TestCtrlCInterrupts: raw mode delivers ctrl-c as a key; it quits as an
// interrupt (exit 130), q quits normally, and inside a prompt ctrl-c
// only cancels the prompt.
func TestCtrlCInterrupts(t *testing.T) {
	h := newHarness(t)
	h.load()
	cmd := h.m.handleKey(widgets.Key{Name: "ctrl+c"})
	if cmd == nil {
		t.Fatal("ctrl+c returned no command")
	}
	if _, ok := cmd().(tea.InterruptMsg); !ok {
		t.Errorf("ctrl+c -> %T, want InterruptMsg", cmd())
	}
	cmd = h.m.handleKey(widgets.Key{Rune: 'q'})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q -> %T, want QuitMsg", cmd())
	}
	h.m.modal = widgets.NewPrompt("x", "")
	h.m.modalDone = make(chan struct{})
	if cmd := h.m.handleKey(widgets.Key{Name: "ctrl+c"}); cmd != nil {
		t.Error("in a prompt ctrl+c belongs to the prompt")
	}
	if h.m.modal != nil {
		t.Error("the prompt should have cancelled")
	}
}

// TestQuitMidActionArmsThenQuits: q during an action warns first and
// quits on a second press within the arming window.
func TestQuitMidActionArmsThenQuits(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.mu.Lock()
	h.m.action = true
	h.m.mu.Unlock()
	if cmd := h.m.handleKey(widgets.Key{Rune: 'q'}); cmd != nil {
		t.Fatal("first q while an action runs must not quit")
	}
	if !strings.Contains(h.m.status, "still running") {
		t.Errorf("status %q", h.m.status)
	}
	if cmd := h.m.handleKey(widgets.Key{Rune: 'q'}); cmd == nil {
		t.Error("second q within the window quits")
	}
	h.m.quitArmedAt = -100
	if cmd := h.m.handleKey(widgets.Key{Rune: 'q'}); cmd != nil {
		t.Error("after the window the first press warns again")
	}
}

// TestConfigKnobsAreWired: ci_recheck_secs, deploy_stuck_secs and
// deploy_refresh_secs reach the code that uses them.
func TestConfigKnobsAreWired(t *testing.T) {
	cfg := config.Default()
	cfg.Reviews.Repos = []model.Repo{infra}
	cfg.Reviews.CIRecheckSecs = 60
	cfg.ReleasePR = config.DefaultReleasePR()
	cfg.ReleasePR.DeployStuckSecs = 120
	cfg.ReleasePR.DeployRefreshSecs = 7
	m := New(context.Background(), Options{Config: cfg, GH: fake.New("alice"), Me: "alice"})
	if m.ci.Recheck != time.Minute {
		t.Errorf("Recheck = %v", m.ci.Recheck)
	}
	if m.deployCache.Stuck() != 2*time.Minute {
		t.Errorf("deploy stuck = %v", m.deployCache.Stuck())
	}
	if m.deployRefresh() != 7*time.Second {
		t.Errorf("deploy refresh = %v", m.deployRefresh())
	}
	cfg.ReleasePR = nil
	m = New(context.Background(), Options{Config: cfg, GH: fake.New("alice"), Me: "alice"})
	if m.deployCache.Stuck() != deploys.StuckAfter || m.deployRefresh() != deploys.RefreshEvery {
		t.Error("defaults without release_pr")
	}
}
