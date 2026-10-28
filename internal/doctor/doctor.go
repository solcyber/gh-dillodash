// Package doctor is `gh dillodash config check`: it validates the config,
// finds the gh login and token, probes what the token can read in every
// repo, and says which workflows are on, all without starting the TUI.
package doctor

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Run prints the report; it returns an error when something is broken.
func Run(ctx context.Context, w io.Writer, cfg *config.Config, version string) error {
	fmt.Fprintf(w, "dillodash %s\n", version)
	fmt.Fprintf(w, "config: %s\n", cfg.Path)
	for _, warn := range cfg.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warn)
	}
	fmt.Fprintf(w, "reviews: %s\n", joinRepos(cfg.Reviews.Repos))
	host := cfg.GitHub.Host
	if host == "" {
		host = "github.com"
	}
	gh, err := ghapi.New(ghapi.Options{Host: host, Version: version, Timeout: 15 * time.Second})
	if err != nil {
		fmt.Fprintf(w, "auth: FAILED: %s\n  sign in with: gh auth login\n", err)
		return err
	}
	fmt.Fprintf(w, "host: %s\ntoken: from %s\n", gh.Host(), gh.TokenSource())
	me, err := gh.Whoami(ctx)
	if err != nil {
		fmt.Fprintf(w, "login: FAILED: %s\n", err)
		return err
	}
	fmt.Fprintf(w, "login: %s", me)
	if cfg.GitHub.Login != "" && !strings.EqualFold(cfg.GitHub.Login, me) {
		fmt.Fprintf(w, "  (config expects %s: gh auth switch)", cfg.GitHub.Login)
	}
	fmt.Fprintln(w)
	failed := false
	for _, repo := range cfg.Reviews.Repos {
		fmt.Fprintf(w, "repo %s:\n", repo)
		res, err := gh.ListOpen(ctx, []model.Repo{repo})
		if err != nil {
			fmt.Fprintf(w, "  list: FAILED: %s\n", err)
			failed = true
			continue
		}
		r := res[repo]
		if r.Err != nil {
			fmt.Fprintf(w, "  list: FAILED: %s\n", r.Err)
			failed = true
			continue
		}
		fmt.Fprintf(w, "  pull requests: %d open, issues: %d open\n", len(r.PRs), len(r.Issues))
		for _, n := range r.Notes {
			fmt.Fprintf(w, "  note: %s\n", n)
		}
		if _, err := gh.WorkflowRuns(ctx, repo, "0000000000000000000000000000000000000000"); err != nil {
			fmt.Fprintf(w, "  actions: not readable (%s) — the CI column falls back to '?' when the check rollup is hidden too\n", short(err))
		} else {
			fmt.Fprintln(w, "  actions: readable")
		}
	}
	if rate := gh.LastRate(); rate != nil && rate.Limit > 0 {
		fmt.Fprintf(w, "graphql budget: %d of %d points left (resets %s)\n", rate.Remaining, rate.Limit, rate.ResetAt.Local().Format("15:04"))
	}
	fmt.Fprintln(w, "workflows:")
	on := func(name string, enabled bool, detail string) {
		state := "off"
		if enabled {
			state = "on"
		}
		fmt.Fprintf(w, "  %-20s %s%s\n", name, state, detail)
	}
	on("terraform_approve", cfg.TerraformApprove != nil, "")
	if m := cfg.MaintenanceWindow; m != nil {
		on("maintenance_window", true, fmt.Sprintf("  manifests in %s, %s, window %s-%s", m.ManifestRepo, m.Timezone, m.Start, m.End))
	} else {
		on("maintenance_window", false, "")
	}
	if r := cfg.ReleasePR; r != nil {
		repos := "auto (ruleset " + r.Ruleset + " in " + r.Org + ")"
		if !r.Auto {
			repos = joinRepos(r.Repos)
		}
		on("release_pr", true, "  repos: "+repos)
	} else {
		on("release_pr", false, "")
	}
	fmt.Fprintln(w, "write permissions (reviews, comments, merges, deployments) are not probed: a 403 names the missing one when it happens")
	if failed {
		return fmt.Errorf("some repos could not be read")
	}
	return nil
}

func joinRepos(repos []model.Repo) string {
	parts := make([]string, len(repos))
	for i, r := range repos {
		parts[i] = r.String()
	}
	return strings.Join(parts, ", ")
}

func short(err error) string {
	s := err.Error()
	if i := strings.Index(s, ": "); i > 0 && len(s) > 80 {
		return s[i+2:]
	}
	return s
}
