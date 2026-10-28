package ghapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// ciWorkers caps the Actions calls in flight during one Annotate.
const ciWorkers = 6

// ciRecheck is how long a settled, passing SHA's cluster is trusted
// before the Actions API is asked again (a re-run may have happened).
const ciRecheck = 900 * time.Second

// CINotVisible is the warning when CI is invisible by every route.
const CINotVisible = "CI not visible to this token"

// RunsFunc fetches the workflow runs on a head SHA (Client.WorkflowRuns).
type RunsFunc func(ctx context.Context, repo model.Repo, headSHA string) ([]model.WorkflowRun, error)

type ciEntry struct {
	cluster model.CICluster
	settled bool
	at      time.Time
}

// CIFallback computes a PR's CI cluster where the token cannot read the
// GraphQL check rollup (fine-grained PATs have no Checks permission at
// all) from the Actions workflow runs on its head SHA, which only needs
// Actions: read. Settled SHAs are cached so finished CI is never
// re-fetched; in-flight ones are re-polled each refresh, failing ones
// too (a re-run may fix them), and passing ones after Recheck. A
// permission failure of the Actions call (401, 403, 404) turns the
// fallback off for that repo until Reset (the manual refresh), so a
// token without the permission is not hammered every refresh; any
// other failure (a timeout, a 502) is transient and is retried next
// refresh, showing the last known cluster meanwhile.
type CIFallback struct {
	// Recheck is how long a settled, passing SHA's cluster is trusted
	// before the Actions API is asked again; 0 means ciRecheck. The
	// config's ci_recheck_secs sets it.
	Recheck time.Duration

	mu       sync.Mutex
	cache    map[string]ciEntry    // head sha -> entry
	disabled map[model.Repo]string // repo -> why the Actions route is off
}

// Annotate computes the cluster of every PR: the GraphQL rollup when the
// token can see it, else the Actions fallback, at most ciWorkers calls
// in flight and one per head SHA (a PR listed twice, as a review row
// and a release row, is asked once). clusters[i] belongs to prs[i].
// warn is the first warning met in prs order (CI invisible by every
// route); err is only ever a rate limit from the Actions call, which
// must back the whole refresh off rather than silently disable CI.
func (f *CIFallback) Annotate(ctx context.Context, now time.Time, prs []*model.PR, fetch RunsFunc) (clusters []model.CICluster, warn string, err error) {
	clusters = make([]model.CICluster, len(prs))
	warns := make([]string, len(prs))
	live := map[string]bool{}
	bySHA := map[string][]int{} // head sha -> indexes needing the Actions route
	var order []string
	for i, pr := range prs {
		if c, ok := model.ClusterCI(pr); ok {
			clusters[i] = c
			continue
		}
		sha := pr.HeadRefOid
		if sha == "" {
			clusters[i], warns[i] = model.CIUnknownCluster(), CINotVisible
			continue
		}
		live[sha] = true
		if _, seen := bySHA[sha]; !seen {
			order = append(order, sha)
		}
		bySHA[sha] = append(bySHA[sha], i)
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(ciWorkers)
	var mu sync.Mutex
	for _, sha := range order {
		idx := bySHA[sha]
		g.Go(func() error {
			c, w, err := f.Lookup(gctx, now, prs[idx[0]], fetch)
			if err != nil {
				return err
			}
			mu.Lock()
			for _, i := range idx {
				clusters[i], warns[i] = c, w
			}
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, "", err
	}
	for _, w := range warns {
		if w != "" {
			warn = w
			break
		}
	}
	f.Prune(live)
	return clusters, warn, nil
}

// Lookup is the Actions-route cluster for one PR, from the cache when it
// is still fresh. warn says why CI is unknown ("?") when it is.
func (f *CIFallback) Lookup(ctx context.Context, now time.Time, pr *model.PR, fetch RunsFunc) (model.CICluster, string, error) {
	sha := pr.HeadRefOid
	if sha == "" {
		return model.CIUnknownCluster(), CINotVisible, nil
	}
	f.mu.Lock()
	if why, off := f.disabled[pr.Repo]; off {
		f.mu.Unlock()
		return model.CIUnknownCluster(), why, nil
	}
	cached, ok := f.cache[sha]
	f.mu.Unlock()
	recheck := f.Recheck
	if recheck <= 0 {
		recheck = ciRecheck
	}
	if ok && cached.settled && len(cached.cluster.Failed) == 0 && now.Sub(cached.at) <= recheck {
		return cached.cluster, "", nil
	}
	if fetch == nil {
		return model.CIUnknownCluster(), CINotVisible, nil
	}
	runs, err := fetch(ctx, pr.Repo, sha)
	if err != nil {
		// a rate-limit 403/429 must trigger global backoff, not a silent
		// session-long CI disable — let the caller catch it
		if IsRateLimit(err) {
			return model.CICluster{}, "", err
		}
		if isPermission(err) {
			// the token cannot read Actions here: don't hammer it every
			// refresh; off until Reset (the manual refresh)
			why := fmt.Sprintf("CI via Actions API failed on %s: %s — token needs Actions: read", pr.Repo.Name, err)
			f.mu.Lock()
			if f.disabled == nil {
				f.disabled = map[model.Repo]string{}
			}
			f.disabled[pr.Repo] = why
			f.mu.Unlock()
			return model.CIUnknownCluster(), why, nil
		}
		// transient (a timeout, a 502): keep showing what we knew, and
		// ask again next refresh
		if ok {
			return cached.cluster, "", nil
		}
		return model.CIUnknownCluster(), fmt.Sprintf("CI via Actions API on %s: %s (retried next refresh)", pr.Repo.Name, err), nil
	}
	entry := clusterRuns(runs, now)
	f.mu.Lock()
	if f.cache == nil {
		f.cache = map[string]ciEntry{}
	}
	f.cache[sha] = entry
	f.mu.Unlock()
	return entry.cluster, "", nil
}

// clusterRuns folds workflow runs (newest first) into a cluster: the
// first run per workflow wins. An empty result may just mean runs
// haven't been created yet for a fresh push, so it never counts as
// settled.
func clusterRuns(runs []model.WorkflowRun, now time.Time) ciEntry {
	var chars []byte
	failed := []string{}
	settled := true
	seen := map[string]bool{}
	for _, r := range runs {
		key := r.Path
		if key == "" {
			key = r.Name
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		g := model.CIGlyph(r.Status, r.Conclusion)
		chars = append(chars, g)
		if g == model.CIFail {
			name := r.Name
			if name == "" {
				name = "?"
			}
			failed = append(failed, name)
		}
		if g == model.CIQueued || g == model.CIRunning {
			settled = false
		}
	}
	return ciEntry{
		cluster: model.FinishCluster(string(chars), failed),
		settled: settled && len(chars) > 0,
		at:      now,
	}
}

// Prune drops cached SHAs no live PR points at once the cache has grown
// past four times the live set.
func (f *CIFallback) Prune(live map[string]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(live)
	if n < 1 {
		n = 1
	}
	if len(f.cache) <= 4*n {
		return
	}
	for sha := range f.cache {
		if !live[sha] {
			delete(f.cache, sha)
		}
	}
}

// isPermission reports whether err is GitHub refusing the request for
// this token (401, 403 or 404, which is what a private repo the token
// cannot see answers) rather than failing to answer it.
func isPermission(err error) bool {
	e, ok := AsError(err)
	if !ok {
		return false
	}
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

// Reset re-enables the Actions route for every repo and forgets every
// cached cluster: the manual refresh re-checks settled CI too (a re-run
// may have happened) and re-tries a repo whose token was just fixed.
func (f *CIFallback) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cache = nil
	f.disabled = nil
}

// Disabled is why the Actions route is off for repo ("" when it is on).
func (f *CIFallback) Disabled(repo model.Repo) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disabled[repo]
}

// Cached reports whether sha has a cached cluster and when it was
// fetched.
func (f *CIFallback) Cached(sha string) (model.CICluster, time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.cache[sha]
	return e.cluster, e.at, ok
}
