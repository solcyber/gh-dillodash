package ghapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// ---- promotion reads ----

// PromoteSet is what the promotion view reads of one repo: the PRs merged
// into the first tier, the staging tier and production since a date, and
// the PRs open into the staging tier.
type PromoteSet struct {
	FirstMerged []*model.PR
	StageMerged []*model.PR
	StageOpen   []*model.PR
	// ProdMerged are PRs merged into production: a hotfix that carried a
	// first-tier change straight there names it, so the change is
	// released, not still waiting on the first tier.
	ProdMerged []*model.PR
	// Notes are searches that hit their page (a PR past it is not read).
	Notes []string
}

// promoteFirst is one search's page; promoteMax the most PRs a search
// reads, page after page.
const (
	promoteFirst = 50
	promoteMax   = 200
)

const promotePRFields = `number title body state isDraft createdAt mergedAt
      headRefName headRefOid baseRefName mergeable mergeStateStatus
      mergeCommit { oid } author { login }
      commits(last: 1) { nodes { commit { committedDate statusCheckRollup { state } } } }`

// promoteAliases are the searches, in request order.
var promoteAliases = []string{"first", "stage", "open", "prod"}

// promoteQuery is the four searches in one request.
func promoteQuery() string {
	var b strings.Builder
	b.WriteString("query($first: String!, $stage: String!, $open: String!, $prod: String!) {\n  " + rateLimitField)
	for _, a := range promoteAliases {
		fmt.Fprintf(&b, "\n  %s: search(query: $%s, type: ISSUE, first: %d) {\n    issueCount pageInfo { hasNextPage endCursor }\n    nodes { ... on PullRequest { %s } }\n  }", a, a, promoteFirst, promotePRFields)
	}
	b.WriteString("\n}\n")
	return b.String()
}

// promoteMoreQuery is one search's next page.
func promoteMoreQuery() string {
	return "query($q: String!, $after: String!) {\n  " + rateLimitField +
		fmt.Sprintf("\n  s: search(query: $q, type: ISSUE, first: %d, after: $after) {\n    issueCount pageInfo { hasNextPage endCursor }\n    nodes { ... on PullRequest { %s } }\n  }\n}\n", promoteFirst, promotePRFields)
}

type promoteSearch struct {
	IssueCount int `json:"issueCount"`
	PageInfo   struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []json.RawMessage `json:"nodes"`
}

// PromoteSearches is the search text PromotePRs sends, by alias (tests
// and the fake read it).
func PromoteSearches(repo model.Repo, first, stage, prod string, since time.Time) map[string]string {
	r := "repo:" + repo.String()
	day := since.UTC().Format("2006-01-02")
	return map[string]string{
		"first": fmt.Sprintf("%s is:pr is:merged base:%s merged:>=%s sort:updated-desc", r, first, day),
		"stage": fmt.Sprintf("%s is:pr is:merged base:%s merged:>=%s sort:updated-desc", r, stage, day),
		"open":  fmt.Sprintf("%s is:pr is:open base:%s sort:updated-desc", r, stage),
		"prod":  fmt.Sprintf("%s is:pr is:merged base:%s merged:>=%s sort:updated-desc", r, prod, day),
	}
}

// PromotePRs reads one repo's promotion PRs in one request of four
// searches. A search that fails fails the call (the view would otherwise
// say a change is where it isn't).
func (c *GitHub) PromotePRs(ctx context.Context, repo model.Repo, first, stage, prod string, since time.Time) (PromoteSet, error) {
	var out PromoteSet
	q := PromoteSearches(repo, first, stage, prod, since)
	vars := map[string]any{"first": q["first"], "stage": q["stage"], "open": q["open"], "prod": q["prod"]}
	data, errs, err := c.graphql(ctx, promoteQuery(), vars)
	if err != nil {
		return out, err
	}
	for _, alias := range promoteAliases {
		var s promoteSearch
		ok, err := data.get(alias, &s)
		if err != nil || !ok {
			msg := "no answer"
			if err != nil {
				msg = err.Error()
			}
			for _, e := range errs {
				if e.pathIs(alias) && e.Message != "" {
					msg = e.Message
				}
			}
			return out, fmt.Errorf("searching %s: %s", q[alias], msg)
		}
		nodes := s.Nodes
		for page := s.PageInfo; page.HasNextPage && page.EndCursor != "" && len(nodes) < promoteMax; {
			more, _, err := c.graphql(ctx, promoteMoreQuery(), map[string]any{"q": q[alias], "after": page.EndCursor})
			if err != nil {
				return out, err
			}
			var next promoteSearch
			if ok, err := more.get("s", &next); err != nil || !ok {
				break
			}
			nodes = append(nodes, next.Nodes...)
			page = next.PageInfo
		}
		var prs []*model.PR
		for _, raw := range nodes {
			var pr model.PR
			if json.Unmarshal(raw, &pr) != nil || pr.Number == 0 {
				continue
			}
			pr.Repo = repo
			p := pr
			prs = append(prs, &p)
		}
		if s.IssueCount > len(nodes) {
			out.Notes = append(out.Notes, fmt.Sprintf("promote: %s: showing %d of %d PRs for %q", repo.Name, len(nodes), s.IssueCount, q[alias]))
		}
		switch alias {
		case "first":
			out.FirstMerged = prs
		case "stage":
			out.StageMerged = prs
		case "prod":
			out.ProdMerged = prs
		default:
			out.StageOpen = prs
		}
	}
	return out, nil
}

// IsAncestor reports whether commit a is on b's history (a == b counts):
// the compare of a...b says "ahead" or "identical". It reads one commit
// of the compare at most.
func (c *GitHub) IsAncestor(ctx context.Context, repo model.Repo, a, b string) (bool, error) {
	var raw struct {
		Status string `json:"status"`
	}
	path := repoPath(repo, "compare/"+url.PathEscape(a)+"..."+url.PathEscape(b)+"?per_page=1")
	if err := c.restJSON(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return false, err
	}
	return raw.Status == "ahead" || raw.Status == "identical", nil
}

// CommitChange reads a commit's parents and changed files (up to ten
// pages of 100).
func (c *GitHub) CommitChange(ctx context.Context, repo model.Repo, sha string) (model.CommitChange, error) {
	var out model.CommitChange
	for page := 1; page <= 10; page++ {
		var raw struct {
			SHA     string `json:"sha"`
			Parents []struct {
				SHA string `json:"sha"`
			} `json:"parents"`
			Files []model.ChangedFile `json:"files"`
		}
		path := repoPath(repo, fmt.Sprintf("commits/%s?per_page=100&page=%d", url.PathEscape(sha), page))
		if err := c.restJSON(ctx, http.MethodGet, path, nil, &raw); err != nil {
			return out, err
		}
		if page == 1 {
			out.SHA = raw.SHA
			for _, p := range raw.Parents {
				out.Parents = append(out.Parents, p.SHA)
			}
		}
		out.Files = append(out.Files, raw.Files...)
		if len(raw.Files) < 100 {
			return out, nil
		}
	}
	out.Truncated = true
	return out, nil
}

// BlobAt is the blob sha of path at ref; exists is false when there is no
// such file there (a 404).
func (c *GitHub) BlobAt(ctx context.Context, repo model.Repo, path, ref string) (sha string, exists bool, err error) {
	var raw struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	p := repoPath(repo, "contents/"+escapeRef(path)+"?ref="+url.QueryEscape(ref))
	if err := c.restJSON(ctx, http.MethodGet, p, nil, &raw); err != nil {
		if e, ok := AsError(err); ok && e.Status == http.StatusNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	if raw.Type != "" && raw.Type != "file" {
		return "", false, fmt.Errorf("%s at %s is a %s, not a file", path, ref, raw.Type)
	}
	return raw.SHA, true, nil
}

// BlobBase64 is a blob's content, base64-encoded (as GitHub stores it,
// without the line breaks).
func (c *GitHub) BlobBase64(ctx context.Context, repo model.Repo, sha string) (string, error) {
	var raw struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := c.restJSON(ctx, http.MethodGet, repoPath(repo, "git/blobs/"+url.PathEscape(sha)), nil, &raw); err != nil {
		return "", err
	}
	if raw.Encoding != "base64" {
		return "", fmt.Errorf("blob %s came back as %q, not base64", sha, raw.Encoding)
	}
	return strings.NewReplacer("\n", "", "\r", "").Replace(raw.Content), nil
}

// BranchHead is a branch's tip.
func (c *GitHub) BranchHead(ctx context.Context, repo model.Repo, branch string) (string, error) {
	var raw struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.restJSON(ctx, http.MethodGet, repoPath(repo, "git/ref/heads/"+escapeRef(branch)), nil, &raw); err != nil {
		return "", err
	}
	return raw.Object.SHA, nil
}

// ---- promotion writes ----

// CreateBranch creates refs/heads/<branch> at sha (contents write).
func (c *GitHub) CreateBranch(ctx context.Context, repo model.Repo, branch, sha string) error {
	return c.restJSON(ctx, http.MethodPost, repoPath(repo, "git/refs"),
		map[string]any{"ref": "refs/heads/" + branch, "sha": sha}, nil)
}

// DeleteRef deletes a ref named without its "refs/" prefix
// ("heads/promote/12-stage", "tags/hold/612").
func (c *GitHub) DeleteRef(ctx context.Context, repo model.Repo, ref string) error {
	return c.restJSON(ctx, http.MethodDelete, repoPath(repo, "git/refs/"+escapeRef(ref)), nil, nil)
}

// CommitOnBranch commits file changes on top of branch with GraphQL's
// createCommitOnBranch, which GitHub signs: the commit lands only while
// the branch is still at expectedHead. adds carry base64 contents. It
// returns the new commit's oid.
func (c *GitHub) CommitOnBranch(ctx context.Context, repo model.Repo, branch, expectedHead, headline, body string, adds []model.FileAdd, dels []string) (string, error) {
	const m = `mutation($input: CreateCommitOnBranchInput!) {
  createCommitOnBranch(input: $input) { commit { oid } }
}`
	additions := make([]map[string]any, 0, len(adds))
	for _, a := range adds {
		additions = append(additions, map[string]any{"path": a.Path, "contents": a.Base64})
	}
	deletions := make([]map[string]any, 0, len(dels))
	for _, d := range dels {
		deletions = append(deletions, map[string]any{"path": d})
	}
	input := map[string]any{
		"branch":          map[string]any{"repositoryNameWithOwner": repo.String(), "branchName": branch},
		"expectedHeadOid": expectedHead,
		"message":         map[string]any{"headline": headline, "body": body},
		"fileChanges":     map[string]any{"additions": additions, "deletions": deletions},
	}
	data, errs, err := c.graphql(ctx, m, map[string]any{"input": input})
	if err != nil {
		return "", err
	}
	if len(errs) > 0 {
		return "", fmt.Errorf("createCommitOnBranch: %s", errs[0].Message)
	}
	var res struct {
		Commit struct {
			Oid string `json:"oid"`
		} `json:"commit"`
	}
	if ok, err := data.get("createCommitOnBranch", &res); err != nil || !ok || res.Commit.Oid == "" {
		return "", fmt.Errorf("createCommitOnBranch: no commit came back")
	}
	return res.Commit.Oid, nil
}

// CreatePR opens a PR from head into base and returns its number.
func (c *GitHub) CreatePR(ctx context.Context, repo model.Repo, base, head, title, body string) (int, error) {
	var raw struct {
		Number int `json:"number"`
	}
	err := c.restJSON(ctx, http.MethodPost, repoPath(repo, "pulls"),
		map[string]any{"base": base, "head": head, "title": title, "body": body}, &raw)
	return raw.Number, err
}

// CreateTag creates an annotated tag name on the commit sha with message
// (a tag object, then refs/tags/<name>); the tagger is the token's user.
func (c *GitHub) CreateTag(ctx context.Context, repo model.Repo, name, message, sha string) error {
	var obj struct {
		SHA string `json:"sha"`
	}
	if err := c.restJSON(ctx, http.MethodPost, repoPath(repo, "git/tags"),
		map[string]any{"tag": name, "message": message, "object": sha, "type": "commit"}, &obj); err != nil {
		return err
	}
	return c.restJSON(ctx, http.MethodPost, repoPath(repo, "git/refs"),
		map[string]any{"ref": "refs/tags/" + name, "sha": obj.SHA}, nil)
}
