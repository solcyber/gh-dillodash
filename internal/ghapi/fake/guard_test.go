package fake_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/model"
)

var (
	okRepo = model.Repo{Owner: "acme", Name: "ops"}
	noRepo = model.Repo{Owner: "acme", Name: "infra"}
)

// writes calls every write of the Client on repo; the names are the
// interface's method names.
func writes(c ghapi.Client, repo model.Repo) map[string]error {
	ctx := context.Background()
	out := map[string]error{}
	out["Approve"] = c.Approve(ctx, repo, 1, "")
	out["Comment"] = c.Comment(ctx, repo, 1, "x")
	_, out["PostComment"] = c.PostComment(ctx, repo, 1, "x")
	out["Dispatch"] = c.Dispatch(ctx, repo, "e", nil)
	_, out["MergePR"] = c.MergePR(ctx, repo, 1, "abc", "t", "")
	out["RequestChanges"] = c.RequestChanges(ctx, repo, 1, "x")
	out["DismissReview"] = c.DismissReview(ctx, repo, 1, 9, "x")
	out["DispatchWorkflow"] = c.DispatchWorkflow(ctx, repo, "w.yml", "main", nil)
	out["ApproveDeployments"] = c.ApproveDeployments(ctx, repo, 5, []int64{1}, "x")
	out["RejectDeployments"] = c.RejectDeployments(ctx, repo, 5, []int64{1}, "x")
	out["RerunRun"] = c.RerunRun(ctx, repo, 5)
	out["RerunFailedJobs"] = c.RerunFailedJobs(ctx, repo, 5)
	out["CancelRun"] = c.CancelRun(ctx, repo, 5)
	out["AddLabels"] = c.AddLabels(ctx, repo, 1, []string{"bug"})
	out["RemoveLabel"] = c.RemoveLabel(ctx, repo, 1, "bug")
	out["AddAssignees"] = c.AddAssignees(ctx, repo, 1, []string{"bob"})
	out["RemoveAssignees"] = c.RemoveAssignees(ctx, repo, 1, []string{"bob"})
	out["SetIssueState"] = c.SetIssueState(ctx, repo, 1, "closed", "completed")
	out["CreateBranch"] = c.CreateBranch(ctx, repo, "promote/1-stage", "abc")
	_, out["CommitOnBranch"] = c.CommitOnBranch(ctx, repo, "promote/1-stage", "abc", "h", "", nil, nil)
	out["DeleteRef"] = c.DeleteRef(ctx, repo, "heads/promote/1-stage")
	_, out["CreatePR"] = c.CreatePR(ctx, repo, "stage", "promote/1-stage", "t", "")
	out["CreateTag"] = c.CreateTag(ctx, repo, "go/1", "m", "abc")
	return out
}

// TestWithAllowedReposRefusesEveryWrite: every write to a repo outside
// allowed_repos is refused with a RepoRefusedError and never reaches the
// client; the same writes to an allowed repo go through.
func TestWithAllowedReposRefusesEveryWrite(t *testing.T) {
	f := fake.New("alice")
	g := ghapi.WithAllowedRepos(f, []model.Repo{{Owner: "ACME", Name: "Ops"}})
	for op, err := range writes(g, noRepo) {
		var re *ghapi.RepoRefusedError
		if !errors.As(err, &re) || re.Repo != noRepo {
			t.Errorf("%s on %s: err = %v, want a RepoRefusedError", op, noRepo, err)
		}
	}
	if len(f.Calls) != 0 {
		t.Fatalf("refused writes reached the client: %+v", f.Calls)
	}
	for op, err := range writes(g, okRepo) {
		if err != nil {
			t.Errorf("%s on the allowed repo (matched case-insensitively): %v", op, err)
		}
	}
	if len(f.Calls) == 0 {
		t.Error("allowed writes were not sent")
	}
	if got := ghapi.WithAllowedRepos(f, nil); got != ghapi.Client(f) {
		t.Error("no allowed_repos: the client is returned as is")
	}
}

// TestWithAllowedReposCoversEveryWrite: every Client method is either a
// read (listed here) or a write the guard is tested on above. A new
// write added to the interface fails this test until it is guarded.
func TestWithAllowedReposCoversEveryWrite(t *testing.T) {
	reads := map[string]bool{
		"Whoami": true, "TeamMember": true, "ListOpen": true, "ListRequested": true, "LastRate": true, "PRDetail": true,
		"IssueDetail": true, "AssignableUsers": true, "PRComments": true, "IssuesWithLabel": true,
		"WaitingRuns": true, "RepoWaitingRuns": true, "PendingDeployments": true, "JobLogs": true,
		"WorkflowRuns": true, "WorkflowFileRuns": true, "RunJobs": true, "PRFiles": true, "PRDiff": true,
		"Compare": true, "TagRefs": true, "OrgRepos": true, "Rulesets": true, "FileContents": true,
		"Fortress": true, "ReleasePRs": true, "RepoLabels": true,
		"PromotePRs": true, "ListOpenInto": true, "IsAncestor": true, "CommitChange": true, "BlobAt": true, "BlobBase64": true, "BranchHead": true,
	}
	tested := writes(fake.New("alice"), okRepo)
	typ := reflect.TypeOf((*ghapi.Client)(nil)).Elem()
	var missing []string
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if _, ok := tested[name]; !reads[name] && !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("Client methods neither listed as reads nor guarded and tested as writes: %v", missing)
	}
}
