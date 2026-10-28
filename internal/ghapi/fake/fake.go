// Package fake is a ghapi.Client that serves canned data from maps and
// records every mutating call, for the UI's tests.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Call is one recorded mutation.
type Call struct {
	Op     string // approve, comment, merge, approve_deployments
	Repo   model.Repo
	Number int
	Body   string  // review/comment body, or the deployment comment
	SHA    string  // merge: the expected head
	Title  string  // merge: the commit title
	Method string  // merge: merge/squash/rebase
	RunID  int64   // approve_deployments
	EnvIDs []int64 // approve_deployments
}

// Client is the fake. Fill the maps, then hand it to the UI; every
// mutating call lands in Calls (and the typed Approvals/Comments/Merges/
// DeployApprovals), and Fail[op] makes that op return the error.
// All methods are safe for concurrent use.
type Client struct {
	mu sync.Mutex

	// User is Whoami's answer.
	User string
	// Rate is LastRate's answer.
	Rate *model.RateLimit

	// Open is ListOpen's answer per repo; a repo missing from the map
	// gets a "repository not visible" Err.
	Open map[model.Repo]ghapi.ListResult
	// PRs are PRDetail's answers; a missing one is "not a pull request".
	PRs map[model.Ident]*model.PR
	// Issues are IssueDetail's answers.
	Issues map[model.Ident]*model.Issue
	// Users are AssignableUsers' answers.
	Users map[model.Repo][]model.Actor
	// Comments are PRComments' answers.
	Comments map[model.Ident][]model.Comment
	// Labelled are IssuesWithLabel's answers, keyed by repo then label.
	Labelled map[model.Repo]map[string][]*model.Issue
	// Runs are WorkflowRuns' answers per head SHA; WaitingRuns filters
	// them to Status == "waiting".
	Runs map[string][]model.WorkflowRun
	// Pending are PendingDeployments' answers per run id.
	Pending map[int64][]model.PendingDeployment
	// Files are PRFiles' answers.
	Files map[model.Ident][]model.PRFile
	// Diffs are PRDiff's answers.
	Diffs map[model.Ident]string
	// Compares are Compare's answers keyed by "base...head".
	Compares map[string]ghapi.CompareResult
	// Tags are TagRefs' answers per repo (filtered by prefix).
	Tags map[model.Repo][]string
	// Orgs are OrgRepos' answers.
	Orgs map[string][]model.RepoListing
	// RepoRulesets are Rulesets' answers.
	RepoRulesets map[model.Repo][]model.Ruleset
	// FortressData is Fortress's answer.
	FortressData map[model.Repo]ghapi.FortressData
	// Releases is ReleasePRs' answer.
	Releases map[model.Repo]ghapi.ReleaseRepo
	// FileData is FileContents' answer, keyed "owner/name:path".
	FileData map[string][]byte

	// Fail makes the named op return the error: whoami, list, pr_detail,
	// issue_detail, users, comments, labelled, approve, comment, merge,
	// waiting_runs, pending, approve_deployments, workflow_runs, files,
	// diff, compare, tags, org_repos, rulesets, fortress, releases.
	Fail map[string]error

	// Merge is MergePR's answer when it succeeds (zero: sha "m3rge"+sha[:3],
	// merged true).
	Merge ghapi.MergeResult

	// Calls records every mutating call in order.
	Calls []Call
	// Approvals are the PR numbers approved, Comments the (number, body)
	// pairs posted, Merges the merge calls, DeployApprovals the
	// deployment approvals — the typed views of Calls.
	Approvals       []int
	CommentsPosted  []Call
	Merges          []Call
	DeployApprovals []Call
	// Reads counts every read op by name, for tests that assert caching.
	Reads map[string]int
}

var _ ghapi.Client = (*Client)(nil)

// New is an empty fake whose Whoami answers user.
func New(user string) *Client {
	return &Client{User: user, Fail: map[string]error{}, Reads: map[string]int{}}
}

func (f *Client) fail(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Reads == nil {
		f.Reads = map[string]int{}
	}
	f.Reads[op]++
	return f.Fail[op]
}

func (f *Client) record(c Call) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, c)
	switch c.Op {
	case "approve":
		f.Approvals = append(f.Approvals, c.Number)
	case "comment":
		f.CommentsPosted = append(f.CommentsPosted, c)
	case "merge":
		f.Merges = append(f.Merges, c)
	case "approve_deployments":
		f.DeployApprovals = append(f.DeployApprovals, c)
	}
}

// CallsFor is every recorded call of one op.
func (f *Client) CallsFor(op string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.Calls {
		if c.Op == op {
			out = append(out, c)
		}
	}
	return out
}

// Whoami is User.
func (f *Client) Whoami(ctx context.Context) (string, error) {
	if err := f.fail("whoami"); err != nil {
		return "", err
	}
	return f.User, nil
}

// LastRate is Rate.
func (f *Client) LastRate() *model.RateLimit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Rate
}

// ListOpen serves Open; every PR and issue is tagged with its repo, so a
// fixture may leave Repo unset. A repo missing from Open gets Err.
func (f *Client) ListOpen(ctx context.Context, repos []model.Repo) (map[model.Repo]ghapi.ListResult, error) {
	if err := f.fail("list"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[model.Repo]ghapi.ListResult, len(repos))
	for _, r := range repos {
		lr, ok := f.Open[r]
		if !ok {
			out[r] = ghapi.ListResult{Err: &ghapi.Error{Message: "repository not visible to this token: not found"}}
			continue
		}
		for _, p := range lr.PRs {
			if p.Repo.IsZero() {
				p.Repo = r
			}
		}
		for _, i := range lr.Issues {
			if i.Repo.IsZero() {
				i.Repo = r
			}
		}
		out[r] = lr
	}
	return out, nil
}

// PRDetail serves PRs.
func (f *Client) PRDetail(ctx context.Context, repo model.Repo, number int) (*model.PR, error) {
	if err := f.fail("pr_detail"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.PRs[model.Ident{Repo: repo.Name, Number: number}]
	if !ok {
		return nil, &ghapi.Error{Message: fmt.Sprintf("#%d is not a pull request in %s", number, repo)}
	}
	if p.Repo.IsZero() {
		p.Repo = repo
	}
	return p, nil
}

// IssueDetail serves Issues.
func (f *Client) IssueDetail(ctx context.Context, repo model.Repo, number int) (*model.Issue, error) {
	if err := f.fail("issue_detail"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.Issues[model.Ident{Repo: repo.Name, Number: number}]
	if !ok {
		return nil, &ghapi.Error{Message: fmt.Sprintf("#%d is not an issue in %s", number, repo)}
	}
	if i.Repo.IsZero() {
		i.Repo = repo
	}
	return i, nil
}

// AssignableUsers serves Users.
func (f *Client) AssignableUsers(ctx context.Context, repo model.Repo) ([]model.Actor, error) {
	if err := f.fail("users"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Actor(nil), f.Users[repo]...), nil
}

// PRComments serves Comments.
func (f *Client) PRComments(ctx context.Context, repo model.Repo, number int) ([]model.Comment, error) {
	if err := f.fail("comments"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Comment(nil), f.Comments[model.Ident{Repo: repo.Name, Number: number}]...), nil
}

// IssuesWithLabel serves Labelled, at most first of them.
func (f *Client) IssuesWithLabel(ctx context.Context, repo model.Repo, label string, first int) ([]*model.Issue, error) {
	if err := f.fail("labelled"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]*model.Issue(nil), f.Labelled[repo][label]...)
	if first > 0 && len(out) > first {
		out = out[:first]
	}
	for _, i := range out {
		if i.Repo.IsZero() {
			i.Repo = repo
		}
	}
	return out, nil
}

// Approve records the approval.
func (f *Client) Approve(ctx context.Context, repo model.Repo, number int, body string) error {
	if err := f.fail("approve"); err != nil {
		return err
	}
	f.record(Call{Op: "approve", Repo: repo, Number: number, Body: body})
	return nil
}

// Comment records the comment.
func (f *Client) Comment(ctx context.Context, repo model.Repo, number int, body string) error {
	if err := f.fail("comment"); err != nil {
		return err
	}
	f.record(Call{Op: "comment", Repo: repo, Number: number, Body: body})
	return nil
}

// MergePR records the merge and answers Merge.
func (f *Client) MergePR(ctx context.Context, repo model.Repo, number int, sha, title, method string) (ghapi.MergeResult, error) {
	if err := f.fail("merge"); err != nil {
		return ghapi.MergeResult{}, err
	}
	if method == "" {
		method = "merge"
	}
	f.record(Call{Op: "merge", Repo: repo, Number: number, SHA: sha, Title: title, Method: method})
	f.mu.Lock()
	defer f.mu.Unlock()
	res := f.Merge
	if res == (ghapi.MergeResult{}) {
		short := sha
		if len(short) > 3 {
			short = short[:3]
		}
		res = ghapi.MergeResult{SHA: "m3rge" + short, Merged: true}
	}
	return res, nil
}

// WaitingRuns is Runs[headSHA] filtered to Status "waiting".
func (f *Client) WaitingRuns(ctx context.Context, repo model.Repo, headSHA string) ([]model.WorkflowRun, error) {
	if err := f.fail("waiting_runs"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.WorkflowRun
	for _, r := range f.Runs[headSHA] {
		if r.Status == "waiting" {
			out = append(out, r)
		}
	}
	return out, nil
}

// WorkflowRuns is Runs[headSHA].
func (f *Client) WorkflowRuns(ctx context.Context, repo model.Repo, headSHA string) ([]model.WorkflowRun, error) {
	if err := f.fail("workflow_runs"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.WorkflowRun(nil), f.Runs[headSHA]...), nil
}

// PendingDeployments is Pending[runID].
func (f *Client) PendingDeployments(ctx context.Context, repo model.Repo, runID int64) ([]model.PendingDeployment, error) {
	if err := f.fail("pending"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.PendingDeployment(nil), f.Pending[runID]...), nil
}

// ApproveDeployments records the approval.
func (f *Client) ApproveDeployments(ctx context.Context, repo model.Repo, runID int64, envIDs []int64, comment string) error {
	if err := f.fail("approve_deployments"); err != nil {
		return err
	}
	f.record(Call{Op: "approve_deployments", Repo: repo, RunID: runID,
		EnvIDs: append([]int64(nil), envIDs...), Body: comment})
	return nil
}

// PRFiles serves Files.
func (f *Client) PRFiles(ctx context.Context, repo model.Repo, number int) ([]model.PRFile, error) {
	if err := f.fail("files"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.PRFile(nil), f.Files[model.Ident{Repo: repo.Name, Number: number}]...), nil
}

// PRDiff serves Diffs.
func (f *Client) PRDiff(ctx context.Context, repo model.Repo, number int) (string, error) {
	if err := f.fail("diff"); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Diffs[model.Ident{Repo: repo.Name, Number: number}], nil
}

// Compare serves Compares["base...head"].
func (f *Client) Compare(ctx context.Context, repo model.Repo, base, head string) (ghapi.CompareResult, error) {
	if err := f.fail("compare"); err != nil {
		return ghapi.CompareResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Compares[base+"..."+head], nil
}

// TagRefs serves Tags[repo] under prefix.
func (f *Client) TagRefs(ctx context.Context, repo model.Repo, prefix string) ([]string, error) {
	if err := f.fail("tags"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, t := range f.Tags[repo] {
		if len(t) >= len(prefix) && t[:len(prefix)] == prefix {
			out = append(out, t)
		}
	}
	return out, nil
}

// OrgRepos serves Orgs.
func (f *Client) OrgRepos(ctx context.Context, org string) ([]model.RepoListing, error) {
	if err := f.fail("org_repos"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.RepoListing(nil), f.Orgs[org]...), nil
}

// Rulesets serves RepoRulesets.
func (f *Client) Rulesets(ctx context.Context, repo model.Repo) ([]model.Ruleset, error) {
	if err := f.fail("rulesets"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Ruleset(nil), f.RepoRulesets[repo]...), nil
}

// Fortress serves FortressData for the repos asked.
func (f *Client) Fortress(ctx context.Context, repos []model.Repo) (map[model.Repo]ghapi.FortressData, error) {
	if err := f.fail("fortress"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[model.Repo]ghapi.FortressData{}
	for _, r := range repos {
		if d, ok := f.FortressData[r]; ok {
			out[r] = d
		}
	}
	return out, nil
}

// ReleasePRs serves Releases for the repos asked; a repo missing from
// Releases gets an Err, like a repo the token can't see.
func (f *Client) ReleasePRs(ctx context.Context, repos []model.Repo, prodBranch, stageBranch string) (map[model.Repo]ghapi.ReleaseRepo, error) {
	if err := f.fail("releases"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[model.Repo]ghapi.ReleaseRepo{}
	for _, r := range repos {
		rr, ok := f.Releases[r]
		if !ok {
			rr = ghapi.ReleaseRepo{Err: &ghapi.Error{Message: "repository not visible to this token: not found"}}
		}
		for _, p := range rr.PRs {
			if p.Repo.IsZero() {
				p.Repo = r
			}
		}
		out[r] = rr
	}
	return out, nil
}

// FileContents serves FileData; the sha is derived from the bytes.
func (f *Client) FileContents(ctx context.Context, repo model.Repo, path, ref string) ([]byte, string, error) {
	if err := f.fail("contents"); err != nil {
		return nil, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Reads != nil {
		f.Reads["contents"]++
	}
	data, ok := f.FileData[repo.String()+":"+path]
	if !ok {
		return nil, "", &ghapi.Error{Method: "GET", URL: repo.String() + "/contents/" + path, Status: 404, Message: "Not Found"}
	}
	sum := sha256.Sum256(data)
	return append([]byte(nil), data...), hex.EncodeToString(sum[:])[:40], nil
}
