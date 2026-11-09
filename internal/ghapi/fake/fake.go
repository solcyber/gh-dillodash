// Package fake is a ghapi.Client that serves canned data from maps and
// records every mutating call, for the UI's tests.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Call is one recorded mutation.
type Call struct {
	Op      string // approve, request_changes, dismiss_review, comment, merge, approve_deployments, reject_deployments, dispatch, dispatch_workflow, rerun, rerun_failed, cancel_run, add_labels, remove_label, add_assignees, remove_assignees, set_state
	Repo    model.Repo
	Number  int
	Body    string         // review/comment body, or the deployment comment
	SHA     string         // merge: the expected head; approve: the commit reviewed
	Title   string         // merge: the commit title
	Method  string         // merge: merge/squash/rebase
	RunID   int64          // approve_deployments
	EnvIDs  []int64        // approve_deployments
	Event   string         // dispatch: the event type
	Payload map[string]any // dispatch: the client payload
	ID      int64          // comment: the id handed back (1, 2, ... per fake); dismiss_review: the review id
	File    string         // dispatch_workflow: the workflow file
	Ref     string         // dispatch_workflow: the ref
	Names   []string       // add_labels, remove_label: the labels; add/remove_assignees: the logins
	State   string         // set_state: "open" or "closed"
	Reason  string         // set_state: completed, not_planned or ""
	// create_branch, delete_ref, commit_on_branch, create_pr, create_tag:
	// Ref is the branch, ref or tag; SHA the target or expected head;
	// Title the headline or PR title; Body the body or tag message;
	// Names the paths written ("+path") and deleted ("-path"); Event the
	// PR's base.
}

// Client is the fake. Fill the maps, then hand it to the UI; every
// mutating call lands in Calls (and the typed Approvals/Comments/Merges/
// DeployApprovals), and Fail[op] makes that op return the error.
// All methods are safe for concurrent use.
type Client struct {
	mu sync.Mutex

	// User is Whoami's answer.
	User string
	// HostName is Host's answer ("github.com" when empty); Source is
	// TokenSource's ("keyring" when empty).
	HostName string
	Source   string
	// Rate is LastRate's answer.
	Rate *model.RateLimit

	// Requested are ListRequested's PRs (the ones in the asked repos are
	// served; Repo must be set).
	Requested []*model.PR
	// OpenInto are ListOpenInto's PRs (the ones in the asked repos whose
	// base is the asked branch and that are not drafts are served).
	OpenInto []*model.PR
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
	// RepoRuns are RepoWaitingRuns' answers per repo, filtered to Status
	// "waiting"; RepoFail makes one repo's RepoWaitingRuns fail (Fail
	// ["repo_waiting_runs"] fails every repo).
	RepoRuns map[model.Repo][]model.WorkflowRun
	RepoFail map[model.Repo]error
	// Pending are PendingDeployments' answers per run id.
	Pending map[int64][]model.PendingDeployment
	// FileRuns are WorkflowFileRuns' answers keyed "owner/name:file"; the
	// fake filters them by event and by created at or after since.
	FileRuns map[string][]model.WorkflowRun
	// Jobs are RunJobs' answers per run id.
	Jobs map[int64][]model.WorkflowJob
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

	// Promote are PromotePRs' answers per repo; Ancestors IsAncestor's,
	// keyed "a..b" (absent: false); Changes CommitChange's per sha;
	// BlobSHAs BlobAt's, keyed "ref:path" (absent: no such file);
	// BlobData BlobBase64's per blob sha; Heads BranchHead's, keyed
	// "owner/name@branch". A successful CreateBranch sets Heads, and
	// CommitOnBranch moves it to "c0mmit<n>".
	Promote   map[model.Repo]ghapi.PromoteSet
	Ancestors map[string]bool
	Changes   map[string]model.CommitChange
	BlobSHAs  map[string]string
	BlobData  map[string]string
	Heads     map[string]string
	// NextPR is the number CreatePR hands back (0: 900).
	NextPR int

	// Teams is each "org/team"'s members for TeamMember; a team that is
	// not in the map is unreadable.
	Teams map[string][]string
	// LabelDefs are RepoLabels' answers.
	LabelDefs map[model.Repo][]model.Label

	// Fail makes the named op return the error: whoami, team_member, list, pr_detail,
	// issue_detail, users, comments, labelled, approve, request_changes,
	// dismiss_review, comment, dispatch, dispatch_workflow, merge,
	// waiting_runs, repo_waiting_runs, pending, approve_deployments,
	// reject_deployments, rerun, rerun_failed, cancel_run, job_logs,
	// workflow_runs, file_runs, jobs, files, diff, compare, tags,
	// org_repos, rulesets, fortress, releases, repo_labels, add_labels,
	// remove_label, add_assignees, remove_assignees, set_state.
	Fail map[string]error

	// JobLog is JobLogs' answer per job id.
	JobLog map[int64]string

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
	Dispatches      []Call
	// RunActions are the re-run, re-run-failed and cancel calls.
	RunActions []Call
	// Reads counts every read op by name, for tests that assert caching.
	Reads map[string]int

	// BeforeOp, when set, runs before an op is served (reads and writes
	// alike, by the names Fail uses), and AfterCall after a mutating call
	// is recorded; neither holds the lock, so they may call Mutate. A
	// demo moves its fixture along with them.
	BeforeOp  func(op string)
	AfterCall func(c Call)
}

// Mutate runs fn with the fake's lock held, so a hook can swap the
// canned answers between two calls.
func (f *Client) Mutate(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

var _ ghapi.Client = (*Client)(nil)

// New is an empty fake whose Whoami answers user.
func New(user string) *Client {
	return &Client{User: user, Fail: map[string]error{}, Reads: map[string]int{}}
}

func (f *Client) fail(op string) error {
	if f.BeforeOp != nil {
		f.BeforeOp(op)
	}
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
	f.recordLocked(c)
	f.mu.Unlock()
	f.after(c)
}

func (f *Client) after(c Call) {
	if f.AfterCall != nil {
		f.AfterCall(c)
	}
}

// recordLocked is record with f.mu held.
func (f *Client) recordLocked(c Call) {
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
	case "dispatch", "dispatch_workflow":
		f.Dispatches = append(f.Dispatches, c)
	case "rerun", "rerun_failed", "cancel_run":
		f.RunActions = append(f.RunActions, c)
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

// TeamMember answers from Teams.
func (f *Client) TeamMember(ctx context.Context, org, team, login string) (ghapi.TeamMembership, error) {
	if err := f.fail("team_member"); err != nil {
		return ghapi.TeamMembership{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	members, ok := f.Teams[org+"/"+team]
	if !ok {
		return ghapi.TeamMembership{}, nil
	}
	for _, m := range members {
		if strings.EqualFold(m, login) {
			return ghapi.TeamMembership{Member: true, Readable: true}, nil
		}
	}
	return ghapi.TeamMembership{Readable: true}, nil
}

// Host is HostName, or github.com.
func (f *Client) Host() string {
	if f.HostName == "" {
		return "github.com"
	}
	return f.HostName
}

// TokenSource is Source, or keyring.
func (f *Client) TokenSource() string {
	if f.Source == "" {
		return "keyring"
	}
	return f.Source
}

// LastRate is Rate.
func (f *Client) LastRate() *model.RateLimit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Rate
}

// ListRequested serves the Requested PRs in repos.
func (f *Client) ListRequested(ctx context.Context, repos []model.Repo) (ghapi.ListResult, error) {
	if err := f.fail("requested"); err != nil {
		return ghapi.ListResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out ghapi.ListResult
	for _, p := range f.Requested {
		for _, r := range repos {
			if p.Repo == r {
				out.PRs = append(out.PRs, p)
				break
			}
		}
	}
	return out, nil
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
	p, ok := f.PRs[model.Ident{Repo: repo, Number: number}]
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
	i, ok := f.Issues[model.Ident{Repo: repo, Number: number}]
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
	return append([]model.Comment(nil), f.Comments[model.Ident{Repo: repo, Number: number}]...), nil
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
func (f *Client) Approve(ctx context.Context, repo model.Repo, number int, body, headSHA string) error {
	if err := f.fail("approve"); err != nil {
		return err
	}
	f.record(Call{Op: "approve", Repo: repo, Number: number, Body: body, SHA: headSHA})
	return nil
}

// Comment records the comment.
func (f *Client) Comment(ctx context.Context, repo model.Repo, number int, body string) error {
	_, err := f.PostComment(ctx, repo, number, body)
	return err
}

// PostComment records the comment and hands back its id: the count of
// comments posted so far (1, 2, ...).
func (f *Client) PostComment(ctx context.Context, repo model.Repo, number int, body string) (int64, error) {
	if err := f.fail("comment"); err != nil {
		return 0, err
	}
	f.mu.Lock()
	id := int64(len(f.CommentsPosted) + 1) // under the same lock as the record: two posts never share an id
	c := Call{Op: "comment", Repo: repo, Number: number, Body: body, ID: id}
	f.recordLocked(c)
	f.mu.Unlock()
	f.after(c)
	return id, nil
}

// RepoLabels serves LabelDefs.
func (f *Client) RepoLabels(ctx context.Context, repo model.Repo) ([]model.Label, error) {
	if err := f.fail("repo_labels"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Label(nil), f.LabelDefs[repo]...), nil
}

// AddLabels records the labels added.
func (f *Client) AddLabels(ctx context.Context, repo model.Repo, number int, labels []string) error {
	if err := f.fail("add_labels"); err != nil {
		return err
	}
	f.record(Call{Op: "add_labels", Repo: repo, Number: number, Names: append([]string(nil), labels...)})
	return nil
}

// RemoveLabel records the label removed.
func (f *Client) RemoveLabel(ctx context.Context, repo model.Repo, number int, label string) error {
	if err := f.fail("remove_label"); err != nil {
		return err
	}
	f.record(Call{Op: "remove_label", Repo: repo, Number: number, Names: []string{label}})
	return nil
}

// AddAssignees records the logins assigned.
func (f *Client) AddAssignees(ctx context.Context, repo model.Repo, number int, logins []string) error {
	if err := f.fail("add_assignees"); err != nil {
		return err
	}
	f.record(Call{Op: "add_assignees", Repo: repo, Number: number, Names: append([]string(nil), logins...)})
	return nil
}

// RemoveAssignees records the logins unassigned.
func (f *Client) RemoveAssignees(ctx context.Context, repo model.Repo, number int, logins []string) error {
	if err := f.fail("remove_assignees"); err != nil {
		return err
	}
	f.record(Call{Op: "remove_assignees", Repo: repo, Number: number, Names: append([]string(nil), logins...)})
	return nil
}

// SetIssueState records the close or reopen.
func (f *Client) SetIssueState(ctx context.Context, repo model.Repo, number int, state, reason string) error {
	if err := f.fail("set_state"); err != nil {
		return err
	}
	f.record(Call{Op: "set_state", Repo: repo, Number: number, State: state, Reason: reason})
	return nil
}

// Dispatch records the repository_dispatch event.
func (f *Client) Dispatch(ctx context.Context, repo model.Repo, eventType string, payload map[string]any) error {
	if err := f.fail("dispatch"); err != nil {
		return err
	}
	f.record(Call{Op: "dispatch", Repo: repo, Event: eventType, Payload: payload})
	return nil
}

// RequestChanges records a REQUEST_CHANGES review.
func (f *Client) RequestChanges(ctx context.Context, repo model.Repo, number int, body string) error {
	if err := f.fail("request_changes"); err != nil {
		return err
	}
	f.record(Call{Op: "request_changes", Repo: repo, Number: number, Body: body})
	return nil
}

// DismissReview records a review dismissal.
func (f *Client) DismissReview(ctx context.Context, repo model.Repo, number int, reviewID int64, message string) error {
	if err := f.fail("dismiss_review"); err != nil {
		return err
	}
	f.record(Call{Op: "dismiss_review", Repo: repo, Number: number, ID: reviewID, Body: message})
	return nil
}

// DispatchWorkflow records a workflow_dispatch.
func (f *Client) DispatchWorkflow(ctx context.Context, repo model.Repo, file, ref string, inputs map[string]any) error {
	if err := f.fail("dispatch_workflow"); err != nil {
		return err
	}
	f.record(Call{Op: "dispatch_workflow", Repo: repo, File: file, Ref: ref, Payload: inputs})
	return nil
}

// RejectDeployments records a deployment rejection.
func (f *Client) RejectDeployments(ctx context.Context, repo model.Repo, runID int64, envIDs []int64, comment string) error {
	if err := f.fail("reject_deployments"); err != nil {
		return err
	}
	f.record(Call{Op: "reject_deployments", Repo: repo, RunID: runID, EnvIDs: append([]int64(nil), envIDs...), Body: comment})
	return nil
}

// RerunRun records a re-run.
func (f *Client) RerunRun(ctx context.Context, repo model.Repo, runID int64) error {
	if err := f.fail("rerun"); err != nil {
		return err
	}
	f.record(Call{Op: "rerun", Repo: repo, RunID: runID})
	return nil
}

// RerunFailedJobs records a re-run of the failed jobs.
func (f *Client) RerunFailedJobs(ctx context.Context, repo model.Repo, runID int64) error {
	if err := f.fail("rerun_failed"); err != nil {
		return err
	}
	f.record(Call{Op: "rerun_failed", Repo: repo, RunID: runID})
	return nil
}

// CancelRun records a cancel.
func (f *Client) CancelRun(ctx context.Context, repo model.Repo, runID int64) error {
	if err := f.fail("cancel_run"); err != nil {
		return err
	}
	f.record(Call{Op: "cancel_run", Repo: repo, RunID: runID})
	return nil
}

// JobLogs serves JobLog.
func (f *Client) JobLogs(ctx context.Context, repo model.Repo, jobID int64) (string, error) {
	if err := f.fail("job_logs"); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.JobLog[jobID], nil
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

// RepoWaitingRuns is RepoRuns[repo] filtered to Status "waiting", or
// RepoFail[repo].
func (f *Client) RepoWaitingRuns(ctx context.Context, repo model.Repo) ([]model.WorkflowRun, error) {
	if err := f.fail("repo_waiting_runs"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.RepoFail[repo]; err != nil {
		return nil, err
	}
	var out []model.WorkflowRun
	for _, r := range f.RepoRuns[repo] {
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

// WorkflowFileRuns is FileRuns["owner/name:file"] narrowed to the event
// and to runs created at or after since, newest first and at most one
// page of 50, as the real listing answers.
func (f *Client) WorkflowFileRuns(ctx context.Context, repo model.Repo, file, event string, since time.Time) ([]model.WorkflowRun, error) {
	if err := f.fail("file_runs"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.WorkflowRun
	for _, r := range f.FileRuns[repo.String()+":"+file] {
		if event != "" && r.Event != event {
			continue
		}
		if !since.IsZero() && r.CreatedAt.Before(since) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > 50 {
		out = out[:50]
	}
	return out, nil
}

// RunJobs is Jobs[runID].
func (f *Client) RunJobs(ctx context.Context, repo model.Repo, runID int64) ([]model.WorkflowJob, error) {
	if err := f.fail("jobs"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.WorkflowJob(nil), f.Jobs[runID]...), nil
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
	return append([]model.PRFile(nil), f.Files[model.Ident{Repo: repo, Number: number}]...), nil
}

// PRDiff serves Diffs.
func (f *Client) PRDiff(ctx context.Context, repo model.Repo, number int) (string, error) {
	if err := f.fail("diff"); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Diffs[model.Ident{Repo: repo, Number: number}], nil
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

// ---- promotion ----

// PromotePRs serves Promote[repo].
func (f *Client) PromotePRs(ctx context.Context, repo model.Repo, first, stage string, since time.Time) (ghapi.PromoteSet, error) {
	if err := f.fail("promote_prs"); err != nil {
		return ghapi.PromoteSet{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Promote[repo], nil
}

// IsAncestor serves Ancestors["a..b"]; a == b is always true.
func (f *Client) IsAncestor(ctx context.Context, repo model.Repo, a, b string) (bool, error) {
	if err := f.fail("is_ancestor"); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return a == b || f.Ancestors[a+".."+b], nil
}

// CommitChange serves Changes[sha].
func (f *Client) CommitChange(ctx context.Context, repo model.Repo, sha string) (model.CommitChange, error) {
	if err := f.fail("commit_change"); err != nil {
		return model.CommitChange{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.Changes[sha]
	if !ok {
		return c, &ghapi.Error{Status: 404, Message: "no commit " + sha}
	}
	return c, nil
}

// BlobAt serves BlobSHAs["ref:path"].
func (f *Client) BlobAt(ctx context.Context, repo model.Repo, path, ref string) (string, bool, error) {
	if err := f.fail("blob_at"); err != nil {
		return "", false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	sha, ok := f.BlobSHAs[ref+":"+path]
	return sha, ok, nil
}

// BlobBase64 serves BlobData[sha].
func (f *Client) BlobBase64(ctx context.Context, repo model.Repo, sha string) (string, error) {
	if err := f.fail("blob"); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.BlobData[sha]
	if !ok {
		return "", &ghapi.Error{Status: 404, Message: "no blob " + sha}
	}
	return d, nil
}

// BranchHead serves Heads["owner/name@branch"].
func (f *Client) BranchHead(ctx context.Context, repo model.Repo, branch string) (string, error) {
	if err := f.fail("branch_head"); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.Heads[repo.String()+"@"+branch]
	if !ok {
		return "", &ghapi.Error{Status: 404, Message: "no branch " + branch}
	}
	return h, nil
}

// CreateBranch records the call and sets the branch's head; an existing
// branch is a 422, as on GitHub.
func (f *Client) CreateBranch(ctx context.Context, repo model.Repo, branch, sha string) error {
	if err := f.fail("create_branch"); err != nil {
		return err
	}
	f.mu.Lock()
	key := repo.String() + "@" + branch
	if _, ok := f.Heads[key]; ok {
		f.mu.Unlock()
		return &ghapi.Error{Status: 422, Message: "Reference already exists"}
	}
	if f.Heads == nil {
		f.Heads = map[string]string{}
	}
	f.Heads[key] = sha
	f.recordLocked(Call{Op: "create_branch", Repo: repo, Ref: branch, SHA: sha})
	f.mu.Unlock()
	return nil
}

// DeleteRef records the call (and forgets a branch's head).
func (f *Client) DeleteRef(ctx context.Context, repo model.Repo, ref string) error {
	if err := f.fail("delete_ref"); err != nil {
		return err
	}
	f.mu.Lock()
	if b, ok := strings.CutPrefix(ref, "heads/"); ok {
		delete(f.Heads, repo.String()+"@"+b)
	}
	f.recordLocked(Call{Op: "delete_ref", Repo: repo, Ref: ref})
	f.mu.Unlock()
	return nil
}

// CommitOnBranch records the call; the branch must be at expectedHead.
func (f *Client) CommitOnBranch(ctx context.Context, repo model.Repo, branch, expectedHead, headline, body string, adds []model.FileAdd, dels []string) (string, error) {
	if err := f.fail("commit_on_branch"); err != nil {
		return "", err
	}
	f.mu.Lock()
	key := repo.String() + "@" + branch
	if f.Heads[key] != expectedHead {
		f.mu.Unlock()
		return "", fmt.Errorf("createCommitOnBranch: expected head %s, branch is at %s", expectedHead, f.Heads[key])
	}
	var names []string
	for _, a := range adds {
		names = append(names, "+"+a.Path)
	}
	for _, d := range dels {
		names = append(names, "-"+d)
	}
	oid := fmt.Sprintf("c0mmit%d", len(f.Calls))
	f.Heads[key] = oid
	f.recordLocked(Call{Op: "commit_on_branch", Repo: repo, Ref: branch, SHA: expectedHead, Title: headline, Body: body, Names: names})
	f.mu.Unlock()
	return oid, nil
}

// CreatePR records the call and hands back NextPR (900 when unset).
func (f *Client) CreatePR(ctx context.Context, repo model.Repo, base, head, title, body string) (int, error) {
	if err := f.fail("create_pr"); err != nil {
		return 0, err
	}
	f.mu.Lock()
	n := f.NextPR
	if n == 0 {
		n = 900
	}
	f.recordLocked(Call{Op: "create_pr", Repo: repo, Number: n, Ref: head, Event: base, Title: title, Body: body})
	f.mu.Unlock()
	return n, nil
}

// CreateTag records the call and adds the tag to Tags.
func (f *Client) CreateTag(ctx context.Context, repo model.Repo, name, message, sha string) error {
	if err := f.fail("create_tag"); err != nil {
		return err
	}
	f.mu.Lock()
	if f.Tags == nil {
		f.Tags = map[model.Repo][]string{}
	}
	f.Tags[repo] = append(f.Tags[repo], name)
	f.recordLocked(Call{Op: "create_tag", Repo: repo, Ref: name, SHA: sha, Body: message})
	f.mu.Unlock()
	return nil
}

// ListOpenInto serves the OpenInto PRs in repos into base.
func (f *Client) ListOpenInto(ctx context.Context, repos []model.Repo, base string) (ghapi.ListResult, error) {
	if err := f.fail("open_into"); err != nil {
		return ghapi.ListResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[model.Repo]bool{}
	for _, r := range repos {
		want[r] = true
	}
	var out ghapi.ListResult
	for _, p := range f.OpenInto {
		if want[p.Repo] && p.BaseRefName == base && !p.IsDraft {
			out.PRs = append(out.PRs, p)
		}
	}
	return out, nil
}
