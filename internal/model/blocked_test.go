package model

import "testing"

func rollup(total int, cs ...CheckContext) *Rollup {
	r := &Rollup{}
	r.Contexts.Nodes = cs
	r.Contexts.TotalCount = total
	if total == 0 {
		r.Contexts.TotalCount = len(cs)
	}
	return r
}

func run(name, status, conclusion string) CheckContext {
	return CheckContext{Typename: "CheckRun", Name: name, Status: status, Conclusion: conclusion}
}

// TestBlockedWhy: the first required check that failed, runs or never
// reported is named; then the review the rules ask for; nothing is
// concluded from rules or checks that were not read.
func TestBlockedWhy(t *testing.T) {
	rules := BranchRules{Known: true, RequiredChecks: []string{"build", "e2e", "gate"}}
	approved := []Review{{State: "APPROVED"}}
	for name, tc := range map[string]struct {
		rules    BranchRules
		rollup   *Rollup
		decision string
		opinions []Review
		want     string
	}{
		"failed wins":   {rules, rollup(0, run("build", "COMPLETED", "SUCCESS"), run("e2e", "COMPLETED", "FAILURE"), run("gate", "IN_PROGRESS", "")), "", nil, `required check "e2e" failed`},
		"running":       {rules, rollup(0, run("build", "COMPLETED", "SUCCESS"), run("e2e", "COMPLETED", "SKIPPED"), run("gate", "QUEUED", "")), "", nil, `required check "gate" is still running`},
		"not reported":  {rules, rollup(0, run("build", "COMPLETED", "SUCCESS")), "", nil, `required checks "e2e" and "gate" haven't reported`},
		"status":        {BranchRules{Known: true, RequiredChecks: []string{"apply-gate"}}, rollup(0, CheckContext{Typename: "StatusContext", Context: "apply-gate", State: "PENDING"}), "", nil, `required check "apply-gate" is still running`},
		"two agree":     {BranchRules{Known: true, RequiredChecks: []string{"build"}}, rollup(0, run("build", "COMPLETED", "FAILURE"), run("build", "COMPLETED", "FAILURE")), "", nil, `required check "build" failed`},
		"two disagree":  {BranchRules{Known: true, RequiredChecks: []string{"build"}}, rollup(0, run("build", "COMPLETED", "CANCELLED"), run("build", "COMPLETED", "SUCCESS")), "APPROVED", approved, ""},
		"past the page": {rules, rollup(80, run("build", "COMPLETED", "SUCCESS")), "APPROVED", approved, ""},
		"no rollup":     {rules, nil, "APPROVED", approved, ""},
		"unknown rules": {BranchRules{RequiredChecks: []string{"build"}}, rollup(0), "", nil, ""},
		// GitHub's verdict on the reviews decides what is said of them
		"review required":      {BranchRules{Known: true, Approvals: 1}, rollup(0), "REVIEW_REQUIRED", nil, "needs an approval"},
		"two approvals":        {BranchRules{Known: true, Approvals: 2}, rollup(0), "REVIEW_REQUIRED", approved, "needs 2 approvals"},
		"owner required":       {BranchRules{Known: true, CodeOwner: true}, rollup(0), "REVIEW_REQUIRED", nil, "needs a code owner's review"},
		"changes requested":    {BranchRules{Known: true, Approvals: 1}, rollup(0), "CHANGES_REQUESTED", []Review{{State: "CHANGES_REQUESTED", Author: Actor{Login: "carol"}}}, "changes requested by carol"},
		"approved, other rule": {BranchRules{Known: true, Approvals: 1, CodeOwner: true}, rollup(0), "APPROVED", approved, ""},
		// no verdict (a branch that asks for no approving review)
		"owner, nobody approved": {BranchRules{Known: true, CodeOwner: true}, rollup(0), "", nil, "needs a code owner's review"},
		"owner, someone did":     {BranchRules{Known: true, CodeOwner: true}, rollup(0), "", approved, ""},
		"owner or check":         {BranchRules{Known: true, CodeOwner: true, RequiredChecks: []string{"build"}}, nil, "", nil, ""},
		"nothing asked":          {BranchRules{Known: true}, rollup(0), "", nil, ""},
		"all green":              {BranchRules{Known: true, RequiredChecks: []string{"build"}, Approvals: 1}, rollup(0, run("build", "COMPLETED", "SUCCESS")), "APPROVED", approved, ""},
	} {
		pr := &PR{ReviewDecision: tc.decision}
		pr.Opinions.Nodes = tc.opinions
		pr.Commits = NewCommits(Commit{Rollup: tc.rollup})
		if got := BlockedWhy(pr, tc.rules); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	// commits not read at all
	if got := BlockedWhy(&PR{}, rules); got != "" {
		t.Errorf("no commits read: %q", got)
	}
	// the stuck chip carries the reason
	pr := &PR{ReviewDecision: "APPROVED", MergeStateStatus: "BLOCKED", BlockedWhy: `required check "gate" hasn't reported`}
	pr.Opinions.Nodes = approved
	if s := StuckOf(pr, nil, "alice"); s.Kind != StuckBlocked || s.Reason != `blocked: required check "gate" hasn't reported` {
		t.Errorf("stuck: %+v", s)
	}
}
