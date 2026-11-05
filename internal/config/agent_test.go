package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// TestScopedOperatorKeys: allowed_repos, approvers_team_strict, [agent]
// and [display] profile parse, and their mistakes are reported.
func TestScopedOperatorKeys(t *testing.T) {
	c := mustParse(t, minimal+`
[github]
approvers_team = "acme/ops-approvers"
approvers_team_strict = true
allowed_repos = ["acme/ops", "ACME/ops", "acme/ops-docs"]

[agent]
handoff_label = "agent"
handoff_comment = "over to the agent"

[display]
profile = "OPS"
`)
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings: %v", c.Warnings)
	}
	if !c.GitHub.ApproversTeamStrict {
		t.Error("strict not read")
	}
	if want := []model.Repo{repo("acme/ops"), repo("acme/ops-docs")}; !reflect.DeepEqual(c.GitHub.AllowedRepos, want) {
		t.Errorf("allowed = %v (deduped case-insensitively)", c.GitHub.AllowedRepos)
	}
	if !c.GitHub.WriteAllowed(repo("Acme/OPS")) || c.GitHub.WriteAllowed(repo("acme/infra")) {
		t.Error("WriteAllowed: listed (any case) yes, other no")
	}
	if c.Agent != (AgentConfig{HandoffLabel: "agent", HandoffComment: "over to the agent"}) || c.Display.Profile != "OPS" {
		t.Errorf("agent = %+v, profile = %q", c.Agent, c.Display.Profile)
	}

	d := mustParse(t, minimal)
	if len(d.GitHub.AllowedRepos) != 0 || !d.GitHub.WriteAllowed(repo("any/repo")) || d.GitHub.ApproversTeamStrict ||
		d.Agent != (AgentConfig{}) || d.Display.Profile != "" {
		t.Errorf("defaults: github %+v agent %+v profile %q", d.GitHub, d.Agent, d.Display.Profile)
	}

	w := mustParse(t, minimal+"[github]\napprovers_team_strict = true\n[agent]\nhandoff_comment = \"hi\"\n")
	joined := strings.Join(w.Warnings, "\n")
	for _, want := range []string{"approvers_team_strict: has no effect without approvers_team", "handoff_comment: has no effect without handoff_label"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning %q missing: %v", want, w.Warnings)
		}
	}

	for src, want := range map[string]string{
		"[github]\nallowed_repos = []\n":                "allowed_repos: must list at least one",
		"[github]\nallowed_repos = [\"nope\"]\n":        `"nope" is not "owner/name"`,
		"[display]\nprofile = \"far-too-long-badge\"\n": "a header badge holds at most 12",
		"[github]\napprovers_team_strict = \"yes\"\n":   "expected a boolean",
	} {
		_, err := parse(t, minimal+src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", src, err, want)
		}
	}
}

// TestAllowedReposWarnsOnWorkflowsOutside: a workflow that writes to a repo
// outside allowed_repos is a config mistake (every write there would be
// refused), so it warns; a workflow inside the list does not.
func TestAllowedReposWarnsOnWorkflowsOutside(t *testing.T) {
	c := mustParse(t, `
[github]
allowed_repos = ["acme/ops"]

[reviews]
repos = ["acme/ops"]

[workflows.terraform_approve]
enabled = true
repos = ["acme/ops"]

[workflows.waiting_runs]
enabled = true
repos = ["acme/ops", "acme/infra"]

[[workflows.dispatch]]
name = "rotate"
repo = "acme/web-portal"
workflow = "rotate.yml"
ref = "main"
`)
	joined := strings.Join(c.Warnings, "\n")
	for _, want := range []string{
		"[workflows.waiting_runs] repos: acme/infra is outside [github] allowed_repos",
		`[[workflows.dispatch]] "rotate": acme/web-portal is outside [github] allowed_repos`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "terraform_approve") || strings.Contains(joined, "acme/ops is outside") {
		t.Errorf("a workflow inside allowed_repos warned:\n%s", joined)
	}
}
