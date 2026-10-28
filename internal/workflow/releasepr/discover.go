package releasepr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// cacheFile is the on-disk shape of the discovery cache.
type cacheFile struct {
	At    float64  `json:"at"`
	Org   string   `json:"org"`
	Repos []string `json:"repos"`
}

// repoNames is the enrolled repos: the config's list, or discovery (the
// org's repos, each one's rulesets) cached in memory and on disk for
// DiscoverTTLSecs. A failed discovery keeps the last list and warns.
func (w *Workflow) repoNames(ctx context.Context, gh ghapi.Client, now time.Time) ([]model.Repo, string) {
	if !w.cfg.Auto {
		return append([]model.Repo(nil), w.cfg.Repos...), ""
	}
	ttl := time.Duration(w.cfg.DiscoverTTLSecs) * time.Second
	if w.repoCache != nil && now.Sub(w.repoAt) < ttl {
		return append([]model.Repo(nil), w.repoCache...), ""
	}
	if cached, ok := LoadCache(w.cfg.Cache, w.org, ttl, now); ok {
		w.repoCache, w.repoAt = cached, now
		return append([]model.Repo(nil), cached...), ""
	}
	found, err := w.discover(ctx, gh)
	if err != nil {
		if w.repoCache != nil {
			return append([]model.Repo(nil), w.repoCache...), "release repos not discovered: " + err.Error()
		}
		return nil, "release repos not discovered: " + err.Error()
	}
	w.repoCache, w.repoAt = found, now
	SaveCache(w.cfg.Cache, w.org, found, now)
	return append([]model.Repo(nil), found...), ""
}

// discover lists the org's repos and reads each non-archived one's
// rulesets.
func (w *Workflow) discover(ctx context.Context, gh ghapi.Client) ([]model.Repo, error) {
	if w.org == "" {
		return nil, errors.New("no org to discover release repos in")
	}
	repos, err := gh.OrgRepos(ctx, w.org)
	if err != nil {
		return nil, err
	}
	byName := map[string][]model.Ruleset{}
	for _, r := range repos {
		if r.Archived {
			continue
		}
		rs, err := gh.Rulesets(ctx, model.Repo{Owner: w.org, Name: r.Name})
		if err != nil {
			return nil, fmt.Errorf("%s rulesets: %w", r.Name, err)
		}
		byName[r.Name] = rs
	}
	return ReposFrom(w.org, w.cfg.Ruleset, repos, byName), nil
}

// ReposFrom is the enrolled repos: not archived, and a ruleset named
// ruleset applies (org or repo sourced). Sorted by name.
func ReposFrom(org, ruleset string, repos []model.RepoListing, rulesets map[string][]model.Ruleset) []model.Repo {
	var out []model.Repo
	for _, r := range repos {
		if r.Archived {
			continue
		}
		for _, rs := range rulesets[r.Name] {
			if rs.Name == ruleset {
				out = append(out, model.Repo{Owner: org, Name: r.Name})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LoadCache reads the discovery cache while it is younger than ttl and
// was written for org; ok is false otherwise.
func LoadCache(path, org string, ttl time.Duration, now time.Time) ([]model.Repo, bool) {
	if path == "" {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var data cacheFile
	if err := json.Unmarshal(raw, &data); err != nil || data.Repos == nil {
		return nil, false
	}
	if data.Org != "" && org != "" && data.Org != org {
		return nil, false
	}
	at := time.Unix(0, int64(data.At*float64(time.Second)))
	if now.Sub(at) > ttl {
		return nil, false
	}
	out := make([]model.Repo, 0, len(data.Repos))
	for _, s := range data.Repos {
		if r, ok := model.ParseRepo(s); ok {
			out = append(out, r)
		} else if org != "" && s != "" {
			out = append(out, model.Repo{Owner: org, Name: s})
		}
	}
	return out, true
}

// SaveCache writes the discovery cache (0600; a failure is silent: the
// next refresh discovers again).
func SaveCache(path, org string, repos []model.Repo, now time.Time) {
	if path == "" {
		return
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.String())
	}
	sort.Strings(names)
	raw, err := json.Marshal(cacheFile{At: float64(now.UnixNano()) / float64(time.Second), Org: org, Repos: names})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}
