package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/releasepr"
)

// withReleasePR turns the harness into one with the release workflow on
// portal, serving one open release PR whose check rollup is invisible
// (what a fine-grained PAT sees) and whose Actions runs are known.
func withReleasePR(t *testing.T, h *harness) *model.PR {
	t.Helper()
	rel := config.DefaultReleasePR()
	rel.Auto, rel.Repos, rel.Org = false, []model.Repo{portal}, "acme"
	h.m.cfg.ReleasePR = rel
	ctx := &workflow.Context{Config: h.m.cfg, Repos: h.m.repos}
	set, err := workflow.NewSet(ctx, releasepr.New(ctx))
	if err != nil {
		t.Fatal(err)
	}
	h.m.wf = set
	p := pr(portal, 300, "Release 2026-10-28", "bob", false)
	p.HeadRefName, p.HeadRefOid = "release/2026-10-28", strings.Repeat("e", 40)
	h.gh.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {Name: portal.Name, PRs: []*model.PR{p}, ProdOid: "p", StageOid: "s"}}
	h.gh.Runs = map[string][]model.WorkflowRun{p.HeadRefOid: {
		{ID: 1, Name: "build", Path: ".github/workflows/build.yml", Status: "completed", Conclusion: "success", HeadSHA: p.HeadRefOid},
		{ID: 2, Name: "e2e", Path: ".github/workflows/e2e.yml", Status: "completed", Conclusion: "failure", HeadSHA: p.HeadRefOid},
	}}
	return p
}

// TestReleaseRowsGetActionsCI (#5): a release row's CI comes through the
// Actions fallback when the rollup is hidden, like a review PR's.
func TestReleaseRowsGetActionsCI(t *testing.T) {
	h := newHarness(t)
	p := withReleasePR(t, h)
	h.load()
	st, _ := h.m.wfState["release_pr"].(*releasepr.State)
	if st == nil || len(st.Releases) != 1 {
		t.Fatalf("release state: %+v warn=%q", st, h.m.warn)
	}
	c, ok := st.Releases[0].Get(workflow.CIKey).(model.CICluster)
	if !ok || c.Glyphs != "+x" || len(c.Failed) != 1 || c.Failed[0] != "e2e" {
		t.Fatalf("release CI from the Actions route: %+v ok=%v", c, ok)
	}
	if cc, ok := h.m.ciOf(p); !ok || cc.Glyphs != "+x" {
		t.Errorf("the core's cluster map knows the release too: %+v %v", cc, ok)
	}
	if s := h.screen(); !strings.Contains(s, "+x") || !strings.Contains(s, "Release 2026-10-28") {
		t.Errorf("row lacks the glyphs:\n%s", s)
	}
	// the token loses Actions on the release repo: degraded, not blank
	h.gh.Fail["workflow_runs"] = &ghapi.Error{Status: 403, Message: "Resource not accessible by personal access token"}
	h.key('r')
	if !strings.Contains(h.m.warn, "degraded: CI via Actions API failed on ") || !strings.Contains(h.m.warn, "Actions: read") {
		t.Errorf("warn %q", h.m.warn)
	}
}

// ciStub is a workflow whose state contributes PRs to the CI pass.
type ciStub struct {
	workflow.Base
	prs []*model.PR
}

type ciStubState struct{ prs []*model.PR }

func (s *ciStubState) CIPRs() []*model.PR { return s.prs }
func (ciStub) Name() string               { return "cistub" }
func (w ciStub) Refresh(context.Context, ghapi.Client, []model.Repo, []*model.PR, []*model.Issue) (workflow.State, []string, error) {
	return &ciStubState{w.prs}, nil, nil
}

// TestCIFallbackSeesWorkflowSHAsAsLive: one CI pass covers the review
// PRs and the workflow's, so neither set prunes the other's cache.
func TestCIFallbackSeesWorkflowSHAsAsLive(t *testing.T) {
	h := newHarness(t)
	var reviewPRs []*model.PR
	for i := 0; i < 20; i++ {
		p := pr(infra, 200+i, "review", "bob", false)
		p.HeadRefOid = strings.Repeat(string(rune('a'+i)), 40)
		reviewPRs = append(reviewPRs, p)
	}
	h.gh.Open[infra] = ghapi.ListResult{PRs: reviewPRs}
	var extra []*model.PR
	for i := 0; i < 2; i++ {
		p := pr(portal, 300+i, "release", "bob", false)
		p.HeadRefOid = strings.Repeat(string(rune('A'+i)), 40)
		extra = append(extra, p)
	}
	set, err := workflow.NewSet(nil, ciStub{prs: extra})
	if err != nil {
		t.Fatal(err)
	}
	h.m.wf = set
	h.gh.Runs = map[string][]model.WorkflowRun{}
	for _, p := range append(append([]*model.PR{}, reviewPRs...), extra...) {
		h.gh.Runs[p.HeadRefOid] = []model.WorkflowRun{{ID: 1, Name: "ci", Path: "ci.yml", Status: "completed", Conclusion: "success"}}
	}
	h.load()
	reads := h.gh.Reads["workflow_runs"]
	if reads != 22 {
		t.Fatalf("first pass asks once per sha: %d", reads)
	}
	h.m.mu.Lock()
	h.m.nextRefreshAt = 0
	h.m.mu.Unlock()
	h.m.Update(tickMsg{})
	h.pump()
	if got := h.gh.Reads["workflow_runs"]; got != reads {
		t.Errorf("settled, passing SHAs are cached across the pass: %d more calls", got-reads)
	}
	if _, _, ok := h.m.ci.Cached(reviewPRs[0].HeadRefOid); !ok {
		t.Error("a review SHA was pruned by the workflow's PRs")
	}
	if _, _, ok := h.m.ci.Cached(extra[0].HeadRefOid); !ok {
		t.Error("a workflow SHA was pruned by the review PRs")
	}
	_ = time.Second
}

// TestListKeyBarShowsDeploysWithReleasePR (#7): D in the list bar while
// release_pr is on, in the help under list as well as detail.
func TestListKeyBarShowsDeploysWithReleasePR(t *testing.T) {
	h := newHarness(t)
	h.load()
	for _, k := range h.m.keysFor(ViewList) {
		if k[0] == "D" {
			t.Fatal("no workflow with deploys: no D in the list bar")
		}
	}
	withReleasePR(t, h)
	keys := h.m.keysFor(ViewList)
	di, dd := -1, -1
	for i, k := range keys {
		switch k[0] {
		case "D":
			di = i
			if k[1] != "deploys" {
				t.Errorf("label %q", k[1])
			}
		case "d":
			dd = i
		}
	}
	if di < 0 || dd < 0 || di > dd {
		t.Errorf("D before d: %v", keys)
	}
	if s := h.screen(); !strings.Contains(s, " D  deploys ") {
		t.Errorf("bar lacks D:\n%s", s)
	}
	h.key('?')
	if s := h.screen(); !strings.Contains(s, "D  approve a merged row's deployments") {
		t.Errorf("help lacks the list D row:\n%s", s)
	}
}
