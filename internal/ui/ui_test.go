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
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/theme"
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

// drain applies only what is already queued, without waiting for the
// goroutines; for tests that drive a modal by hand.
func (h *harness) drain() { drain(h.m, h.msgs) }

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
	if !h.m.localApproved(h.gh.PRs[model.Ident{Repo: "infra", Number: 101}]) {
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
	h.gh.PRs[model.Ident{Repo: "infra", Number: 102}] = h.gh.Open[infra].PRs[1]
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
