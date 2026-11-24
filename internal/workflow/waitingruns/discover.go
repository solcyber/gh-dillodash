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

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// With repos = "auto" the repos are found, not listed: every repo of the
// organizations with an environment that needs a reviewer. A repo that
// gains one is read without anybody editing a config (a hand list that
// missed one hid its waiting deploys entirely). The environments change
// rarely, so discovery is kept discover_ttl_secs, in memory and on disk,
// and redone in the background: about one call per repo every six hours.

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
// gated repos (less any [github] allowed_repos leaves out). A discovery
// that is due runs in the background while the last list serves; with
// no list at all it runs now. One that fails, or finds nothing it could
// read, falls back to the review repos and says so: a wait is never
// hidden because discovery broke.
func (w *Workflow) repos(ctx context.Context, gh ghapi.Client, now time.Time) ([]model.Repo, string) {
	if !w.cfg.Auto {
		return append([]model.Repo(nil), w.cfg.Repos...), ""
	}
	ttl := time.Duration(w.cfg.DiscoverTTLSecs) * time.Second
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	orgs := strings.Join(w.cfg.Orgs, "+")
	w.discMu.Lock()
	defer w.discMu.Unlock()
	if w.disc.repos == nil {
		if cached, at, ok := loadDiscovery(w.cfg.Cache, orgs); ok {
			w.disc = discovered{cached, at}
		}
	}
	if w.disc.repos != nil {
		if now.Sub(w.disc.at) >= ttl && !w.discovering {
			// rediscover beside the refresh; the last list serves meanwhile
			w.discovering = true
			go func() {
				dctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				found, failed, err := Discover(dctx, gh, w.cfg.Orgs)
				w.discMu.Lock()
				defer w.discMu.Unlock()
				w.discovering = false
				if err == nil && failed == 0 {
					w.disc = discovered{found, clock.Now()}
					saveDiscovery(w.cfg.Cache, orgs, found, clock.Now())
				} else {
					w.disc.at = clock.Now().Add(-ttl + 10*time.Minute) // try again in ten minutes
				}
			}()
		}
		return w.allowed(w.disc.repos), ""
	}
	found, failed, err := Discover(ctx, gh, w.cfg.Orgs)
	switch {
	case err != nil && len(w.fallback) > 0:
		return append([]model.Repo(nil), w.fallback...), "repos with gated environments not discovered (" + err.Error() + "): reading the review repos instead"
	case err != nil:
		return nil, "repos with gated environments not discovered: " + err.Error()
	case failed > 0 && len(found) == 0 && len(w.fallback) > 0:
		return append([]model.Repo(nil), w.fallback...), fmt.Sprintf("the environments of %d repos could not be read (the token needs Environments: Read): reading the review repos instead", failed)
	}
	note := ""
	if failed > 0 {
		// not kept: the next refresh tries again
		note = fmt.Sprintf("the environments of %d repos could not be read; their waiting runs are not listed", failed)
		w.disc = discovered{found, now.Add(-ttl + 10*time.Minute)}
	} else {
		w.disc = discovered{found, now}
		saveDiscovery(w.cfg.Cache, orgs, found, now)
	}
	return w.allowed(found), note
}

// allowed drops the repos [github] allowed_repos keeps dillodash from
// writing to: D there would always be refused.
func (w *Workflow) allowed(repos []model.Repo) []model.Repo {
	var out []model.Repo
	for _, r := range repos {
		if w.writeAllowed == nil || w.writeAllowed(r) {
			out = append(out, r)
		}
	}
	return out
}

// Discover lists the organizations' repos and keeps the ones with an
// environment that has required reviewers (not archived), sorted; failed
// counts the repos whose environments could not be read. The environments
// are read eight at a time.
func Discover(ctx context.Context, gh ghapi.Client, orgs []string) (found []model.Repo, failed int, err error) {
	if len(orgs) == 0 {
		return nil, 0, fmt.Errorf("no organization to discover repos in (set [workflows.waiting_runs] orgs or repos)")
	}
	var mu sync.Mutex
	for _, org := range orgs {
		listing, err := gh.OrgRepos(ctx, org)
		if err != nil {
			return nil, 0, fmt.Errorf("%s repos: %w", org, err)
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
					mu.Lock()
					failed++
					mu.Unlock()
					return nil
				}
				for _, e := range envs {
					if e.Gated() {
						mu.Lock()
						found = append(found, repo)
						mu.Unlock()
						break
					}
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, 0, err
		}
	}
	sort.Slice(found, func(i, j int) bool { return strings.ToLower(found[i].String()) < strings.ToLower(found[j].String()) })
	return found, failed, nil
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
