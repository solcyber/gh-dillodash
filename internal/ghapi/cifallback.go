package ghapi

import (
	"context"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// CIRecheck is how long a settled, passing SHA's cluster is trusted
// before the Actions API is asked again (a re-run may have happened).
const CIRecheck = 900 * time.Second

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
// too (a re-run may fix them), and passing ones after CIRecheck. A
// non-rate-limit failure of the Actions call turns the fallback off for
// that repo until the process restarts (or Reset), so a token without
// the permission is not hammered every refresh.
type CIFallback struct {
	// Recheck overrides CIRecheck (tests).
	Recheck time.Duration

	mu       sync.Mutex
	cache    map[string]ciEntry    // head sha -> entry
	disabled map[model.Repo]string // repo -> why the Actions route is off
}

// Annotate computes the cluster of every PR: the GraphQL rollup when the
// token can see it, else the Actions fallback. clusters[i] belongs to
// prs[i]. warn is the first warning met (CI invisible by every route);
// err is only ever a rate limit from the Actions call, which must back
// the whole refresh off rather than silently disable CI.
func (f *CIFallback) Annotate(ctx context.Context, now time.Time, prs []*model.PR, fetch RunsFunc) (clusters []model.CICluster, warn string, err error) {
	clusters = make([]model.CICluster, len(prs))
	live := map[string]bool{}
	for i, pr := range prs {
		if c, ok := model.ClusterCI(pr); ok {
			clusters[i] = c
			continue
		}
		if pr.HeadRefOid != "" {
			live[pr.HeadRefOid] = true
		}
		c, w, err := f.Lookup(ctx, now, pr, fetch)
		if err != nil {
			return nil, "", err
		}
		clusters[i] = c
		if warn == "" {
			warn = w
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
		recheck = CIRecheck
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
		// a non-rate-limit 403 (e.g. token lacks Actions: read): don't
		// hammer it every refresh; stays off until restart
		why := "CI via Actions API failed: " + err.Error() + " — token needs Actions: read"
		f.mu.Lock()
		if f.disabled == nil {
			f.disabled = map[model.Repo]string{}
		}
		f.disabled[pr.Repo] = why
		f.mu.Unlock()
		return model.CIUnknownCluster(), why, nil
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

// Flush forgets every cached cluster (the manual refresh re-checks
// settled CI too, e.g. after a re-run).
func (f *CIFallback) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cache = nil
}

// Reset re-enables the Actions route for every repo and flushes.
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

// Age backdates a cached SHA by d (tests: expire a cluster).
func (f *CIFallback) Age(sha string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.cache[sha]; ok {
		e.at = e.at.Add(-d)
		f.cache[sha] = e
	}
}
