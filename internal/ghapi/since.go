package ghapi

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// sinceFirst is how many merged PRs per branch MergedSince lists; the
// count is exact past it.
const sinceFirst = 30

// MergedPR is one PR merged into a branch.
type MergedPR struct {
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	MergedAt time.Time `json:"mergedAt"`
	Author   struct {
		Login string `json:"login"`
	} `json:"author"`
}

// Since is what merged into the two tiers below production after a
// moment (a release's cut), and whether the two branches now hold the
// same content.
type Since struct {
	// First and Stage are the PRs merged into each branch after the
	// moment, oldest first, at most sinceFirst of them; the counts are
	// the totals.
	First, Stage           []MergedPR
	FirstCount, StageCount int
	// FirstTree and StageTree are the branches' root tree ids: equal
	// trees are equal content however the commits got there (a squashed
	// promotion never shares a commit with what it promotes).
	FirstTree, StageTree string
}

// Same reports the two branches hold the same content; false when either
// tree was not read.
func (s Since) Same() bool { return s.FirstTree != "" && s.FirstTree == s.StageTree }

// Known reports both trees were read.
func (s Since) Known() bool { return s.FirstTree != "" && s.StageTree != "" }

const sinceQuery = `
query($owner: String!, $name: String!, $firstRef: String!, $stageRef: String!,
      $qFirst: String!, $qStage: String!) {
  ` + rateLimitField + `
  r0: repository(owner: $owner, name: $name) {
    first: ref(qualifiedName: $firstRef) { target { ... on Commit { tree { oid } } } }
    stage: ref(qualifiedName: $stageRef) { target { ... on Commit { tree { oid } } } }
  }
  mFirst: search(query: $qFirst, type: ISSUE, first: 30) {
    issueCount nodes { ... on PullRequest { number title mergedAt author { login } } } }
  mStage: search(query: $qStage, type: ISSUE, first: 30) {
    issueCount nodes { ... on PullRequest { number title mergedAt author { login } } } }
}`

type sinceTree struct {
	Target struct {
		Tree struct {
			Oid string `json:"oid"`
		} `json:"tree"`
	} `json:"target"`
}

type sinceSearch struct {
	IssueCount int        `json:"issueCount"`
	Nodes      []MergedPR `json:"nodes"`
}

// SinceSearch is the search text MergedSince sends for one branch (tests
// and the fake read it).
func SinceSearch(repo model.Repo, branch string, since time.Time) string {
	return fmt.Sprintf("repo:%s is:pr is:merged base:%s merged:>%s", repo, branch, since.UTC().Format(time.RFC3339))
}

// MergedSince reads, in one query, the PRs merged into first and into
// stage after since and the two branches' root trees.
func (c *GitHub) MergedSince(ctx context.Context, repo model.Repo, first, stage string, since time.Time) (Since, error) {
	vars := repoVars(repo)
	vars["firstRef"] = refName(first)
	vars["stageRef"] = refName(stage)
	vars["qFirst"] = SinceSearch(repo, first, since)
	vars["qStage"] = SinceSearch(repo, stage, since)
	data, errs, err := c.graphql(ctx, sinceQuery, vars)
	if err != nil {
		return Since{}, err
	}
	var node struct {
		First *sinceTree `json:"first"`
		Stage *sinceTree `json:"stage"`
	}
	if err := repoNode(data, errs, "r0", &node); err != nil {
		return Since{}, err
	}
	var out Since
	if node.First != nil {
		out.FirstTree = node.First.Target.Tree.Oid
	}
	if node.Stage != nil {
		out.StageTree = node.Stage.Target.Tree.Oid
	}
	read := func(key string) ([]MergedPR, int, error) {
		var s sinceSearch
		if _, err := data.get(key, &s); err != nil {
			return nil, 0, &Error{Message: "bad response: " + err.Error()}
		}
		// a non-PR node decodes to a zero entry: drop it
		prs := s.Nodes[:0]
		for _, p := range s.Nodes {
			if p.Number != 0 {
				prs = append(prs, p)
			}
		}
		sort.SliceStable(prs, func(i, j int) bool { return prs[i].MergedAt.Before(prs[j].MergedAt) })
		n := s.IssueCount
		if n < len(prs) {
			n = len(prs)
		}
		return prs, n, nil
	}
	if out.First, out.FirstCount, err = read("mFirst"); err != nil {
		return Since{}, err
	}
	if out.Stage, out.StageCount, err = read("mStage"); err != nil {
		return Since{}, err
	}
	return out, nil
}
