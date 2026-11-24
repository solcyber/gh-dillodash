package waitingruns

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// With repos = "auto" the repos are found, not listed: every repo of the
// organizations with an environment that needs a reviewer. A repo that
// gains one is read without anybody editing a config (a hand list that
// missed one hid its waiting deploys entirely). The environments change
// rarely, so discovery is kept discover_ttl_secs, in memory and on disk;
// its reads are conditional, so a rediscovery that finds nothing new
// costs nothing against the budget.

// discoveryFile is the on-disk shape of the discovery cache.
type discoveryFile struct {
	At    float64  `json:"at"`
	Orgs  string   `json:"orgs"`
	Repos []string `json:"repos"`
}

// discovered (mu held by the caller of repos) is the last discovery.
type discovered struct {
	repos []model.Repo
	at    time.Time
}

// repos is what the workflow reads: the config's list, or the discovered
// gated repos. A discovery that fails keeps the last list and warns.
func (w *Workflow) repos(ctx context.Context, gh ghapi.Client, now time.Time) ([]model.Repo, string) {
	if !w.cfg.Auto {
		return append([]model.Repo(nil), w.cfg.Repos...), ""
	}
	ttl := time.Duration(w.cfg.DiscoverTTLSecs) * time.Second
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	w.discMu.Lock()
	defer w.discMu.Unlock()
	if w.disc.repos != nil && now.Sub(w.disc.at) < ttl {
		return append([]model.Repo(nil), w.disc.repos...), ""
	}
	orgs := strings.Join(w.cfg.Orgs, "+")
	if cached, at, ok := loadDiscovery(w.cfg.Cache, orgs); ok && now.Sub(at) < ttl {
		w.disc = discovered{cached, at}
		return append([]model.Repo(nil), cached...), ""
	}
	found, err := Discover(ctx, gh, w.cfg.Orgs)
	if err != nil {
		if ghapi.IsRateLimit(err) || w.disc.repos != nil {
			return append([]model.Repo(nil), w.disc.repos...), "repos with gated environments not discovered: " + err.Error()
		}
		return nil, "repos with gated environments not discovered: " + err.Error()
	}
	w.disc = discovered{found, now}
	saveDiscovery(w.cfg.Cache, orgs, found, now)
	return append([]model.Repo(nil), found...), ""
}

// Discover lists the organizations' repos and keeps the ones with an
// environment that has required reviewers (not archived), sorted. The
// environments are read eight at a time.
func Discover(ctx context.Context, gh ghapi.Client, orgs []string) ([]model.Repo, error) {
	if len(orgs) == 0 {
		return nil, fmt.Errorf("no organization to discover repos in (set [workflows.waiting_runs] orgs or repos)")
	}
	var (
		mu  sync.Mutex
		out []model.Repo
	)
	for _, org := range orgs {
		listing, err := gh.OrgRepos(ctx, org)
		if err != nil {
			return nil, fmt.Errorf("%s repos: %w", org, err)
		}
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(8)
		for _, r := range listing {
			if r.Archived {
				continue
			}
			repo := model.Repo{Owner: org, Name: r.Name}
			g.Go(func() error {
				envs, err := gh.Environments(gctx, repo)
				if err != nil {
					if ghapi.IsRateLimit(err) {
						return err
					}
					return nil // a repo the token can't read environments of is not one it can approve in
				}
				for _, e := range envs {
					if e.Gated() {
						mu.Lock()
						out = append(out, repo)
						mu.Unlock()
						break
					}
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].String()) < strings.ToLower(out[j].String()) })
	return out, nil
}

func loadDiscovery(path, orgs string) ([]model.Repo, time.Time, bool) {
	if path == "" {
		return nil, time.Time{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	var data discoveryFile
	if json.Unmarshal(raw, &data) != nil || data.Repos == nil || !strings.EqualFold(data.Orgs, orgs) {
		return nil, time.Time{}, false
	}
	out := make([]model.Repo, 0, len(data.Repos))
	for _, s := range data.Repos {
		if r, ok := model.ParseRepo(s); ok {
			out = append(out, r)
		}
	}
	return out, time.Unix(0, int64(data.At*float64(time.Second))), true
}

// saveDiscovery writes the cache (0600); a failure is silent: the next
// start discovers again.
func saveDiscovery(path, orgs string, repos []model.Repo, now time.Time) {
	if path == "" {
		return
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.String())
	}
	raw, err := json.Marshal(discoveryFile{At: float64(now.UnixNano()) / float64(time.Second), Orgs: orgs, Repos: names})
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}
