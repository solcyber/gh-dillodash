package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

var (
	infra  = model.Repo{Owner: "acme", Name: "infra"}
	portal = model.Repo{Owner: "acme", Name: "web-portal"}
)

func TestFakeServesAndRecords(t *testing.T) {
	f := New("alice")
	f.Open = map[model.Repo]ghapi.ListResult{
		infra: {
			PRs:    []*model.PR{{Number: 3543, Title: "canary"}, {Number: 3550, Title: "widen the role"}},
			Issues: []*model.Issue{{Number: 3530, Title: "an issue"}},
			Notes:  []string{"showing the newest 2 of 3 open PRs"},
		},
	}
	f.PRs = map[model.Ident]*model.PR{{Repo: infra, Number: 3543}: {Number: 3543, Title: "canary", Body: "detail"}}
	f.Runs = map[string][]model.WorkflowRun{"h1": {
		{ID: 7, Name: "apply", Status: "waiting"},
		{ID: 8, Name: "plan", Status: "completed", Conclusion: "success"},
	}}
	f.Pending = map[int64][]model.PendingDeployment{7: {{}}}
	f.Releases = map[model.Repo]ghapi.ReleaseRepo{infra: {PRs: []*model.PR{{Number: 1042}}, ProdOid: "m"}}
	ctx := context.Background()

	if u, err := f.Whoami(ctx); err != nil || u != "alice" {
		t.Errorf("whoami: %q %v", u, err)
	}
	out, err := f.ListOpen(ctx, []model.Repo{infra, portal})
	if err != nil {
		t.Fatal(err)
	}
	if r := out[infra]; len(r.PRs) != 2 || r.PRs[0].Repo != infra || r.Issues[0].Repo != infra || len(r.Notes) != 1 {
		t.Errorf("infra: %+v", r)
	}
	if r := out[portal]; r.Err == nil {
		t.Error("a repo missing from Open is not visible")
	}
	pr, err := f.PRDetail(ctx, infra, 3543)
	if err != nil || pr.Body != "detail" || pr.Repo != infra {
		t.Errorf("detail: %+v %v", pr, err)
	}
	if _, err := f.PRDetail(ctx, infra, 1); err == nil {
		t.Error("missing detail errors")
	}
	if runs, _ := f.WaitingRuns(ctx, infra, "h1"); len(runs) != 1 || runs[0].ID != 7 {
		t.Errorf("waiting runs: %+v", runs)
	}
	if runs, _ := f.WorkflowRuns(ctx, infra, "h1"); len(runs) != 2 {
		t.Errorf("workflow runs: %+v", runs)
	}
	rel, _ := f.ReleasePRs(ctx, []model.Repo{infra, portal}, "main", "stage")
	if rel[infra].PRs[0].Repo != infra || rel[portal].Err == nil {
		t.Errorf("releases: %+v", rel)
	}

	// mutations are recorded in order, with their arguments
	if err := f.Approve(ctx, infra, 3543, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.Comment(ctx, infra, 3543, "/approve"); err != nil {
		t.Fatal(err)
	}
	res, err := f.MergePR(ctx, infra, 1042, "abcdef", "Release", "")
	if err != nil || !res.Merged || res.SHA != "m3rgeabc" {
		t.Errorf("merge: %+v %v", res, err)
	}
	if err := f.ApproveDeployments(ctx, infra, 7, []int64{55}, "go"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 4 || f.Calls[0].Op != "approve" || f.Calls[1].Body != "/approve" ||
		f.Calls[2].Method != "merge" || f.Calls[2].SHA != "abcdef" || f.Calls[3].EnvIDs[0] != 55 || f.Calls[3].RunID != 7 {
		t.Errorf("calls: %+v", f.Calls)
	}
	if len(f.Approvals) != 1 || f.Approvals[0] != 3543 || len(f.CommentsPosted) != 1 || len(f.Merges) != 1 || len(f.DeployApprovals) != 1 {
		t.Errorf("typed views: %v %v %v %v", f.Approvals, f.CommentsPosted, f.Merges, f.DeployApprovals)
	}
	if got := f.CallsFor("comment"); len(got) != 1 || got[0].Number != 3543 {
		t.Errorf("CallsFor: %+v", got)
	}
	if f.Reads["list"] != 1 || f.Reads["pr_detail"] != 2 {
		t.Errorf("reads: %v", f.Reads)
	}
}

func TestFakeFailures(t *testing.T) {
	f := New("alice")
	boom := &ghapi.Error{Method: "POST", Status: 500, Message: "boom"}
	f.Fail["comment"] = boom
	f.Fail["merge"] = &ghapi.Error{Method: "PUT", Status: 405, Message: "Pull Request is not mergeable"}
	f.Fail["list"] = &ghapi.Error{Status: 403, Message: "rate limited", RetryAt: time.Now().Add(time.Minute)}
	ctx := context.Background()
	if err := f.Comment(ctx, infra, 1, "x"); !errors.Is(err, boom) {
		t.Errorf("comment fails: %v", err)
	}
	if _, err := f.MergePR(ctx, infra, 1, "s", "", ""); err == nil {
		t.Error("merge fails")
	}
	if len(f.Calls) != 0 {
		t.Errorf("a failed mutation is not recorded: %+v", f.Calls)
	}
	if _, err := f.ListOpen(ctx, []model.Repo{infra}); !ghapi.IsRateLimit(err) {
		t.Errorf("list rate limit: %v", err)
	}
	if err := f.Approve(ctx, infra, 1, ""); err != nil || len(f.Approvals) != 1 {
		t.Error("other ops still work")
	}
	var _ ghapi.Client = f
}

// TestFakeFileRunsAndJobs: FileRuns answers by repo and file, narrowed
// to the event and the since bound; Jobs answers by run id; both have a
// Fail key and count as reads.
func TestFakeFileRunsAndJobs(t *testing.T) {
	f := New("alice")
	t0 := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	f.FileRuns = map[string][]model.WorkflowRun{"acme/infra:apply.yml": {
		{ID: 3, Event: "repository_dispatch", CreatedAt: t0.Add(time.Minute), DisplayTitle: "apply · plan rel:web-portal#42"},
		{ID: 2, Event: "push", CreatedAt: t0.Add(time.Minute)},
		{ID: 1, Event: "repository_dispatch", CreatedAt: t0.Add(-time.Hour)},
	}}
	f.Jobs = map[int64][]model.WorkflowJob{3: {{ID: 30, Name: "app release", Steps: []model.WorkflowStep{{Name: "Merge the release PR"}}}}}
	ctx := context.Background()
	runs, err := f.WorkflowFileRuns(ctx, infra, "apply.yml", "repository_dispatch", t0)
	if err != nil || len(runs) != 1 || runs[0].ID != 3 {
		t.Fatalf("filtered: %+v %v", runs, err)
	}
	if runs, _ := f.WorkflowFileRuns(ctx, infra, "apply.yml", "", time.Time{}); len(runs) != 3 {
		t.Errorf("unfiltered: %d", len(runs))
	}
	if runs, _ := f.WorkflowFileRuns(ctx, portal, "apply.yml", "", time.Time{}); len(runs) != 0 {
		t.Errorf("another repo: %d", len(runs))
	}
	if jobs, err := f.RunJobs(ctx, infra, 3); err != nil || len(jobs) != 1 || jobs[0].Steps[0].Name != "Merge the release PR" {
		t.Errorf("jobs: %+v %v", jobs, err)
	}
	if f.Reads["file_runs"] != 3 || f.Reads["jobs"] != 1 {
		t.Errorf("reads: %v", f.Reads)
	}
	f.Fail["jobs"] = &ghapi.Error{Status: 404, Message: "Not Found"}
	f.Fail["file_runs"] = &ghapi.Error{Status: 403, Message: "forbidden"}
	if _, err := f.RunJobs(ctx, infra, 3); err == nil {
		t.Error("jobs fail")
	}
	if _, err := f.WorkflowFileRuns(ctx, infra, "apply.yml", "", time.Time{}); err == nil {
		t.Error("file runs fail")
	}
}

// TestFakeRepoWaitingRuns: RepoRuns answers by repo, narrowed to the
// waiting ones; RepoFail fails one repo, Fail every repo.
func TestFakeRepoWaitingRuns(t *testing.T) {
	f := New("alice")
	f.RepoRuns = map[model.Repo][]model.WorkflowRun{infra: {
		{ID: 1, Status: "waiting"}, {ID: 2, Status: "in_progress"},
	}}
	boom := &ghapi.Error{Status: 403, Message: "Resource not accessible by personal access token"}
	f.RepoFail = map[model.Repo]error{portal: boom}
	ctx := context.Background()
	if runs, err := f.RepoWaitingRuns(ctx, infra); err != nil || len(runs) != 1 || runs[0].ID != 1 {
		t.Errorf("infra: %+v %v", runs, err)
	}
	if _, err := f.RepoWaitingRuns(ctx, portal); !errors.Is(err, boom) {
		t.Errorf("portal fails: %v", err)
	}
	f.Fail["repo_waiting_runs"] = boom
	if _, err := f.RepoWaitingRuns(ctx, infra); !errors.Is(err, boom) {
		t.Errorf("Fail fails every repo: %v", err)
	}
	if f.Reads["repo_waiting_runs"] != 3 {
		t.Errorf("reads: %v", f.Reads)
	}
}
