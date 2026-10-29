package ghapi

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// listChunk is how many repos share one aliased list request.
const listChunk = 4

// listWorkers caps the list requests in flight: GitHub's secondary
// limits frown on many concurrent requests from one token.
const listWorkers = 6

// listRepoNode is one alias of the list query.
type listRepoNode struct {
	Issues struct {
		TotalCount int            `json:"totalCount"`
		Nodes      []*model.Issue `json:"nodes"`
	} `json:"issues"`
	PullRequests struct {
		TotalCount int         `json:"totalCount"`
		Nodes      []*model.PR `json:"nodes"`
	} `json:"pullRequests"`
}

// ListOpen reads every repo's open PRs and issues: one aliased GraphQL
// request per chunk of four repos, at most listWorkers in flight. A null
// alias becomes that repo's Err; an error on an alias's issues path
// becomes a note (fine-grained PATs without Issues: read); a repo with
// more open than one page shows gets a note too. A chunk that fails
// outright (network, 5xx, no data at all) sets Err on each of its repos
// and the other chunks still land; the call itself fails only on a
// rate limit, which must back the whole refresh off, or when every
// chunk failed, which is an outage rather than a blip.
func (c *GitHub) ListOpen(ctx context.Context, repos []model.Repo) (map[model.Repo]ListResult, error) {
	out := make(map[model.Repo]ListResult, len(repos))
	if len(repos) == 0 {
		return out, nil
	}
	var mu sync.Mutex
	var g errgroup.Group // no shared cancel: one chunk's 502 must not abort the rest
	g.SetLimit(listWorkers)
	var firstErr error
	chunks, failed := 0, 0
	for start := 0; start < len(repos); start += listChunk {
		end := min(start+listChunk, len(repos))
		chunk := repos[start:end]
		chunks++
		g.Go(func() error {
			res, err := c.listChunk(ctx, chunk)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				if firstErr == nil || (IsRateLimit(err) && !IsRateLimit(firstErr)) {
					firstErr = err
				}
				for _, r := range chunk {
					out[r] = ListResult{Err: err}
				}
				return nil
			}
			for r, lr := range res {
				out[r] = lr
			}
			return nil
		})
	}
	_ = g.Wait()
	if firstErr != nil && (IsRateLimit(firstErr) || failed == chunks) {
		return out, firstErr
	}
	return out, nil
}

// listChunk runs one aliased request for up to listChunk repos.
func (c *GitHub) listChunk(ctx context.Context, repos []model.Repo) (map[model.Repo]ListResult, error) {
	vars := make(map[string]any, 2*len(repos))
	for i, r := range repos {
		vars[fmt.Sprintf("o%d", i)] = r.Owner
		vars[fmt.Sprintf("n%d", i)] = r.Name
	}
	data, errs, err := c.graphql(ctx, listQuery(len(repos)), vars)
	if err != nil {
		return nil, err
	}
	out := make(map[model.Repo]ListResult, len(repos))
	for i, r := range repos {
		alias := fmt.Sprintf("r%d", i)
		var node listRepoNode
		if err := repoNode(data, errs, alias, &node); err != nil {
			out[r] = ListResult{Err: err}
			continue
		}
		lr := ListResult{}
		for _, e := range errs {
			if e.pathIs(alias, "issues") {
				msg := e.Message
				if msg == "" {
					msg = "GraphQL error"
				}
				lr.Notes = append(lr.Notes, "issues hidden — token needs Issues: read ("+msg+")")
				break
			}
		}
		for _, p := range node.PullRequests.Nodes {
			if p != nil {
				p.Repo = r
				lr.PRs = append(lr.PRs, p)
			}
		}
		for _, is := range node.Issues.Nodes {
			if is != nil {
				is.Repo = r
				lr.Issues = append(lr.Issues, is)
			}
		}
		if t := node.PullRequests.TotalCount; t > len(lr.PRs) {
			lr.Notes = append(lr.Notes, fmt.Sprintf("showing the newest %d of %d open PRs", len(lr.PRs), t))
		}
		if t := node.Issues.TotalCount; t > len(lr.Issues) {
			lr.Notes = append(lr.Notes, fmt.Sprintf("showing the newest %d of %d open issues", len(lr.Issues), t))
		}
		out[r] = lr
	}
	return out, nil
}
