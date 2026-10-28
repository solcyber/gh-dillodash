package ghapi

import (
	"context"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// releaseWorkers bounds the release queries in flight.
const releaseWorkers = 6

type releaseNode struct {
	NameWithOwner string `json:"nameWithOwner"`
	IsArchived    bool   `json:"isArchived"`
	Prod          *struct {
		Target struct {
			Oid string `json:"oid"`
		} `json:"target"`
	} `json:"prod"`
	Stage *struct {
		Target struct {
			Oid string `json:"oid"`
		} `json:"target"`
	} `json:"stage"`
	Open struct {
		TotalCount int         `json:"totalCount"`
		Nodes      []*model.PR `json:"nodes"`
	} `json:"open"`
	Merged struct {
		Nodes []*model.PR `json:"nodes"`
	} `json:"merged"`
}

// ReleasePRs reads every repo's open and recently merged PRs into
// prodBranch plus the tips of prodBranch and stageBranch, one query per
// repo with at most six in flight. A repo that fails for anything but a
// rate limit (a network error gets one retry) comes back with Err; a
// rate limit fails the whole call so the refresh backs off.
func (c *GitHub) ReleasePRs(ctx context.Context, repos []model.Repo, prodBranch, stageBranch string) (map[model.Repo]ReleaseRepo, error) {
	out := make(map[model.Repo]ReleaseRepo, len(repos))
	if len(repos) == 0 {
		return out, nil
	}
	if prodBranch == "" {
		prodBranch = "main"
	}
	if stageBranch == "" {
		stageBranch = prodBranch
	}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(releaseWorkers)
	for _, repo := range repos {
		g.Go(func() error {
			rr, err := c.releaseRepo(gctx, repo, prodBranch, stageBranch)
			if err != nil {
				return err
			}
			mu.Lock()
			out[repo] = rr
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// releaseRepo reads one repo. Its error return is only ever a rate
// limit; anything else lands in ReleaseRepo.Err.
func (c *GitHub) releaseRepo(ctx context.Context, repo model.Repo, prod, stage string) (ReleaseRepo, error) {
	vars := repoVars(repo)
	vars["prod"] = prod
	vars["prodRef"] = refName(prod)
	vars["stageRef"] = refName(stage)
	var data gqlData
	var errs []GQLError
	var err error
	for attempt := 1; attempt <= 2; attempt++ {
		data, errs, err = c.graphql(ctx, releaseQuery, vars)
		if err == nil {
			break
		}
		if IsRateLimit(err) {
			return ReleaseRepo{}, err
		}
		if attempt == 2 || !IsNetwork(err) || ctx.Err() != nil {
			return ReleaseRepo{Err: err}, nil
		}
	}
	var node releaseNode
	if err := repoNode(data, errs, "r0", &node); err != nil {
		return ReleaseRepo{Err: err}, nil
	}
	rr := ReleaseRepo{Name: node.NameWithOwner, Archived: node.IsArchived}
	if node.Prod != nil {
		rr.ProdOid = node.Prod.Target.Oid
	}
	if node.Stage != nil {
		rr.StageOid = node.Stage.Target.Oid
	}
	for _, list := range [][]*model.PR{node.Open.Nodes, node.Merged.Nodes} {
		for _, p := range list {
			if p != nil {
				p.Repo = repo
				rr.PRs = append(rr.PRs, p)
			}
		}
	}
	return rr, nil
}
