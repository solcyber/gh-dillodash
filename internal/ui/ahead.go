package ui

import (
	"context"
	"strings"
	"sync"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// aheadMax caps the compare calls one refresh makes for stale approvals;
// the rest wait for the next refresh (each pair is fetched once).
const aheadMax = 8

// aheadCache remembers how far a head is ahead of a stale approval's
// commit. Both shas are fixed points, so an answer never goes stale.
type aheadCache struct {
	mu sync.Mutex
	n  map[string]int
}

// annotate sets ApprovalAhead on every PR whose approval is stale, with
// one compare call per new (repo, approved, head), side by side.
// Advisory: a failed compare leaves the count out of the reason and is
// retried next time.
func (a *aheadCache) annotate(ctx context.Context, gh ghapi.Client, prs []*model.PR, me string) {
	type ask struct {
		repo       model.Repo
		base, head string
	}
	keys := make([]string, len(prs))
	asks := map[string]ask{}
	for i, pr := range prs {
		s := model.StuckOf(pr, nil, me)
		if s.Kind != model.StuckStale || s.ApprovedSHA == "" || s.HeadSHA == "" {
			continue
		}
		key := pr.Repo.String() + "\x00" + s.ApprovedSHA + "\x00" + s.HeadSHA
		keys[i] = key
		a.mu.Lock()
		_, ok := a.n[key]
		a.mu.Unlock()
		if !ok && len(asks) < aheadMax {
			asks[key] = ask{pr.Repo, s.ApprovedSHA, s.HeadSHA}
		}
	}
	var wg sync.WaitGroup
	for key, q := range asks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }() // a panic is an unanswered compare: the chip waits
			c, err := gh.Compare(ctx, q.repo, q.base, q.head)
			if err != nil {
				return
			}
			a.mu.Lock()
			if a.n == nil {
				a.n = map[string]int{}
			}
			a.n[key] = c.AheadBy
			a.mu.Unlock()
		}()
	}
	wg.Wait()
	for i, pr := range prs {
		if keys[i] == "" {
			continue
		}
		a.mu.Lock()
		n, ok := a.n[keys[i]]
		a.mu.Unlock()
		if ok {
			pr.ApprovalAhead = n
		}
	}
}

// blockedMax bounds the branches whose rules one refresh reads.
const blockedMax = 12

// annotateBlocked sets BlockedWhy on every PR GitHub blocks: the base
// branch's rules, read once per branch (side by side; a repeat is a
// conditional request), against the PR's own checks and reviews.
// Advisory: rules that can't be read leave the generic reason.
func annotateBlocked(ctx context.Context, gh ghapi.Client, prs []*model.PR) {
	type branch struct {
		repo model.Repo
		base string
	}
	want := map[branch]bool{}
	for _, pr := range prs {
		if strings.EqualFold(pr.MergeStateStatus, "BLOCKED") && pr.BaseRefName != "" && len(want) < blockedMax {
			want[branch{pr.Repo, pr.BaseRefName}] = true
		}
	}
	if len(want) == 0 {
		return
	}
	var mu sync.Mutex
	rules := map[branch]model.BranchRules{}
	var wg sync.WaitGroup
	for b := range want {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }() // a panic is rules not read
			r, err := gh.BranchRules(ctx, b.repo, b.base)
			if err != nil {
				return
			}
			mu.Lock()
			rules[b] = r
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, pr := range prs {
		if strings.EqualFold(pr.MergeStateStatus, "BLOCKED") {
			pr.BlockedWhy = model.BlockedWhy(pr, rules[branch{pr.Repo, pr.BaseRefName}])
		}
	}
}
