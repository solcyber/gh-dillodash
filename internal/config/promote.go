package config

import (
	"strings"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// PromoteConfig is [workflows.promote]: moving a change through a
// branch-per-tier repo, first tier to production.
type PromoteConfig struct {
	Repos []model.Repo
	// Tiers are the branches in order: the first tier (feature PRs land
	// here), the staging tier (a promotion PR per unit, tagged go or
	// hold), and production (reached through the release PR).
	Tiers []string
	// FirstTierReviews is which open PRs into the first tier the review
	// list shows for a repo in [reviews] requested_repos: "requested" (the
	// ones that ask for your review, you reviewed or opened, as for any
	// requested repo) or "open" (every open, non-draft PR not opened by a
	// GitHub App; an agent's account counts, even in [bots]).
	FirstTierReviews string
	// LookbackDays bounds the merged PRs the view reads.
	LookbackDays int
	// GoTagPrefix and HoldTagPrefix name a staging unit's tag ("go/612");
	// release_pr's when it is on.
	GoTagPrefix, HoldTagPrefix string
	// BranchPrefix starts a promotion branch's name ("promote/206-stage").
	BranchPrefix string
	// ApprovalComment goes on a deployment D approves from a promotion
	// row; placeholders {repo} {workflow} {run}.
	ApprovalComment string
}

// FirstTierChoices are first_tier_reviews' values.
var FirstTierChoices = []string{"requested", "open"}

// First, Stage and Prod are the three tiers.
func (p *PromoteConfig) First() string { return p.Tiers[0] }
func (p *PromoteConfig) Stage() string { return p.Tiers[1] }
func (p *PromoteConfig) Prod() string  { return p.Tiers[2] }

// Comment renders approval_comment for a run.
func (p *PromoteConfig) Comment(repo model.Repo, workflow string, run int64) string {
	return (&WaitingRunsConfig{ApprovalComment: p.ApprovalComment}).Comment(repo, workflow, run)
}

// OpenFirstTier is the promote repos whose open first-tier PRs all join
// the review list: first_tier_reviews = "open" and listed in [reviews]
// requested_repos (a [reviews] repo lists every PR already).
func (c *Config) OpenFirstTier() []model.Repo {
	p := c.Promote
	if p == nil || p.FirstTierReviews != "open" {
		return nil
	}
	var out []model.Repo
	for _, r := range p.Repos {
		if containsRepo(c.Reviews.Requested, r) {
			out = append(out, r)
		}
	}
	return out
}

func containsRepo(list []model.Repo, r model.Repo) bool {
	for _, x := range list {
		if strings.EqualFold(x.String(), r.String()) {
			return true
		}
	}
	return false
}

// DefaultPromote is [workflows.promote] with every default; Repos stays
// nil (= the [reviews] repos).
func DefaultPromote() *PromoteConfig {
	return &PromoteConfig{
		Tiers:            []string{"dev", "stage", "main"},
		FirstTierReviews: "requested",
		LookbackDays:     14,
		GoTagPrefix:      "go/",
		HoldTagPrefix:    "hold/",
		BranchPrefix:     "promote/",
		ApprovalComment:  "dillodash: approved {workflow} run {run} in {repo}",
	}
}

func (p *parser) parsePromote(wt *table, cfg *Config) {
	t, enabled := p.workflowEnabled(wt, "promote")
	w := DefaultPromote()
	if rp := cfg.ReleasePR; rp != nil {
		w.GoTagPrefix, w.HoldTagPrefix = rp.GoTagPrefix, rp.HoldTagPrefix
	}
	repos, _ := t.repos("repos")
	w.Repos = repos
	if tiers, ok := t.strs("tiers", w.Tiers); ok {
		seen := map[string]bool{}
		bad := len(tiers) != 3
		trimmed := make([]string, 0, len(tiers))
		for _, b := range tiers {
			b = strings.TrimSpace(b)
			if b == "" || seen[b] {
				bad = true
			}
			seen[b] = true
			trimmed = append(trimmed, b)
		}
		if bad {
			t.errf("tiers", "needs three distinct branches in order: first, staging, production (got %q)", tiers)
		} else {
			w.Tiers = trimmed
		}
	}
	w.FirstTierReviews = t.choice("first_tier_reviews", w.FirstTierReviews, FirstTierChoices)
	w.LookbackDays = t.int("lookback_days", w.LookbackDays)
	if w.LookbackDays < 1 || w.LookbackDays > 90 {
		t.errf("lookback_days", "must be 1 to 90, got %d", w.LookbackDays)
		w.LookbackDays = 14
	}
	w.GoTagPrefix = t.nonEmpty("go_tag_prefix", w.GoTagPrefix)
	w.HoldTagPrefix = t.nonEmpty("hold_tag_prefix", w.HoldTagPrefix)
	if w.GoTagPrefix == w.HoldTagPrefix {
		t.errf("hold_tag_prefix", "must differ from go_tag_prefix (%q)", w.GoTagPrefix)
	}
	w.BranchPrefix = t.nonEmpty("branch_prefix", w.BranchPrefix)
	w.ApprovalComment = t.nonEmpty("approval_comment", w.ApprovalComment)
	for _, ph := range placeholderRE.FindAllString(w.ApprovalComment, -1) {
		switch ph {
		case "{repo}", "{workflow}", "{run}":
		default:
			t.errf("approval_comment", "unknown placeholder %s (allowed: {repo} {workflow} {run})", ph)
		}
	}
	t.done()

	if !enabled {
		return
	}
	if len(w.Repos) == 0 {
		w.Repos = append([]model.Repo(nil), cfg.Reviews.Repos...)
	}
	if rp := cfg.ReleasePR; rp != nil && (rp.StagingBranch != w.Stage() || rp.ProductionBranch != w.Prod()) {
		t.warnf("tiers", "%q and %q differ from release_pr's staging_branch %q and production_branch %q; the last hop is release_pr's", w.Stage(), w.Prod(), rp.StagingBranch, rp.ProductionBranch)
	}
	for _, r := range w.Repos {
		if !containsRepo(cfg.Reviews.Repos, r) && !containsRepo(cfg.Reviews.Requested, r) {
			t.warnf("repos", "%s is in neither [reviews] repos nor requested_repos; its open %s PRs are not listed", r, w.First())
		}
	}
	cfg.Promote = w
}
