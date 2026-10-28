package ghapi

import (
	"context"
	"fmt"
	"sync"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// listChunk is how many repos share one aliased list request.
const listChunk = 4

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
// request per chunk of four repos, chunks in flight concurrently. A null
// alias becomes that repo's Err; an error on an alias's issues path
// becomes a note (fine-grained PATs without Issues: read); a repo with
// more open than one page shows gets a note too. Only a request that
// fails outright (network, rate limit, no data at all) is returned as
// the error, and then for every repo of that chunk as well.
func (c *GitHub) ListOpen(ctx context.Context, repos []model.Repo) (map[model.Repo]ListResult, error) {
	out := make(map[model.Repo]ListResult, len(repos))
	if len(repos) == 0 {
		return out, nil
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	for start := 0; start < len(repos); start += listChunk {
		end := start + listChunk
		if end > len(repos) {
			end = len(repos)
		}
		chunk := repos[start:end]
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := c.listChunk(ctx, chunk)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil || (IsRateLimit(err) && !IsRateLimit(firstErr)) {
					firstErr = err
				}
				for _, r := range chunk {
					out[r] = ListResult{Err: err}
				}
				return
			}
			for r, lr := range res {
				out[r] = lr
			}
		}()
	}
	wg.Wait()
	return out, firstErr
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
