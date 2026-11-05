package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/waitingruns"
)

// TestDOnAWaitingRun: a dispatched run waiting on an environment and on
// no listed PR gets a row; D on it goes to the workflow (RowDeployer)
// before the merged-PR path, and approves through the viewer's token.
func TestDOnAWaitingRun(t *testing.T) {
	h := newHarness(t)
	wr := config.DefaultWaitingRuns()
	wr.Repos = []model.Repo{infra}
	h.m.cfg.WaitingRuns = wr
	set, err := workflow.NewSet(&workflow.Context{Config: h.m.cfg, Repos: h.m.repos}, waitingruns.New(wr))
	if err != nil {
		t.Fatal(err)
	}
	h.m.wf = set
	run := model.WorkflowRun{ID: 4242, Name: "deploy", Status: "waiting", HeadSHA: strings.Repeat("d", 40), HeadBranch: "main",
		Event: "workflow_dispatch", DisplayTitle: "deploy web-portal to prod", Actor: model.Actor{Login: "bob"},
		HTMLURL: "https://github.com/acme/infra/actions/runs/4242", CreatedAt: t0.Add(-time.Hour)}
	var prod model.PendingDeployment
	prod.Environment.ID, prod.Environment.Name, prod.CurrentUserCanApprove = 55, "prod", true
	h.gh.RepoRuns = map[model.Repo][]model.WorkflowRun{infra: {run}}
	h.gh.Pending = map[int64][]model.PendingDeployment{4242: {prod}}
	h.load()
	s := h.screen()
	for _, want := range []string{"waiting approvals  1", "  run  [approve: prod] deploy main bob ", "deploy web-portal to prod", " D  deploys ", "1 run waiting, 1 on you (D)"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	for i, r := range h.m.rows {
		if r.Kind == waitingruns.RowRun {
			h.m.selected = i
		}
	}
	var asked []string
	h.m.tp.Confirm = func(q string) bool { asked = append(asked, q); return true }
	h.key('D')
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "approve 1 deployment of infra deploy run 4242 as alice?") {
		t.Errorf("asked %q", asked)
	}
	if got := h.gh.DeployApprovals; len(got) != 1 || got[0].RunID != 4242 || got[0].EnvIDs[0] != 55 ||
		got[0].Body != "dillodash: approved deploy run 4242 in acme/infra" {
		t.Errorf("approvals: %+v", got)
	}
	h.key('o')
	if len(h.opened) != 1 || h.opened[0] != run.HTMLURL {
		t.Errorf("o opened %v", h.opened)
	}
}

// TestDInDetailAfterAnExternalMerge: a PR merged by someone else (a
// release job) while its detail is open. After the refresh the detail
// carries the merge commit, so D in the detail finds the deploy waiting
// on it instead of saying "not merged" while M says "already merged".
// A merged answer without the merge commit re-reads rather than sending
// the approver to M.
func TestDInDetailAfterAnExternalMerge(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	if h.m.view != ViewDetail {
		t.Fatalf("view = %s", h.m.view)
	}
	id := model.Ident{Repo: infra, Number: 101}
	sha := strings.Repeat("e", 40)

	// merged, but the answer has no merge commit: re-read, never "not merged"
	h.gh.Mutate(func() {
		h.gh.PRs[id].State = "MERGED"
		h.gh.PRs[id].MergeCommit = nil
	})
	h.key('r')
	h.key('D')
	if s := h.m.status; strings.Contains(s, "not merged") || !strings.Contains(s, "re-reading its merge commit") {
		t.Errorf("merged without a sha: status %q", s)
	}

	// the detail answer carries the merge commit: D reads the deploys on it
	run := model.WorkflowRun{ID: 77, Name: "migrate", Status: "waiting", HeadSHA: sha, HeadBranch: "main", Event: "push",
		HTMLURL: "https://github.com/acme/infra/actions/runs/77", CreatedAt: t0}
	var prod model.PendingDeployment
	prod.Environment.ID, prod.Environment.Name, prod.CurrentUserCanApprove = 9, "prod", true
	h.gh.Mutate(func() {
		h.gh.PRs[id].MergeCommit = &model.MergeCommit{Oid: sha}
		h.gh.Runs = map[string][]model.WorkflowRun{sha: {run}}
		h.gh.Pending = map[int64][]model.PendingDeployment{77: {prod}}
	})
	h.key('r')
	h.m.tp.Confirm = func(q string) bool { return true }
	h.key('D')
	if got := h.gh.DeployApprovals; len(got) != 1 || got[0].RunID != 77 || got[0].EnvIDs[0] != 9 {
		t.Errorf("D in the detail did not approve the waiting prod deploy: %+v (status %q)", got, h.m.status)
	}
}

// TestDInDetailOpenAcrossTheMerge: the detail was opened while the PR
// was open and stays open while it merges (a release NOW). D, with no
// refresh in between, re-reads the PR and approves the deploy waiting on
// the merge commit; it never does nothing.
func TestDInDetailOpenAcrossTheMerge(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	if h.m.view != ViewDetail {
		t.Fatalf("view = %s", h.m.view)
	}
	id := model.Ident{Repo: infra, Number: 101}
	sha := strings.Repeat("f", 40)
	run := model.WorkflowRun{ID: 78, Name: "migrate", Status: "waiting", HeadSHA: sha, HeadBranch: "main", Event: "push",
		HTMLURL: "https://github.com/acme/infra/actions/runs/78", CreatedAt: t0}
	var prod model.PendingDeployment
	prod.Environment.ID, prod.Environment.Name, prod.CurrentUserCanApprove = 9, "prod", true
	h.gh.Mutate(func() {
		h.gh.PRs[id].State = "MERGED"
		h.gh.PRs[id].MergeCommit = &model.MergeCommit{Oid: sha}
		h.gh.Runs = map[string][]model.WorkflowRun{sha: {run}}
		h.gh.Pending = map[int64][]model.PendingDeployment{78: {prod}}
	})
	h.m.tp.Confirm = func(q string) bool { return true }
	h.key('D') // no r: the detail still holds the open PR
	if got := h.gh.DeployApprovals; len(got) != 1 || got[0].RunID != 78 {
		t.Errorf("D did not re-read the merged PR: %+v (status %q, err %q)", got, h.m.status, h.m.errText)
	}
	if pr, ok := h.m.detail.(*model.PR); !ok || pr.State != "MERGED" {
		t.Errorf("the detail keeps the fresh read: %+v", h.m.detail)
	}

	// still open: it says so, and the read failing says that
	h.gh.Mutate(func() { h.gh.PRs[id].State, h.gh.PRs[id].MergeCommit = "OPEN", nil })
	h.key('r')
	h.key('D')
	if !strings.Contains(h.m.status, "not merged yet") {
		t.Errorf("open: status %q", h.m.status)
	}
	h.gh.Fail["pr_detail"] = errors.New("HTTP 502")
	h.key('D')
	if !strings.Contains(h.m.errText, "couldn't re-read") {
		t.Errorf("a failed re-read says so: status %q err %q", h.m.status, h.m.errText)
	}
}

// TestDApprovePathIsUnchanged: D on a merged PR with one deploy that
// waits on you asks one y/N question and approves on y. It never shows
// a menu first (reject lives under x, with its own confirm).
func TestDApprovePathIsUnchanged(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	id := model.Ident{Repo: infra, Number: 101}
	sha := strings.Repeat("f", 40)
	run := model.WorkflowRun{ID: 88, Name: "deploy", Status: "waiting", HeadSHA: sha, HeadBranch: "main", Event: "push", CreatedAt: t0}
	var prod model.PendingDeployment
	prod.Environment.ID, prod.Environment.Name, prod.CurrentUserCanApprove = 3, "prod", true
	h.gh.Mutate(func() {
		h.gh.PRs[id].State = "MERGED"
		h.gh.PRs[id].MergeCommit = &model.MergeCommit{Oid: sha}
		h.gh.Runs = map[string][]model.WorkflowRun{sha: {run}}
		h.gh.Pending = map[int64][]model.PendingDeployment{88: {prod}}
	})
	h.key('r')
	var asked []string
	h.m.tp.Pick = func(title string, _ []string) int { t.Errorf("D showed a menu: %q", title); return -1 }
	h.m.tp.Confirm = func(q string) bool { asked = append(asked, q); return true }
	h.key('D')
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "approve 1 deployment of #101 on merge ") {
		t.Errorf("one approve question: %q", asked)
	}
	if got := h.gh.DeployApprovals; len(got) != 1 || got[0].RunID != 88 {
		t.Errorf("approved: %+v", got)
	}
	if len(h.gh.CallsFor("reject_deployments")) != 0 {
		t.Error("D never rejects")
	}
}
