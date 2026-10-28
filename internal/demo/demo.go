// Package demo is the offline fixture `gh dillodash --demo` runs on: a
// fake GitHub with a few repos' worth of pull requests, issues, comments,
// reviews, checks and files, so the dashboard can be shown, screenshotted
// and tried without a token. The vocabulary is fictional: the acme org,
// alice, bob and carol, and infra-bot.
package demo

import (
	"fmt"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Me is the demo's acting login.
const Me = "alice"

// Repos are the demo's review repos.
var Repos = []model.Repo{{Owner: "acme", Name: "infra"}, {Owner: "acme", Name: "web-portal"}}

func at(d time.Duration) time.Time { return time.Now().Add(-d) }

func actor(l string) model.Actor { return model.Actor{Login: l} }

func rollup(checks ...string) *model.Rollup {
	r := &model.Rollup{State: "SUCCESS"}
	for _, c := range checks {
		name, state, _ := strings.Cut(c, ":")
		ctx := model.CheckContext{Typename: "CheckRun", Name: name, Status: "COMPLETED", Conclusion: state}
		switch state {
		case "running":
			ctx.Status, ctx.Conclusion = "IN_PROGRESS", ""
			r.State = "PENDING"
		case "queued":
			ctx.Status, ctx.Conclusion = "QUEUED", ""
			r.State = "PENDING"
		case "failure":
			r.State = "FAILURE"
		}
		r.Contexts.Nodes = append(r.Contexts.Nodes, ctx)
	}
	r.Contexts.TotalCount = len(r.Contexts.Nodes)
	return r
}

func pr(repo model.Repo, n int, title, author, head string, age time.Duration, adds, dels int, checks ...string) *model.PR {
	p := &model.PR{Repo: repo, Number: n, Title: title, State: "OPEN", HeadRefName: head, BaseRefName: "main",
		HeadRefOid: fmt.Sprintf("%040x", n*7919), CreatedAt: at(age), UpdatedAt: at(age / 3),
		Additions: adds, Deletions: dels, ChangedFiles: 1 + adds/40, Author: actor(author)}
	p.Commits = model.NewCommits(model.Commit{CommittedDate: at(age / 2), Rollup: rollup(checks...)})
	return p
}

func issue(repo model.Repo, n int, title, author string, age time.Duration, labels ...string) *model.Issue {
	i := &model.Issue{Repo: repo, Number: n, Title: title, State: "OPEN", CreatedAt: at(age), UpdatedAt: at(age / 2), Author: actor(author)}
	for _, l := range labels {
		i.Labels.Nodes = append(i.Labels.Nodes, model.Label{Name: l})
	}
	i.Comments.TotalCount = n % 4
	return i
}

const body = `## Summary

Widens the deploy role so the warehouse functions can read the staging
bucket during the nightly load. Scoped to the sixteen functions in
` + "`terraform/live/prod/07-warehouse`" + `; nothing else changes.

| stack | plan | destroys |
|---|---|---|
| prod/07-warehouse | 3 to add, 1 to change | 0 |
| nonprod/07-warehouse | 3 to add, 1 to change | 0 |

` + "```hcl" + `
resource "aws_iam_role_policy" "warehouse_read" {
  name = "warehouse-read"
  role = aws_iam_role.deploy.id
}
` + "```" + `

- [x] planned in CI
- [ ] approved
`

// Client builds the fake GitHub.
func Client() *fake.Client {
	infra, portal := Repos[0], Repos[1]
	gh := fake.New(Me)
	gh.Rate = &model.RateLimit{Limit: 5000, Remaining: 4812, ResetAt: time.Now().Add(37 * time.Minute)}
	gh.Open = map[model.Repo]ghapi.ListResult{}
	gh.PRs = map[model.Ident]*model.PR{}
	gh.Issues = map[model.Ident]*model.Issue{}
	gh.Users = map[model.Repo][]model.Actor{}
	gh.Files = map[model.Ident][]model.PRFile{}
	gh.Diffs = map[model.Ident]string{}
	gh.FortressData = map[model.Repo]ghapi.FortressData{}

	infraPRs := []*model.PR{
		pr(infra, 3543, "widen the deploy role to the warehouse functions", "bob", "warehouse-role", 26*time.Hour, 84, 12, "plan:success", "lint:success", "policy:success"),
		pr(infra, 3544, "rotate the SES DKIM keys (drill)", "carol", "ses-dkim-drill", 5*time.Hour, 31, 31, "plan:success", "lint:success", "policy:running"),
		pr(infra, 3550, "bump the canary stack's instance size", "infra-bot", "infra-bot/canary-size", 70*time.Hour, 6, 6, "plan:success", "lint:failure", "policy:success"),
		pr(infra, 3551, "wip: split the network stack", "bob", "net-split", 9*24*time.Hour, 410, 220, "plan:queued", "lint:success"),
	}
	infraPRs[3].IsDraft = true
	infraPRs[1].ReviewDecision = "APPROVED"
	infraPRs[1].Opinions.Nodes = []model.Review{{Author: actor("alice"), State: "APPROVED"}}
	portalPRs := []*model.PR{
		pr(portal, 912, "release/2026-10-28", "infra-bot", "release/2026-10-28", 3*time.Hour, 1204, 388, "build:success", "e2e:success"),
		pr(portal, 915, "fix: the signup form loses the referral code", "carol", "fix-referral", 2*24*time.Hour, 18, 4, "build:success", "e2e:failure"),
	}
	issues := []*model.Issue{
		issue(infra, 3540, "nightly load ran 40 minutes late on Tuesday", "carol", 2*24*time.Hour, "ops"),
		issue(infra, 3522, "drop the legacy VPN endpoint", "bob", 11*24*time.Hour, "enhancement"),
		issue(portal, 900, "customers see a blank dashboard after password reset", "carol", 6*time.Hour, "bug", "sev2"),
	}
	gh.Open[infra] = ghapi.ListResult{PRs: infraPRs, Issues: issues[:2]}
	gh.Open[portal] = ghapi.ListResult{PRs: portalPRs, Issues: issues[2:]}
	for _, p := range append(infraPRs, portalPRs...) {
		d := *p
		d.Body = body
		d.Comments.Nodes = []model.Comment{
			{Author: actor("carol"), CreatedAt: at(20 * time.Hour), Body: "Looks right to me. One question: does the read need `s3:ListBucket` on the whole bucket, or only the prefix?"},
			{Author: actor(p.Author.Login), CreatedAt: at(18 * time.Hour), Body: "Only the prefix, I tightened it in the last push."},
		}
		d.Comments.TotalCount = 2
		d.Reviews.Nodes = []model.Review{{Author: actor("carol"), State: "COMMENTED", SubmittedAt: at(20 * time.Hour)}}
		if p.ReviewDecision == "APPROVED" {
			d.Reviews.Nodes = append(d.Reviews.Nodes, model.Review{Author: actor("alice"), State: "APPROVED", SubmittedAt: at(2 * time.Hour), Body: "Drill looks complete."})
		}
		d.Reviews.TotalCount = len(d.Reviews.Nodes)
		d.MergeStateStatus = "CLEAN"
		gh.PRs[p.Ident()] = &d
		gh.Files[p.Ident()] = []model.PRFile{
			{Filename: "terraform/live/prod/07-warehouse/iam.tf", Status: "modified", Additions: p.Additions - 4, Deletions: p.Deletions, Patch: "@@ -12,6 +12,9 @@ resource \"aws_iam_role\" \"deploy\" {\n   name = \"deploy\"\n+  inline_policy {\n+    name = \"warehouse-read\"\n+  }\n }\n-# old comment\n"},
			{Filename: "docs/runbooks/warehouse.md", Status: "added", Additions: 4, Deletions: 0, Patch: "@@ -0,0 +1,4 @@\n+# Warehouse\n+\n+The nightly load reads the staging bucket.\n+"},
		}
		gh.Diffs[p.Ident()] = "diff --git a/terraform/live/prod/07-warehouse/iam.tf b/terraform/live/prod/07-warehouse/iam.tf\n" + gh.Files[p.Ident()][0].Patch
	}
	for _, i := range issues {
		d := *i
		d.Body = "Seen on three accounts since the deploy at 14:10. Rolling back is not an option: the migration already ran."
		d.Comments.Nodes = []model.Comment{{Author: actor("bob"), CreatedAt: at(3 * time.Hour), Body: "Reproduced. The session cookie is scoped to the old host."}}
		d.Comments.TotalCount = 1
		gh.Issues[i.Ident()] = &d
	}
	for _, r := range Repos {
		gh.Users[r] = []model.Actor{{Login: "alice", Name: "Alice"}, {Login: "bob", Name: "Bob"}, {Login: "carol", Name: "Carol"}}
		var branches []ghapi.FortressBranch
		for i, n := range []string{"main", "net-split", "warehouse-role", "ses-dkim-drill", "spike/otel", "old/2025-cleanup"} {
			branches = append(branches, ghapi.FortressBranch{Name: n, CommittedDate: at(time.Duration(i*i+1) * 24 * time.Hour)})
		}
		var commits []ghapi.FortressCommit
		for i := 0; i < 12; i++ {
			commits = append(commits, ghapi.FortressCommit{Oid: fmt.Sprintf("%040x", i*31+7), MessageHeadline: fmt.Sprintf("change %d", i), CommittedDate: at(time.Duration(i*9+2) * time.Hour)})
		}
		gh.FortressData[r] = ghapi.FortressData{Branches: branches, BranchTotal: len(branches), Default: "main", Commits: commits, OpenPRs: gh.Open[r].PRs}
	}
	return gh
}
