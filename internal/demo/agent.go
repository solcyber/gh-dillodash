package demo

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// The agent demo (`--demo-agent`) is a team that works with an AI agent
// that is a GitHub App: its own repo applies infrastructure on approval
// (no maintenance window), a legacy repo applies on merge, the agent
// opens PRs and takes issues handed to it by a label, and only the
// approvers team may /approve. Fictional: dana (you), eli and fen, the
// agent Ada (ada-ops[bot]), acme/ops and acme/legacy-tf.

// AgentMe is the agent demo's acting login.
const AgentMe = "dana"

// AgentRepos are the agent demo's repos: the team's own, then the legacy
// one whose merges apply.
var AgentRepos = []model.Repo{{Owner: "acme", Name: "ops"}, {Owner: "acme", Name: "legacy-tf"}}

const agentLogin = "ada-ops"

// AgentConfig is the agent demo's config: two repos, writes allowed to
// both only, strict approvers, the agent named with its own section,
// merges in the legacy repo marked as applies, plans on approval.
func AgentConfig() *config.Config {
	cfg := config.Default()
	cfg.Reviews.Repos = AgentRepos
	cfg.Reviews.MergeApplies = []model.Repo{AgentRepos[1]}
	cfg.GitHub.ApproversTeam = "acme/ops-approvers"
	cfg.GitHub.ApproversTeamStrict = true
	cfg.GitHub.AllowedRepos = AgentRepos
	cfg.Display.Profile = "OPS"
	cfg.Agent = config.AgentConfig{HandoffLabel: "ada", Name: "Ada", Login: agentLogin, Section: true}
	cfg.Bots.Logins = append(cfg.Bots.Logins, agentLogin)
	ta := config.DefaultTerraformApprove()
	ta.Repos = []model.Repo{AgentRepos[0]}
	cfg.TerraformApprove = ta
	wr := config.DefaultWaitingRuns()
	wr.Repos = []model.Repo{AgentRepos[0]}
	cfg.WaitingRuns = wr
	return cfg
}

// AgentClient builds the agent demo's fake GitHub.
func AgentClient() *fake.Client {
	ops, legacy := AgentRepos[0], AgentRepos[1]
	gh := fake.New(AgentMe)
	gh.Rate = &model.RateLimit{Limit: 5000, Remaining: 4930, ResetAt: clock.Now().Add(41 * time.Minute)}
	gh.Open = map[model.Repo]ghapi.ListResult{}
	gh.PRs = map[model.Ident]*model.PR{}
	gh.Issues = map[model.Ident]*model.Issue{}
	gh.Users = map[model.Repo][]model.Actor{}
	gh.Files = map[model.Ident][]model.PRFile{}
	gh.Diffs = map[model.Ident]string{}
	gh.Runs = map[string][]model.WorkflowRun{}
	gh.FortressData = map[model.Repo]ghapi.FortressData{}
	gh.Teams = map[string][]string{"acme/ops-approvers": {"dana", "eli", "fen"}}
	ada := agentLogin + "[bot]"

	opsPRs := []*model.PR{
		pr(ops, 8, "add the zone's first two stacks (00-baseline, 01-network)", ada, "ada/first-stacks", 3*24*time.Hour, 646, 0, "plan:success", "preflight:success"),
		pr(ops, 9, "ceremony: live fence verification", ada, "ada/fence-verification", 3*24*time.Hour, 86, 1, "plan:success", "preflight:success", "ceremony-plan:success"),
		pr(ops, 12, "alerts: route the zone's alarms to the shared mailbox", "eli", "eli/alerts", 7*time.Hour, 40, 6, "plan:success", "preflight:success"),
	}
	legacyPRs := []*model.PR{
		pr(legacy, 197, "move the syslog collector's state into the new zone", ada, "ada/syslog-state", 5*time.Hour, 22, 30, "plan:success"),
	}
	legacyPRs[0].ReviewDecision = "APPROVED"
	legacyPRs[0].Opinions.Nodes = []model.Review{{Author: actor("fen"), State: "APPROVED"}}
	issues := []*model.Issue{
		issue(ops, 2, "first applies: 00-baseline and 01-network", "fen", 3*24*time.Hour, "ada", "effort/s"),
		issue(ops, 6, "fence verification ceremony", "fen", 3*24*time.Hour, "category:security"),
		issue(ops, 14, "the shared memory service drops writes under load", ada, 20*time.Hour, "bug"),
		issue(legacy, 5, "archive this repo once its contents have moved", "dana", 4*24*time.Hour, "migration"),
	}
	gh.Open[ops] = ghapi.ListResult{PRs: opsPRs, Issues: issues[:3]}
	gh.Open[legacy] = ghapi.ListResult{PRs: legacyPRs, Issues: issues[3:]}
	for _, is := range issues {
		gh.Issues[is.Ident()] = is
	}
	for _, p := range append(opsPRs, legacyPRs...) {
		d := *p
		d.Body = "Opened by Ada. The plan digest CI posts below is what an approval applies: it applies at once (no window).\n"
		d.MergeStateStatus = "CLEAN"
		gh.PRs[p.Ident()] = &d
		gh.Files[p.Ident()] = []model.PRFile{{Filename: "terraform/live/prod/01-network/main.tf", Status: "added", Additions: p.Additions, Patch: "@@ -0,0 +1,3 @@\n+module \"vpc\" {\n+  cidr = \"10.0.0.0/16\"\n+}\n"}}
		gh.Diffs[p.Ident()] = "diff --git a/terraform/live/prod/01-network/main.tf b/terraform/live/prod/01-network/main.tf\n" + gh.Files[p.Ident()][0].Patch
		gh.Users[p.Repo] = []model.Actor{actor("dana"), actor("eli"), actor("fen")}
	}
	// #8: a plan-set over two stacks, planned for its head, apply on approval
	set := gh.PRs[opsPRs[0].Ident()]
	head := set.HeadRefOid
	set.Comments.Nodes = []model.Comment{
		bot(agentDigest("prod/00-baseline", 21, 7), 30*time.Hour),
		bot(agentDigest("prod/01-network", 22, 35), 30*time.Hour),
		bot(agentPlanSet(head, 23, [][2]any{{"prod/00-baseline", 21}, {"prod/01-network", 22}}), 30*time.Hour),
	}
	set.Comments.TotalCount = len(set.Comments.Nodes)
	// #9: a ceremony with its dry-run passed
	cer := gh.PRs[opsPRs[1].Ident()]
	cer.Comments.Nodes = []model.Comment{bot(fmt.Sprintf("### ceremony `ceremonies/2026-11-06-fence-verification.sh`\n\nCeremony-id: `%s`\nHead: `%s`\nSummary: prove the fence with real calls\n"+
		"Window: n/a (apply on approval)\n\nDry-run: **passed**\n\nTo approve: `/approve %s`", hexID(24), cer.HeadRefOid, hexID(24)), 30*time.Hour)}
	cer.Comments.TotalCount = 1
	// #12: a single plan
	one := gh.PRs[opsPRs[2].Ident()]
	one.Comments.Nodes = []model.Comment{bot(agentDigest("prod/02-alerts", 25, 0), time.Hour)}
	one.Comments.TotalCount = 1
	for _, p := range []*model.PR{set, cer, one} {
		gh.Runs[p.HeadRefOid] = append(gh.Runs[p.HeadRefOid], model.WorkflowRun{ID: 4062542, Name: "plan", Path: ".github/workflows/plan.yml",
			Status: "completed", Conclusion: "success", Event: "pull_request", HeadSHA: p.HeadRefOid, CreatedAt: at(70 * time.Hour)})
	}
	return gh
}

// agentDigest is a plan digest from a repo that applies on approval.
func agentDigest(stack string, id, adds int) string {
	return fmt.Sprintf("### plan `%s`\n\nPlan-id: `%s`\nPlan: %d to add, 0 to change, 0 to destroy.\nWindow: **n/a (apply on approval)**\nhttps://github.com/acme/ops/actions/runs/4062542\n", stack, hexID(id), adds)
}

// agentPlanSet is the plan-set digest over members ({stack, plan seed}).
func agentPlanSet(head string, setSeed int, members [][2]any) string {
	var ms []map[string]any
	for _, m := range members {
		ms = append(ms, map[string]any{"stack": m[0], "plan_id": hexID(m[1].(int)), "destroys": 0})
	}
	meta, _ := json.Marshal(map[string]any{"set_id": hexID(setSeed), "head_sha": head, "members": ms, "skipped": []string{}})
	return fmt.Sprintf("### plan-set\n%d change-bearing stack(s), 0 destroy(s) in total.\nPlan-set-id: `%s`\nTo approve every stack in this set: `/approve %s`\n<!-- plan-set: %s -->",
		len(members), hexID(setSeed), hexID(setSeed), meta)
}
