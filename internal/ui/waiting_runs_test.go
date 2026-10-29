package ui

import (
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
