package ui

import (
	"context"
	"regexp"
	"slices"
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
	"github.com/solcyber/gh-dillodash/internal/text"
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
	opened, copied []string // URLs the o and y keys would have started a program with
	t              *testing.T
	m              *Model
	gh             *fake.Client
	msgs           chan tea.Msg
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
	// o and y must never start the real browser or clipboard tool from a
	// test (every run used to open the fixture's URLs in Chrome); the
	// harness records them instead.
	m.tp = &testPrompter{
		OpenURL: func(url string) bool { h.opened = append(h.opened, url); return true },
		Copy:    func(s string) string { h.copied = append(h.copied, s); return "test clipboard" },
	}
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
	if h.m.backoff.Streak() != 1 {
		t.Errorf("streak = %d", h.m.backoff.Streak())
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
	if got := h.m.backoff.Until(); !got.Equal(until) {
		t.Errorf("backoff until = %v, want %v", got, until)
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

func TestToKeyMultiRune(t *testing.T) {
	for _, tc := range []struct {
		msg  tea.KeyPressMsg
		want widgets.Key
	}{
		{tea.KeyPressMsg{Code: 'c', Text: "c"}, widgets.R('c')},
		{tea.KeyPressMsg{Code: '中', Text: "中"}, widgets.R('中')},
		{tea.KeyPressMsg{Code: '👍', Text: "👍🏽"}, widgets.T("👍🏽")},
		{tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, widgets.K("ctrl+c")},
		{tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, widgets.R(' ')},
	} {
		if got := toKey(tc.msg); got != tc.want {
			t.Errorf("toKey(%v) = %+v, want %+v", tc.msg, got, tc.want)
		}
	}
}

// TestPasteIntoFilter: a bracketed paste lands in the live filter.
func TestPasteIntoFilter(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.tp.Prompt = nil // the real prompt
	h.m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	h.pump() // the action goroutine is now parked on the modal
	if h.m.modal == nil {
		t.Fatal("no prompt up")
	}
	h.m.Update(tea.PasteMsg{Content: "rotate"})
	if h.m.filter != "rotate" {
		t.Fatalf("live filter after the paste: %q", h.m.filter)
	}
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	h.pump()
	if h.m.filter != "rotate" || h.m.modal != nil {
		t.Errorf("filter %q modal %v", h.m.filter, h.m.modal)
	}
	if s := h.screen(); !strings.Contains(s, "rotate the signing keys") || strings.Contains(s, "widen the deploy") {
		t.Errorf("screen:\n%s", s)
	}
}

// TestTypingInModalKeepsAmbient: keys typed in a prompt count as
// activity, so ambient motion does not idle out while someone types.
func TestTypingInModalKeepsAmbient(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.mu.Lock()
	h.m.modal = widgets.NewPrompt("x", "")
	h.m.modalDone = make(chan struct{})
	h.m.lastKey = -1000
	h.m.mu.Unlock()
	h.m.handleKey(widgets.R('a'))
	if h.m.lastKey < 0 {
		t.Error("lastKey did not advance for a key typed in a prompt")
	}
	h.m.handleKey(widgets.K("esc"))
}

// TestIdleTickSleepsToTheNextBoundary: with nothing animating the loop
// sleeps until the next instant the screen changes.
func TestIdleTickSleepsToTheNextBoundary(t *testing.T) {
	h := newHarness(t)
	h.load()
	was := fx.Now
	t.Cleanup(func() { fx.Now = was })
	now := 1000.0
	fx.Now = func() float64 { return now }
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	h.m.fx.Stop("splash")
	h.m.ambient, h.m.animating, h.m.status = false, false, ""
	h.m.nextRefreshAt = now + 120.5 // the header countdown ticks at .5
	near := func(got, want int) bool { return got >= want-10 && got <= want+10 }
	if ms := h.m.tickMS(); !near(ms, 505) {
		t.Errorf("countdown boundary: %d", ms)
	}
	h.m.status, h.m.statusKind, h.m.statusAt = "x", "info", now-5.6 // fades at 5.2, gone at 6.0
	if ms := h.m.tickMS(); !near(ms, 405) {
		t.Errorf("toast expiry: %d", ms)
	}
	h.m.status = ""
	h.m.nextRefreshAt = now + 0.3
	if ms := h.m.tickMS(); !near(ms, 305) {
		t.Errorf("refresh due: %d", ms)
	}
	h.m.nextRefreshAt = now + 3600
	h.m.loaded = false
	h.m.errText = ""
	if ms := h.m.tickMS(); ms != idleMaxMS {
		t.Errorf("nothing to wait for: %d", ms)
	}
	h.m.loaded = true
	h.m.animating = true
	if ms := h.m.tickMS(); ms != TickMS {
		t.Errorf("a spinner ticks: %d", ms)
	}
}

func TestStaleTickIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.mu.Lock()
	h.m.nextRefreshAt = 0
	gen := h.m.tickGen
	h.m.mu.Unlock()
	_, cmd := h.m.Update(tickMsg{gen: gen - 1})
	if cmd != nil || h.m.refreshing {
		t.Error("a stale tick must do nothing")
	}
	_, cmd = h.m.Update(tickMsg{gen: gen})
	if cmd == nil || !h.m.refreshing {
		t.Error("the live tick refreshes and reschedules")
	}
	h.pump()
}

func TestKeyWakesTheClock(t *testing.T) {
	h := newHarness(t)
	h.load()
	gen := h.m.tickGen
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if cmd == nil || h.m.tickGen != gen+1 {
		t.Errorf("a key reticks: cmd=%v gen %d -> %d", cmd != nil, gen, h.m.tickGen)
	}
	h.pump()
}

func TestGlideAndRawResetOnOpen(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.mu.Lock()
	h.m.glide["detail"] = glideEntry{40, 0, fx.Now()}
	h.m.raw = true
	h.m.mu.Unlock()
	h.named("enter")
	if _, ok := h.m.glide["detail"]; ok {
		t.Error("the detail glide survived opening another PR")
	}
	if h.m.raw {
		t.Error("raw markdown mode stuck across PRs")
	}
}

func TestSearchClearedWhenLeavingThePage(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	h.m.mu.Lock()
	h.m.searchQ = "summary"
	h.m.mu.Unlock()
	h.key('f')
	if h.m.view != ViewFiles || h.m.searchQ != "" {
		t.Errorf("view %s searchQ %q", h.m.view, h.m.searchQ)
	}
}

func TestNextMatchWithoutASearch(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	h.key('n')
	if !strings.Contains(h.m.status, "no search yet") {
		t.Errorf("status %q", h.m.status)
	}
}

func TestRefilterFollowsIdentThenNearest(t *testing.T) {
	h := newHarness(t)
	h.load()
	// rows: header, #101, #102, header, issue #7: select the issue
	h.key('G')
	if row, ok := h.m.Selected(); !ok || row.Kind != "issue" {
		t.Fatalf("G selects the issue, got row %d", h.m.selected)
	}
	h.m.mu.Lock()
	h.m.filter = "widen" // only #101 survives
	h.m.refilter()
	sel := h.m.rows[h.m.selected]
	h.m.mu.Unlock()
	if p, ok := sel.Item.(*model.PR); !ok || p.Number != 101 {
		t.Errorf("the cursor should park on the nearest row, got %v", sel)
	}
}

func TestManualRefreshReportsOnce(t *testing.T) {
	h := newHarness(t)
	h.load()
	if strings.Contains(h.m.status, "refreshed at") {
		t.Fatal("an automatic refresh must not toast")
	}
	h.key('r')
	if !strings.Contains(h.m.status, "refreshed at") {
		t.Errorf("r reports: %q", h.m.status)
	}
	h.m.mu.Lock()
	h.m.status = ""
	h.m.nextRefreshAt = 0
	gen := h.m.tickGen
	h.m.mu.Unlock()
	h.m.Update(tickMsg{gen: gen})
	h.pump()
	if strings.Contains(h.m.status, "refreshed at") {
		t.Error("the next automatic refresh must not reuse the manual toast")
	}
}

func TestGotoWhileActionRunningIsANoop(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.mu.Lock()
	h.m.action = true
	h.m.view = ViewFortress
	h.m.detailFrom = ViewList
	h.m.mu.Unlock()
	h.m.handleKey(widgets.R(':'))
	if h.m.detailFrom != ViewList {
		t.Error("a swallowed : must not change detailFrom")
	}
	h.m.mu.Lock()
	h.m.action, h.m.view = false, ViewList
	h.m.mu.Unlock()
}

func TestApproveRefusesAlreadyApproved(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	h.m.tp.Confirm = func(string) bool { return true }
	h.key('a')
	h.key('a')
	if len(h.gh.Approvals) != 1 || !strings.Contains(h.m.status, "already approved by you") {
		t.Errorf("approvals %v status %q", h.gh.Approvals, h.m.status)
	}
}

func TestCommentInlineCancelAndBlank(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "", false }
	h.key('c')
	if h.m.status != "comment cancelled" || len(h.gh.CommentsPosted) != 0 {
		t.Errorf("Esc: status %q posted %d", h.m.status, len(h.gh.CommentsPosted))
	}
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "   ", true }
	h.key('c')
	if h.m.status != "comment cancelled (empty)" || len(h.gh.CommentsPosted) != 0 {
		t.Errorf("blank: status %q posted %d", h.m.status, len(h.gh.CommentsPosted))
	}
}

// TestSearchOffsetsSurviveCaseFolding: a line whose lower-casing changes
// its rune count (İ) must not shift the hit overlay.
func TestSearchOffsetsSurviveCaseFolding(t *testing.T) {
	h := newHarness(t)
	d := h.gh.PRs[h.gh.Open[infra].PRs[0].Ident()]
	d.Body = "Deploy to İstanbul and STRASSE first.\n"
	h.load()
	h.named("enter")
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "istanbul", true }
	h.key('/')
	_ = h.screen() // the detail lines are laid out by the frame
	h.m.mu.Lock()
	hits := h.m.currentHits()
	h.m.mu.Unlock()
	if len(hits) != 1 {
		t.Fatalf("hits %+v", hits)
	}
	line := h.m.detailLines[hits[0].line].Text()
	runes := []rune(line)
	if got := string(runes[hits[0].start:hits[0].end]); got != "İstanbul" {
		t.Errorf("hit covers %q in %q", got, line)
	}
}

// TestOpenAndCopyStayInTheHarness: o and y hand the row's URL to the
// model's hooks and never start a program; the status line says so.
// (Before the hooks, every test run opened the fixture's URLs in the
// developer's browser and overwrote their clipboard.)
func TestOpenAndCopyStayInTheHarness(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.key('o')
	h.key('y')
	want := []string{"https://github.com/acme/infra/pull/101"}
	if !slices.Equal(h.opened, want) || !slices.Equal(h.copied, want) {
		t.Fatalf("opened %v copied %v, want %v", h.opened, h.copied, want)
	}
	if !strings.Contains(h.screen(), "copied https://github.com/acme/infra/pull/101 to the test clipboard") {
		t.Errorf("screen:\n%s", h.screen())
	}
	if m := New(context.Background(), Options{Config: config.Default(), GH: h.gh, Workflows: h.m.wf, Me: "alice", Host: "github.com"}); m.tp != nil {
		t.Error("a Model outside the harness must reach the real programs")
	}
}

// stubPoller is a workflow with something in flight to watch.
type stubPoller struct {
	workflow.Base
	live    bool
	err     error
	polls   int
	applied int
}

func (*stubPoller) Name() string                  { return "poller" }
func (p *stubPoller) PollEvery() time.Duration    { return 30 * time.Second }
func (p *stubPoller) Live(workflow.App) bool      { return p.live }
func (p *stubPoller) ApplyPoll(workflow.App, any) { p.applied++ }
func (p *stubPoller) Poll(context.Context, ghapi.Client) (any, error) {
	p.polls++
	return "patch", p.err
}

// pollHarness loads the dashboard with a stub poller and a pinned frame
// clock.
func pollHarness(t *testing.T, live bool) (*harness, *stubPoller, *float64) {
	t.Helper()
	h := newHarness(t)
	p := &stubPoller{live: live}
	set, err := workflow.NewSet(nil, p)
	if err != nil {
		t.Fatal(err)
	}
	h.m.wf = set
	was := fx.Now
	t.Cleanup(func() { fx.Now = was })
	now := 1000.0
	fx.Now = func() float64 { return now }
	h.load()
	return h, p, &now
}

// TestPollRunsOnlyWhileLive: nothing in flight arms no poll; something
// in flight arms one at the cadence, the tick runs it, the patch lands,
// and the next is armed; back to idle disarms it.
func TestPollRunsOnlyWhileLive(t *testing.T) {
	h, p, now := pollHarness(t, false)
	if h.m.nextPollAt != 0 {
		t.Fatalf("idle poller armed a poll at %v", h.m.nextPollAt)
	}
	p.live = true
	h.key('r') // the refresh lands and re-arms from the poller's answer
	if h.m.nextPollAt != *now+30 {
		t.Fatalf("armed at %v, want %v", h.m.nextPollAt, *now+30)
	}
	h.m.mu.Lock()
	h.m.ambient, h.m.animating, h.m.status = false, false, ""
	h.m.nextRefreshAt = *now + 3600
	h.m.loaded = false // the header countdown would wake every second
	ms := h.m.tickMS()
	h.m.loaded = true
	h.m.mu.Unlock()
	if ms < 29900 || ms > 30100 {
		t.Errorf("the idle tick wakes for the poll: %d", ms)
	}
	*now += 31
	h.m.Update(tickMsg{gen: h.m.tickGen})
	h.pump()
	if p.polls != 1 || p.applied != 1 || h.m.polling {
		t.Fatalf("polls=%d applied=%d polling=%v", p.polls, p.applied, h.m.polling)
	}
	if h.m.nextPollAt != *now+30 {
		t.Errorf("re-armed at %v, want %v", h.m.nextPollAt, *now+30)
	}
	p.live = false
	*now += 31
	h.m.Update(tickMsg{gen: h.m.tickGen})
	h.pump()
	if p.polls != 2 || h.m.nextPollAt != 0 {
		t.Errorf("after the last live poll: polls=%d next=%v", p.polls, h.m.nextPollAt)
	}
}

// TestPollRateLimitPauses: a poll's rate limit raises the refresh's wall
// and says so; no poll runs while it stands.
func TestPollRateLimitPauses(t *testing.T) {
	h, p, now := pollHarness(t, true)
	until := time.Now().Add(90 * time.Second)
	p.err = &ghapi.Error{Status: 403, Message: "secondary rate limit", RetryAt: until}
	*now += 31
	h.m.Update(tickMsg{gen: h.m.tickGen})
	h.pump()
	if p.applied != 1 {
		t.Errorf("the patch before the wall still lands: %d", p.applied)
	}
	if h.m.backoff.RateLimitWait(time.Now()) <= 0 || !strings.Contains(h.m.warn, "rate limited — live progress paused until") {
		t.Fatalf("wall %v warn %q", h.m.backoff.RateLimitWait(time.Now()), h.m.warn)
	}
	if h.m.nextPollAt < *now+80 {
		t.Errorf("the next poll waits for the wall: %v (now %v)", h.m.nextPollAt, *now)
	}
	*now += 31
	h.m.Update(tickMsg{gen: h.m.tickGen})
	h.pump()
	if p.polls != 1 {
		t.Errorf("polled under the wall: %d", p.polls)
	}
}

// TestPollSkippedWhileRefreshing: a refresh in flight owns the data; the
// poll waits for it and is re-armed when it lands.
func TestPollSkippedWhileRefreshing(t *testing.T) {
	h, p, now := pollHarness(t, true)
	*now += 31
	h.m.mu.Lock()
	h.m.refreshing = true
	h.m.poll()
	h.m.refreshing = false
	h.m.mu.Unlock()
	if p.polls != 0 || h.m.polling {
		t.Errorf("polled under a refresh: %d", p.polls)
	}
	h.key('r')
	if h.m.nextPollAt != *now+30 {
		t.Errorf("re-armed by the refresh: %v", h.m.nextPollAt)
	}
}

// TestPollFailureRetriesLater: a poll that fails for any other reason
// keeps the last progress and retries at twice the cadence.
func TestPollFailureRetriesLater(t *testing.T) {
	h, p, now := pollHarness(t, true)
	p.err = &ghapi.Error{Status: 502, Message: "Bad Gateway"}
	*now += 31
	h.m.Update(tickMsg{gen: h.m.tickGen})
	h.pump()
	if h.m.nextPollAt != *now+60 || h.m.backoff.RateLimitWait(time.Now()) > 0 || h.m.errText != "" {
		t.Errorf("next=%v (now %v) wall=%v err=%q", h.m.nextPollAt, *now, h.m.backoff.RateLimitWait(time.Now()), h.m.errText)
	}
}

// liveRowStub claims a row kind whose rows are live.
type liveRowStub struct {
	workflow.Base
	live bool
}

func (*liveRowStub) Name() string { return "liverow" }
func (s *liveRowStub) RowKinds() map[string]workflow.RowKind {
	return map[string]workflow.RowKind{"hot": {
		Segs: func(workflow.App, workflow.Row, workflow.Layout, int, int) []text.Seg {
			return []text.Seg{text.S("hot", "text")}
		},
		Live: func(workflow.App, workflow.Row) bool { return s.live },
	}}
}
func (s *liveRowStub) Sections(workflow.App, []string, string) []workflow.Row {
	return []workflow.Row{{Kind: "hot", Item: &model.PR{Repo: infra, Number: 900, Title: "hot"}}}
}

// TestRowKindLiveAnimates: a workflow row whose kind says it is live
// keeps the frame clock running; a quiet one lets the dashboard idle.
func TestRowKindLiveAnimates(t *testing.T) {
	h := newHarness(t)
	s := &liveRowStub{live: true}
	set, err := workflow.NewSet(nil, s)
	if err != nil {
		t.Fatal(err)
	}
	h.m.wf = set
	h.load()
	h.screen()
	if !h.m.animating {
		t.Error("a live row animates")
	}
	s.live = false
	h.screen()
	if h.m.animating {
		t.Error("a quiet row does not")
	}
}
