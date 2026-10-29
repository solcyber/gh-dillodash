// Package demo is the offline fixture `gh dillodash --demo` runs on: a
// fake GitHub with a few repos' worth of pull requests, issues, comments,
// reviews, checks and files, so the dashboard can be shown, screenshotted
// and tried without a token. The vocabulary is fictional: the acme org,
// alice, bob and carol, and infra-bot.
package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
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

// bodies are the other pull requests' descriptions, by number (the
// warehouse one above is infra#41's and the fallback).
var bodies = map[int]string{
	42: "## Summary\n\nThe quarterly drill: rotate the DKIM signing keys for the mail domain and publish the new selector. " +
		"The old selector stays published for a week so mail in flight still verifies.\n\n- [x] planned in CI\n- [x] drill runbook followed\n",
	43: "Automated: the canary's p95 CPU sat above 80% for seven days, so its instance size moves up one step.\n\n" +
		"| stack | from | to |\n|---|---|---|\n| nonprod/15-window-canary | t3.medium | t3.large |\n\nQueued for the window: the resize restarts the canary.\n",
	44: "Work in progress: splitting the network stack into the VPC and the edge, so a change to one stops planning the other.\n\n- [x] VPC\n- [ ] edge\n- [ ] state moves\n",
	90: "Yesterday's cut for production.\n\n- fix: the signup form loses the referral code (#92)\n- the dashboard's empty state after a password reset (#19)\n",
	91: "Today's cut for production.\n\n- the billing page's new invoice table\n- faster search on the accounts list\n- dependency updates\n",
	92: "## Summary\n\nThe referral code from the landing page was dropped when the form re-rendered after a validation error. " +
		"It now lives in the form state, not the query string.\n\n```ts\nconst [referral] = useState(() => params.get(\"ref\") ?? \"\");\n```\n\n- [x] unit test for the re-render\n- [ ] e2e: the referral flow\n",
}

// bodyOf is a pull request's description in the demo.
func bodyOf(n int) string {
	if b, ok := bodies[n]; ok {
		return b
	}
	return body
}

// Config is the demo's config: the two repos and all four workflows on.
func Config() *config.Config {
	cfg := config.Default()
	cfg.Reviews.Repos = Repos
	cfg.TerraformApprove = config.DefaultTerraformApprove()
	mw := config.DefaultMaintenanceWindow()
	mw.ManifestRepo = Repos[0]
	mw.ManifestTitle = "Window {night} 23:00 ET"
	cfg.MaintenanceWindow = mw
	rp := config.DefaultReleasePR()
	rp.Auto = false
	rp.Repos = []model.Repo{Repos[1]}
	rp.Org = "acme"
	rp.ApprovalQueuesForWindow = true
	cfg.ReleasePR = rp
	wr := config.DefaultWaitingRuns()
	wr.Repos = Repos
	cfg.WaitingRuns = wr
	return cfg
}

// reviewer is a required reviewer of an environment: a login, or a team
// as "team:<slug>".
func reviewer(who string) model.DeploymentReviewer {
	var r model.DeploymentReviewer
	if slug, ok := strings.CutPrefix(who, "team:"); ok {
		r.Type, r.Reviewer.Slug = "Team", slug
	} else {
		r.Type, r.Reviewer.Login = "User", who
	}
	return r
}

// pending is one environment a run waits on.
func pending(env string, mine bool, reviewers ...string) model.PendingDeployment {
	var p model.PendingDeployment
	p.Environment.Name, p.CurrentUserCanApprove = env, mine
	for _, r := range reviewers {
		p.Reviewers = append(p.Reviewers, reviewer(r))
	}
	return p
}

// waitingRuns are the runs waiting on an environment with no listed PR
// behind them (the waiting_runs section): a production deploy bob
// dispatched by hand that waits on the viewer, and an ops run of carol's
// that waits on the database team.
func waitingRuns(gh *fake.Client) {
	infra, portal := Repos[0], Repos[1]
	deploy := model.WorkflowRun{ID: 7101, Name: "deploy", Path: ".github/workflows/deploy.yml", Status: "waiting", Event: "workflow_dispatch",
		DisplayTitle: "deploy tier=prod · v2026.10.28", HeadBranch: "main", Actor: actor("bob"), RunAttempt: 1,
		HTMLURL: "https://github.com/acme/infra/actions/runs/7101", CreatedAt: at(25 * time.Minute), UpdatedAt: at(24 * time.Minute)}
	vacuum := model.WorkflowRun{ID: 7102, Name: "db-maintenance", Path: ".github/workflows/db-maintenance.yml", Status: "waiting", Event: "workflow_dispatch",
		DisplayTitle: "vacuum the sessions table", HeadBranch: "main", Actor: actor("carol"), RunAttempt: 1,
		HTMLURL: "https://github.com/acme/web-portal/actions/runs/7102", CreatedAt: at(2 * time.Hour), UpdatedAt: at(2 * time.Hour)}
	gh.RepoRuns = map[model.Repo][]model.WorkflowRun{infra: {deploy}, portal: {vacuum}}
	gh.Pending = map[int64][]model.PendingDeployment{
		7101: {pending("prod", true, "alice", "team:sre")},
		7102: {pending("prod-db", false, "team:dba")},
	}
}

// night is the next window night as YYYY-MM-DD: tonight before the 21:00
// cutoff in New York, else tomorrow.
func night() (string, time.Time) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	day := now
	if now.Hour() >= 21 {
		day = now.Add(24 * time.Hour)
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 23, 0, 0, 0, loc)
	return day.Format("2006-01-02"), start.UTC()
}

func hexID(seed int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("dillodash-demo-plan-%d", seed)))
	return hex.EncodeToString(sum[:])
}

func digest(stack string, id int, destroys int, verdict string) string {
	return fmt.Sprintf("### plan `%s`\n\nPlan-id: `%s`\nPlan: 3 to add, 1 to change, %d to destroy.\nWindow (policy; live): **%s**\nhttps://github.com/acme/infra/actions/runs/4062542\n", stack, hexID(id), destroys, verdict)
}

func bot(body string, age time.Duration) model.Comment {
	return model.Comment{Author: actor("github-actions"), CreatedAt: at(age), Body: body}
}

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
		pr(infra, 41, "widen the deploy role to the warehouse functions", "bob", "warehouse-role", 26*time.Hour, 84, 12, "plan:success", "lint:success", "policy:success"),
		pr(infra, 42, "rotate the mail signing keys (drill)", "carol", "mail-keys-drill", 5*time.Hour, 31, 31, "plan:success", "lint:success", "policy:running"),
		pr(infra, 43, "bump the canary stack's instance size", "infra-bot", "infra-bot/canary-size", 70*time.Hour, 6, 6, "plan:success", "lint:failure", "policy:success"),
		pr(infra, 44, "wip: split the network stack", "bob", "net-split", 9*24*time.Hour, 410, 220, "plan:queued", "lint:success"),
	}
	infraPRs[3].IsDraft = true
	infraPRs[1].ReviewDecision = "APPROVED"
	infraPRs[1].Opinions.Nodes = []model.Review{{Author: actor("alice"), State: "APPROVED"}}
	portalPRs := []*model.PR{
		pr(portal, 91, "release/2026-10-28", "infra-bot", "release/2026-10-28", 3*time.Hour, 1204, 388, "build:success", "e2e:success"),
		pr(portal, 92, "fix: the signup form loses the referral code", "carol", "fix-referral", 2*24*time.Hour, 18, 4, "build:success", "e2e:failure"),
	}
	issues := []*model.Issue{
		issue(infra, 17, "nightly load ran 40 minutes late on Tuesday", "carol", 2*24*time.Hour, "ops"),
		issue(infra, 18, "drop the legacy VPN endpoint", "bob", 11*24*time.Hour, "enhancement"),
		issue(portal, 19, "customers see a blank dashboard after password reset", "carol", 6*time.Hour, "bug", "sev2"),
	}
	gh.Open[infra] = ghapi.ListResult{PRs: infraPRs, Issues: issues[:2]}
	gh.Open[portal] = ghapi.ListResult{PRs: portalPRs, Issues: issues[2:]}
	for _, p := range append(infraPRs, portalPRs...) {
		d := *p
		d.Body = bodyOf(p.Number)
		d.Comments.Nodes = []model.Comment{
			{Author: actor("carol"), CreatedAt: at(20 * time.Hour), Body: "Looks right to me. One question: was this tried against staging first?"},
			{Author: actor(p.Author.Login), CreatedAt: at(18 * time.Hour), Body: "Yes, it ran there yesterday; the run is linked in the description of the last push."},
		}
		if p.Number == 41 {
			d.Comments.Nodes[0].Body = "Looks right to me. One question: does the read need `s3:ListBucket` on the whole bucket, or only the prefix?"
			d.Comments.Nodes[1].Body = "Only the prefix, I tightened it in the last push."
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
	// workflows: plan digests, a queued PR with its night's manifest, a release repo
	nightDate, start := night()
	ta := gh.PRs[infraPRs[0].Ident()]
	ta.Comments.Nodes = append([]model.Comment{bot(digest("prod/07-warehouse", 1, 0, "non-disruptive"), 12*time.Hour), bot(digest("nonprod/07-warehouse", 2, 0, "non-disruptive"), 12*time.Hour)}, ta.Comments.Nodes...)
	ta.Comments.TotalCount = len(ta.Comments.Nodes)
	tb := gh.PRs[infraPRs[1].Ident()]
	tb.Comments.Nodes = append([]model.Comment{bot(digest("prod/03-mail", 3, 1, "disruptive · NOT revertible"), 2*time.Hour)}, tb.Comments.Nodes...)
	tb.Comments.TotalCount = len(tb.Comments.Nodes)
	queued := infraPRs[2]
	queued.Labels.Nodes = append(queued.Labels.Nodes, model.Label{Name: "window:queued"}, model.Label{Name: "window:scheduled"})
	marker := fmt.Sprintf("<!-- window-queue {\"night\": %q, \"plan_id\": %q, \"stacks\": [\"nonprod/15-window-canary\"], \"queued_at\": %q, \"revertible\": true} -->", nightDate, hexID(4), at(28*time.Hour).UTC().Format(time.RFC3339))
	qc := []model.Comment{bot(digest("nonprod/15-window-canary", 4, 0, "disruptive · revertible"), 30*time.Hour), bot("Queued for the maintenance window of "+nightDate+".\n\n"+marker, 28*time.Hour)}
	gh.Comments = map[model.Ident][]model.Comment{queued.Ident(): qc}
	qd := gh.PRs[queued.Ident()]
	qd.Comments.Nodes = append(qc, qd.Comments.Nodes...)
	qd.Comments.TotalCount = len(qd.Comments.Nodes)
	manifest := &model.Issue{Repo: infra, Number: 20, Title: "Window " + nightDate + " 23:00 ET", State: "OPEN", CreatedAt: at(30 * time.Hour), UpdatedAt: at(2 * time.Hour), Author: actor("github-actions")}
	manifest.Labels.Nodes = []model.Label{{Name: "window-manifest"}}
	manifest.Body = fmt.Sprintf("Tonight's window.\n\n<!-- window-manifest:begin\n{\"night\": %q, \"phase\": \"open\", \"attendees\": [\"carol\"], \"items\": [{\"id\": \"#43\", \"repo\": \"infra\", \"pr\": 43, \"flag\": \"scheduled\", \"revertible\": true, \"start_utc\": %q, \"release_by_utc\": %q, \"status\": \"\", \"reasons\": []}]}\nwindow-manifest:end -->", nightDate, start.Add(4*time.Minute).Format(time.RFC3339), start.Add(14*time.Minute).Format(time.RFC3339))
	manifest.Comments.Nodes = []model.Comment{bot("@carol is attending the night of "+nightDate+".", 3*time.Hour)}
	manifest.Comments.TotalCount = 1
	gh.Labelled = map[model.Repo]map[string][]*model.Issue{infra: {"window-manifest": {manifest}}}
	infraIssues := append([]*model.Issue{manifest}, issues[:2]...)
	gh.Open[infra] = ghapi.ListResult{PRs: infraPRs, Issues: infraIssues}
	gh.Issues[manifest.Ident()] = manifest
	rel := *gh.PRs[portalPRs[0].Ident()]
	rel.Labels = model.Labels{}
	// the release copy has no rollup (what a fine-grained PAT sees), so
	// its CI comes through the Actions fallback; the review copy keeps
	// the rollup, so the demo shows both routes
	rel.Commits = model.Commits{}
	gh.Runs = map[string][]model.WorkflowRun{rel.HeadRefOid: {
		{ID: 501, Name: "build", Path: ".github/workflows/build.yml", Status: "completed", Conclusion: "success", HeadSHA: rel.HeadRefOid},
		{ID: 502, Name: "e2e", Path: ".github/workflows/e2e.yml", Status: "in_progress", HeadSHA: rel.HeadRefOid},
	}}
	// a release in flight: yesterday's cut, released NOW forty minutes
	// ago; the manifest repo's apply run is deploying it (the row's rail
	// and the detail's mission-control card come from this)
	flight := pr(portal, 90, "release/2026-10-27", "infra-bot", "release/2026-10-27", 26*time.Hour, 980, 240, "build:success", "e2e:success")
	flight.State, flight.ReviewDecision = "MERGED", "APPROVED"
	flight.Opinions.Nodes = []model.Review{{Author: actor("alice"), State: "APPROVED"}}
	mergedAt := at(6 * time.Minute)
	flight.MergedAt = &mergedAt
	flight.MergeCommit = &model.MergeCommit{Oid: hexID(90)[:40]}
	flight.Commits = model.Commits{}
	fd := *flight
	fd.Body = bodyOf(90)
	fd.Comments.Nodes = []model.Comment{{Author: actor("alice"), CreatedAt: at(8 * time.Minute), Body: "/release-now \"the referral fix is holding up signups\""}}
	fd.Comments.TotalCount = 1
	fd.Reviews.Nodes = []model.Review{{Author: actor("alice"), State: "APPROVED", SubmittedAt: at(9 * time.Minute), Body: "Ship it."}}
	fd.Reviews.TotalCount = 1
	gh.PRs[flight.Ident()] = &fd
	gh.Comments[flight.Ident()] = fd.Comments.Nodes
	gh.Runs[flight.MergeCommit.Oid] = []model.WorkflowRun{
		{ID: 601, Name: "deploy-api", Path: ".github/workflows/deploy-api.yml", Status: "completed", Conclusion: "success", HeadSHA: flight.MergeCommit.Oid, CreatedAt: at(5 * time.Minute)},
		{ID: 602, Name: "deploy-web", Path: ".github/workflows/deploy-web.yml", Status: "completed", Conclusion: "success", HeadSHA: flight.MergeCommit.Oid, CreatedAt: at(5 * time.Minute)},
		{ID: 603, Name: "migrate", Path: ".github/workflows/migrate.yml", Status: "in_progress", HeadSHA: flight.MergeCommit.Oid, CreatedAt: at(5 * time.Minute)},
	}
	gh.FileRuns = map[string][]model.WorkflowRun{infra.String() + ":apply.yml": {{
		ID: 4062601, Name: "apply", DisplayTitle: "apply · PR #90 · plan rel:web-portal#90", Event: "repository_dispatch", Status: "in_progress", RunAttempt: 1,
		HTMLURL: "https://github.com/acme/infra/actions/runs/4062601", CreatedAt: at(7*time.Minute + 30*time.Second), UpdatedAt: at(time.Minute),
	}}}
	steps := []string{"Inspect dispatch payload", "Checkout main", "Re-validate the approval from the release PR", "Registry facts", "Assume the deploy role", "Alarm baseline",
		"Merge the release PR (merge commit, named head)", "Approve the environment deployments of that merge", "Wait for the deploy", "Verify (alarms after the settle)", "Outcome, labels, event and comment", "Write the apply-log entry", "Fail the run unless the release verified"}
	job := model.WorkflowJob{ID: 11, RunID: 4062601, Name: "app release (merge at the slot / release-now)", Status: "in_progress", RunAttempt: 1, StartedAt: at(7 * time.Minute)}
	t := at(7 * time.Minute)
	for i, name := range steps {
		s := model.WorkflowStep{Number: i + 1, Name: name}
		switch {
		case i < 8:
			d := []time.Duration{2, 4, 3, 1, 6, 9, 2, 0}[i] * time.Second
			s.Status, s.Conclusion, s.StartedAt, s.CompletedAt = "completed", "success", t, t.Add(d)
			if i == 7 {
				s.Conclusion = "skipped"
			}
			t = t.Add(d)
		case i == 8:
			s.Status, s.StartedAt = "in_progress", t
		default:
			s.Status = "queued"
		}
		job.Steps = append(job.Steps, s)
	}
	gh.Jobs = map[int64][]model.WorkflowJob{4062601: {job}}
	waitingRuns(gh)
	gh.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {Name: portal.Name, PRs: []*model.PR{&rel, flight}, ProdOid: "aaaa0001", StageOid: "cccc0003"}}
	gh.Tags = map[model.Repo][]string{portal: {"go/905", "hold/907"}}
	gh.Compares = map[string]ghapi.CompareResult{"main...stage": {AheadBy: 3, TotalCommits: 3, Commits: []ghapi.CompareCommit{
		{SHA: "bbbb0001", Message: "Merge pull request #905 from acme/feat-search", Parents: []string{"aaaa0001", "dddd0001"}},
		{SHA: "bbbb0002", Message: "fix: referral code on signup (#907)", Parents: []string{"bbbb0001"}},
		{SHA: "cccc0003", Message: "chore: bump deps (#910)", Parents: []string{"bbbb0002"}},
	}}}
	for _, i := range issues {
		d := *i
		d.Body = "Seen on three accounts since the last deploy. Rolling back is not an option: the migration already ran."
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
