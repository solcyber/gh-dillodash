package ghapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// ---- GraphQL reads -----------------------------------------------------

// Whoami is the token's login.
func (c *GitHub) Whoami(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := c.restJSON(ctx, http.MethodGet, "user", nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

// PRDetail is the detail query's view of a PR.
func (c *GitHub) PRDetail(ctx context.Context, repo model.Repo, number int) (*model.PR, error) {
	vars := repoVars(repo)
	vars["number"] = number
	data, errs, err := c.graphql(ctx, prDetailQuery, vars)
	if err != nil {
		return nil, err
	}
	var node struct {
		PullRequest *model.PR `json:"pullRequest"`
	}
	if err := repoNode(data, errs, "repository", &node); err != nil {
		return nil, err
	}
	if node.PullRequest == nil {
		return nil, &Error{Message: fmt.Sprintf("#%d is not a pull request in %s", number, repo)}
	}
	node.PullRequest.Repo = repo
	return node.PullRequest, nil
}

// IssueDetail is the detail query's view of an issue.
func (c *GitHub) IssueDetail(ctx context.Context, repo model.Repo, number int) (*model.Issue, error) {
	vars := repoVars(repo)
	vars["number"] = number
	data, errs, err := c.graphql(ctx, issueDetailQuery, vars)
	if err != nil {
		return nil, err
	}
	var node struct {
		Issue *model.Issue `json:"issue"`
	}
	if err := repoNode(data, errs, "repository", &node); err != nil {
		return nil, err
	}
	if node.Issue == nil {
		return nil, &Error{Message: fmt.Sprintf("#%d is not an issue in %s", number, repo)}
	}
	node.Issue.Repo = repo
	return node.Issue, nil
}

// AssignableUsers is who can be assigned in the repo (first 50).
func (c *GitHub) AssignableUsers(ctx context.Context, repo model.Repo) ([]model.Actor, error) {
	data, errs, err := c.graphql(ctx, usersQuery, repoVars(repo))
	if err != nil {
		return nil, err
	}
	var node struct {
		AssignableUsers struct {
			Nodes []model.Actor `json:"nodes"`
		} `json:"assignableUsers"`
	}
	if err := repoNode(data, errs, "repository", &node); err != nil {
		return nil, err
	}
	return node.AssignableUsers.Nodes, nil
}

// PRComments is the PR's last 100 comments.
func (c *GitHub) PRComments(ctx context.Context, repo model.Repo, number int) ([]model.Comment, error) {
	vars := repoVars(repo)
	vars["number"] = number
	data, errs, err := c.graphql(ctx, prCommentsQuery, vars)
	if err != nil {
		return nil, err
	}
	var node struct {
		PullRequest *struct {
			Comments model.Comments `json:"comments"`
		} `json:"pullRequest"`
	}
	if err := repoNode(data, errs, "repository", &node); err != nil {
		return nil, err
	}
	if node.PullRequest == nil {
		return nil, &Error{Message: fmt.Sprintf("#%d is not a pull request in %s", number, repo)}
	}
	return node.PullRequest.Comments.Nodes, nil
}

// IssuesWithLabel is the open issues carrying label, newest first, with
// their last 100 comments. first <= 0 means 10.
func (c *GitHub) IssuesWithLabel(ctx context.Context, repo model.Repo, label string, first int) ([]*model.Issue, error) {
	if first <= 0 {
		first = 10
	}
	vars := repoVars(repo)
	vars["label"] = label
	vars["first"] = first
	data, errs, err := c.graphql(ctx, issuesWithLabelQuery, vars)
	if err != nil {
		return nil, err
	}
	var node struct {
		Issues struct {
			Nodes []*model.Issue `json:"nodes"`
		} `json:"issues"`
	}
	if err := repoNode(data, errs, "repository", &node); err != nil {
		return nil, err
	}
	out := make([]*model.Issue, 0, len(node.Issues.Nodes))
	for _, is := range node.Issues.Nodes {
		if is != nil {
			is.Repo = repo
			out = append(out, is)
		}
	}
	return out, nil
}

// ---- REST writes -------------------------------------------------------

// Approve posts an APPROVE review.
func (c *GitHub) Approve(ctx context.Context, repo model.Repo, number int, body string) error {
	return c.restJSON(ctx, http.MethodPost, repoPath(repo, fmt.Sprintf("pulls/%d/reviews", number)),
		map[string]any{"event": "APPROVE", "body": body}, nil)
}

// Comment posts an issue/PR comment.
func (c *GitHub) Comment(ctx context.Context, repo model.Repo, number int, body string) error {
	return c.restJSON(ctx, http.MethodPost, repoPath(repo, fmt.Sprintf("issues/%d/comments", number)),
		map[string]any{"body": body}, nil)
}

// MergePR merges the PR, only if its head is still sha (GitHub refuses
// otherwise). method is merge, squash or rebase; "" means merge.
func (c *GitHub) MergePR(ctx context.Context, repo model.Repo, number int, sha, title, method string) (MergeResult, error) {
	if method == "" {
		method = "merge"
	}
	payload := map[string]any{"merge_method": method}
	if sha != "" {
		payload["sha"] = sha
	}
	if title != "" {
		payload["commit_title"] = title
	}
	var res MergeResult
	err := c.restJSON(ctx, http.MethodPut, repoPath(repo, fmt.Sprintf("pulls/%d/merge", number)), payload, &res)
	return res, err
}

// ---- Actions -----------------------------------------------------------

type runsPage struct {
	WorkflowRuns []model.WorkflowRun `json:"workflow_runs"`
}

// WaitingRuns is the workflow runs on headSHA waiting on an environment.
func (c *GitHub) WaitingRuns(ctx context.Context, repo model.Repo, headSHA string) ([]model.WorkflowRun, error) {
	var page runsPage
	path := repoPath(repo, "actions/runs?head_sha="+url.QueryEscape(headSHA)+"&status=waiting&per_page=50")
	if err := c.restJSON(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	return page.WorkflowRuns, nil
}

// WorkflowRuns is every run on headSHA (newest first, up to 100).
func (c *GitHub) WorkflowRuns(ctx context.Context, repo model.Repo, headSHA string) ([]model.WorkflowRun, error) {
	var page runsPage
	path := repoPath(repo, "actions/runs?head_sha="+url.QueryEscape(headSHA)+"&per_page=100")
	if err := c.restJSON(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	return page.WorkflowRuns, nil
}

// PendingDeployments is the environments a run waits on.
func (c *GitHub) PendingDeployments(ctx context.Context, repo model.Repo, runID int64) ([]model.PendingDeployment, error) {
	var out []model.PendingDeployment
	path := repoPath(repo, fmt.Sprintf("actions/runs/%d/pending_deployments", runID))
	if err := c.restJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ApproveDeployments approves the run's pending deployments to envIDs.
func (c *GitHub) ApproveDeployments(ctx context.Context, repo model.Repo, runID int64, envIDs []int64, comment string) error {
	if envIDs == nil {
		envIDs = []int64{}
	}
	path := repoPath(repo, fmt.Sprintf("actions/runs/%d/pending_deployments", runID))
	return c.restJSON(ctx, http.MethodPost, path,
		map[string]any{"environment_ids": envIDs, "state": "approved", "comment": comment}, nil)
}

// ---- REST reads --------------------------------------------------------

// maxPages bounds every paged listing (1000 items is plenty).
const maxPages = 10

// PRFiles is the PR's changed files, up to ten pages of 100.
func (c *GitHub) PRFiles(ctx context.Context, repo model.Repo, number int) ([]model.PRFile, error) {
	var files []model.PRFile
	for page := 1; page <= maxPages; page++ {
		var batch []model.PRFile
		path := repoPath(repo, fmt.Sprintf("pulls/%d/files?per_page=100&page=%d", number, page))
		if err := c.restJSON(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		files = append(files, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return files, nil
}

// PRDiff is the PR's unified diff.
func (c *GitHub) PRDiff(ctx context.Context, repo model.Repo, number int) (string, error) {
	return c.restRaw(ctx, repoPath(repo, fmt.Sprintf("pulls/%d", number)), acceptDiff)
}

// Compare is REST compare base...head: how far head is ahead, and its
// commits (the first page, up to 250).
func (c *GitHub) Compare(ctx context.Context, repo model.Repo, base, head string) (CompareResult, error) {
	var raw struct {
		AheadBy      int `json:"ahead_by"`
		BehindBy     int `json:"behind_by"`
		TotalCommits int `json:"total_commits"`
		Commits      []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
			Parents []struct {
				SHA string `json:"sha"`
			} `json:"parents"`
		} `json:"commits"`
	}
	path := repoPath(repo, "compare/"+url.PathEscape(base)+"..."+url.PathEscape(head)+"?per_page=250")
	if err := c.restJSON(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return CompareResult{}, err
	}
	res := CompareResult{AheadBy: raw.AheadBy, BehindBy: raw.BehindBy, TotalCommits: raw.TotalCommits}
	for _, cm := range raw.Commits {
		cc := CompareCommit{SHA: cm.SHA, Message: cm.Commit.Message}
		for _, p := range cm.Parents {
			cc.Parents = append(cc.Parents, p.SHA)
		}
		res.Commits = append(res.Commits, cc)
	}
	return res, nil
}

// TagRefs is the tag names under prefix ("go/" -> ["go/612", ...]).
func (c *GitHub) TagRefs(ctx context.Context, repo model.Repo, prefix string) ([]string, error) {
	var refs []struct {
		Ref string `json:"ref"`
	}
	path := repoPath(repo, "git/matching-refs/tags/"+strings.TrimLeft(prefix, "/"))
	if err := c.restJSON(ctx, http.MethodGet, path, nil, &refs); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if strings.HasPrefix(r.Ref, "refs/tags/") {
			out = append(out, strings.TrimPrefix(r.Ref, "refs/tags/"))
		}
	}
	return out, nil
}

// OrgRepos is every repo of the org (up to ten pages of 100).
func (c *GitHub) OrgRepos(ctx context.Context, org string) ([]model.RepoListing, error) {
	var repos []model.RepoListing
	for page := 1; page <= maxPages; page++ {
		var batch []model.RepoListing
		path := fmt.Sprintf("orgs/%s/repos?type=all&per_page=100&page=%d", url.PathEscape(org), page)
		if err := c.restJSON(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		for _, r := range batch {
			if r.Name != "" {
				repos = append(repos, r)
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	return repos, nil
}

// Rulesets is the rulesets that apply to a repo (org and repo sourced).
func (c *GitHub) Rulesets(ctx context.Context, repo model.Repo) ([]model.Ruleset, error) {
	var out []model.Ruleset
	if err := c.restJSON(ctx, http.MethodGet, repoPath(repo, "rulesets"), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FileContents reads one file through the contents API.
func (c *GitHub) FileContents(ctx context.Context, repo model.Repo, path, ref string) ([]byte, string, error) {
	var out struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		SHA      string `json:"sha"`
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	p := repoPath(repo, "contents/"+strings.Join(segs, "/"))
	if ref != "" {
		p += "?ref=" + url.QueryEscape(ref)
	}
	if err := c.restJSON(ctx, http.MethodGet, p, nil, &out); err != nil {
		return nil, "", err
	}
	if out.Type != "file" {
		return nil, "", fmt.Errorf("%s in %s is a %s, not a file", path, repo, out.Type)
	}
	if out.Encoding != "base64" {
		return nil, "", fmt.Errorf("%s in %s: unexpected encoding %q", path, repo, out.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, "", fmt.Errorf("%s in %s: %w", path, repo, err)
	}
	return data, out.SHA, nil
}
