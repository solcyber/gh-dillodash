package ghapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// requestedWhose are the searches a requested repo is read through: a PR
// is listed when my review is requested (directly or through a team), I
// reviewed it (so an approval of mine that went stale still shows), or I
// opened it.
var requestedWhose = []string{"review-requested:@me", "reviewed-by:@me", "author:@me"}

// requestedQueryMax keeps one search's text under GitHub's 256-character
// limit; more repos split into more searches in the same request.
const requestedQueryMax = 200

// requestedFirst is one search's page; a PR past it is noted, not read.
const requestedFirst = 50

// requestedSearches is the search text for each alias: every group of
// repos (each group's qualifiers under requestedQueryMax) times each of
// requestedWhose.
func requestedSearches(repos []model.Repo) []string {
	var groups []string
	cur := ""
	for _, r := range repos {
		q := "repo:" + r.String()
		if cur != "" && len(cur)+1+len(q) > requestedQueryMax-60 {
			groups = append(groups, cur)
			cur = ""
		}
		if cur != "" {
			cur += " "
		}
		cur += q
	}
	if cur != "" {
		groups = append(groups, cur)
	}
	var out []string
	for _, g := range groups {
		for _, w := range requestedWhose {
			out = append(out, "is:pr is:open archived:false "+w+" "+g)
		}
	}
	return out
}

// requestedQuery is the GraphQL text for n searches: aliases s0..s(n-1),
// each with its own $q%d.
func requestedQuery(n int) string {
	var decl, aliases strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			decl.WriteString(", ")
		}
		fmt.Fprintf(&decl, "$q%d: String!", i)
		fmt.Fprintf(&aliases, "\n  s%d: search(query: $q%d, type: ISSUE, first: %d) {\n    issueCount\n    nodes { ... on PullRequest { ...ListPR repository { nameWithOwner } } }\n  }", i, i, requestedFirst)
	}
	return "query(" + decl.String() + ") {\n  " + rateLimitField + aliases.String() + "\n}\n" + listPRFragment
}

type requestedSearch struct {
	IssueCount int               `json:"issueCount"`
	Nodes      []json.RawMessage `json:"nodes"`
}

// ListRequested reads the open PRs of repos that ask for my review, that
// I reviewed, or that I opened: one request of aliased searches, de-duped.
// A search that fails is a note; the call fails only when the request
// does.
func (c *GitHub) ListRequested(ctx context.Context, repos []model.Repo) (ListResult, error) {
	var out ListResult
	if len(repos) == 0 {
		return out, nil
	}
	searches := requestedSearches(repos)
	vars := make(map[string]any, len(searches))
	for i, q := range searches {
		vars[fmt.Sprintf("q%d", i)] = q
	}
	data, errs, err := c.graphql(ctx, requestedQuery(len(searches)), vars)
	if err != nil {
		return out, err
	}
	want := map[string]model.Repo{}
	for _, r := range repos {
		want[strings.ToLower(r.String())] = r
	}
	seen := map[model.Ident]bool{}
	for i := range searches {
		alias := fmt.Sprintf("s%d", i)
		var s requestedSearch
		ok, err := data.get(alias, &s)
		if err != nil || !ok {
			msg := "no answer"
			if err != nil {
				msg = err.Error()
			}
			for _, e := range errs {
				if e.pathIs(alias) && e.Message != "" {
					msg = e.Message
					break
				}
			}
			out.Notes = append(out.Notes, "requested repos: a search failed ("+msg+")")
			continue
		}
		for _, raw := range s.Nodes {
			var where struct {
				Repository struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
			}
			var pr model.PR
			if json.Unmarshal(raw, &where) != nil || json.Unmarshal(raw, &pr) != nil || pr.Number == 0 {
				continue
			}
			repo, ok := want[strings.ToLower(where.Repository.NameWithOwner)]
			if !ok {
				continue // search never strays, but a renamed repo could
			}
			pr.Repo = repo
			if seen[pr.Ident()] {
				continue
			}
			seen[pr.Ident()] = true
			p := pr
			out.PRs = append(out.PRs, &p)
		}
		if s.IssueCount > len(s.Nodes) {
			out.Notes = append(out.Notes, fmt.Sprintf("requested repos: showing %d of %d PRs for %q", len(s.Nodes), s.IssueCount, strings.Fields(searches[i])[3]))
		}
	}
	return out, nil
}
